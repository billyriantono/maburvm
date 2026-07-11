package sshconsole

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	panelMiddleware "github.com/maburvm/panel/internal/panel/middleware"
	"github.com/stretchr/testify/require"
)

type stubResolver struct {
	host string
	err  error
}

func (s stubResolver) ResolveVMHost(_ context.Context, _ string) (string, error) {
	return s.host, s.err
}

type stubOwner struct {
	owner string
	err   error
}

func (s stubOwner) VMOwner(_ context.Context, _ string) (string, error) {
	return s.owner, s.err
}

// TestGenerateTokenRejectsForeignVM verifies a non-admin cannot mint an SSH
// console token for a VM they do not own (the IDOR fix).
func TestGenerateTokenRejectsForeignVM(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/vms/vm-1/ssh/token", strings.NewReader(`{"username":"root","password":"pw"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("vm-1")
	c.Set(panelMiddleware.UserContextKey, &panelMiddleware.UserContext{ID: uuid.New()})

	// Owner is a different user → must be rejected as not found.
	h := NewHandler(NewProxyServer(nil, "test-secret"), stubResolver{host: "10.0.0.5"}, stubOwner{owner: uuid.New().String()})
	require.NoError(t, h.GenerateToken(c))
	require.Equal(t, http.StatusNotFound, rec.Code, "a client must not open a console to a VM they don't own")
}

// TestGenerateTokenAllowsOwnedVM verifies the owner can mint a token.
func TestGenerateTokenAllowsOwnedVM(t *testing.T) {
	e := echo.New()
	uid := uuid.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/vms/vm-1/ssh/token", strings.NewReader(`{"username":"root","password":"pw"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("vm-1")
	c.Set(panelMiddleware.UserContextKey, &panelMiddleware.UserContext{ID: uid})

	h := NewHandler(NewProxyServer(nil, "test-secret"), stubResolver{host: "10.0.0.5"}, stubOwner{owner: uid.String()})
	require.NoError(t, h.GenerateToken(c))
	require.Equal(t, http.StatusOK, rec.Code)
}

// TestGenerateTokenReadsUserContext guards the regression where the handler read
// a non-existent "user_id" context key (RequireAuth sets "user") and always 401'd.
func TestGenerateTokenReadsUserContext(t *testing.T) {
	e := echo.New()
	body := strings.NewReader(`{"username":"root","password":"hunter2"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/vms/vm-1/ssh/token", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("vm-1")
	// Simulate RequireAuth having authenticated the request.
	c.Set(panelMiddleware.UserContextKey, &panelMiddleware.UserContext{ID: uuid.New()})

	h := NewHandler(NewProxyServer(nil, "test-secret"), stubResolver{host: "10.0.0.5"}, nil)
	require.NoError(t, h.GenerateToken(c))
	require.Equal(t, http.StatusOK, rec.Code, "authenticated request must not be rejected")

	var resp struct {
		Success bool                   `json:"success"`
		Data    map[string]interface{} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	require.NotEmpty(t, resp.Data["token"], "a token should be issued")
	require.Equal(t, "/ws/ssh", resp.Data["ws_path"])
}

func TestGenerateTokenRejectsUnauthenticated(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/vms/vm-1/ssh/token", strings.NewReader(`{"password":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("vm-1")
	// No user context set.

	h := NewHandler(NewProxyServer(nil, "test-secret"), stubResolver{host: "10.0.0.5"}, nil)
	require.NoError(t, h.GenerateToken(c))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestGenerateTokenRequiresReachableIP(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/vms/vm-1/ssh/token", strings.NewReader(`{"username":"root","password":"pw"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues("vm-1")
	c.Set(panelMiddleware.UserContextKey, &panelMiddleware.UserContext{ID: uuid.New()})

	// Resolver returns no host → the console can't connect.
	h := NewHandler(NewProxyServer(nil, "test-secret"), stubResolver{host: ""}, nil)
	require.NoError(t, h.GenerateToken(c))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}
