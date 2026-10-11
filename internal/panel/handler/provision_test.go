package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// newProvisionEcho wires a fresh Echo with the Phase 0 contained provision
// routes registered, so route-level (unauthenticated, 503) behavior can be
// asserted without a database.
func newProvisionEcho(t *testing.T) *echo.Echo {
	t.Helper()
	e := echo.New()
	RegisterProvisionRoutes(e, NewProvisionHandler("", ""))
	return e
}

func TestProvision_InstallScriptReturns503JSON(t *testing.T) {
	e := newProvisionEcho(t)

	req := httptest.NewRequest(http.MethodGet, "/install-agent.sh", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if ct := rec.Header().Get(echo.HeaderContentType); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type = %q, want application/json", ct)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (body=%s)", err, rec.Body.String())
	}
	if body["error"] != "agent_bootstrap_unavailable" {
		t.Fatalf("error = %q, want agent_bootstrap_unavailable", body["error"])
	}
	if body["message"] == "" {
		t.Fatal("message must not be empty")
	}
	// No installer/script output must be served.
	if strings.Contains(rec.Body.String(), "#!/usr/bin/env bash") {
		t.Fatal("installer script body leaked despite containment")
	}
	assertNoLeak(t, rec.Body.String())
}

func TestProvision_AgentBinaryReturns503JSON(t *testing.T) {
	e := newProvisionEcho(t)

	for _, url := range []string{
		"/api/v1/nodes/agent-binary",
		"/api/v1/nodes/agent-binary?arch=arm64",
		"/api/v1/nodes/agent-binary/checksum?arch=amd64",
	} {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status for %q = %d, want %d", url, rec.Code, http.StatusServiceUnavailable)
		}
		if ct := rec.Header().Get(echo.HeaderContentType); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("content-type for %q = %q, want application/json", url, ct)
		}

		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("body for %q is not JSON: %v (body=%s)", url, err, rec.Body.String())
		}
		if body["error"] != "agent_bootstrap_unavailable" {
			t.Fatalf("error for %q = %q, want agent_bootstrap_unavailable", url, body["error"])
		}
		if body["message"] == "" {
			t.Fatalf("message for %q must not be empty", url)
		}
		// No binary artifact must be served (Content-Disposition + ELF/binary).
		if rec.Header().Get("Content-Disposition") != "" {
			t.Fatalf("Content-Disposition set for %q despite containment", url)
		}
		assertNoLeak(t, rec.Body.String())
	}
}

// assertNoLeak fails the test if the response body reveals internal/secret
// details (paths, tokens, env, or the embedded installer/binary markers).
func assertNoLeak(t *testing.T, body string) {
	t.Helper()
	leakMarkers := []string{
		"__PANEL_URL__",
		"AGENT_BINARY_DIR",
		"PANEL_PUBLIC_URL",
		"TOKEN=",
		"Environment=",
		"agent-",
		"maburvm-agent",
		"/opt/maburvm",
		"bin/linux",
	}
	for _, m := range leakMarkers {
		if strings.Contains(body, m) {
			t.Fatalf("contained response leaked internal detail %q: body=%s", m, body)
		}
	}
}

// newUncontainedProvisionEcho builds the provision routes against a temp
// binaryDir holding the given files, with containment lifted — the same way
// Phase 1 will run it — so the real serving behavior can be asserted.
func newUncontainedProvisionEcho(t *testing.T, files map[string][]byte) *echo.Echo {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	h := NewProvisionHandler(dir, "https://panel.example")
	h.contained = false
	e := echo.New()
	RegisterProvisionRoutes(e, h)
	return e
}

func sha256Hex(t *testing.T, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TestProvision_AgentBinaryServesChecksumHeader asserts the binary download is
// accompanied by its SHA-256 so scripted consumers can verify in one request.
func TestProvision_AgentBinaryServesChecksumHeader(t *testing.T) {
	blob := []byte("\x7fELF-fake-agent-binary")
	e := newUncontainedProvisionEcho(t, map[string][]byte{"agent-amd64": blob})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/agent-binary?arch=amd64", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.Bytes(); string(got) != string(blob) {
		t.Fatalf("body mismatch: got %q", got)
	}
	want := sha256Hex(t, blob)
	if got := rec.Header().Get("X-Checksum-Sha256"); got != want {
		t.Fatalf("X-Checksum-Sha256 = %q, want %q", got, want)
	}
}

// TestProvision_ChecksumEndpoint asserts the standalone checksum endpoint
// returns the correct digest for a present binary and 503s for a missing one.
func TestProvision_ChecksumEndpoint(t *testing.T) {
	blob := []byte("\x7fELF-arm64-agent")
	e := newUncontainedProvisionEcho(t, map[string][]byte{"agent-arm64": blob})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/agent-binary/checksum?arch=arm64", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		Arch      string `json:"arch"`
		Algorithm string `json:"algorithm"`
		Digest    string `json:"digest"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (body=%s)", err, rec.Body.String())
	}
	if body.Arch != "arm64" || body.Algorithm != "sha256" || body.Digest != sha256Hex(t, blob) {
		t.Fatalf("checksum body = %+v", body)
	}

	// Missing arch binary → the same 503 diagnostic shape as the binary endpoint.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/nodes/agent-binary/checksum?arch=amd64", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing-arch status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestProvision_InstallScriptEmbedsDigests asserts the served installer has
// every placeholder resolved: panel URL plus per-arch SHA-256 digests, so the
// node-side verification never runs against an unreplaced template marker.
func TestProvision_InstallScriptEmbedsDigests(t *testing.T) {
	amd := []byte("\x7fELF-amd64")
	arm := []byte("\x7fELF-arm64")
	e := newUncontainedProvisionEcho(t, map[string][]byte{
		"agent-amd64": amd,
		"agent-arm64": arm,
	})

	req := httptest.NewRequest(http.MethodGet, "/install-agent.sh", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	script := rec.Body.String()
	for _, marker := range []string{"__PANEL_URL__", "__AGENT_SHA256_AMD64__", "__AGENT_SHA256_ARM64__"} {
		if strings.Contains(script, marker) {
			t.Fatalf("installer still contains unresolved placeholder %q", marker)
		}
	}
	if !strings.Contains(script, sha256Hex(t, amd)) || !strings.Contains(script, sha256Hex(t, arm)) {
		t.Fatal("installer does not embed both arch digests")
	}
	// The verify step must gate chmod/exec: verification (sha256sum) has to
	// appear before the binary becomes executable and is moved into place.
	verifyIdx := strings.Index(script, "sha256sum")
	chmodIdx := strings.Index(script, "chmod +x")
	if verifyIdx == -1 || chmodIdx == -1 || verifyIdx > chmodIdx {
		t.Fatalf("installer must verify (sha256sum) before chmod +x: verify=%d chmod=%d", verifyIdx, chmodIdx)
	}
}

// TestProvision_InstallScriptMissingBinaryAborts asserts that when a binary is
// absent on the panel, the installer for that arch carries an empty digest and
// aborts with a clear error instead of installing an unverified download.
func TestProvision_InstallScriptMissingBinaryAborts(t *testing.T) {
	e := newUncontainedProvisionEcho(t, map[string][]byte{"agent-amd64": []byte("\x7fELF-amd64")})

	req := httptest.NewRequest(http.MethodGet, "/install-agent.sh", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	script := rec.Body.String()
	// arm64 digest is empty because agent-arm64 was not built; the script's
	// guard must treat that as fatal.
	if !strings.Contains(script, `AGENT_SHA256_ARM64="${AGENT_SHA256_ARM64:-}"`) {
		t.Fatalf("arm64 digest not templated empty, script=%s", script)
	}
	if !strings.Contains(script, "no SHA-256 published") {
		t.Fatal("installer lacks the empty-digest abort guard")
	}
}
