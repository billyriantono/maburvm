package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// cacheDownloadName matches the names the agent gives the files it downloads
// into the template cache (resolveTemplateImage): <12 hex>-<name> for a URL,
// img-<16 hex>.qcow2 for a stored image, clone-<id>.qcow2 for a disk pulled
// from another node, each with .part while it is being written. Anything else
// in the directory was put there by hand, such as a Windows template built on
// the node, and cannot be fetched again.
var cacheDownloadName = regexp.MustCompile(`^([0-9a-f]{12}-.+|img-[0-9a-f]{16}\.qcow2(\.part)?|clone-[^/]+\.qcow2)$`)

// IsCacheDownload reports whether a file name in the template cache is one the
// agent downloaded, and so may remove to make room.
func IsCacheDownload(name string) bool { return cacheDownloadName.MatchString(name) }

// EvictLRU removes the least recently used downloads directly in dir, oldest
// first, until those left total at most maxBytes. Only files the agent
// downloaded count and are removed (IsCacheDownload); one last used within
// keep (a download in progress, a template a create is copying) or one inUse
// reports is never removed, and subdirectories are not looked into. It returns
// what it removed and the bytes of downloads left, which stay above maxBytes
// when the rest is in use.
func EvictLRU(dir string, maxBytes int64, keep time.Duration, inUse func(path string) bool) (removed []string, left int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0
	}
	type file struct {
		path  string
		size  int64
		mtime time.Time
	}
	var files []file
	for _, e := range entries {
		if !IsCacheDownload(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		files = append(files, file{filepath.Join(dir, e.Name()), info.Size(), info.ModTime()})
		left += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mtime.Before(files[j].mtime) })
	for _, f := range files {
		if left <= maxBytes {
			break
		}
		if time.Since(f.mtime) < keep || inUse(f.path) {
			continue
		}
		if os.Remove(f.path) == nil {
			removed = append(removed, f.path)
			left -= f.size
		}
	}
	return removed, left
}

// TemplateCacheName is the file a template downloaded from url is cached as:
// <12 hex of the url>-<name>, with the first 16 hex of the image's sha256
// between them when it is known. A refreshed image (a new checksum for the same
// url) is then a new file, and the copy VMs were built from is untouched.
func TemplateCacheName(url, sha256 string) string {
	sum := sha256Sum([]byte(url))
	base := filepath.Base(url)
	if i := strings.IndexAny(base, "?#"); i >= 0 {
		base = base[:i]
	}
	if base == "" || base == "/" || base == "." {
		base = "image.qcow2"
	}
	// An archive is unpacked, so the cached file is the image itself.
	base = strings.TrimSuffix(base, ".zip")
	if len(sha256) >= 16 {
		return fmt.Sprintf("%x-%s-%s", sum[:6], strings.ToLower(sha256[:16]), base)
	}
	return fmt.Sprintf("%x-%s", sum[:6], base)
}

// StaleTemplateCopies are the other cached copies of the image at url in dir:
// the ones TemplateCacheName gave it under an earlier checksum, or none.
// Partial downloads are left to their own download.
func StaleTemplateCopies(dir, url, keep string) []string {
	sum := sha256Sum([]byte(url))
	prefix := fmt.Sprintf("%x-", sum[:6])
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if n == keep || !strings.HasPrefix(n, prefix) || strings.HasSuffix(n, ".part") || !e.Type().IsRegular() {
			continue
		}
		out = append(out, filepath.Join(dir, n))
	}
	return out
}

// UploadCacheName is where an image uploaded to the panel is kept in the
// template cache. It is never evicted: the node cannot fetch it again.
func UploadCacheName(sha256, name string) string {
	return "upload-" + strings.ToLower(sha256[:16]) + "-" + name
}

// FileSHA256 is the hex sha256 of a file and its length.
func FileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// VerifySHA256 checks a file against the sha256 it must have.
func VerifySHA256(path, want string) error {
	got, _, err := FileSHA256(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch: the image should have sha256 %s and has %s", strings.ToLower(want), got)
	}
	return nil
}

func sha256Sum(b []byte) [32]byte { return sha256.Sum256(b) }
