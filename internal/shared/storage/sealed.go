package storage

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
)

// Backups can be encrypted before they leave the node, so a store and anyone
// holding its credential see only ciphertext. The format is self-describing:
//
//	header: "MBVMENC1" | key ID (16 bytes, a UUID) | salt (32 bytes)
//	chunks: AES-256-GCM of up to 64 KiB each, tag appended
//
// Each file gets its own key, derived with HKDF-SHA256 from the destination's
// key and the salt, so the nonce can be a plain counter. The nonce's last byte
// marks the final chunk, so a file cut short does not decrypt. Every chunk
// authenticates the header too. A reader given a file without the header
// passes it through, so backups made before encryption still restore.

// SealedMagic starts every encrypted backup file.
const SealedMagic = "MBVMENC1"

const (
	sealedChunk     = 64 << 10
	sealedHeaderLen = len(SealedMagic) + 16 + 32
	sealedKeyLen    = 32
)

// BackupKey is one of a destination's encryption keys.
type BackupKey struct {
	ID  string // a UUID
	Key []byte // 32 bytes
}

// ErrNoBackupKey is a file encrypted with a key that was not given.
var ErrNoBackupKey = errors.New("the backup is encrypted with a key the panel did not send")

// SealedSize is how big a file of plain bytes is once encrypted.
func SealedSize(plain int64) int64 {
	chunks := (plain + sealedChunk - 1) / sealedChunk
	if chunks == 0 {
		chunks = 1
	}
	return int64(sealedHeaderLen) + plain + chunks*16
}

func fileAEAD(key, salt []byte) (cipher.AEAD, error) {
	if len(key) != sealedKeyLen {
		return nil, fmt.Errorf("a backup key must be %d bytes", sealedKeyLen)
	}
	k, err := hkdf.Key(sha256.New, key, salt, "maburvm backup v1", sealedKeyLen)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func chunkNonce(n uint64, last bool) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[3:11], n)
	if last {
		nonce[11] = 1
	}
	return nonce
}

// Seal returns a reader of r's bytes encrypted with key.
func Seal(r io.Reader, key BackupKey) (io.Reader, error) {
	id, err := uuid.Parse(key.ID)
	if err != nil {
		return nil, fmt.Errorf("backup key ID: %w", err)
	}
	header := make([]byte, 0, sealedHeaderLen)
	header = append(header, SealedMagic...)
	header = append(header, id[:]...)
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	header = append(header, salt...)
	aead, err := fileAEAD(key.Key, salt)
	if err != nil {
		return nil, err
	}
	return &sealer{src: bufio.NewReaderSize(r, sealedChunk+1), aead: aead, header: header, out: header,
		sealed: make([]byte, 0, sealedChunk+16)}, nil
}

type sealer struct {
	src    *bufio.Reader
	aead   cipher.AEAD
	header []byte
	out    []byte // what is ready to be read
	sealed []byte // the chunk buffer out points into
	n      uint64
	done   bool
	buf    [sealedChunk]byte
}

func (s *sealer) Read(p []byte) (int, error) {
	for len(s.out) == 0 {
		if s.done {
			return 0, io.EOF
		}
		k, err := io.ReadFull(s.src, s.buf[:])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return 0, err
		}
		// The last chunk is the one nothing follows: a full one at the very end
		// of the input is the last too.
		last := k < sealedChunk
		if !last {
			if _, perr := s.src.Peek(1); perr == io.EOF {
				last = true
			} else if perr != nil {
				return 0, perr
			}
		}
		s.sealed = s.aead.Seal(s.sealed[:0], chunkNonce(s.n, last), s.buf[:k], s.header)
		s.out, s.n, s.done = s.sealed, s.n+1, last
	}
	n := copy(p, s.out)
	s.out = s.out[n:]
	return n, nil
}

// Unseal returns a reader of r decrypted with whichever of keys its header
// names. A file without the header is returned as it is, so backups made
// before encryption read the same. encrypted reports which it was.
func Unseal(r io.Reader, keys []BackupKey) (out io.Reader, encrypted bool, err error) {
	br := bufio.NewReaderSize(r, sealedChunk+16+1)
	head, err := br.Peek(sealedHeaderLen)
	if !bytes.HasPrefix(head, []byte(SealedMagic)) {
		if err != nil && err != io.EOF && err != bufio.ErrBufferFull {
			return nil, false, err
		}
		return br, false, nil
	}
	if err != nil {
		return nil, true, fmt.Errorf("the encrypted backup is cut short: %w", err)
	}
	header := append([]byte(nil), head...)
	_, _ = br.Discard(sealedHeaderLen)
	id, _ := uuid.FromBytes(header[len(SealedMagic) : len(SealedMagic)+16])
	var key []byte
	for _, k := range keys {
		if kid, perr := uuid.Parse(k.ID); perr == nil && kid == id {
			key = k.Key
			break
		}
	}
	if key == nil {
		return nil, true, fmt.Errorf("%w (key %s)", ErrNoBackupKey, id)
	}
	aead, err := fileAEAD(key, header[len(SealedMagic)+16:])
	if err != nil {
		return nil, true, err
	}
	return &unsealer{src: br, aead: aead, header: header}, true, nil
}

type unsealer struct {
	src    *bufio.Reader
	aead   cipher.AEAD
	header []byte
	out    []byte
	n      uint64
	done   bool
	buf    [sealedChunk + 16]byte
}

func (u *unsealer) Read(p []byte) (int, error) {
	for len(u.out) == 0 {
		if u.done {
			return 0, io.EOF
		}
		k, err := io.ReadFull(u.src, u.buf[:])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return 0, err
		}
		last := k < len(u.buf)
		if !last {
			if _, perr := u.src.Peek(1); perr == io.EOF {
				last = true
			} else if perr != nil {
				return 0, perr
			}
		}
		plain, oerr := u.aead.Open(u.buf[:0], chunkNonce(u.n, last), u.buf[:k], u.header)
		if oerr != nil {
			return 0, fmt.Errorf("the encrypted backup is damaged or cut short at chunk %d", u.n)
		}
		u.out, u.n, u.done = plain, u.n+1, last
	}
	n := copy(p, u.out)
	u.out = u.out[n:]
	return n, nil
}

// SealedStore is a store whose uploads are encrypted with Key, when set, and
// whose downloads are decrypted with whichever of Keys a file names. Files
// stored without encryption download as they are.
type SealedStore struct {
	Store
	Key  *BackupKey
	Keys []BackupKey
}

// SealingKeyID is the key uploads are encrypted with; "" when they are not.
func (s *SealedStore) SealingKeyID() string {
	if s.Key == nil {
		return ""
	}
	return s.Key.ID
}

// Upload encrypts body on its way to the store.
func (s *SealedStore) Upload(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	if s.Key == nil {
		return s.Store.Upload(ctx, key, body, size, contentType)
	}
	// ponytail: the sealed stream cannot seek, so the S3 uploader buffers each
	// part in memory (about four 64 MiB parts) instead of reading the file in
	// place; an io.ReaderAt over the chunks would avoid it if node memory is short.
	sealed, err := Seal(body, *s.Key)
	if err != nil {
		return err
	}
	return s.Store.Upload(ctx, key, sealed, SealedSize(size), "application/octet-stream")
}

// Download decrypts what it reads when the file is encrypted.
func (s *SealedStore) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, err := s.Store.Download(ctx, key)
	if err != nil {
		return nil, err
	}
	r, _, err := Unseal(rc, s.Keys)
	if err != nil {
		rc.Close()
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{r, rc}, nil
}
