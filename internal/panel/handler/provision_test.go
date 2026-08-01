package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

	for _, url := range []string{"/api/v1/nodes/agent-binary", "/api/v1/nodes/agent-binary?arch=arm64"} {
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
