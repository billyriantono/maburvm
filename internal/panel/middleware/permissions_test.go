package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/shared/models"
)

// TestStaffRolePermissionMatrix pins what each built-in role may do. A route
// that checks a permission not listed for a role refuses that role.
func TestStaffRolePermissionMatrix(t *testing.T) {
	perms := func(id string) []string {
		r, ok := builtInRole(id)
		require.True(t, ok, id)
		return append(r.Permissions, PermStaff)
	}
	matrix := []struct {
		perm                                     string
		owner, admin, support, billing, readonly bool
	}{
		{PermStaff, true, true, true, true, true},
		{"vm:read", true, true, true, true, true},
		{"vm:power", true, true, true, false, false},
		{"vm:console", true, true, true, false, false},
		{"vm:rescue", true, true, true, false, false},
		{"vm:lifecycle", true, true, false, false, false},
		{"vm:update", true, true, false, false, false},
		{"vm:delete", true, true, false, false, false},
		{"vm:credentials", true, true, false, false, false},
		{"vm:import", true, true, false, false, false},
		{"import:manage", true, true, false, false, false},
		{"vm:migrate", true, true, false, false, false},
		{"node:drain", true, true, false, false, false},
		{"node:recover", true, true, false, false, false},
		{"backup:read", true, true, true, false, true},
		{"backup:update", true, true, false, false, false},
		{"node:read", true, true, true, false, true},
		{"node:update", true, true, false, false, false},
		{"network:read", true, true, true, false, true},
		{"network:update", true, true, false, false, false},
		{"user:read", true, true, true, true, true},
		{"user:create", true, true, false, true, false},
		{"user:update", true, true, false, true, false},
		{"user:delete", true, true, false, false, false},
		{"user:recover", true, true, true, false, false},
		{PermImpersonate, true, true, false, false, false},
		{"plan:update", true, true, false, true, false},
		{"billing:read", true, true, false, true, true},
		{"billing:update", true, true, false, true, false},
		{"audit:read", true, true, true, false, true},
		{"ops:read", true, true, true, false, true},
		{"ops:update", true, true, false, false, false},
		{"settings:read", true, true, false, false, true},
		{"settings:update", true, true, false, false, false},
		{PermStaffManage, true, false, false, false, false},
		// Not in the catalog: only the Administrator's wildcard reaches it.
		{PermAll, true, true, false, false, false},
	}
	for _, m := range matrix {
		assert.Equal(t, m.owner, Can(perms(StaffRoleOwner), m.perm), "owner %s", m.perm)
		assert.Equal(t, m.admin, Can(perms(StaffRoleAdministrator), m.perm), "administrator %s", m.perm)
		assert.Equal(t, m.support, Can(perms(StaffRoleSupport), m.perm), "support %s", m.perm)
		assert.Equal(t, m.billing, Can(perms(StaffRoleBilling), m.perm), "billing %s", m.perm)
		assert.Equal(t, m.readonly, Can(perms(StaffRoleReadOnly), m.perm), "readonly %s", m.perm)
	}
	// Every built-in role's permissions are ones a route knows.
	for _, r := range BuiltInStaffRoles {
		for _, p := range r.Permissions {
			assert.True(t, InCatalog(p) || p == PermAll, "%s holds unknown %s", r.ID, p)
		}
	}
	// A customer is never staff, and never reaches what only staff may.
	client := GetPermissionsForRole(models.RoleClient)
	for _, p := range []string{PermStaff, "user:read", "node:read", "network:read", "settings:read", "ops:read", "billing:read", PermImpersonate, "vm:migrate", "node:drain"} {
		assert.False(t, Can(client, p), "client %s", p)
	}
	for _, p := range []string{"vm:power", "vm:rescue", "vm:console", "vm:lifecycle", "vm:credentials"} {
		assert.True(t, Can(client, p), "client keeps %s on their own VMs", p)
	}
}

func TestCanGrantOnlyWhatIsHeld(t *testing.T) {
	support, _ := builtInRole(StaffRoleSupport)
	admin, _ := builtInRole(StaffRoleAdministrator)
	owner, _ := builtInRole(StaffRoleOwner)
	assert.True(t, CanGrant(owner.Permissions, admin.Permissions))
	assert.True(t, CanGrant(owner.Permissions, []string{PermStaffManage}))
	assert.True(t, CanGrant(admin.Permissions, support.Permissions))
	assert.False(t, CanGrant(admin.Permissions, []string{PermStaffManage}), "the wildcard does not cover staff management")
	assert.False(t, CanGrant(support.Permissions, admin.Permissions))
	assert.False(t, CanGrant([]string{"user:read", PermStaffManage}, []string{"user:read", "user:update"}))
	assert.True(t, CanGrant([]string{"user:read"}, []string{PermStaff, "user:read"}))
}

func TestStaffRoleKey(t *testing.T) {
	founding := uuid.New()
	key := func(id uuid.UUID, stored string) string {
		u := &models.User{ID: id}
		if stored != "" {
			u.StaffRole = &stored
		}
		return StaffRoleKey(u, founding)
	}
	assert.Equal(t, StaffRoleOwner, key(founding, StaffRoleSupport), "the founding administrator is the Owner whatever is stored")
	assert.Equal(t, StaffRoleAdministrator, key(uuid.New(), ""), "nothing stored: today's full administrator")
	assert.Equal(t, StaffRoleAdministrator, key(uuid.New(), StaffRoleOwner), "Owner cannot be stored on anyone else")
	assert.Equal(t, StaffRoleSupport, key(uuid.New(), StaffRoleSupport))
}

func TestRequireByMethod(t *testing.T) {
	e := echo.New()
	mw := RequireByMethod("settings:read", "settings:update")
	call := func(method string, perms ...string) int {
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(method, "/", nil), rec)
		c.Set(UserContextKey, &UserContext{Permissions: perms})
		_ = mw(func(c echo.Context) error { return c.NoContent(http.StatusOK) })(c)
		return rec.Code
	}
	assert.Equal(t, http.StatusOK, call(http.MethodGet, "settings:read"))
	assert.Equal(t, http.StatusForbidden, call(http.MethodPut, "settings:read"))
	assert.Equal(t, http.StatusOK, call(http.MethodPut, "settings:update"))
	assert.Equal(t, http.StatusOK, call(http.MethodDelete, PermAll))
	assert.Equal(t, http.StatusForbidden, call(http.MethodGet, "vm:read"))
}

func TestImpersonationForbiddenPaths(t *testing.T) {
	for _, c := range []struct {
		method, path string
		forbidden    bool
	}{
		{http.MethodPost, "/api/v1/settings/change-password", true},
		{http.MethodPut, "/api/v1/settings/profile", true},
		{http.MethodPost, "/api/v1/settings/2fa/disable", true},
		{http.MethodPost, "/api/v1/settings/sessions/revoke-others", true},
		{http.MethodGet, "/api/v1/settings/profile", false},
		{http.MethodPost, "/api/v1/api-keys", true},
		{http.MethodGet, "/api/v1/api-keys", false},
		{http.MethodPost, "/api/v1/vms/x/initial-password/reveal", true},
		{http.MethodGet, "/api/v1/vms/x/initial-password", false},
		{http.MethodPost, "/api/v1/mcp", true},
		{http.MethodPost, "/api/v1/vms/x/start", false},
		{http.MethodPost, "/api/v1/auth/impersonation/end", false},
	} {
		assert.Equal(t, c.forbidden, impersonationForbidden(c.method, c.path), "%s %s", c.method, c.path)
	}
}
