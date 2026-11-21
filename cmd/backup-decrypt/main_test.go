package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/shared/storage"
)

// An exported key list (bare or as the endpoint answered) decrypts a file.
func TestRun(t *testing.T) {
	dir := t.TempDir()
	key := storage.BackupKey{ID: uuid.NewString(), Key: make([]byte, 32)}
	_, _ = rand.Read(key.Key)
	plain := bytes.Repeat([]byte("qcow2"), 50000)
	sealed, err := storage.Seal(bytes.NewReader(plain), key)
	require.NoError(t, err)
	enc, err := io.ReadAll(sealed)
	require.NoError(t, err)
	in := filepath.Join(dir, "b.enc")
	require.NoError(t, os.WriteFile(in, enc, 0o600))
	for i, list := range []string{
		`[{"id":"` + key.ID + `","key":"` + hex.EncodeToString(key.Key) + `"}]`,
		`{"success":true,"data":[{"id":"` + key.ID + `","key":"` + hex.EncodeToString(key.Key) + `","current":true}]}`,
	} {
		keys := filepath.Join(dir, "keys.json")
		require.NoError(t, os.WriteFile(keys, []byte(list), 0o600))
		out := filepath.Join(dir, "out", uuid.NewString())
		require.NoError(t, os.MkdirAll(filepath.Dir(out), 0o700))
		require.NoError(t, run(keys, in, out), "list %d", i)
		got, err := os.ReadFile(out)
		require.NoError(t, err)
		require.True(t, bytes.Equal(plain, got))
	}
	require.Error(t, run("", in, "-"))
}
