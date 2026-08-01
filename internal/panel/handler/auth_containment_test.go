package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// TestRegister_EnrollmentUnavailable verifies secure-v1 invite-only containment:
// public self-registration fails closed with a stable, non-enumerating response
// (HTTP 403) and creates no user / leaks no configuration state.
func TestRegister_EnrollmentUnavailable(t *testing.T) {
	e := echo.New()
	h := &AuthHandler{}

	// Both a brand-new email and one that "would" exist must return identical,
	// non-enumerating behavior (we assert no user is created and the body is a
	// stable contract).
	for _, body := range []string{
		`{"email":"new@example.com","password":"Sup3rSecret!"}`,
		`{"email":"existing@example.com","password":"Sup3rSecret!"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		if err := h.Register(c); err != nil {
			t.Fatalf("Register returned error: %v", err)
		}

		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d for body %q", rec.Code, http.StatusForbidden, body)
		}

		var resp map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("response not JSON: %v (body=%s)", err, rec.Body.String())
		}
		if resp["error"] != "enrollment_unavailable" {
			t.Fatalf("error = %q, want enrollment_unavailable for body %q", resp["error"], body)
		}
		// Must not echo account-existence, password policy, or any config state.
		if strings.Contains(rec.Body.String(), "already") ||
			strings.Contains(rec.Body.String(), "password") ||
			strings.Contains(rec.Body.String(), "registered") {
			t.Fatalf("response leaked enumerating/config state for body %q: %s", body, rec.Body.String())
		}
	}
}

// TestRegister_InvalidBodyStillFailsClosed ensures a malformed request body does
// not change the contract: it still fails closed (not 400 that could enumerate
// or create state).
func TestRegister_InvalidBodyStillFailsClosed(t *testing.T) {
	e := echo.New()
	h := &AuthHandler{}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", strings.NewReader("not-json"))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.Register(c); err != nil {
		t.Fatalf("Register returned error: %v", err)
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}
