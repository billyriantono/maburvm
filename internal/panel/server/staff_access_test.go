package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/shared/config"
	"github.com/maburvm/panel/internal/shared/models"
)

const staffTestPassword = "Correct-Horse-9-battery"

type staffFixture struct {
	db                                       *gorm.DB
	s                                        *Server
	owner, admin, support, billing, customer *models.User
}

// newStaffFixture seeds the founding administrator (the Owner), a second
// administrator with no role stored (as every staff account had before roles),
// a Support and a Billing account, and a customer, and wires the auth, user,
// staff role and settings routes.
func newStaffFixture(t *testing.T) *staffFixture {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(staffTestPassword), bcrypt.MinCost)
	require.NoError(t, err)
	f := &staffFixture{
		owner: adminUser("owner@example.com"), admin: adminUser("admin@example.com"),
		support: adminUser("support@example.com"), billing: adminUser("billing@example.com"),
		customer: clientUser("customer@example.com"),
	}
	for _, u := range []*models.User{f.owner, f.admin, f.support, f.billing, f.customer} {
		u.PasswordHash = string(hash)
	}
	f.db = setupUserContainmentDB(t, f.owner, f.admin, f.support, f.billing, f.customer)
	for _, ddl := range []string{
		`ALTER TABLE users ADD COLUMN staff_role TEXT`,
		`CREATE TABLE user_sessions (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, method TEXT NOT NULL, ip_address TEXT NOT NULL DEFAULT '',
			user_agent TEXT NOT NULL DEFAULT '', impersonator_id TEXT, created_at DATETIME, last_seen_at DATETIME, expires_at DATETIME, revoked_at DATETIME)`,
		`CREATE TABLE sign_in_events (id TEXT PRIMARY KEY, user_id TEXT, email TEXT NOT NULL DEFAULT '', method TEXT NOT NULL, success BOOLEAN NOT NULL,
			note TEXT NOT NULL DEFAULT '', ip_address TEXT NOT NULL DEFAULT '', user_agent TEXT NOT NULL DEFAULT '', created_at DATETIME)`,
		`CREATE TABLE staff_roles (id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', permissions TEXT NOT NULL DEFAULT '[]',
			created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE audit_logs (id TEXT PRIMARY KEY, user_id TEXT, action TEXT NOT NULL, resource_type TEXT, resource_id TEXT,
			ip_address TEXT, user_agent TEXT, details TEXT, before_snapshot TEXT, after_snapshot TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
	} {
		require.NoError(t, f.db.Exec(ddl).Error)
	}
	require.NoError(t, f.db.Exec(`UPDATE users SET staff_role = 'support' WHERE id = ?`, f.support.ID.String()).Error)
	require.NoError(t, f.db.Exec(`UPDATE users SET staff_role = 'billing' WHERE id = ?`, f.billing.ID.String()).Error)

	cfg := &config.Config{}
	cfg.JWT.AESKey = "0123456789abcdef0123456789abcdef"
	f.s = NewServer(f.db, cfg)
	v1 := f.s.echo.Group("/api/v1")
	f.s.setupAuthRoutes(v1)
	f.s.setupUserRoutes(v1)
	f.s.setupStaffRoleRoutes(v1)
	f.s.setupSettingsRoutes(v1)
	return f
}

type staffReq struct {
	method, path, body string
	bearer             string
	cookies            []*http.Cookie
}

func (f *staffFixture) do(t *testing.T, r staffReq) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set("User-Agent", "staff-test")
	if r.bearer != "" {
		req.Header.Set(echo.HeaderAuthorization, "Bearer "+r.bearer)
	}
	for _, ck := range r.cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	f.s.echo.ServeHTTP(rec, req)
	return rec
}

func (f *staffFixture) as(t *testing.T, u *models.User, method, path, body string) *httptest.ResponseRecorder {
	return f.do(t, staffReq{method: method, path: path, body: body, bearer: mintToken(t, u)})
}

// login signs in with the password and returns the session cookie.
func (f *staffFixture) login(t *testing.T, email string) *http.Cookie {
	t.Helper()
	rec := f.do(t, staffReq{method: http.MethodPost, path: "/api/v1/auth/login",
		body: `{"email":"` + email + `","password":"` + staffTestPassword + `"}`})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	ck := cookieNamed(rec, middleware.AccessTokenCookieName)
	require.NotNil(t, ck)
	return ck
}

// legacyToken is a token with no session id, issued a little while ago.
func legacyToken(t *testing.T, u *models.User) string {
	t.Helper()
	iat := time.Now().Add(-time.Minute)
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, middleware.JWTClaims{
		UserID: u.ID.String(), Email: u.Email, Role: string(u.Role), TokenType: "access",
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(iat.Add(time.Hour)), IssuedAt: jwt.NewNumericDate(iat)},
	}).SignedString([]byte(testJWTSecret))
	require.NoError(t, err)
	return signed
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == name {
			return ck
		}
	}
	return nil
}

func dataOf(t *testing.T, rec *httptest.ResponseRecorder) json.RawMessage {
	t.Helper()
	var body struct {
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return body.Data
}

// The roles hold on real routes: Support recovers accounts but does not change
// them; Billing changes customers but recovers nothing; an Administrator keeps
// everything it had except staff management; existing staff with no role are
// Administrators.
func TestStaffRolesOnUserRoutes(t *testing.T) {
	f := newStaffFixture(t)
	cust := "/api/v1/users/" + f.customer.ID.String()

	assert.Equal(t, http.StatusOK, f.as(t, f.support, http.MethodGet, "/api/v1/users", "").Code)
	assert.Equal(t, http.StatusForbidden, f.as(t, f.support, http.MethodPut, cust, `{"name":"x"}`).Code)
	assert.Equal(t, http.StatusOK, f.as(t, f.support, http.MethodPost, cust+"/unlock", "").Code)
	assert.Equal(t, http.StatusForbidden, f.as(t, f.support, http.MethodDelete, cust, "").Code)
	assert.Equal(t, http.StatusForbidden, f.as(t, f.support, http.MethodPost, cust+"/suspend", `{}`).Code)

	assert.Equal(t, http.StatusOK, f.as(t, f.billing, http.MethodPut, cust, `{"name":"Customer Name"}`).Code)
	assert.Equal(t, http.StatusForbidden, f.as(t, f.billing, http.MethodPost, cust+"/unlock", "").Code)

	assert.Equal(t, http.StatusOK, f.as(t, f.admin, http.MethodPost, cust+"/unlock", "").Code)
	assert.Equal(t, http.StatusOK, f.as(t, f.admin, http.MethodPut, cust, `{"name":"Again"}`).Code)

	// A customer reaches none of it.
	assert.Equal(t, http.StatusForbidden, f.as(t, f.customer, http.MethodGet, "/api/v1/users", "").Code)
	assert.Equal(t, http.StatusForbidden, f.as(t, f.customer, http.MethodGet, "/api/v1/staff-roles", "").Code)

	// The user page shows each staff account's effective role.
	var owner models.User
	require.NoError(t, json.Unmarshal(dataOf(t, f.as(t, f.admin, http.MethodGet, "/api/v1/users/"+f.owner.ID.String(), "")), &owner))
	require.NotNil(t, owner.StaffRole)
	assert.Equal(t, middleware.StaffRoleOwner, *owner.StaffRole)
	var admin models.User
	require.NoError(t, json.Unmarshal(dataOf(t, f.as(t, f.owner, http.MethodGet, "/api/v1/users/"+f.admin.ID.String(), "")), &admin))
	assert.Equal(t, "Administrator", admin.StaffRoleName)
}

func TestStaffRoleAssignment(t *testing.T) {
	f := newStaffFixture(t)
	assign := func(by, target *models.User, role string) *httptest.ResponseRecorder {
		return f.as(t, by, http.MethodPut, "/api/v1/users/"+target.ID.String()+"/staff-role", `{"role":"`+role+`"}`)
	}

	// An Administrator does not manage staff; nor can anyone change their own role or the Owner's.
	assert.Equal(t, http.StatusForbidden, assign(f.admin, f.support, middleware.StaffRoleBilling).Code)
	assert.Equal(t, http.StatusForbidden, assign(f.owner, f.owner, middleware.StaffRoleSupport).Code)
	assert.Equal(t, http.StatusBadRequest, assign(f.owner, f.customer, middleware.StaffRoleSupport).Code)
	assert.Equal(t, http.StatusBadRequest, assign(f.owner, f.admin, middleware.StaffRoleOwner).Code)
	assert.Equal(t, http.StatusBadRequest, assign(f.owner, f.admin, "no-such-role").Code)

	// The Owner moves Support to Billing; the account's access follows at once.
	cust := "/api/v1/users/" + f.customer.ID.String()
	assert.Equal(t, http.StatusForbidden, f.as(t, f.support, http.MethodPut, cust, `{"name":"x"}`).Code)
	rec := assign(f.owner, f.support, middleware.StaffRoleBilling)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, http.StatusOK, f.as(t, f.support, http.MethodPut, cust, `{"name":"x"}`).Code)

	// A custom role: made by the Owner, given to the administrator. Holding
	// staff management in it, they still cannot hand out more than they hold.
	rec = f.as(t, f.owner, http.MethodPost, "/api/v1/staff-roles",
		`{"name":"Team lead","permissions":["user:read","user:recover","staff:manage"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var lead middleware.StaffRole
	require.NoError(t, json.Unmarshal(dataOf(t, rec), &lead))
	require.Equal(t, http.StatusOK, assign(f.owner, f.admin, lead.ID).Code)

	assert.Equal(t, http.StatusForbidden, assign(f.admin, f.billing, middleware.StaffRoleSupport).Code, "Billing holds user:update, which the lead lacks")
	rec = f.as(t, f.admin, http.MethodPost, "/api/v1/staff-roles", `{"name":"Bigger","permissions":["user:delete"]}`)
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	rec = f.as(t, f.admin, http.MethodPost, "/api/v1/staff-roles", `{"name":"Desk","permissions":["user:read"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var desk middleware.StaffRole
	require.NoError(t, json.Unmarshal(dataOf(t, rec), &desk))

	// Names are unique, built-ins are fixed, permissions must exist.
	assert.Equal(t, http.StatusConflict, f.as(t, f.owner, http.MethodPost, "/api/v1/staff-roles", `{"name":"desk","permissions":[]}`).Code)
	assert.Equal(t, http.StatusConflict, f.as(t, f.owner, http.MethodPost, "/api/v1/staff-roles", `{"name":"Support","permissions":[]}`).Code)
	assert.Equal(t, http.StatusBadRequest, f.as(t, f.owner, http.MethodPost, "/api/v1/staff-roles", `{"name":"X","permissions":["admin:access"]}`).Code)
	assert.Equal(t, http.StatusBadRequest, f.as(t, f.owner, http.MethodPut, "/api/v1/staff-roles/support", `{"name":"Mine","permissions":[]}`).Code)

	// A role in use is not deleted.
	assert.Equal(t, http.StatusConflict, f.as(t, f.owner, http.MethodDelete, "/api/v1/staff-roles/"+lead.ID, "").Code)
	assert.Equal(t, http.StatusOK, f.as(t, f.owner, http.MethodDelete, "/api/v1/staff-roles/"+desk.ID, "").Code)

	var list struct {
		Roles []struct {
			ID      string `json:"id"`
			Members int    `json:"members"`
		} `json:"roles"`
	}
	require.NoError(t, json.Unmarshal(dataOf(t, f.as(t, f.support, http.MethodGet, "/api/v1/staff-roles", "")), &list))
	members := map[string]int{}
	for _, r := range list.Roles {
		members[r.ID] = r.Members
	}
	assert.Equal(t, 1, members[middleware.StaffRoleOwner])
	assert.Equal(t, 2, members[middleware.StaffRoleBilling])
	assert.Equal(t, 1, members[lead.ID])
}

func TestImpersonation(t *testing.T) {
	f := newStaffFixture(t)
	path := "/api/v1/users/" + f.customer.ID.String() + "/impersonate"

	// Support may not; a bearer token (a script, not a browser) may not either.
	supportCk := f.login(t, f.support.Email)
	assert.Equal(t, http.StatusForbidden, f.do(t, staffReq{method: http.MethodPost, path: path, cookies: []*http.Cookie{supportCk}}).Code)
	assert.Equal(t, http.StatusForbidden, f.as(t, f.admin, http.MethodPost, path, "").Code)

	adminCk := f.login(t, f.admin.Email)
	rec := f.do(t, staffReq{method: http.MethodPost, path: path, cookies: []*http.Cookie{adminCk}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	viewCk, staffCk := cookieNamed(rec, middleware.AccessTokenCookieName), cookieNamed(rec, middleware.StaffCookieName)
	require.NotNil(t, viewCk)
	require.NotNil(t, staffCk)
	assert.Equal(t, middleware.StaffCookiePath, staffCk.Path)
	assert.True(t, staffCk.HttpOnly)

	// The view is the customer's, and says who is looking.
	rec = f.do(t, staffReq{method: http.MethodGet, path: "/api/v1/auth/me", cookies: []*http.Cookie{viewCk}})
	require.Equal(t, http.StatusOK, rec.Code)
	var me struct {
		ID           string            `json:"id"`
		Role         string            `json:"role"`
		Impersonator map[string]string `json:"impersonator"`
	}
	require.NoError(t, json.Unmarshal(dataOf(t, rec), &me))
	assert.Equal(t, f.customer.ID.String(), me.ID)
	assert.Equal(t, f.admin.Email, me.Impersonator["email"])

	// Their password, email, two-factor and sessions stay theirs.
	for _, p := range []string{"/api/v1/settings/change-password", "/api/v1/settings/2fa/disable", "/api/v1/settings/sessions/revoke-others"} {
		rec = f.do(t, staffReq{method: http.MethodPost, path: p, body: `{}`, cookies: []*http.Cookie{viewCk}})
		assert.Equal(t, http.StatusForbidden, rec.Code, p)
		assert.Contains(t, rec.Body.String(), "impersonation_forbidden", p)
	}
	assert.Equal(t, http.StatusOK, f.do(t, staffReq{method: http.MethodGet, path: "/api/v1/settings/sessions", cookies: []*http.Cookie{viewCk}}).Code)

	// The customer's history shows it.
	var history []models.SignInEvent
	require.NoError(t, f.db.Where("user_id = ?", f.customer.ID.String()).Find(&history).Error)
	require.Len(t, history, 1)
	assert.Equal(t, middleware.SignInImpersonation, history[0].Method)
	assert.Equal(t, f.admin.Email, history[0].Note)

	// Ending it gives the browser the staff session back and closes the view.
	rec = f.do(t, staffReq{method: http.MethodPost, path: "/api/v1/auth/impersonation/end", cookies: []*http.Cookie{viewCk, staffCk}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	back := cookieNamed(rec, middleware.AccessTokenCookieName)
	require.NotNil(t, back)
	assert.Equal(t, adminCk.Value, back.Value)
	assert.Equal(t, http.StatusUnauthorized, f.do(t, staffReq{method: http.MethodGet, path: "/api/v1/auth/me", cookies: []*http.Cookie{viewCk}}).Code)
	assert.Equal(t, http.StatusOK, f.do(t, staffReq{method: http.MethodGet, path: "/api/v1/auth/me", cookies: []*http.Cookie{back}}).Code)

	// Both ends are on record, the end with both actors.
	var end models.AuditLog
	require.NoError(t, f.db.Where("action = ?", "user.impersonate.end").First(&end).Error)
	assert.Equal(t, f.admin.ID.String(), end.Details["impersonator_id"])
	var start int64
	f.db.Model(&models.AuditLog{}).Where("action = ? AND user_id = ?", "user.impersonate.start", f.admin.ID.String()).Count(&start)
	assert.Equal(t, int64(1), start)

	// The staff member changing their password or signing out everywhere ends their views too.
	rec = f.do(t, staffReq{method: http.MethodPost, path: path, cookies: []*http.Cookie{adminCk}})
	require.Equal(t, http.StatusOK, rec.Code)
	viewCk = cookieNamed(rec, middleware.AccessTokenCookieName)
	require.NoError(t, f.db.Exec(`UPDATE users SET token_revoked_at = ? WHERE id = ?`, time.Now(), f.admin.ID.String()).Error)
	assert.Equal(t, http.StatusUnauthorized, f.do(t, staffReq{method: http.MethodGet, path: "/api/v1/auth/me", cookies: []*http.Cookie{viewCk}}).Code)
	var listed []struct {
		Method string `json:"method"`
	}
	require.NoError(t, json.Unmarshal(dataOf(t, f.do(t, staffReq{method: http.MethodGet, path: "/api/v1/settings/sessions",
		cookies: []*http.Cookie{f.login(t, f.customer.Email)}})), &listed))
	for _, s := range listed {
		assert.NotEqual(t, middleware.SignInImpersonation, s.Method, "the customer's list still shows the ended view")
	}
	require.NoError(t, f.db.Exec(`UPDATE users SET token_revoked_at = NULL WHERE id = ?`, f.admin.ID.String()).Error)

	// A view whose staff member lost the permission ends at the next request.
	rec = f.do(t, staffReq{method: http.MethodPost, path: path, cookies: []*http.Cookie{adminCk}})
	require.Equal(t, http.StatusOK, rec.Code)
	viewCk = cookieNamed(rec, middleware.AccessTokenCookieName)
	require.NoError(t, f.db.Exec(`UPDATE users SET staff_role = 'support' WHERE id = ?`, f.admin.ID.String()).Error)
	assert.Equal(t, http.StatusUnauthorized, f.do(t, staffReq{method: http.MethodGet, path: "/api/v1/auth/me", cookies: []*http.Cookie{viewCk}}).Code)

	// Only customers are viewed this way.
	assert.Equal(t, http.StatusBadRequest, f.do(t, staffReq{method: http.MethodPost,
		path: "/api/v1/users/" + f.billing.ID.String() + "/impersonate", cookies: []*http.Cookie{f.login(t, f.owner.Email)}}).Code)
}

func TestSessionsAndSignInHistory(t *testing.T) {
	f := newStaffFixture(t)
	first := f.login(t, f.customer.Email)
	second := f.login(t, f.customer.Email)
	legacy := legacyToken(t, f.customer) // signed in before sessions were recorded
	rec := f.do(t, staffReq{method: http.MethodPost, path: "/api/v1/auth/login", body: `{"email":"customer@example.com","password":"wrong"}`})
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	var sessions []struct {
		ID      string `json:"id"`
		Current bool   `json:"current"`
		Method  string `json:"method"`
	}
	rec = f.do(t, staffReq{method: http.MethodGet, path: "/api/v1/settings/sessions", cookies: []*http.Cookie{second}})
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(dataOf(t, rec), &sessions))
	require.Len(t, sessions, 2)
	var firstID string
	for _, s := range sessions {
		assert.Equal(t, middleware.SignInPassword, s.Method)
		if !s.Current {
			firstID = s.ID
		}
	}
	require.NotEmpty(t, firstID)

	var signIns []models.SignInEvent
	require.NoError(t, json.Unmarshal(dataOf(t, f.do(t, staffReq{method: http.MethodGet, path: "/api/v1/settings/sign-ins", cookies: []*http.Cookie{second}})), &signIns))
	require.Len(t, signIns, 3)
	assert.False(t, signIns[0].Success)
	assert.Equal(t, "wrong_email_or_password", signIns[0].Note)

	// One session out: it stops working, the other does not. The one in use is signed out instead.
	var currentID string
	for _, s := range sessions {
		if s.Current {
			currentID = s.ID
		}
	}
	assert.Equal(t, http.StatusBadRequest, f.do(t, staffReq{method: http.MethodDelete, path: "/api/v1/settings/sessions/" + currentID, cookies: []*http.Cookie{second}}).Code)
	rec = f.do(t, staffReq{method: http.MethodDelete, path: "/api/v1/settings/sessions/" + firstID, cookies: []*http.Cookie{second}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, http.StatusUnauthorized, f.do(t, staffReq{method: http.MethodGet, path: "/api/v1/auth/me", cookies: []*http.Cookie{first}}).Code)
	assert.Equal(t, http.StatusOK, f.do(t, staffReq{method: http.MethodGet, path: "/api/v1/auth/me", cookies: []*http.Cookie{second}}).Code)

	// Everywhere else out, including a token with no session behind it; this
	// session carries on with a fresh token.
	third := f.login(t, f.customer.Email)
	rec = f.do(t, staffReq{method: http.MethodPost, path: "/api/v1/settings/sessions/revoke-others", cookies: []*http.Cookie{second}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	renewed := cookieNamed(rec, middleware.AccessTokenCookieName)
	require.NotNil(t, renewed)
	assert.Equal(t, http.StatusUnauthorized, f.do(t, staffReq{method: http.MethodGet, path: "/api/v1/auth/me", cookies: []*http.Cookie{third}}).Code)
	assert.Equal(t, http.StatusUnauthorized, f.do(t, staffReq{method: http.MethodGet, path: "/api/v1/auth/me", bearer: legacy}).Code)
	assert.Equal(t, http.StatusOK, f.do(t, staffReq{method: http.MethodGet, path: "/api/v1/auth/me", cookies: []*http.Cookie{renewed}}).Code)
	require.NoError(t, json.Unmarshal(dataOf(t, f.do(t, staffReq{method: http.MethodGet, path: "/api/v1/settings/sessions", cookies: []*http.Cookie{renewed}})), &sessions))
	require.Len(t, sessions, 1)
	assert.True(t, sessions[0].Current)

	// Staff see and end a customer's sessions; another staff account's are the Owner's to end.
	rec = f.as(t, f.support, http.MethodGet, "/api/v1/users/"+f.customer.ID.String()+"/sessions", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, http.StatusOK, f.as(t, f.support, http.MethodGet, "/api/v1/users/"+f.customer.ID.String()+"/sign-ins", "").Code)
	assert.Equal(t, http.StatusForbidden, f.as(t, f.support, http.MethodGet, "/api/v1/users/"+f.owner.ID.String()+"/sessions", "").Code)
	assert.Equal(t, http.StatusOK, f.as(t, f.owner, http.MethodGet, "/api/v1/users/"+f.support.ID.String()+"/sign-ins", "").Code)
	assert.Equal(t, http.StatusForbidden, f.as(t, f.billing, http.MethodPost, "/api/v1/users/"+f.customer.ID.String()+"/sessions/revoke-all", "").Code)
	assert.Equal(t, http.StatusForbidden, f.as(t, f.support, http.MethodPost, "/api/v1/users/"+f.admin.ID.String()+"/sessions/revoke-all", "").Code)
	require.Equal(t, http.StatusOK, f.as(t, f.support, http.MethodPost, "/api/v1/users/"+f.customer.ID.String()+"/sessions/revoke-all", "").Code)
	assert.Equal(t, http.StatusUnauthorized, f.do(t, staffReq{method: http.MethodGet, path: "/api/v1/auth/me", cookies: []*http.Cookie{renewed}}).Code)

	// An API key cannot turn itself into a browser session that outlives it.
	require.NoError(t, f.db.Exec(`CREATE TABLE api_keys (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, name TEXT NOT NULL,
		key_hash TEXT NOT NULL UNIQUE, prefix TEXT NOT NULL, last_used_at DATETIME, expires_at DATETIME, is_active BOOLEAN DEFAULT 1,
		role TEXT, read_only BOOLEAN NOT NULL DEFAULT 0, scope TEXT NOT NULL DEFAULT '', allowed_ips TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`).Error)
	const key = "mvk_sessions_test_token"
	require.NoError(t, f.db.Create(&models.APIKey{UserID: f.customer.ID.String(), Name: "ci", KeyHash: models.HashAPIToken(key), Prefix: "mvk_sess", IsActive: true}).Error)
	rec = f.do(t, staffReq{method: http.MethodPost, path: "/api/v1/settings/sessions/revoke-others", bearer: key})
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Nil(t, cookieNamed(rec, middleware.AccessTokenCookieName))

	// The founding administrator's billing-scope key manages customers only: it
	// cannot mint a staff account, change a role or redirect a staff email.
	const billingKey = "mvk_billing_scope_token"
	require.NoError(t, f.db.Create(&models.APIKey{UserID: f.owner.ID.String(), Name: "billing", KeyHash: models.HashAPIToken(billingKey),
		Prefix: "mvk_bill", IsActive: true, Scope: models.APIKeyScopeBilling}).Error)
	rec = f.do(t, staffReq{method: http.MethodPost, path: "/api/v1/users", bearer: billingKey,
		body: `{"email":"minted@example.com","password":"` + staffTestPassword + `","role":"admin"}`})
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	var minted int64
	f.db.Table("users").Where("email = ?", "minted@example.com").Count(&minted)
	assert.Zero(t, minted)
	rec = f.do(t, staffReq{method: http.MethodPut, path: "/api/v1/users/" + f.admin.ID.String(), bearer: billingKey, body: `{"email":"taken@example.com"}`})
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	rec = f.do(t, staffReq{method: http.MethodPut, path: "/api/v1/users/" + f.customer.ID.String(), bearer: billingKey, body: `{"role":"admin"}`})
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	rec = f.do(t, staffReq{method: http.MethodPut, path: "/api/v1/users/" + f.customer.ID.String(), bearer: billingKey, body: `{"name":"Billed Customer"}`})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// A key limited to addresses may not change the account's sign-in, which
	// would work from anywhere.
	const officeKey = "mvk_office_only_token"
	require.NoError(t, f.db.Create(&models.APIKey{UserID: f.customer.ID.String(), Name: "office", KeyHash: models.HashAPIToken(officeKey),
		Prefix: "mvk_offi", IsActive: true, AllowedIPs: []string{"192.0.2.0/24"}}).Error)
	rec = f.do(t, staffReq{method: http.MethodPost, path: "/api/v1/settings/sessions/revoke-others", bearer: officeKey})
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "may not manage account settings or API keys")
	// A refused email change writes nothing, the name sent with it included.
	cust := f.login(t, f.customer.Email)
	rec = f.do(t, staffReq{method: http.MethodPut, path: "/api/v1/settings/profile", cookies: []*http.Cookie{cust},
		body: `{"name":"Renamed Elsewhere","email":"elsewhere@example.com","current_password":"wrong"}`})
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	var custName string
	require.NoError(t, f.db.Table("users").Select("name").Where("id = ?", f.customer.ID.String()).Scan(&custName).Error)
	assert.NotEqual(t, "Renamed Elsewhere", custName)

	// Signing out ends only this session.
	a, b := f.login(t, f.customer.Email), f.login(t, f.customer.Email)
	require.Equal(t, http.StatusOK, f.do(t, staffReq{method: http.MethodPost, path: "/api/v1/auth/logout", cookies: []*http.Cookie{a}}).Code)
	assert.Equal(t, http.StatusUnauthorized, f.do(t, staffReq{method: http.MethodGet, path: "/api/v1/auth/me", cookies: []*http.Cookie{a}}).Code)
	assert.Equal(t, http.StatusOK, f.do(t, staffReq{method: http.MethodGet, path: "/api/v1/auth/me", cookies: []*http.Cookie{b}}).Code)
}
