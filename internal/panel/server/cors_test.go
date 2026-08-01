package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maburvm/panel/internal/shared/config"
)

// TestParseAllowedOrigins covers the exact CORS policy (Oracle requirement C):
// replace (not append) the localhost fallback, default to localhost when unset,
// and reject wildcard origins fail-closed.
func TestParseAllowedOrigins(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "unset defaults to localhost only",
			in:   "",
			want: []string{"http://localhost:3000"},
		},
		{
			name: "empty after trim defaults to localhost only",
			in:   "   ",
			want: []string{"http://localhost:3000"},
		},
		{
			name: "configured prod origin replaces localhost",
			in:   "https://panel.example.com",
			want: []string{"https://panel.example.com"},
		},
		{
			name: "leading/trailing space and slash normalized, replaces localhost",
			in:   "  https://app.example.com/ ",
			want: []string{"https://app.example.com"},
		},
		{
			name: "multi-origin comma separated preserved, excludes localhost",
			in:   "https://a.example.com,https://b.example.com",
			want: []string{"https://a.example.com", "https://b.example.com"},
		},
		{
			name: "wildcard rejected (empty result denies all)",
			in:   "*",
			want: []string{},
		},
		{
			name: "wildcard among valid origins is dropped, valid kept",
			in:   "https://a.example.com,*",
			want: []string{"https://a.example.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseAllowedOrigins(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("index %d: expected %q, got %q", i, tt.want[i], got[i])
				}
			}
		})
	}
}

// corsEcho builds a server echo with the given ALLOWED_ORIGINS and returns it
// with health routes registered (so we can exercise CORS headers without a DB).
func corsEcho(t *testing.T, allowedOrigins string) *Server {
	t.Helper()
	cfg := &config.Config{}
	cfg.Server.AllowedOrigins = allowedOrigins
	s := NewServer(nil, cfg)
	s.setupHealthRoutes()
	return s
}

// TestCORS_ConfiguredProdExcludesLocalhost proves a prod origin is allowed and
// localhost is NOT (since configured origins replace the fallback).
func TestCORS_ConfiguredProdExcludesLocalhost(t *testing.T) {
	s := corsEcho(t, "https://panel.example.com")

	// Prod origin allowed.
	req := httptest.NewRequest(http.MethodOptions, "/health", nil)
	req.Header.Set("Origin", "https://panel.example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://panel.example.com" {
		t.Errorf("expected prod origin allowed, got %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("expected credentials true")
	}

	// localhost NOT allowed (replaced, not appended).
	req2 := httptest.NewRequest(http.MethodOptions, "/health", nil)
	req2.Header.Set("Origin", "http://localhost:3000")
	req2.Header.Set("Access-Control-Request-Method", "GET")
	rec2 := httptest.NewRecorder()
	s.echo.ServeHTTP(rec2, req2)
	if rec2.Header().Get("Access-Control-Allow-Origin") == "http://localhost:3000" {
		t.Errorf("localhost must NOT be allowed when prod origin configured")
	}
}

// TestCORS_LocalFallbackWorks proves the default localhost origin is allowed
// when ALLOWED_ORIGINS is unset.
func TestCORS_LocalFallbackWorks(t *testing.T) {
	s := corsEcho(t, "")

	req := httptest.NewRequest(http.MethodOptions, "/health", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Errorf("expected localhost allowed by default, got %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("expected credentials true")
	}
}

// TestCORS_WildcardRejected confirms a wildcard '*' config denies every
// cross-origin request (fail-closed): no Access-Control-Allow-Origin header is
// emitted, so a browser will refuse to share the response with the caller.
func TestCORS_WildcardRejected(t *testing.T) {
	s := corsEcho(t, "*")

	req := httptest.NewRequest(http.MethodOptions, "/health", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	ao := rec.Header().Get("Access-Control-Allow-Origin")
	if ao != "" {
		t.Fatalf("wildcard config must deny all origins, got allow-origin %q", ao)
	}
}

// TestCORS_PreflightResponds confirms an OPTIONS preflight returns 204 with the
// expected CORS headers for an allowed origin.
func TestCORS_PreflightResponds(t *testing.T) {
	s := corsEcho(t, "https://panel.example.com")

	req := httptest.NewRequest(http.MethodOptions, "/health", nil)
	req.Header.Set("Origin", "https://panel.example.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204 for preflight, got %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://panel.example.com" {
		t.Errorf("expected allow-origin header, got %q", rec.Header().Get("Access-Control-Allow-Origin"))
	}
}
