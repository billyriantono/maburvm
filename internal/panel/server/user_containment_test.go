package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/shared/config"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestMain pins the JWT signing secret for the test process so minted access
// tokens validate against the same key the middleware resolves (the secret
// package caches the resolved pair process-wide and requires BOTH keys).
func TestMain(m *testing.M) {
	os.Setenv("JWT_SECRET_KEY", testJWTSecret)
	// 32-byte AES key (raw) so secret resolution succeeds.
	os.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	os.Exit(m.Run())
}

// testJWTSecret is injected for the test process so we can mint valid access
// tokens deterministically (mirrors how the middleware resolves JWT_SECRET_KEY).
const testJWTSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// setupUserContainmentDB builds an in-memory SQLite DB with the users table and
// seeds the given users (in order). created_at order is what the founding-admin
// resolution relies on, so callers control insertion order explicitly.
func setupUserContainmentDB(t *testing.T, users ...*models.User) *gorm.DB {
	t.Helper()
	// Unique shared-cache in-memory DB per test so seeded data does not collide
	// across tests (a fixed name would persist rows from prior tests).
	name := "file:usercontainment_" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(name), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	ddl := `CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		email TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		role TEXT DEFAULT 'client',
quota_mode TEXT NOT NULL DEFAULT 'legacy',
				two_factor_secret TEXT,
		two_factor_backup_codes TEXT,
		ip_whitelist TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		token_revoked_at DATETIME, deleted_at DATETIME
	)`
	require.NoError(t, db.Exec(ddl).Error)
	// Seed in deterministic order with explicit timestamps so founding-admin
	// resolution (created_at ASC) is reproducible.
	for i, u := range users {
		if u.ID == uuid.Nil {
			u.ID = uuid.New()
		}
		created := time.Now().Add(time.Duration(i) * time.Minute)
		hash := "hashed"
		if u.PasswordHash == "" {
			u.PasswordHash = hash
		}
		err := db.Exec(
			"INSERT INTO users (id, email, password_hash, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
			u.ID.String(), u.Email, u.PasswordHash, string(u.Role), created, created,
		).Error
		require.NoError(t, err)
	}
	return db
}

// mintToken signs an access token for the given user (no DB fetch needed here;
// the middleware re-fetches the user from DB to derive role/permissions).
func mintToken(t *testing.T, user *models.User) string {
	t.Helper()
	now := time.Now()
	claims := middleware.JWTClaims{
		UserID:    user.ID.String(),
		Email:     user.Email,
		Role:      string(user.Role),
		TokenType: "access",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Subject:   user.ID.String(),
			Issuer:    "maburvm-panel",
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString([]byte(testJWTSecret))
	require.NoError(t, err)
	return signed
}

// newUserServer wires a Server with the given DB and registers ONLY the
// user-management routes (plus their auth/permission middleware) so we can
// exercise Phase 1A containment without standing up the whole route tree.
func newUserServer(t *testing.T, db *gorm.DB) *Server {
	t.Helper()
	s := NewServer(db, &config.Config{})
	// Register only the user routes group via the same setup used in production.
	v1 := s.echo.Group("/api/v1")
	s.setupUserRoutes(v1)
	return s
}

func doUserReq(t *testing.T, s *Server, method, path, token string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	if token != "" {
		req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)
	return rec
}

func countUsersByRole(t *testing.T, db *gorm.DB, role models.UserRole) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(&models.User{}).Where("role = ? AND deleted_at IS NULL", role).Count(&n).Error)
	return n
}

// --- Founding admin setup helper -------------------------------------------

func adminUser(email string) *models.User {
	return &models.User{Email: email, Role: models.RoleAdmin}
}
func clientUser(email string) *models.User {
	return &models.User{Email: email, Role: models.RoleClient}
}

// TestUserRoutes_DirectClientCreateRejected verifies that creating a client via
// the legacy direct user API is rejected (invite-only) rather than silently
// creating a client.
func TestUserRoutes_DirectClientCreateRejected(t *testing.T) {
	founding := adminUser("founding@example.com")
	db := setupUserContainmentDB(t, founding)
	s := newUserServer(t, db)

	tok := mintToken(t, founding)
	before := countUsersByRole(t, db, models.RoleClient)

	rec := doUserReq(t, s, http.MethodPost, "/api/v1/users", tok,
		`{"email":"newclient@example.com","password":"Sup3rSecret!","role":"client"}`)

	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "invitation_only_unavailable", body["error"])

	// No client should have been created.
	after := countUsersByRole(t, db, models.RoleClient)
	assert.Equal(t, before, after, "a client must not be created")
}

// TestUserRoutes_FoundingAdminCanCreateAdmin verifies the founding admin can
// create another admin and it is persisted.
func TestUserRoutes_FoundingAdminCanCreateAdmin(t *testing.T) {
	founding := adminUser("founding@example.com")
	db := setupUserContainmentDB(t, founding)
	s := newUserServer(t, db)

	tok := mintToken(t, founding)
	before := countUsersByRole(t, db, models.RoleAdmin)

	rec := doUserReq(t, s, http.MethodPost, "/api/v1/users", tok,
		`{"email":"newadmin@example.com","password":"Sup3rSecret!","role":"admin"}`)

	assert.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	after := countUsersByRole(t, db, models.RoleAdmin)
	assert.Equal(t, before+1, after, "founding admin must be able to create an admin")
}

// TestUserRoutes_PeerAdminCannotCreateAdmin verifies an ordinary (non-founding)
// admin is denied creating an admin.
func TestUserRoutes_PeerAdminCannotCreateAdmin(t *testing.T) {
	// founding seeded first, then a peer admin (created later => not founding).
	founding := adminUser("founding@example.com")
	peer := adminUser("peer@example.com")
	db := setupUserContainmentDB(t, founding, peer)
	s := newUserServer(t, db)

	tok := mintToken(t, peer)
	before := countUsersByRole(t, db, models.RoleAdmin)

	rec := doUserReq(t, s, http.MethodPost, "/api/v1/users", tok,
		`{"email":"another@example.com","password":"Sup3rSecret!","role":"admin"}`)

	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Contains(t, body["error"], "founding_administrator")

	after := countUsersByRole(t, db, models.RoleAdmin)
	assert.Equal(t, before, after, "peer admin must not create an admin")
}

// TestUserRoutes_PeerAdminCannotCreateClient verifies a peer admin also cannot
// directly create a client (still invite-only for everyone).
func TestUserRoutes_PeerAdminCannotCreateClient(t *testing.T) {
	founding := adminUser("founding@example.com")
	peer := adminUser("peer@example.com")
	db := setupUserContainmentDB(t, founding, peer)
	s := newUserServer(t, db)

	tok := mintToken(t, peer)
	before := countUsersByRole(t, db, models.RoleClient)

	rec := doUserReq(t, s, http.MethodPost, "/api/v1/users", tok,
		`{"email":"c@example.com","password":"Sup3rSecret!","role":"client"}`)

	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	after := countUsersByRole(t, db, models.RoleClient)
	assert.Equal(t, before, after)
}

// TestUserRoutes_FoundingAdminCanPromoteAdmin verifies the founding admin can
// promote a client to admin.
func TestUserRoutes_FoundingAdminCanPromoteAdmin(t *testing.T) {
	founding := adminUser("founding@example.com")
	client := clientUser("client@example.com")
	db := setupUserContainmentDB(t, founding, client)
	s := newUserServer(t, db)

	tok := mintToken(t, founding)
	rec := doUserReq(t, s, http.MethodPut, "/api/v1/users/"+client.ID.String(), tok,
		`{"role":"admin"}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		Data models.User `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, models.RoleAdmin, resp.Data.Role)
}

// TestUserRoutes_PeerAdminCannotPromoteAdmin verifies a peer admin is denied
// promoting a client to admin.
func TestUserRoutes_PeerAdminCannotPromoteAdmin(t *testing.T) {
	founding := adminUser("founding@example.com")
	peer := adminUser("peer@example.com")
	client := clientUser("client@example.com")
	db := setupUserContainmentDB(t, founding, peer, client)
	s := newUserServer(t, db)

	tok := mintToken(t, peer)
	rec := doUserReq(t, s, http.MethodPut, "/api/v1/users/"+client.ID.String(), tok,
		`{"role":"admin"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Contains(t, body["error"], "founding_administrator")
}

// TestUserRoutes_FoundingAdminCanDemoteAdmin verifies the founding admin can
// demote another admin to client.
func TestUserRoutes_FoundingAdminCanDemoteAdmin(t *testing.T) {
	founding := adminUser("founding@example.com")
	other := adminUser("other@example.com")
	db := setupUserContainmentDB(t, founding, other)
	s := newUserServer(t, db)

	tok := mintToken(t, founding)
	rec := doUserReq(t, s, http.MethodPut, "/api/v1/users/"+other.ID.String(), tok,
		`{"role":"client"}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		Data models.User `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, models.RoleClient, resp.Data.Role)
}

// TestUserRoutes_PeerAdminCannotDemoteAdmin verifies a peer admin is denied
// demoting another admin.
func TestUserRoutes_PeerAdminCannotDemoteAdmin(t *testing.T) {
	founding := adminUser("founding@example.com")
	peer := adminUser("peer@example.com")
	other := adminUser("other@example.com")
	db := setupUserContainmentDB(t, founding, peer, other)
	s := newUserServer(t, db)

	tok := mintToken(t, peer)
	rec := doUserReq(t, s, http.MethodPut, "/api/v1/users/"+other.ID.String(), tok,
		`{"role":"client"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}

// TestUserRoutes_LastAdminDemoteGuarded verifies the last active admin cannot be
// demoted (no active admin would remain).
func TestUserRoutes_LastAdminDemoteGuarded(t *testing.T) {
	founding := adminUser("founding@example.com")
	db := setupUserContainmentDB(t, founding)
	s := newUserServer(t, db)

	tok := mintToken(t, founding)
	rec := doUserReq(t, s, http.MethodPut, "/api/v1/users/"+founding.ID.String(), tok,
		`{"role":"client"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "cannot_demote_last_active_admin", body["error"])

	// Role unchanged.
	still := models.RoleClient
	require.NoError(t, db.Model(&models.User{}).Where("id = ?", founding.ID).Select("role").Scan(&still).Error)
	assert.Equal(t, models.RoleAdmin, still)
}

// TestUserRoutes_LastAdminDeleteGuarded verifies the last active admin cannot be
// deleted (no active admin would remain).
func TestUserRoutes_LastAdminDeleteGuarded(t *testing.T) {
	founding := adminUser("founding@example.com")
	db := setupUserContainmentDB(t, founding)
	s := newUserServer(t, db)

	tok := mintToken(t, founding)
	rec := doUserReq(t, s, http.MethodDelete, "/api/v1/users/"+founding.ID.String(), tok, "")
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "cannot_delete_last_active_admin", body["error"])

	// Still present.
	remaining := countUsersByRole(t, db, models.RoleAdmin)
	assert.Equal(t, int64(1), remaining)
}

// TestUserRoutes_DeleteAdminLeavesFoundingIfMultiple verifies deleting a
// non-last admin succeeds (founding admin path is not blocked by last-admin).
func TestUserRoutes_DeleteAdminLeavesFoundingIfMultiple(t *testing.T) {
	founding := adminUser("founding@example.com")
	other := adminUser("other@example.com")
	db := setupUserContainmentDB(t, founding, other)
	s := newUserServer(t, db)

	tok := mintToken(t, founding)
	rec := doUserReq(t, s, http.MethodDelete, "/api/v1/users/"+other.ID.String(), tok, "")
	assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	remaining := countUsersByRole(t, db, models.RoleAdmin)
	assert.Equal(t, int64(1), remaining)
}

// TestUserRoutes_PermissionMiddlewareStillEnforced verifies an unauthenticated
// request is rejected before any containment logic runs (existing auth checks
// are preserved).
func TestUserRoutes_PermissionMiddlewareStillEnforced(t *testing.T) {
	founding := adminUser("founding@example.com")
	db := setupUserContainmentDB(t, founding)
	s := newUserServer(t, db)

	// No token => 401, not a create.
	rec := doUserReq(t, s, http.MethodPost, "/api/v1/users", "",
		`{"email":"x@example.com","password":"Sup3rSecret!","role":"admin"}`)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestUserRoutes_ClientCannotCallUserAdminEndpoints verifies ordinary clients
// (without user:* permission) are forbidden from user-management endpoints.
func TestUserRoutes_ClientCannotCallUserAdminEndpoints(t *testing.T) {
	founding := adminUser("founding@example.com")
	client := clientUser("client@example.com")
	db := setupUserContainmentDB(t, founding, client)
	s := newUserServer(t, db)

	tok := mintToken(t, client)
	// Clients lack user:create/read; the group middleware rejects them.
	rec := doUserReq(t, s, http.MethodGet, "/api/v1/users", tok, "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// TestFoundingAdminIDResolution verifies the earliest-created active admin is
// selected as the founding admin deterministically.
func TestFoundingAdminIDResolution(t *testing.T) {
	founding := adminUser("founding@example.com")
	peer := adminUser("peer@example.com")
	db := setupUserContainmentDB(t, founding, peer)
	s := newUserServer(t, db)

	got := s.foundingAdminID(context.Background())
	assert.Equal(t, founding.ID, got)

	// If the founding admin is soft-deleted, the next-earliest active admin is
	// chosen (resolution follows creation order among active admins).
	require.NoError(t, db.Model(&models.User{}).Where("id = ?", founding.ID).
		Update("deleted_at", time.Now()).Error)
	got2 := s.foundingAdminID(context.Background())
	assert.Equal(t, peer.ID, got2)
}

// TestUserRoutes_NonRoleUpdateIsSelective verifies that an email-only PUT uses a
// column-scoped update and never writes the `role` column. We simulate a stale
// pre-read by directly writing a divergent `role` to the row AFTER the handler's
// pre-read, then issuing an email update; the role must be preserved (the
// selective update must not clobber it).
func TestUserRoutes_NonRoleUpdateIsSelective(t *testing.T) {
	founding := adminUser("founding@example.com")
	target := clientUser("target@example.com")
	db := setupUserContainmentDB(t, founding, target)
	s := newUserServer(t, db)

	tok := mintToken(t, founding)

	// Simulate the row having been concurrently promoted to admin between the
	// handler's GET-by-ID pre-read and its UPDATE (a stale pre-read scenario).
	require.NoError(t, db.Model(&models.User{}).Where("id = ?", target.ID).
		Update("role", string(models.RoleAdmin)).Error)

	rec := doUserReq(t, s, http.MethodPut, "/api/v1/users/"+target.ID.String(), tok,
		`{"email":"renamed@example.com"}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		Data models.User `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	// Email was changed as requested.
	assert.Equal(t, "renamed@example.com", resp.Data.Email)
	// Role must remain what the concurrent transition set (admin), proving the
	// selective update did NOT overwrite role with the stale pre-read value.
	assert.Equal(t, models.RoleAdmin, resp.Data.Role)

	// Confirm at the DB layer that role is still admin (not clobbered).
	var still models.UserRole
	require.NoError(t, db.Model(&models.User{}).Where("id = ?", target.ID).
		Select("role").Scan(&still).Error)
	assert.Equal(t, models.RoleAdmin, still)
}

// TestUserRoutes_NonRoleUpdatePreservesRoleAgainstStalePreRead is the inverse of
// the above with an admin target: a concurrent demotion must survive an email
// update made from a stale (admin) pre-read.
func TestUserRoutes_NonRoleUpdatePreservesRoleAgainstStalePreRead(t *testing.T) {
	founding := adminUser("founding@example.com")
	target := adminUser("target@example.com")
	db := setupUserContainmentDB(t, founding, target)
	s := newUserServer(t, db)

	tok := mintToken(t, founding)

	// Concurrent demotion to client happens after the handler's pre-read.
	require.NoError(t, db.Model(&models.User{}).Where("id = ?", target.ID).
		Update("role", string(models.RoleClient)).Error)

	rec := doUserReq(t, s, http.MethodPut, "/api/v1/users/"+target.ID.String(), tok,
		`{"email":"renamed2@example.com"}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var still models.UserRole
	require.NoError(t, db.Model(&models.User{}).Where("id = ?", target.ID).
		Select("role").Scan(&still).Error)
	// The selective email update must not resurrect the stale 'admin' role.
	assert.Equal(t, models.RoleClient, still)
}

// TestUserRoutes_NoBodyUpdateIsNoOp verifies that a PUT with neither email nor a
// role change is a safe no-op that does not touch role and does not error.
func TestUserRoutes_NoBodyUpdateIsNoOp(t *testing.T) {
	founding := adminUser("founding@example.com")
	target := clientUser("target@example.com")
	db := setupUserContainmentDB(t, founding, target)
	s := newUserServer(t, db)

	tok := mintToken(t, founding)
	rec := doUserReq(t, s, http.MethodPut, "/api/v1/users/"+target.ID.String(), tok, `{}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var still models.UserRole
	require.NoError(t, db.Model(&models.User{}).Where("id = ?", target.ID).
		Select("role").Scan(&still).Error)
	assert.Equal(t, models.RoleClient, still)
}

// TestUserRoutes_RoleTransitionStillInsideLock verifies that a role transition
// still requires founding authority and updates role correctly (regression for
// the still-locked path after the selective-update refactor).
func TestUserRoutes_RoleTransitionStillInsideLock(t *testing.T) {
	founding := adminUser("founding@example.com")
	target := clientUser("target@example.com")
	db := setupUserContainmentDB(t, founding, target)
	s := newUserServer(t, db)

	tok := mintToken(t, founding)
	rec := doUserReq(t, s, http.MethodPut, "/api/v1/users/"+target.ID.String(), tok,
		`{"role":"admin"}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp struct {
		Data models.User `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, models.RoleAdmin, resp.Data.Role)

	// Combined email + role change in one request also persists email.
	tok2 := mintToken(t, founding)
	rec2 := doUserReq(t, s, http.MethodPut, "/api/v1/users/"+target.ID.String(), tok2,
		`{"role":"client","email":"demoted@example.com"}`)
	assert.Equal(t, http.StatusOK, rec2.Code, rec2.Body.String())
	var resp2 struct {
		Data models.User `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec2.Body.Bytes(), &resp2))
	assert.Equal(t, models.RoleClient, resp2.Data.Role)
	assert.Equal(t, "demoted@example.com", resp2.Data.Email)
}

// TestUserRoutes_StaleProfileCannotRevertQuotaMode proves that a non-role
// profile update (email) can NEVER revert a concurrently-managed quota_mode
// back to legacy. We set quota_mode='managed' on the row AFTER the handler's
// pre-read, then issue an email-only PUT; the quota_mode must remain 'managed'.
func TestUserRoutes_StaleProfileCannotRevertQuotaMode(t *testing.T) {
	founding := adminUser("founding@example.com")
	target := clientUser("target@example.com")
	db := setupUserContainmentDB(t, founding, target)
	// Mark the target as managed quota (simulating the concurrent quota service).
	require.NoError(t, db.Model(&models.User{}).Where("id = ?", target.ID).
		Update("quota_mode", string(models.QuotaModeManaged)).Error)
	s := newUserServer(t, db)

	tok := mintToken(t, founding)
	rec := doUserReq(t, s, http.MethodPut, "/api/v1/users/"+target.ID.String(), tok,
		`{"email":"renamed-qm@example.com"}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var final models.User
	require.NoError(t, db.WithContext(context.Background()).Where("id = ?", target.ID).First(&final).Error)
	// Email changed, but quota_mode must NOT have been reverted by the profile save.
	assert.Equal(t, "renamed-qm@example.com", final.Email)
	assert.Equal(t, models.QuotaModeManaged, final.QuotaMode, "profile update must not revert managed quota_mode to legacy")
}

// TestUserRoutes_IPWhitelistUpdateSucceeds proves the (non-role) ip_whitelist
// field is a permitted, writable column and persists through the selective path.
func TestUserRoutes_IPWhitelistUpdateSucceeds(t *testing.T) {
	founding := adminUser("founding@example.com")
	target := clientUser("target@example.com")
	db := setupUserContainmentDB(t, founding, target)
	s := newUserServer(t, db)

	tok := mintToken(t, founding)
	rec := doUserReq(t, s, http.MethodPut, "/api/v1/users/"+target.ID.String(), tok,
		`{"ip_whitelist":["203.0.113.5","198.51.100.9"]}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var final models.User
	require.NoError(t, db.WithContext(context.Background()).Where("id = ?", target.ID).First(&final).Error)
	assert.Equal(t, []string{"203.0.113.5", "198.51.100.9"}, final.IPWhitelist)
	// And role was untouched.
	assert.Equal(t, models.RoleClient, final.Role)
}

// TestUserRoutes_IPWhitelistInvalidRejected proves a malformed IP whitelist is
// rejected with 400 and nothing is written (the email field is NOT applied).
func TestUserRoutes_IPWhitelistInvalidRejected(t *testing.T) {
	founding := adminUser("founding@example.com")
	target := clientUser("target@example.com")
	db := setupUserContainmentDB(t, founding, target)
	s := newUserServer(t, db)

	tok := mintToken(t, founding)
	rec := doUserReq(t, s, http.MethodPut, "/api/v1/users/"+target.ID.String(), tok,
		`{"email":"keepme@example.com","ip_whitelist":["not-an-ip"]}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	var final models.User
	require.NoError(t, db.WithContext(context.Background()).Where("id = ?", target.ID).First(&final).Error)
	assert.Equal(t, "target@example.com", final.Email, "invalid whitelist must be a no-op; email unchanged")
}

// TestUserRoutes_RoleTransitionWithIPWhitelist proves a combined role +
// ip_whitelist PUT (inside the advisory-locked boundary tx) persists BOTH the
// role change and the whitelist, while quota_mode stays untouched.
func TestUserRoutes_RoleTransitionWithIPWhitelist(t *testing.T) {
	founding := adminUser("founding@example.com")
	target := clientUser("target@example.com")
	db := setupUserContainmentDB(t, founding, target)
	require.NoError(t, db.Model(&models.User{}).Where("id = ?", target.ID).
		Update("quota_mode", string(models.QuotaModeManaged)).Error)
	s := newUserServer(t, db)

	tok := mintToken(t, founding)
	rec := doUserReq(t, s, http.MethodPut, "/api/v1/users/"+target.ID.String(), tok,
		`{"role":"admin","ip_whitelist":["203.0.113.9"]}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var final models.User
	require.NoError(t, db.WithContext(context.Background()).Where("id = ?", target.ID).First(&final).Error)
	assert.Equal(t, models.RoleAdmin, final.Role)
	assert.Equal(t, []string{"203.0.113.9"}, final.IPWhitelist)
	assert.Equal(t, models.QuotaModeManaged, final.QuotaMode, "quota_mode untouched by role transition")
}
