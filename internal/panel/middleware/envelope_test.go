package middleware

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every error answer comes out in the one envelope, keeping the top-level
// fields clients already read; success answers and JSON-RPC errors are left
// as they were written.
func TestErrorEnvelopeShapesEveryErrorAnswer(t *testing.T) {
	e := echo.New()
	e.Use(ErrorEnvelope())
	e.GET("/title-and-message", func(c echo.Context) error {
		return c.JSON(http.StatusForbidden, map[string]any{"error": "Forbidden", "message": "Insufficient permissions"})
	})
	e.GET("/code-only", func(c echo.Context) error {
		return c.JSON(http.StatusConflict, map[string]any{"error": "email_exists"})
	})
	e.GET("/sentence-only", func(c echo.Context) error {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "Failed to list API keys"})
	})
	e.GET("/explicit-code", func(c echo.Context) error {
		return c.JSON(http.StatusServiceUnavailable, map[string]any{"code": "maintenance", "message": "Back soon", "details": map[string]any{"until": "noon"}})
	})
	e.GET("/billing-style", func(c echo.Context) error {
		return c.JSON(http.StatusBadRequest, map[string]any{"success": false, "message": "Invalid event type", "request_id": "r1"})
	})
	e.GET("/returned", func(c echo.Context) error { return echo.NewHTTPError(http.StatusTeapot, "short and stout") })
	e.GET("/plain-error", func(c echo.Context) error { return errors.New("boom") })
	e.GET("/rpc", func(c echo.Context) error {
		return c.JSON(http.StatusBadRequest, map[string]any{"jsonrpc": "2.0", "error": map[string]any{"code": -32700, "message": "parse error"}})
	})
	e.GET("/ok", func(c echo.Context) error { return c.JSON(http.StatusOK, map[string]any{"success": true, "data": 1}) })
	e.GET("/text", func(c echo.Context) error { return c.String(http.StatusNotFound, "nothing here") })

	get := func(path string) (*httptest.ResponseRecorder, map[string]any) {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec, body
	}
	errObj := func(body map[string]any) map[string]any {
		obj, ok := body["error"].(map[string]any)
		require.True(t, ok, "error is an object: %v", body)
		return obj
	}

	cases := []struct {
		path, code, message string
		status              int
	}{
		{"/title-and-message", "forbidden", "Insufficient permissions", http.StatusForbidden},
		{"/code-only", "email_exists", "email_exists", http.StatusConflict},
		{"/sentence-only", "internal_error", "Failed to list API keys", http.StatusInternalServerError},
		{"/explicit-code", "maintenance", "Back soon", http.StatusServiceUnavailable},
		{"/billing-style", "bad_request", "Invalid event type", http.StatusBadRequest},
		{"/returned", "error", "short and stout", http.StatusTeapot},
		{"/plain-error", "internal_error", "Internal Server Error", http.StatusInternalServerError},
		{"/no-such-route", "not_found", "Not Found", http.StatusNotFound},
	}
	for _, tc := range cases {
		rec, body := get(tc.path)
		assert.Equal(t, tc.status, rec.Code, tc.path)
		assert.Equal(t, false, body["success"], tc.path)
		assert.Equal(t, tc.message, body["message"], "%s keeps a top-level message", tc.path)
		obj := errObj(body)
		assert.Equal(t, tc.code, obj["code"], tc.path)
		assert.Equal(t, tc.message, obj["message"], tc.path)
	}

	_, body := get("/explicit-code")
	assert.Equal(t, "maintenance", body["code"], "an existing top-level code stays")
	assert.Equal(t, map[string]any{"until": "noon"}, errObj(body)["details"])
	_, body = get("/billing-style")
	assert.Equal(t, "r1", body["request_id"], "other top-level fields stay")
	_, body = get("/title-and-message")
	assert.NotContains(t, body, "code", "no top-level code is invented")

	rec, body := get("/rpc")
	assert.EqualValues(t, -32700, body["error"].(map[string]any)["code"], "JSON-RPC errors keep their numeric code")
	assert.NotContains(t, body, "success")
	_ = rec

	rec, body = get("/ok")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, map[string]any{"success": true, "data": float64(1)}, body)

	rec, _ = get("/text")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "nothing here", rec.Body.String(), "a non-JSON error passes through")
}

// per_page is accepted wherever page_size is, starting on page 1.
func TestErrorEnvelopeAliasesPerPage(t *testing.T) {
	e := echo.New()
	e.Use(ErrorEnvelope())
	e.GET("/list", func(c echo.Context) error {
		return c.String(http.StatusOK, c.QueryParam("page")+"/"+c.QueryParam("page_size"))
	})
	for _, tc := range []struct{ query, want string }{
		{"per_page=10", "1/10"},
		{"per_page=10&page=3", "3/10"},
		{"per_page=10&page_size=5&page=2", "2/5"},
		{"", "/"},
	} {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/list?"+tc.query, nil))
		assert.Equal(t, tc.want, strings.TrimSpace(rec.Body.String()), tc.query)
	}
}
