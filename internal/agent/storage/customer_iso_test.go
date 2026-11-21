package storage

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCustomerISOName(t *testing.T) {
	a := CustomerISOName("https://example.com/isos/debian-12.iso?token=1")
	b := CustomerISOName("https://example.com/isos/debian-12.iso?token=2")
	if a == b {
		t.Error("two URLs share a cache file")
	}
	if !strings.HasSuffix(a, "-debian-12.iso") {
		t.Errorf("name %q lost its readable tail", a)
	}
	for _, raw := range []string{
		"https://example.com/../../etc/passwd",
		"https://example.com/a b'\"<&>.iso",
		"https://example.com/%2e%2e%2f%2e%2e%2fshadow",
		"https://example.com/.hidden",
		"https://example.com/",
		"https://example.com",
		"https://example.com/" + strings.Repeat("x", 500) + ".iso",
		"::not a url::",
	} {
		name := CustomerISOName(raw)
		if name != filepath.Base(name) || strings.HasPrefix(name, ".") || len(name) > 100 {
			t.Errorf("%q gave an unsafe name %q", raw, name)
		}
		if strings.Trim(name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-") != "" {
			t.Errorf("%q gave a name with odd characters: %q", raw, name)
		}
	}
	// Deleting a VM removes its "-seed.iso"; a customer's download must not pass for one.
	for _, raw := range []string{"https://example.com/x-seed.iso", "https://example.com/seed.iso", "https://example.com/" + strings.Repeat("x", 500) + "-seed.iso"} {
		if name := CustomerISOName(raw); strings.HasSuffix(name, "-seed.iso") || len(name) > 100 {
			t.Errorf("%q gave %q, which reads as a VM's own setup disc", raw, name)
		}
	}
}

func TestPruneUnused(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, age time.Duration) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		os.Chtimes(p, when, when)
		return p
	}
	attached := write("aa-attached.iso", 30*24*time.Hour)
	old := write("bb-old.iso", 8*24*time.Hour)
	recent := write("cc-recent.iso", time.Hour)
	downloading := write(".download-1", time.Minute)
	abandoned := write(".download-2", 3*time.Hour)
	// Things that are not this cache's files are never removed.
	outside := filepath.Join(t.TempDir(), "operator.iso")
	os.WriteFile(outside, []byte("x"), 0o644)
	link := filepath.Join(dir, "dd-link.iso")
	os.Symlink(outside, link)
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	nested := filepath.Join(sub, "ee.iso")
	os.WriteFile(nested, []byte("x"), 0o644)
	for _, p := range []string{link, nested} {
		when := time.Now().Add(-30 * 24 * time.Hour)
		os.Chtimes(p, when, when)
	}

	inUse := func(p string) bool { return p == attached }
	exists := func(p string) bool { _, err := os.Lstat(p); return err == nil }

	PruneUnused(dir, inUse, 7*24*time.Hour)
	for p, want := range map[string]bool{attached: true, old: false, recent: true, downloading: true, abandoned: false, outside: true, link: true, nested: true} {
		if exists(p) != want {
			t.Errorf("after the weekly prune %s exists=%v, want %v", filepath.Base(p), exists(p), want)
		}
	}

	// On detach: everything unused goes at once, but never a running download.
	removed := PruneUnused(dir, inUse, 0)
	if len(removed) != 1 || removed[0] != recent {
		t.Errorf("removed %v, want only %s", removed, recent)
	}
	if !exists(attached) || !exists(downloading) || !exists(outside) || !exists(nested) {
		t.Error("the immediate prune removed something still needed")
	}

	if got := PruneUnused(filepath.Join(dir, "missing"), inUse, 0); got != nil {
		t.Errorf("a missing directory removed %v", got)
	}
}

func TestDownloadRefusesAFileOverTheLimit(t *testing.T) {
	big := bytes.Repeat([]byte("0123456789abcdef"), 4096) // 64 KiB
	for name, declare := range map[string]bool{"declared": true, "undeclared": false} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if !declare {
				w.(http.Flusher).Flush() // chunked: no length up front
			}
			w.Write(big)
		}))
		tm := NewTemplateManager()
		tm.maxBytes = 1000
		dest := filepath.Join(t.TempDir(), "x.iso")
		err := tm.DownloadTemplate(srv.URL, dest)
		srv.Close()
		if !errors.Is(err, errDownloadRefused) {
			t.Errorf("%s: err=%v, want a refusal", name, err)
		}
		if calls.Load() != 1 {
			t.Errorf("%s: asked %d times for a file that will never fit", name, calls.Load())
		}
		if left, _ := os.ReadDir(filepath.Dir(dest)); len(left) != 0 {
			t.Errorf("%s: left %d files behind", name, len(left))
		}
	}

	// At the limit exactly is fine.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(big) }))
	defer srv.Close()
	tm := NewTemplateManager()
	tm.maxBytes = int64(len(big))
	if err := tm.DownloadTemplate(srv.URL, filepath.Join(t.TempDir(), "x.iso")); err != nil {
		t.Errorf("a file of exactly the limit: %v", err)
	}
}

func TestDownloadKeepsSpaceFree(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte("small"))
	}))
	defer srv.Close()

	tm := NewTemplateManager()
	tm.minFree = 1 << 62 // more than any disk has
	dest := filepath.Join(t.TempDir(), "x.iso")
	if err := tm.DownloadTemplate(srv.URL, dest); !errors.Is(err, errDownloadRefused) {
		t.Errorf("err=%v, want a refusal", err)
	}
	if calls.Load() != 1 {
		t.Errorf("asked %d times", calls.Load())
	}
	if _, err := os.Stat(dest); err == nil {
		t.Error("the file was kept")
	}

	tm.minFree = 1
	if err := tm.DownloadTemplate(srv.URL, dest); err != nil {
		t.Errorf("with space to spare: %v", err)
	}
}

// The test server is on loopback, which is exactly what a customer's URL must
// not reach.
func TestCustomerDownloaderRefusesInternalAddresses(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte("internal"))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "x.iso")
	start := time.Now()
	err := NewCustomerDownloader(1<<20, 0).DownloadTemplate(srv.URL, dest)
	if !errors.Is(err, errDownloadRefused) {
		t.Errorf("err=%v, want a refusal", err)
	}
	if calls.Load() != 0 {
		t.Error("the internal server was reached")
	}
	if time.Since(start) > time.Second {
		t.Error("a refusal was retried")
	}
	// The operator's own downloader still may: private mirrors are legitimate.
	if err := NewTemplateManager().DownloadTemplate(srv.URL, dest); err != nil {
		t.Errorf("operator download from a private address: %v", err)
	}
}
