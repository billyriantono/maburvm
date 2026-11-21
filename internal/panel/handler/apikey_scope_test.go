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

// A narrowed key reaches only its scope's routes, over REST and MCP alike,
// and a key with an address list is refused from anywhere else.
func TestAPIKeyScopesAndAddressesAreEnforced(t *testing.T) {
	if os.Getenv("JWT_SECRET_KEY") == "" {
		os.Setenv("JWT_SECRET_KEY", "test-jwt-secret-key-for-vm-route-tests")
	}
	if os.Getenv("AES_KEY") == "" {
		os.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	}
	db := newVMRouteTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS api_keys (
		id TEXT PRIMARY KEY, user_id TEXT, name TEXT, key_hash TEXT, prefix TEXT,
		last_used_at DATETIME, expires_at DATETIME, is_active BOOLEAN, role TEXT, read_only BOOLEAN NOT NULL DEFAULT 0, scope TEXT NOT NULL DEFAULT '', allowed_ips TEXT,
		created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`).Error)

	owner := &models.User{ID: uuid.New(), Email: "scoped@example.com", PasswordHash: "x", Role: models.RoleClient}
	require.NoError(t, db.Create(owner).Error)
	vmID := uuid.New().String()
	require.NoError(t, db.Create(&models.VM{ID: vmID, UserID: owner.ID.String(), NodeID: uuid.New().String(), Hostname: "scoped-vm"}).Error)
	addKey := func(token, scope, allowed string) string {
		token = models.APITokenPrefix + token
		require.NoError(t, db.Exec(`INSERT INTO api_keys (id, user_id, name, key_hash, prefix, is_active, scope, allowed_ips, created_at, updated_at)
			VALUES (?, ?, 'k', ?, 'mvk_test', 1, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			uuid.New().String(), owner.ID.String(), models.HashAPIToken(token), scope, allowed).Error)
		return token
	}
	power := addKey("powertoken", models.APIKeyScopeVMPower, "")
	office := addKey("officetoken", "", `["203.0.113.0/24"]`)

	e := echo.New()
	e.Use(middleware.ErrorEnvelope())
	RegisterVMRoutes(e, newTestVMHandler(t, db), db)
	RegisterMCPRoutes(e, db)
	RegisterAPIKeyRoutes(e, NewAPIKeyHandler(service.NewAPIKeyService(db)), db)

	call := func(token, from, method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		req.RemoteAddr = from + ":4000"
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	const anywhere = "198.51.100.9"

	code, out := call(power, anywhere, http.MethodGet, "/api/v1/vms/"+vmID, "")
	require.Equal(t, http.StatusOK, code, out)
	code, out = call(power, anywhere, http.MethodDelete, "/api/v1/vms/"+vmID, "")
	require.Equal(t, http.StatusForbidden, code)
	require.Contains(t, out["message"], "may only list VMs")
	require.Equal(t, "forbidden", out["error"].(map[string]any)["code"])
	code, _ = call(power, anywhere, http.MethodGet, "/api/v1/api-keys", "")
	require.Equal(t, http.StatusForbidden, code, "a narrowed key never manages keys")

	_, out = call(power, anywhere, http.MethodPost, "/api/v1/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var names []string
	for _, tl := range out["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	require.ElementsMatch(t, []string{"list_vms", "get_vm", "get_vm_metrics", "get_vm_operation", "vm_power"}, names)
	_, out = call(power, anywhere, http.MethodPost, "/api/v1/mcp",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"delete_vm","arguments":{"id":"`+vmID+`"}}}`)
	result := out["result"].(map[string]any)
	require.Equal(t, true, result["isError"])
	require.Contains(t, result["content"].([]any)[0].(map[string]any)["text"], "scope")

	code, out = call(office, anywhere, http.MethodGet, "/api/v1/vms/"+vmID, "")
	require.Equal(t, http.StatusForbidden, code)
	require.Equal(t, "api_key_address", out["error"].(map[string]any)["code"])
	code, _ = call(office, "203.0.113.40", http.MethodGet, "/api/v1/vms/"+vmID, "")
	require.Equal(t, http.StatusOK, code)

	// A customer key lists the customer tools, never the staff ones.
	full := addKey("fulltoken", "", "")
	_, out = call(full, anywhere, http.MethodPost, "/api/v1/mcp", `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	names = names[:0]
	for _, tl := range out["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	require.Contains(t, names, "list_backups")
	require.Contains(t, names, "add_firewall_rule")
	require.Contains(t, names, "list_floating_ips")
	for _, staffOnly := range []string{"list_nodes", "list_users", "list_alerts", "list_ip_pools", "get_vm_migration"} {
		require.NotContains(t, names, staffOnly)
	}
}
