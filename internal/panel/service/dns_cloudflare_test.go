package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/shared/models"
)

// fakeCloudflare is the slice of the Cloudflare API the provider uses, with
// one zone holding whatever records were written.
type fakeCloudflare struct {
	mu      sync.Mutex
	zone    string
	records map[string]cfRecord
	next    int
	calls   []string
}

func (f *fakeCloudflare) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	reply := func(result any) {
		raw, _ := json.Marshal(result)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": json.RawMessage(raw), "result_info": map[string]int{"page": 1, "total_pages": 1}})
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"code": 9109, "message": "Invalid access token"}}})
		return
	}
	switch {
	case r.URL.Path == "/zones":
		if r.URL.Query().Get("name") == f.zone {
			reply([]map[string]string{{"id": "z1"}})
		} else {
			reply([]map[string]string{})
		}
	case r.URL.Path == "/zones/z1/dns_records" && r.Method == http.MethodGet:
		out := []cfRecord{}
		for _, rec := range f.records {
			if (r.URL.Query().Get("type") == "" || rec.Type == r.URL.Query().Get("type")) &&
				(r.URL.Query().Get("name") == "" || rec.Name == r.URL.Query().Get("name")) {
				out = append(out, rec)
			}
		}
		reply(out)
	case r.URL.Path == "/zones/z1/dns_records" && r.Method == http.MethodPost:
		var rec cfRecord
		_ = json.NewDecoder(r.Body).Decode(&rec)
		f.next++
		rec.ID = "r" + string(rune('0'+f.next))
		f.records[rec.ID] = rec
		reply(rec)
	case strings.HasPrefix(r.URL.Path, "/zones/z1/dns_records/"):
		id := strings.TrimPrefix(r.URL.Path, "/zones/z1/dns_records/")
		if r.Method == http.MethodDelete {
			delete(f.records, id)
		} else {
			var rec cfRecord
			_ = json.NewDecoder(r.Body).Decode(&rec)
			rec.ID = id
			f.records[id] = rec
		}
		reply(map[string]string{"id": id})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestCloudflarePTR(t *testing.T) {
	fake := &fakeCloudflare{zone: "0-127.113.0.203.in-addr.arpa", records: map[string]cfRecord{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	p := &cloudflareProvider{baseURL: srv.URL, token: "tok", http: srv.Client()}
	ctx := context.Background()

	rec, err := ptrLocation("203.0.113.10", "0-127.113.0.203.in-addr.arpa")
	require.NoError(t, err)
	require.NoError(t, p.SetPTR(ctx, rec, "mail.example.com."))
	require.NoError(t, p.SetPTR(ctx, rec, "mx.example.com"), "a second set replaces, never duplicates")

	got, err := p.ListPTRs(ctx, "0-127.113.0.203.in-addr.arpa.")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"10.0-127.113.0.203.in-addr.arpa.": "mx.example.com."}, got)

	require.NoError(t, p.ClearPTR(ctx, rec))
	require.Empty(t, fake.records)

	// A zone not on the account is an error to set, and simply empty to list.
	other, _ := ptrLocation("198.51.100.1", "")
	require.ErrorContains(t, p.SetPTR(ctx, other, "x.example.com"), "not on this Cloudflare account")
	empty, err := p.ListPTRs(ctx, "100.51.198.in-addr.arpa.")
	require.NoError(t, err)
	require.Empty(t, empty)

	bad := &cloudflareProvider{baseURL: srv.URL, token: "wrong", http: srv.Client()}
	require.ErrorContains(t, bad.SetPTR(ctx, rec, "x.example.com"), "Invalid access token")
}

// A sync keeps unchanged records, adds new ones, deletes stale ones, and
// leaves the apex NS and record types the panel does not manage alone.
func TestCloudflareSyncZone(t *testing.T) {
	fake := &fakeCloudflare{zone: "example.com", records: map[string]cfRecord{
		"keep":  {ID: "keep", Type: "A", Name: "www.example.com", Content: "203.0.113.1", TTL: 3600},
		"stale": {ID: "stale", Type: "A", Name: "old.example.com", Content: "203.0.113.9", TTL: 3600},
		"ns":    {ID: "ns", Type: "NS", Name: "example.com", Content: "ns1.cloudflare.example", TTL: 3600},
		"caa":   {ID: "caa", Type: "CAA", Name: "example.com", Content: "0 issue \"ca.example\"", TTL: 3600},
	}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	p := &cloudflareProvider{baseURL: srv.URL, token: "tok", http: srv.Client()}

	zone := &models.DNSZone{Name: "example.com"}
	require.NoError(t, p.SyncZone(context.Background(), zone, []models.DNSRecord{
		{Name: "www", Type: "A", Content: "203.0.113.1", TTL: 3600},
		{Name: "@", Type: "MX", Content: "mail.example.com", Priority: 10, TTL: 3600},
		{Name: "_sip._tcp", Type: "SRV", Content: "5 5060 sip.example.com", Priority: 1, TTL: 3600},
		{Name: "@", Type: "NS", Content: "ns1.example.net", TTL: 3600}, // Cloudflare's to run: never sent
	}))
	require.Contains(t, fake.records, "keep")
	require.Contains(t, fake.records, "ns")
	require.Contains(t, fake.records, "caa")
	require.NotContains(t, fake.records, "stale")
	require.Len(t, fake.records, 5)
	for _, c := range fake.calls {
		require.NotEqual(t, "DELETE /zones/z1/dns_records/keep", c, "an unchanged record is not rewritten")
	}
	require.Error(t, p.DeleteZone(context.Background(), "example.com"), "a Cloudflare zone is never deleted by the panel")
}
