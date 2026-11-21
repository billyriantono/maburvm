package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// DirStore keeps backups in a directory on a file system mounted on the node
// (NFS, CIFS, a local disk). Every call checks the mount is still there: with
// it gone the directory would be on the root file system, and filling that
// stops the node.
type DirStore struct{ base string }

// rootDevice is the device of "/"; replaced in tests.
var rootDevice = func() (uint64, error) { return deviceOf("/") }

// CheckMount refuses a directory that is not on a mounted file system other
// than the root one, or that does not exist.
func CheckMount(dir string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != strings.TrimRight(dir, "/") || dir == "/" {
		return refused("%q must be an absolute path to a directory, not /", dir)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return refused("%s does not exist on this node; mount the backup file system there first", dir)
		}
		return refused("%s cannot be read: %v", dir, err)
	}
	if !fi.IsDir() {
		return refused("%s is not a directory", dir)
	}
	dev, err := deviceOf(dir)
	if err != nil {
		return refused("%s cannot be checked: %v", dir, err)
	}
	root, err := rootDevice()
	if err != nil {
		return refused("the root file system cannot be checked: %v", err)
	}
	if dev == root {
		return refused("%s is on this node's root file system, not a mounted one; mount the backup file system there (or it is not mounted right now)", dir)
	}
	return nil
}

// OpenDir checks dir and returns the store in it.
func OpenDir(dir string) (*DirStore, error) {
	dir = strings.TrimRight(strings.TrimSpace(dir), "/")
	if err := CheckMount(dir); err != nil {
		return nil, err
	}
	return &DirStore{base: dir}, nil
}

func (d *DirStore) file(key string) (string, error) {
	if err := CheckMount(d.base); err != nil {
		return "", err
	}
	k, err := cleanKey(key)
	if err != nil {
		return "", err
	}
	p := filepath.Join(d.base, filepath.FromSlash(k))
	return p, d.inside(filepath.Dir(p))
}

// inside refuses a directory that a symbolic link in the store leads out of
// it: whoever controls the mounted file system must not steer the node's
// writes and deletes onto its own disks.
func (d *DirStore) inside(dir string) error {
	// The deepest part that exists is what a link could lead elsewhere.
	real, err := filepath.EvalSymlinks(dir)
	for errors.Is(err, fs.ErrNotExist) && len(dir) > len(d.base) {
		dir = filepath.Dir(dir)
		real, err = filepath.EvalSymlinks(dir)
	}
	if err != nil {
		return dirErr("resolve", dir, err)
	}
	base, err := filepath.EvalSymlinks(d.base)
	if err != nil {
		return dirErr("resolve", d.base, err)
	}
	if real != base && !strings.HasPrefix(real, base+string(filepath.Separator)) {
		return refused("%s leads out of %s", dir, d.base)
	}
	return nil
}

func dirErr(op, p string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return refused("%s %s: permission denied", op, p)
	}
	return fmt.Errorf("%s %s: %w", op, p, err)
}

// Upload writes beside the final name, flushes it and renames it there.
func (d *DirStore) Upload(ctx context.Context, key string, body io.Reader, size int64, _ string) error {
	p, err := d.file(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return dirErr("create directory", filepath.Dir(p), err)
	}
	if err := d.inside(filepath.Dir(p)); err != nil {
		return err
	}
	tmp := p + ".part"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return dirErr("create", tmp, err)
	}
	n, werr := io.Copy(f, ctxReader{ctx, body})
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil && size > 0 && n != size {
		werr = fmt.Errorf("wrote %d bytes of %d", n, size)
	}
	if werr == nil {
		werr = os.Rename(tmp, p)
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return dirErr("write", p, werr)
	}
	return nil
}

func (d *DirStore) Download(_ context.Context, key string) (io.ReadCloser, error) {
	p, err := d.file(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, dirErr("open", p, err)
	}
	return f, nil
}

func (d *DirStore) GetSize(_ context.Context, key string) (int64, error) {
	p, err := d.file(key)
	if err != nil {
		return 0, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return 0, dirErr("stat", p, err)
	}
	return fi.Size(), nil
}

func (d *DirStore) Exists(_ context.Context, key string) (bool, error) {
	p, err := d.file(key)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, dirErr("stat", p, err)
	}
	return true, nil
}

func (d *DirStore) Delete(_ context.Context, key string) error {
	p, err := d.file(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return dirErr("delete", p, err)
	}
	return nil
}

func (d *DirStore) Close() error { return nil }

// ctxReader stops a copy when its context ends.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
