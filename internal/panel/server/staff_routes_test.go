package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Routes a staff account reaches whatever its role: its own account, keys
// and notifications, and the catalogues customers read too.
var (
	ownWriteRoutes = map[string]bool{
		"/api/v1/auth/login": true, "/api/v1/auth/forgot-password": true, "/api/v1/auth/reset-password": true,
		"/api/v1/auth/logout": true, "/api/v1/auth/impersonation/end": true, "/api/v1/auth/register": true,
		"/api/v1/auth/oidc/2fa": true, "/api/v1/auth/first-run": true,
		"/api/v1/settings/profile": true, "/api/v1/settings/change-password": true, "/api/v1/settings/2fa/setup": true,
		"/api/v1/settings/2fa/verify": true, "/api/v1/settings/2fa/disable": true, "/api/v1/settings/sessions/:id": true,
		"/api/v1/settings/sessions/revoke-others": true, "/api/v1/api-keys": true, "/api/v1/api-keys/:id": true,
		"/api/v1/ssh-keys": true, "/api/v1/ssh-keys/:id": true, "/api/v1/ssh-keys/generate": true,
		"/api/v1/recipes": true, "/api/v1/recipes/:id": true,
		"/api/v1/notifications/:id/read": true, "/api/v1/notifications/read-all": true,
	}
	anyStaffReadRoutes = map[string]bool{
		"/api/v1/auth/client-ip": true, "/api/v1/auth/me": true, "/api/v1/auth/oidc/config": true,
		"/api/v1/auth/oidc/login": true, "/api/v1/auth/oidc/callback": true, "/api/v1/auth/first-run": true,
		"/api/v1/auth/billing-sso": true, "/api/v1/branding": true, "/api/v1/branding/logo": true, "/api/v1/branding/login-image": true,
		"/api/v1/settings/profile": true, "/api/v1/settings/sessions": true, "/api/v1/settings/sign-ins": true,
		"/api/v1/staff-roles": true, "/api/v1/dashboard/stats": true, "/api/v1/dashboard/overview": true,
		"/api/v1/search": true, "/api/v1/self-service": true, "/api/v1/setup": true, "/api/v1/system/version": true,
		"/api/v1/api-keys": true, "/api/v1/notifications": true, "/api/v1/quota": true, "/api/v1/recipes": true,
		"/api/v1/ssh-keys": true, "/api/v1/plans": true, "/api/v1/plans/:id": true, "/api/v1/regions": true,
		"/api/v1/templates": true, "/api/v1/templates/:id": true,
		"/api/v1/nodes/agent-binary": true, "/api/v1/nodes/agent-binary/checksum": true,
		"/api/v1/openapi.json": true,
	}
)

// Every route the panel serves holds a staff role to its permissions: a
// Read-only account changes nothing but its own account, and an account whose
// role grants nothing reads nothing past its own account. A new route that
// forgets its permission check fails here.
func TestStaffRolesOnEveryRoute(t *testing.T) {
	f := newStaffFixture(t)
	require.NoError(t, f.db.Exec(`UPDATE users SET staff_role = 'readonly' WHERE id = ?`, f.support.ID.String()).Error)
	require.NoError(t, f.db.Exec(`INSERT INTO staff_roles (id, name, permissions) VALUES ('nothing', 'Nothing', '[]')`).Error)
	require.NoError(t, f.db.Exec(`UPDATE users SET staff_role = 'nothing' WHERE id = ?`, f.billing.ID.String()).Error)
	readOnly, nothing := f.support, f.billing

	s := NewServer(f.db, f.s.cfg)
	s.SetupRoutes()
	checked := 0
	for _, r := range s.echo.Routes() {
		// The status stream never answers; it is gated like the VM list.
		if !strings.HasPrefix(r.Path, "/api/v1/") || r.Method == "echo_route_not_found" || r.Path == "/api/v1/events/vm-status" {
			continue
		}
		who, allowed := readOnly, ownWriteRoutes[r.Path]
		if r.Method == http.MethodGet {
			who, allowed = nothing, anyStaffReadRoutes[r.Path]
		}
		if allowed {
			continue
		}
		path := r.Path
		for _, seg := range strings.Split(path, "/") {
			if strings.HasPrefix(seg, ":") {
				path = strings.Replace(path, seg, "00000000-0000-0000-0000-000000000001", 1)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		req := httptest.NewRequest(r.Method, strings.ReplaceAll(path, "*", "x"), strings.NewReader(`{}`)).WithContext(ctx)
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		req.Header.Set(echo.HeaderAuthorization, "Bearer "+mintToken(t, who))
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)
		cancel()
		checked++
		assert.Contains(t, []int{http.StatusForbidden, http.StatusUnauthorized}, rec.Code,
			"%s %s as %s answered %d: %s", r.Method, r.Path, who.Email, rec.Code, rec.Body.String())
	}
	assert.Greater(t, checked, 200, "the route walk saw too few routes to mean anything")
}
