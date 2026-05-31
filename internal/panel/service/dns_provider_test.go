package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
)

func TestRecordFQDN(t *testing.T) {
	require.Equal(t, "example.com.", recordFQDN("example.com", "@"))
	require.Equal(t, "example.com.", recordFQDN("example.com", ""))
	require.Equal(t, "www.example.com.", recordFQDN("example.com", "www"))
	require.Equal(t, "www.example.com.", recordFQDN("example.com", "www.example.com"))
	require.Equal(t, "absolute.test.", recordFQDN("example.com", "absolute.test."))
}

func TestBuildRRsets(t *testing.T) {
	zone := &models.DNSZone{Name: "example.com"}
	records := []models.DNSRecord{
		{Name: "@", Type: "A", Content: "203.0.113.1", TTL: 3600},
		{Name: "@", Type: "A", Content: "203.0.113.2", TTL: 3600}, // grouped with the first
		{Name: "www", Type: "CNAME", Content: "example.com", TTL: 300},
		{Name: "@", Type: "MX", Content: "mail.example.com", Priority: 10, TTL: 3600},
		{Name: "@", Type: "TXT", Content: "v=spf1 -all", TTL: 3600},
	}
	rrsets := buildRRsets(zone, records)

	byKey := map[string]pdnsRRset{}
	for _, rr := range rrsets {
		byKey[rr.Name+"|"+rr.Type] = rr
	}

	apexA := byKey["example.com.|A"]
	require.Len(t, apexA.Records, 2, "two A records group into one rrset")
	require.Equal(t, "REPLACE", apexA.ChangeType)

	cname := byKey["www.example.com.|CNAME"]
	require.Equal(t, "example.com.", cname.Records[0].Content, "CNAME target gets a trailing dot")

	mx := byKey["example.com.|MX"]
	require.Equal(t, "10 mail.example.com.", mx.Records[0].Content, "MX content is 'priority host.'")

	txt := byKey["example.com.|TXT"]
	require.Equal(t, "\"v=spf1 -all\"", txt.Records[0].Content, "TXT content is quoted")
}

func TestReverseZoneName(t *testing.T) {
	z, err := reverseZoneName("203.0.113.10")
	require.NoError(t, err)
	require.Equal(t, "113.0.203.in-addr.arpa.", z, "IPv4 /24 reverse zone")

	if _, err := reverseZoneName("nope"); err == nil {
		t.Error("expected error for invalid IP")
	}

	z6, err := reverseZoneName("2001:db8::1")
	require.NoError(t, err)
	require.Contains(t, z6, "ip6.arpa.")
}

func TestPDNSProviderSetPTR(t *testing.T) {
	var gotPatch pdnsZone
	created := false

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/servers/localhost/zones", func(w http.ResponseWriter, r *http.Request) {
		// Create the reverse zone when missing.
		require.Equal(t, http.MethodPost, r.Method)
		var z pdnsZone
		_ = json.NewDecoder(r.Body).Decode(&z)
		require.Equal(t, "113.0.203.in-addr.arpa.", z.Name)
		created = true
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/api/v1/servers/localhost/zones/113.0.203.in-addr.arpa.", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			http.Error(w, "not found", http.StatusNotFound) // zone absent → triggers create
		case http.MethodPatch:
			require.NoError(t, json.NewDecoder(r.Body).Decode(&gotPatch))
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := &pdnsProvider{baseURL: srv.URL, apiKey: "secret", serverID: "localhost", http: srv.Client()}
	require.NoError(t, p.SetPTR(context.Background(), "203.0.113.10", "vm1.example.com"))

	require.True(t, created, "reverse zone is auto-created when missing")
	require.Len(t, gotPatch.RRsets, 1)
	require.Equal(t, "10.113.0.203.in-addr.arpa.", gotPatch.RRsets[0].Name, "full reversed PTR name")
	require.Equal(t, "PTR", gotPatch.RRsets[0].Type)
	require.Equal(t, "REPLACE", gotPatch.RRsets[0].ChangeType)
	require.Equal(t, "vm1.example.com.", gotPatch.RRsets[0].Records[0].Content, "PTR target is fully-qualified")
}

func TestPDNSProviderSyncZoneCreatesAndPatches(t *testing.T) {
	var gotPatch pdnsZone
	created := false

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/servers/localhost/zones", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "secret", r.Header.Get("X-API-Key"))
		created = true
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/api/v1/servers/localhost/zones/example.com.", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			http.Error(w, "not found", http.StatusNotFound) // zone doesn't exist yet
		case http.MethodPatch:
			require.NoError(t, json.NewDecoder(r.Body).Decode(&gotPatch))
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := &pdnsProvider{baseURL: srv.URL, apiKey: "secret", serverID: "localhost", http: srv.Client()}
	zone := &models.DNSZone{Name: "example.com", PrimaryNS: "ns1.example.com"}
	records := []models.DNSRecord{
		{Name: "www", Type: "A", Content: "203.0.113.2", TTL: 300},
		{Name: "@", Type: "MX", Content: "mail.example.com", Priority: 10, TTL: 3600},
	}
	require.NoError(t, p.SyncZone(context.Background(), zone, records))
	require.True(t, created, "missing zone should be created")
	require.Len(t, gotPatch.RRsets, 2)
}

func TestPDNSProviderSyncZoneDeletesStale(t *testing.T) {
	existing := pdnsZone{RRsets: []pdnsRRset{
		{Name: "example.com.", Type: "SOA", Records: []pdnsRecord{{Content: "ns1.example.com. host.example.com. 1 1 1 1 1"}}},
		{Name: "example.com.", Type: "NS", Records: []pdnsRecord{{Content: "ns1.example.com."}}},
		{Name: "www.example.com.", Type: "A", Records: []pdnsRecord{{Content: "1.1.1.1"}}},
		{Name: "old.example.com.", Type: "A", Records: []pdnsRecord{{Content: "9.9.9.9"}}},
	}}
	var gotPatch pdnsZone

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/servers/localhost/zones/example.com.", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(existing)
		case http.MethodPatch:
			require.NoError(t, json.NewDecoder(r.Body).Decode(&gotPatch))
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	p := &pdnsProvider{baseURL: srv.URL, apiKey: "secret", serverID: "localhost", http: srv.Client()}
	zone := &models.DNSZone{Name: "example.com", PrimaryNS: "ns1.example.com"}
	// Desired keeps only www; "old" must be deleted, SOA + apex NS must be left alone.
	records := []models.DNSRecord{{Name: "www", Type: "A", Content: "203.0.113.2", TTL: 300}}
	require.NoError(t, p.SyncZone(context.Background(), zone, records))

	changes := map[string]string{} // "name|type" -> changetype
	for _, rr := range gotPatch.RRsets {
		changes[rr.Name+"|"+rr.Type] = rr.ChangeType
	}
	require.Equal(t, "REPLACE", changes["www.example.com.|A"])
	require.Equal(t, "DELETE", changes["old.example.com.|A"], "stale record is deleted")
	require.NotContains(t, changes, "example.com.|SOA", "SOA is never touched")
	require.NotContains(t, changes, "example.com.|NS", "apex NS is left to PowerDNS")
}
