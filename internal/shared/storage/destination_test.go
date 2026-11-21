package storage

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeStore is the part of an S3-compatible store a backup needs: put, get,
// head and delete of whole objects, addressed path-style.
type fakeStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	// refuse makes every request for that method fail the way a store does
	// when the credentials may not do it.
	refuse map[string]bool
}

func newFakeStore(t *testing.T) (*fakeStore, *Client) {
	t.Helper()
	fs := &fakeStore{objects: map[string][]byte{}, refuse: map[string]bool{}}
	srv := httptest.NewServer(fs)
	t.Cleanup(srv.Close)
	c, err := Destination{Endpoint: srv.URL, Bucket: "bucket", AccessKey: "ak", SecretKey: "sk"}.Client()
	if err != nil {
		t.Fatal(err)
	}
	return fs, c
}

func (f *fakeStore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse[r.Method] {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`)
		return
	}
	key := r.URL.Path
	switch r.Method {
	case http.MethodPut:
		body, err := readS3Body(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.objects[key] = body
	case http.MethodGet, http.MethodHead:
		body, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method == http.MethodGet {
			w.Write(body)
		}
	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// readS3Body returns an upload's payload, unwrapping the SDK's chunked
// signing when it is used.
func readS3Body(r *http.Request) ([]byte, error) {
	if !strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") &&
		!strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") {
		return io.ReadAll(r.Body)
	}
	var out []byte
	br := bufio.NewReader(r.Body)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		size, err := strconv.ParseInt(strings.TrimSpace(strings.SplitN(line, ";", 2)[0]), 16, 64)
		if err != nil {
			return nil, err
		}
		if size == 0 {
			return out, nil
		}
		chunk := make([]byte, size)
		if _, err := io.ReadFull(br, chunk); err != nil {
			return nil, err
		}
		out = append(out, chunk...)
		br.ReadString('\n')
	}
}

func TestProbeWritesReadsAndDeletes(t *testing.T) {
	fs, c := newFakeStore(t)
	if err := c.Probe(context.Background(), "prefix/.test"); err != nil {
		t.Fatalf("probe of a working store: %v", err)
	}
	if len(fs.objects) != 0 {
		t.Fatalf("the probe left %d object(s) behind", len(fs.objects))
	}
}

func TestProbeNamesTheFailingStep(t *testing.T) {
	for method, step := range map[string]string{http.MethodPut: ProbeWrite, http.MethodGet: ProbeRead, http.MethodDelete: ProbeDelete} {
		fs, c := newFakeStore(t)
		fs.refuse[method] = true
		err := c.Probe(context.Background(), "k")
		var pe *ProbeError
		if !errors.As(err, &pe) || pe.Step != step {
			t.Fatalf("refusing %s: got %v, want a failure at %q", method, err, step)
		}
		if !Rejected(err) {
			t.Fatalf("refusing %s: a store that answered 403 was not reported as a refusal: %v", method, err)
		}
	}
}

func TestUnreachableStoreIsNotARefusal(t *testing.T) {
	c, err := Destination{Endpoint: "http://127.0.0.1:1", Bucket: "b", AccessKey: "ak", SecretKey: "sk"}.Client()
	if err != nil {
		t.Fatal(err)
	}
	err = c.Probe(context.Background(), "k")
	if err == nil || Rejected(err) {
		t.Fatalf("a store that cannot be reached must fail without counting as a refusal: %v", err)
	}
}

func TestExistsTellsMissingFromFailed(t *testing.T) {
	fs, c := newFakeStore(t)
	ctx := context.Background()
	if ok, err := c.Exists(ctx, "nope"); err != nil || ok {
		t.Fatalf("missing object: ok=%v err=%v", ok, err)
	}
	fs.objects["/bucket/there"] = []byte("x")
	if ok, err := c.Exists(ctx, "there"); err != nil || !ok {
		t.Fatalf("present object: ok=%v err=%v", ok, err)
	}
	fs.refuse[http.MethodHead] = true
	if _, err := c.Exists(ctx, "there"); err == nil {
		t.Fatal("a refused request was reported as a missing object")
	}
}

func TestDestinationNeedsBucketAndKeys(t *testing.T) {
	for _, d := range []Destination{{}, {Bucket: "b"}, {Bucket: "b", AccessKey: "a"}, {AccessKey: "a", SecretKey: "s"}} {
		if _, err := d.Client(); !errors.Is(err, ErrDestinationIncomplete) {
			t.Fatalf("%+v: got %v", d, err)
		}
	}
}

func TestJoinKey(t *testing.T) {
	for _, c := range [][3]string{
		{"", "backups/a", "backups/a"},
		{"vps", "backups/a", "vps/backups/a"},
		{"/vps/prod/", "backups/a", "vps/prod/backups/a"},
		{" / ", "backups/a", "backups/a"},
	} {
		if got := JoinKey(c[0], c[1]); got != c[2] {
			t.Fatalf("JoinKey(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}
