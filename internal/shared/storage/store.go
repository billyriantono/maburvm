package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// Store is where backup files are kept: an S3 bucket, an SFTP server or a
// directory mounted on a node. Keys are slash-separated paths inside it.
type Store interface {
	Upload(ctx context.Context, key string, body io.Reader, size int64, contentType string) error
	Download(ctx context.Context, key string) (io.ReadCloser, error)
	GetSize(ctx context.Context, key string) (int64, error)
	Exists(ctx context.Context, key string) (bool, error)
	// Delete succeeds for a key that is not there, as S3 does.
	Delete(ctx context.Context, key string) error
	Close() error
}

// Kinds of backup destination.
const (
	KindS3   = "s3"
	KindSFTP = "sftp"
	KindPath = "path"
)

// SFTPTarget is an SSH server backups are copied to.
type SFTPTarget struct {
	Host string
	Port int
	User string
	// Password or PrivateKey (PEM), one of them.
	Password   string
	PrivateKey string
	// HostKey is the server's public key, as "ssh-ed25519 AAAA..." or a line
	// of ssh-keyscan output. Any other key is refused.
	HostKey string
	// Path is the directory backups go under, absolute or from the login's home.
	Path string
}

// Target is a backup destination in the one form every store opens from.
type Target struct {
	Kind string // "" means S3
	S3   Destination
	SFTP SFTPTarget
	// Path is, for KindPath, a directory on a file system mounted on the node.
	Path string
}

// Open connects to the destination.
func (t Target) Open(ctx context.Context) (Store, error) {
	switch t.Kind {
	case "", KindS3:
		return t.S3.Client()
	case KindSFTP:
		return DialSFTP(ctx, t.SFTP)
	case KindPath:
		return OpenDir(t.Path)
	}
	return nil, fmt.Errorf("unknown backup destination kind %q", t.Kind)
}

// RefusedError is a store that answered and said no: wrong credentials, a
// host key that does not match, a directory not mounted. Like an S3 error code
// it does not change on its own, so Rejected reports it and nothing retries it.
type RefusedError struct{ Err error }

func (e *RefusedError) Error() string     { return e.Err.Error() }
func (e *RefusedError) Unwrap() error     { return e.Err }
func (e *RefusedError) ErrorCode() string { return "Refused" }

func refused(format string, a ...any) error {
	return &RefusedError{Err: fmt.Errorf(format, a...)}
}

// cleanKey checks a key names a file inside the store, never above it.
func cleanKey(key string) (string, error) {
	k := path.Clean("/" + strings.TrimSpace(key))[1:]
	if k == "" || k != strings.TrimPrefix(strings.TrimSpace(key), "/") || strings.HasPrefix(k, "../") {
		return "", fmt.Errorf("invalid key %q", key)
	}
	return k, nil
}

// ProbeStore proves a store can be used: it writes a small file at key, reads
// it back and deletes it.
func ProbeStore(ctx context.Context, s Store, key string) error {
	body := []byte("backup destination test " + key)
	if err := s.Upload(ctx, key, strings.NewReader(string(body)), int64(len(body)), "text/plain"); err != nil {
		return &ProbeError{Step: ProbeWrite, Err: err}
	}
	rc, err := s.Download(ctx, key)
	if err == nil {
		var got []byte
		got, err = io.ReadAll(io.LimitReader(rc, int64(len(body))+1))
		rc.Close()
		if err == nil && string(got) != string(body) {
			err = errors.New("the file read back is not the one written")
		}
	}
	// Deleted even when the read failed, so a failed test leaves nothing behind.
	delErr := s.Delete(ctx, key)
	if err != nil {
		return &ProbeError{Step: ProbeRead, Err: err}
	}
	if delErr != nil {
		return &ProbeError{Step: ProbeDelete, Err: delErr}
	}
	return nil
}
