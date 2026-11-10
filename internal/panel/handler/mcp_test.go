package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// TestMCP drives the endpoint end to end: API-key-only auth, the handshake,
// and a tool call replayed through the real VM route.
func TestMCP(t *testing.T) {
	if os.Getenv("JWT_SECRET_KEY") == "" {
		os.Setenv("JWT_SECRET_KEY", "test-jwt-secret-key-for-vm-route-tests")
	}
	if os.Getenv("AES_KEY") == "" {
		os.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	}
	db := newVMRouteTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS api_keys (
		id TEXT PRIMARY KEY, user_id TEXT, name TEXT, key_hash TEXT, prefix TEXT,
		last_used_at DATETIME, expires_at DATETIME, is_active BOOLEAN, role TEXT, read_only BOOLEAN NOT NULL DEFAULT 0,
		created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`).Error)

	user := &models.User{ID: uuid.New(), Email: "mcp@example.com", PasswordHash: "x", Role: models.RoleAdmin}
	require.NoError(t, db.Create(user).Error)
	vmID := uuid.New().String()
	require.NoError(t, db.Create(&models.VM{ID: vmID, UserID: user.ID.String(), NodeID: uuid.New().String(), Hostname: "mcp-vm"}).Error)
	addKey := func(token, role string, readOnly bool) string {
		token = models.APITokenPrefix + token
		require.NoError(t, db.Exec(`INSERT INTO api_keys (id, user_id, name, key_hash, prefix, is_active, role, read_only, created_at, updated_at)
			VALUES (?, ?, 'mcp', ?, 'mvk_test', 1, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			uuid.New().String(), user.ID.String(), models.HashAPIToken(token), role, readOnly).Error)
		return token
	}
	key := addKey("testtoken", "", false)

	e := echo.New()
	RegisterVMRoutes(e, newTestVMHandler(t, db), db)
	RegisterMCPRoutes(e, db)
	RegisterAPIKeyRoutes(e, NewAPIKeyHandler(service.NewAPIKeyService(db)), db)

	call := func(auth, body string) (int, map[string]any) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	bearer := "Bearer " + key

	// No key, or a session JWT instead of a key: refused.
	code, _ := call("", `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	require.Equal(t, http.StatusUnauthorized, code)
	jwt, err := middleware.GenerateAccessToken(user)
	require.NoError(t, err)
	code, _ = call("Bearer "+jwt, `{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	require.Equal(t, http.StatusUnauthorized, code)

	_, out := call(bearer, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	require.Equal(t, "2025-03-26", out["result"].(map[string]any)["protocolVersion"])

	code, _ = call(bearer, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	require.Equal(t, http.StatusAccepted, code)

	_, out = call(bearer, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	require.Len(t, out["result"].(map[string]any)["tools"], len(mcpTools))

	text := func(out map[string]any) (string, bool) {
		r := out["result"].(map[string]any)
		return r["content"].([]any)[0].(map[string]any)["text"].(string), r["isError"].(bool)
	}

	_, out = call(bearer, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_vm","arguments":{"id":"`+vmID+`"}}}`)
	got, isErr := text(out)
	require.False(t, isErr, got)
	require.Contains(t, got, "mcp-vm")

	// Path arguments can't smuggle extra path segments.
	_, out = call(bearer, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_vm","arguments":{"id":"../nodes"}}}`)
	_, isErr = text(out)
	require.True(t, isErr)

	_, out = call(bearer, `{"jsonrpc":"2.0","id":5,"method":"nope"}`)
	require.EqualValues(t, -32601, out["error"].(map[string]any)["code"])

	rest := func(token, method, path, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}

	// Read-only key: reads work over MCP, writes are hidden and refused, and
	// the REST API refuses writes directly too.
	ro := "Bearer " + addKey("readonly", "", true)
	_, out = call(ro, `{"jsonrpc":"2.0","id":6,"method":"tools/list"}`)
	readTools := 0
	for _, tl := range mcpTools {
		if tl.ReadOnly {
			readTools++
		}
	}
	require.Len(t, out["result"].(map[string]any)["tools"], readTools)
	_, out = call(ro, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"get_vm","arguments":{"id":"`+vmID+`"}}}`)
	got, isErr = text(out)
	require.False(t, isErr, got)
	_, out = call(ro, `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"vm_power","arguments":{"id":"`+vmID+`","action":"stop"}}}`)
	got, isErr = text(out)
	require.True(t, isErr)
	require.Contains(t, got, "read-only")
	require.Equal(t, http.StatusForbidden, rest(strings.TrimPrefix(ro, "Bearer "), http.MethodPost, "/api/v1/vms/"+vmID+"/stop", `{}`))

	// An admin's client-role key can't mint itself a broader key; the admin's
	// full key still can.
	clientKey := addKey("clientrole", "client", false)
	require.Equal(t, http.StatusForbidden, rest(clientKey, http.MethodPost, "/api/v1/api-keys", `{"name":"x"}`))
	require.Equal(t, http.StatusCreated, rest(key, http.MethodPost, "/api/v1/api-keys", `{"name":"x"}`))
}
