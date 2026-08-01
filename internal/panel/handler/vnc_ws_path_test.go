package handler

import (
	"net/url"
	"strings"
	"testing"
)

// TestBuildVNCWSPathIsRelativeSameOrigin asserts the VNC WebSocket path is
// relative and never leaks an absolute internal panel host (e.g. panel:8080).
// This guards the Phase 0 same-origin remediation: the browser must derive the
// absolute ws:// or wss:// URL from window.location, not from a server-provided
// host.
func TestBuildVNCWSPathIsRelativeSameOrigin(t *testing.T) {
	const token = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.payload.sig+n=1"

	got := buildVNCWSPath(token)

	// Must be a relative path: no scheme, no "//".
	if strings.Contains(got, "://") {
		t.Fatalf("VNC ws path must be relative, got absolute URL: %q", got)
	}
	if strings.HasPrefix(got, "//") {
		t.Fatalf("VNC ws path must not be protocol-relative, got: %q", got)
	}

	// Must point at the same-origin /ws/vnc endpoint and carry the token.
	if got != "/ws/vnc?token="+url.QueryEscape(token) {
		t.Fatalf("unexpected VNC ws path: %q", got)
	}

	// The token must be present and URL-escaped (no raw '+' / '=' leaking raw).
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("VNC ws path is not a valid relative URL: %v", err)
	}
	if u.Host != "" {
		t.Fatalf("VNC ws path must not contain a host, got host %q in %q", u.Host, got)
	}
	if u.Path != "/ws/vnc" {
		t.Fatalf("VNC ws path must target /ws/vnc, got path %q in %q", u.Path, got)
	}
	if gotParam := u.Query().Get("token"); gotParam != token {
		t.Fatalf("token not round-tripped correctly: got %q want %q", gotParam, token)
	}
}
