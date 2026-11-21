package storage

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
