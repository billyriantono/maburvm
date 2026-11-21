package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testKey(t *testing.T) BackupKey {
	t.Helper()
	k := make([]byte, 32)
	_, err := rand.Read(k)
	require.NoError(t, err)
	return BackupKey{ID: uuid.NewString(), Key: k}
}

// Every size round-trips, the size is known before encrypting (S3 wants it up
// front), and the bytes stored are not the plain ones.
func TestSealRoundTrip(t *testing.T) {
	key := testKey(t)
	for _, n := range []int{0, 1, 55, 56, sealedChunk - 1, sealedChunk, sealedChunk + 1, 3*sealedChunk + 17, 2 * sealedChunk} {
		plain := make([]byte, n)
		_, _ = rand.Read(plain)
		r, err := Seal(bytes.NewReader(plain), key)
		require.NoError(t, err)
		sealed, err := io.ReadAll(r)
		require.NoError(t, err)
		assert.Equal(t, SealedSize(int64(n)), int64(len(sealed)), "size of %d", n)
		if n > 64 {
			assert.False(t, bytes.Contains(sealed, plain[:64]), "plain bytes are not stored")
		}
		out, enc, err := Unseal(bytes.NewReader(sealed), []BackupKey{testKey(t), key})
		require.NoError(t, err)
		assert.True(t, enc)
		got, err := io.ReadAll(out)
		require.NoError(t, err, "size %d", n)
		assert.Equal(t, plain, got, "size %d", n)
	}
}

// A file made before encryption reads as it is, whatever its size.
func TestUnsealPassesPlainThrough(t *testing.T) {
	for _, plain := range [][]byte{nil, []byte("QFI\xfb"), bytes.Repeat([]byte("x"), 3*sealedChunk)} {
		out, enc, err := Unseal(bytes.NewReader(plain), nil)
		require.NoError(t, err)
		assert.False(t, enc)
		got, err := io.ReadAll(out)
		require.NoError(t, err)
		assert.Equal(t, len(plain), len(got))
		assert.True(t, bytes.Equal(plain, got))
	}
}

// A missing key, a wrong key, a damaged byte, a file cut short or with a chunk
// dropped: none of them reads as a good backup.
func TestUnsealRefusesTampering(t *testing.T) {
	key := testKey(t)
	plain := bytes.Repeat([]byte("backup"), sealedChunk)
	r, err := Seal(bytes.NewReader(plain), key)
	require.NoError(t, err)
	sealed, err := io.ReadAll(r)
	require.NoError(t, err)

	_, _, err = Unseal(bytes.NewReader(sealed), []BackupKey{testKey(t)})
	assert.True(t, errors.Is(err, ErrNoBackupKey))

	wrong := BackupKey{ID: key.ID, Key: testKey(t).Key}
	read := func(b []byte, keys ...BackupKey) error {
		out, _, err := Unseal(bytes.NewReader(b), keys)
		if err != nil {
			return err
		}
		_, err = io.ReadAll(out)
		return err
	}
	assert.Error(t, read(sealed, wrong), "a key with the right ID but other bytes")
	flipped := append([]byte(nil), sealed...)
	flipped[len(flipped)/2] ^= 1
	assert.Error(t, read(flipped, key), "a damaged byte")
	assert.Error(t, read(sealed[:len(sealed)-100], key), "cut inside the last chunk")
	chunk := sealedChunk + 16
	assert.Error(t, read(sealed[:sealedHeaderLen+chunk], key), "cut at a chunk boundary")
	dropped := append(append([]byte(nil), sealed[:sealedHeaderLen]...), sealed[sealedHeaderLen+chunk:]...)
	assert.Error(t, read(dropped, key), "a chunk dropped")
	assert.NoError(t, read(sealed, key))
}

type memStore struct {
	Store
	files map[string][]byte
	sizes map[string]int64
}

func (m *memStore) Upload(_ context.Context, key string, body io.Reader, size int64, _ string) error {
	b, err := io.ReadAll(body)
	m.files[key], m.sizes[key] = b, size
	return err
}

func (m *memStore) Download(_ context.Context, key string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(m.files[key])), nil
}

// A sealed store encrypts what it uploads with its current key and reads back
// files under an older key and files stored plain.
func TestSealedStore(t *testing.T) {
	old, cur := testKey(t), testKey(t)
	mem := &memStore{files: map[string][]byte{}, sizes: map[string]int64{}}
	ctx := context.Background()
	require.NoError(t, (&SealedStore{Store: mem, Key: &old}).Upload(ctx, "a", bytes.NewReader([]byte("first")), 5, ""))
	require.NoError(t, mem.Upload(ctx, "plain", bytes.NewReader([]byte("plain")), 5, ""))
	s := &SealedStore{Store: mem, Key: &cur, Keys: []BackupKey{old, cur}}
	require.NoError(t, s.Upload(ctx, "b", bytes.NewReader([]byte("second")), 6, ""))
	assert.Equal(t, int64(len(mem.files["b"])), mem.sizes["b"], "the size sent is the encrypted size")
	assert.Equal(t, cur.ID, s.SealingKeyID())
	for _, c := range []struct{ key, want string }{{"a", "first"}, {"b", "second"}, {"plain", "plain"}} {
		key, want := c.key, c.want
		rc, err := s.Download(ctx, key)
		require.NoError(t, err)
		got, err := io.ReadAll(rc)
		require.NoError(t, err)
		assert.Equal(t, want, string(got))
	}
}
