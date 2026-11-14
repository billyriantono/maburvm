package storage

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestDownloadTemplateSurvivesABrokenAndAStalledTransfer(t *testing.T) {
	image := bytes.Repeat([]byte("0123456789abcdef"), 8192)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "131072")
		switch calls.Add(1) {
		case 1: // the connection drops part-way
			w.Write(image[:5000])
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
			}
		case 2: // the transfer goes quiet
			w.Write(image[:5000])
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		default:
			w.Write(image)
		}
	}))
	defer srv.Close()

	tm := NewTemplateManager()
	tm.stall = 300 * time.Millisecond
	dest := filepath.Join(t.TempDir(), "image.img")
	if err := tm.DownloadTemplate(srv.URL, dest); err != nil {
		t.Fatalf("download: %v", err)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, image) {
		t.Errorf("got %d bytes, want the %d byte image", len(got), len(image))
	}
	if calls.Load() != 3 {
		t.Errorf("server saw %d requests, want 3", calls.Load())
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), ".download-*")); len(left) != 0 {
		t.Errorf("partial downloads left behind: %v", left)
	}
}

func TestDownloadTemplateDoesNotRetryAMissingFile(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	if err := NewTemplateManager().DownloadTemplate(srv.URL, filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("a 404 was accepted")
	}
	if calls.Load() != 1 {
		t.Errorf("a 404 was asked for %d times", calls.Load())
	}
}
