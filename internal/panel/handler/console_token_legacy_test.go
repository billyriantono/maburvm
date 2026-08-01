package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// TestGenerateConsoleTokenLegacyContained verifies the legacy
// POST /api/v1/vms/:id/console/token endpoint is explicitly contained: it must
// return a 503 with the stable machine-readable error and must NOT leak any
// internal endpoint, host, token, or bind-address detail.
func TestGenerateConsoleTokenLegacyContained(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/vms/vm-1/console/token", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("vm-1")

	h := &VMHandler{} // no services required for the contained stub
	requireNoErr := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("handler returned unexpected error: %v", err)
		}
	}
	requireNoErr(h.GenerateConsoleToken(c))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("legacy console token must return 503, got %d", rec.Code)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response body is not valid JSON: %v", err)
	}

	if body["code"] != "vnc_console_legacy_unavailable" {
		t.Fatalf("missing stable error code, got %v", body["code"])
	}
	if msg, ok := body["message"].(string); !ok || msg == "" {
		t.Fatalf("missing human message, got %v", body["message"])
	}

	// Leak assertions: no token / ws_path / websocket_url / host detail.
	if _, ok := body["data"]; ok {
		t.Fatalf("legacy endpoint must not return a data payload, got: %s", rec.Body.String())
	}
	lower := strings.ToLower(rec.Body.String())
	for _, leak := range []string{"websocket_url", "ws_path", "ws_url", "token", "panel:8080", "localhost:8080", "secret", "jti"} {
		if strings.Contains(lower, leak) {
			t.Fatalf("legacy endpoint leaked %q in body: %s", leak, rec.Body.String())
		}
	}
}
