package storage

import (
	"testing"
)

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		name      string
		endpoint  string
		forceHTTP bool
		want      string
		wantErr   bool
	}{
		{name: "empty endpoint", endpoint: "", forceHTTP: false, want: ""},
		{name: "scheme-less default https", endpoint: "minio:9000", forceHTTP: false, want: "https://minio:9000"},
		{name: "scheme-less force http", endpoint: "minio:9000", forceHTTP: true, want: "http://minio:9000"},
		{name: "explicit https", endpoint: "https://s3.amazonaws.com", forceHTTP: false, want: "https://s3.amazonaws.com"},
		{name: "explicit http", endpoint: "http://localhost:9000", forceHTTP: true, want: "http://localhost:9000"},
		// Explicit scheme wins even if ForceHTTP is true (no silent downgrade).
		{name: "explicit https ignores force http", endpoint: "https://minio:9000", forceHTTP: true, want: "https://minio:9000"},
		// Unsupported scheme is rejected, not normalized.
		{name: "unsupported scheme", endpoint: "ftp://minio:9000", forceHTTP: false, wantErr: true},
		{name: "file scheme rejected", endpoint: "file:///tmp", forceHTTP: false, wantErr: true},
		// Fail-closed: whitespace must be rejected, NOT trimmed into a different
		// endpoint and NOT reinterpreted as the empty SDK-default.
		{name: "whitespace-only rejected", endpoint: "   ", forceHTTP: false, wantErr: true},
		{name: "leading whitespace rejected", endpoint: "  minio:9000", forceHTTP: false, wantErr: true},
		{name: "trailing whitespace rejected", endpoint: "minio:9000  ", forceHTTP: false, wantErr: true},
		{name: "both-sided whitespace rejected", endpoint: "  minio:9000  ", forceHTTP: true, wantErr: true},
		{name: "whitespace-only with scheme rejected", endpoint: "  https://minio:9000", forceHTTP: false, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeEndpoint(tc.endpoint, tc.forceHTTP)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (endpoint=%q)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("normalizeEndpoint(%q, forceHTTP=%v) = %q; want %q", tc.endpoint, tc.forceHTTP, got, tc.want)
			}
		})
	}
}

func TestNewClientEndpointAndPathStyle(t *testing.T) {
	// Scheme-less + ForceHTTP yields http; path-style honored from config.
	cfg := &Config{
		Endpoint:     "minio:9000",
		AccessKey:    "ak",
		SecretKey:    "sk",
		Bucket:       "b",
		ForceHTTP:    true,
		UsePathStyle: true,
	}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	if client.Endpoint() != "http://minio:9000" {
		t.Fatalf("endpoint = %q; want http://minio:9000", client.Endpoint())
	}
	if !client.UsePathStyle() {
		t.Fatalf("UsePathStyle() = false; want true")
	}
	if !client.ForceHTTP() {
		t.Fatalf("ForceHTTP() = false; want true")
	}

	// Scheme-less default uses https.
	cfg2 := &Config{
		Endpoint:     "s3.amazonaws.com",
		AccessKey:    "ak",
		SecretKey:    "sk",
		Bucket:       "b",
		UsePathStyle: false,
	}
	client2, err := NewClient(cfg2)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	if client2.Endpoint() != "https://s3.amazonaws.com" {
		t.Fatalf("endpoint = %q; want https://s3.amazonaws.com", client2.Endpoint())
	}
	if client2.UsePathStyle() {
		t.Fatalf("UsePathStyle() = true; want false")
	}
}

func TestNewClientInvalidScheme(t *testing.T) {
	cfg := &Config{
		Endpoint:  "ftp://minio:9000",
		AccessKey: "ak",
		SecretKey: "sk",
		Bucket:    "b",
	}
	if _, err := NewClient(cfg); err == nil {
		t.Fatalf("expected error for unsupported scheme, got nil")
	}
}

func TestNewClientWhitespaceEndpointRejected(t *testing.T) {
	// A whitespace-only endpoint must NOT be accepted as the empty SDK-default
	// (which would silently fall back to provider default resolution) and must
	// NOT be trimmed into a usable endpoint. It fails closed.
	cases := []string{"   ", "  minio:9000", "minio:9000  "}
	for _, ep := range cases {
		cfg := &Config{
			Endpoint:  ep,
			AccessKey: "ak",
			SecretKey: "sk",
			Bucket:    "b",
		}
		if _, err := NewClient(cfg); err == nil {
			t.Fatalf("endpoint %q: expected error for whitespace-contaminated endpoint, got nil", ep)
		}
	}
}

func TestNewClientHonorsPathStyleFlag(t *testing.T) {
	// NewClient treats UsePathStyle literally: it does not invent a default.
	// The "default to path-style for legacy MinIO callers" guarantee is owned
	// by the agent/queue env constructors, which set UsePathStyle=true when the
	// flag is omitted. At this layer, an explicit false must stay false.
	cfg := &Config{
		Endpoint:     "minio:9000",
		AccessKey:    "ak",
		SecretKey:    "sk",
		Bucket:       "b",
		UsePathStyle: false,
	}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	if client.UsePathStyle() {
		t.Fatalf("UsePathStyle() = true; want false (flag passed explicitly false)")
	}
}
