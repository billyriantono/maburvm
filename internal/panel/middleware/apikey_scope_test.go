package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"

	"github.com/maburvm/panel/internal/shared/models"
)

func TestScopeAllowsRoute(t *testing.T) {
	cases := []struct {
		scope, method, path string
		want                bool
	}{
		{models.APIKeyScopeFull, http.MethodDelete, "/api/v1/nodes/:id", true},
		{models.APIKeyScopeReadOnly, http.MethodPost, "/api/v1/vms", true}, // the method rule refuses it, not the route list
		{models.APIKeyScopeVMPower, http.MethodGet, "/api/v1/vms", true},
		{models.APIKeyScopeVMPower, http.MethodHead, "/api/v1/vms/:id", true},
		{models.APIKeyScopeVMPower, http.MethodPost, "/api/v1/vms/:id/restart", true},
		{models.APIKeyScopeVMPower, http.MethodPost, "/api/v1/vms/:id/rebuild", false},
		{models.APIKeyScopeVMPower, http.MethodDelete, "/api/v1/vms/:id", false},
		{models.APIKeyScopeVMPower, http.MethodGet, "/api/v1/ssh-keys", false},
		{models.APIKeyScopeVMPower, http.MethodGet, "/api/v1/vmsx", false},
		{models.APIKeyScopeBilling, http.MethodGet, "/api/v1/billing/deliveries", true},
		{models.APIKeyScopeBilling, http.MethodPost, "/api/v1/users/:id/suspend", true},
		{models.APIKeyScopeBilling, http.MethodGet, "/api/v1/vms/:id/bandwidth/history", true},
		{models.APIKeyScopeBilling, http.MethodPost, "/api/v1/vms/:id/start", false},
		{models.APIKeyScopeBilling, http.MethodGet, "/api/v1/userslist", false},
		{models.APIKeyScopeBilling, http.MethodPost, "/api/v1/reports/usage", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, ScopeAllowsRoute(tc.scope, tc.method, tc.path), "%s %s %s", tc.scope, tc.method, tc.path)
	}
}

// A key narrower than its owner, or limited to addresses, cannot let someone
// else into the account through its team; the owner's full key can.
func TestRestrictedKeyKeepsOutOfTheTeam(t *testing.T) {
	e := echo.New()
	for _, tc := range []struct {
		name string
		u    UserContext
		want bool
	}{
		{"full key", UserContext{APIKeyScope: models.APIKeyScopeFull}, false},
		{"downgraded key", UserContext{APIKeyScope: models.APIKeyScopeFull, APIKeyRestricted: true}, true},
		{"address-limited key", UserContext{APIKeyScope: models.APIKeyScopeFull, APIKeyAddressLimited: true}, true},
	} {
		c := e.NewContext(httptest.NewRequest(http.MethodPost, "/api/v1/account/members", nil), httptest.NewRecorder())
		c.SetPath("/api/v1/account/members")
		assert.Equal(t, tc.want, apiKeyScopeError(c, &tc.u) != "", tc.name)
	}
}

// A narrowed key keeps only the scope's permissions its owner holds, and
// stays a staff key when its owner is staff.
func TestScopePermissions(t *testing.T) {
	admin := []string{PermAll, PermStaff}
	assert.Equal(t, []string{"vm:read", "vm:power", PermStaff}, ScopePermissions(models.APIKeyScopeVMPower, admin))
	billingStaff := []string{"user:read", "user:create", "user:update", "vm:read", "plan:update", "billing:read", "billing:update", PermStaff}
	assert.Equal(t, []string{"vm:read", PermStaff}, ScopePermissions(models.APIKeyScopeVMPower, billingStaff), "a scope never adds what the owner lacks")
	client := GetPermissionsForRole(models.RoleClient)
	assert.Equal(t, []string{"vm:read", "floating_ip:read"}, ScopePermissions(models.APIKeyScopeBilling, client))
	assert.Equal(t, client, ScopePermissions(models.APIKeyScopeFull, client))
	assert.Equal(t, client, ScopePermissions(models.APIKeyScopeReadOnly, client))
}
