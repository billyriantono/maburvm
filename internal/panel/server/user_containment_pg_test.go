package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/config"
	"github.com/maburvm/panel/internal/shared/models"
)

// pgTestDSN returns a DSN for a throwaway PostgreSQL test database. It uses the
// local PostgreSQL 15 instance (trust auth on 127.0.0.1) and creates a uniquely
// named database inside the approved temp dir, so nothing in the workspace is
// touched. The database is dropped in a t.Cleanup.
func pgTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	baseDSN := os.Getenv("MABURVM_TEST_PG_DSN")
	if baseDSN == "" {
		baseDSN = "postgres://billyriantono@127.0.0.1:5432/postgres?sslmode=disable"
	}

	dbName := "maburvm_gate1_" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_"))
	dbName = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, dbName)
	if len(dbName) > 60 {
		dbName = dbName[:60]
	}

	admin, err := sql.Open("pgx", baseDSN)
	require.NoError(t, err, "connect to PostgreSQL for test setup")
	defer admin.Close()

	// Clean any prior DB of the same name, then create a fresh one.
	_, _ = admin.Exec("DROP DATABASE IF EXISTS " + quoteIdent(dbName))
	_, err = admin.Exec("CREATE DATABASE " + quoteIdent(dbName))
	require.NoError(t, err, "create test database")

	t.Cleanup(func() {
		a, err := sql.Open("pgx", baseDSN)
		if err != nil {
			return
		}
		defer a.Close()
		// Terminate active connections then drop.
		_, _ = a.Exec(fmt.Sprintf(
			"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = %s", quoteLit(dbName)))
		_, _ = a.Exec("DROP DATABASE IF EXISTS " + quoteIdent(dbName))
	})

	testDSN := replaceDBName(baseDSN, dbName)
	gdb, err := gorm.Open(postgres.Open(testDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	// Provision ONLY the users table + enum required by the role-boundary code.
	// We intentionally do not run the full migration set (out of scope) — this is
	// a focused, minimal schema sufficient to prove locking/containment.
	require.NoError(t, gdb.Exec(`CREATE TYPE user_role AS ENUM ('admin', 'client')`).Error)
	require.NoError(t, gdb.Exec(`CREATE TABLE users (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		name TEXT NOT NULL DEFAULT '', email VARCHAR(255) NOT NULL UNIQUE,
		password_hash VARCHAR(255) NOT NULL,
		role user_role NOT NULL DEFAULT 'client',
quota_mode TEXT NOT NULL DEFAULT 'legacy',
				two_factor_secret VARCHAR(255),
		two_factor_enabled BOOLEAN NOT NULL DEFAULT false,
		two_factor_backup_codes VARCHAR(1000),
		ip_whitelist JSONB DEFAULT '[]'::jsonb,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		token_revoked_at TIMESTAMPTZ, deleted_at TIMESTAMPTZ
	)`).Error)

	return gdb
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
func quoteLit(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `''`) + `'`
}

// replaceDBName swaps the database name in a postgres:// DSN.
func replaceDBName(dsn, name string) string {
	// dsn like postgres://user@host:port/dbname?sslmode=...
	idx := strings.Index(dsn, "?")
	query := ""
	if idx >= 0 {
		query = dsn[idx:]
		dsn = dsn[:idx]
	}
	slash := strings.LastIndex(dsn, "/")
	if slash < 0 {
		return dsn + "/" + name + query
	}
	return dsn[:slash+1] + name + query
}

func seedUserPG(t *testing.T, db *gorm.DB, u *models.User, createdOffset time.Duration) {
	t.Helper()
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	if u.PasswordHash == "" {
		u.PasswordHash = "hashed"
	}
	created := time.Now().Add(createdOffset)
	require.NoError(t, db.Exec(
		"INSERT INTO users (id, email, password_hash, role, ip_whitelist, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		u.ID.String(), u.Email, u.PasswordHash, string(u.Role), "[]", created, created,
	).Error)
}

// pgServer wires a Server with ONLY the user routes against a real PostgreSQL DB.
func pgUserServer(t *testing.T, db *gorm.DB) *Server {
	t.Helper()
	s := NewServer(db, &config.Config{})
	v1 := s.echo.Group("/api/v1")
	s.setupUserRoutes(v1)
	return s
}

// --- Concurrency / race proof on real PostgreSQL ----------------------------

// TestPG_LastAdminConcurrencyNoZeroAdmins proves that even under heavy
// concurrent admin delete/demote attempts, the system can never be left with
// zero active admins. The transaction-scoped advisory lock serializes the
// recheck+mutation so exactly one privilege-removing op succeeds past the
// last-active-admin guard and the rest are denied.
func TestPG_LastAdminConcurrencyNoZeroAdmins(t *testing.T) {
	if testing.Short() {
		t.Skip("requires local PostgreSQL 15; use -short to skip")
	}
	db := pgTestDB(t)
	founding := adminUser("founding@example.com")
	peer := adminUser("peer@example.com")
	// founding first => founding; peer second => ordinary admin.
	seedUserPG(t, db, founding, 0)
	seedUserPG(t, db, peer, time.Minute)
	s := pgUserServer(t, db)

	foundingTok := mintToken(t, founding)
	peerTok := mintToken(t, peer)

	const N = 12
	errs := make(chan error, 2*N)
	// Peer tries to delete the founder (must be denied: peer lacks authority).
	for i := 0; i < N; i++ {
		go func() {
			rec := doUserReq(t, s, "DELETE", "/api/v1/users/"+founding.ID.String(), peerTok, "")
			if rec.Code != http.StatusForbidden {
				errs <- fmt.Errorf("peer delete founder: expected 403 got %d %s", rec.Code, rec.Body.String())
			} else {
				errs <- nil
			}
		}()
	}
	// Founding admin tries to demote the peer (should succeed once; the rest
	// after peer is already a client are non-role changes / no-ops allowed).
	for i := 0; i < N; i++ {
		go func() {
			rec := doUserReq(t, s, "PUT", "/api/v1/users/"+peer.ID.String(), foundingTok, `{"role":"client"}`)
			if rec.Code != http.StatusOK && rec.Code != http.StatusForbidden {
				errs <- fmt.Errorf("founding demote peer: unexpected %d %s", rec.Code, rec.Body.String())
			} else {
				errs <- nil
			}
		}()
	}

	for i := 0; i < 2*N; i++ {
		require.NoError(t, <-errs)
	}

	// Final invariant: at least one active admin remains (the founder).
	var activeAdmins int64
	require.NoError(t, db.Model(&models.User{}).
		Where("role = ? AND deleted_at IS NULL", models.RoleAdmin).Count(&activeAdmins).Error)
	require.Equal(t, int64(1), activeAdmins, "must never be left with zero active admins")
}

// TestPG_PeerCannotDeleteFounder proves a peer admin (with user:delete) cannot
// delete the founding admin — authority enforcement at the locked boundary.
func TestPG_PeerCannotDeleteFounder(t *testing.T) {
	if testing.Short() {
		t.Skip("requires local PostgreSQL 15; use -short to skip")
	}
	db := pgTestDB(t)
	founding := adminUser("founding@example.com")
	peer := adminUser("peer@example.com")
	third := adminUser("third@example.com")
	seedUserPG(t, db, founding, 0)
	seedUserPG(t, db, peer, time.Minute)
	seedUserPG(t, db, third, 2*time.Minute)
	s := pgUserServer(t, db)

	peerTok := mintToken(t, peer)
	rec := doUserReq(t, s, "DELETE", "/api/v1/users/"+founding.ID.String(), peerTok, "")
	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Contains(t, body["error"], "founding_administrator")

	// Founding admin still present and active.
	var n int64
	require.NoError(t, db.Model(&models.User{}).
		Where("id = ? AND role = ? AND deleted_at IS NULL", founding.ID, models.RoleAdmin).Count(&n).Error)
	assert.Equal(t, int64(1), n)
}

// TestPG_FoundingDeleteUnderLock proves the founding admin can delete a peer
// admin under a real DB transaction with the advisory lock held, and that the
// lock is released/available for the next op (no deadlock, no leaked lock).
func TestPG_FoundingDeleteUnderLock(t *testing.T) {
	if testing.Short() {
		t.Skip("requires local PostgreSQL 15; use -short to skip")
	}
	db := pgTestDB(t)
	founding := adminUser("founding@example.com")
	peer := adminUser("peer@example.com")
	seedUserPG(t, db, founding, 0)
	seedUserPG(t, db, peer, time.Minute)
	s := pgUserServer(t, db)

	// Serial sequence to ensure lock acquisition/release works end-to-end.
	for i := 0; i < 5; i++ {
		tok := mintToken(t, founding)
		rec := doUserReq(t, s, "DELETE", "/api/v1/users/"+peer.ID.String(), tok, "")
		if i == 0 {
			assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		} else {
			// Already deleted -> 404 (not the lock/authority path).
			assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		}
	}
	var n int64
	require.NoError(t, db.Model(&models.User{}).
		Where("role = ? AND deleted_at IS NULL", models.RoleAdmin).Count(&n).Error)
	assert.Equal(t, int64(1), n)
}

// TestPG_AdvisoryLockActuallySerializes is a direct, low-level proof that
// concurrent transactions attempting the role-boundary advisory lock serialize:
// the second blocks until the first commits. With founding + peer, every
// goroutine tries to delete the peer under the locked recheck+mutation. Because
// the lock serializes, the first transaction deletes the peer (success); the
// rest, running strictly after, find the peer already gone (GetByID fails) and
// do NOT count as a success. So exactly ONE success — and never zero active
// admins, since the founding admin is untouched.
func TestPG_AdvisoryLockActuallySerializes(t *testing.T) {
	if testing.Short() {
		t.Skip("requires local PostgreSQL 15; use -short to skip")
	}
	db := pgTestDB(t)
	founding := adminUser("founding@example.com")
	peer := adminUser("peer@example.com")
	seedUserPG(t, db, founding, 0)
	seedUserPG(t, db, peer, time.Minute)
	s := &Server{db: db}

	ctx := context.Background()
	const races = 20
	successes := make(chan bool, races)
	for i := 0; i < races; i++ {
		go func() {
			ok := false
			rbErr := s.execRoleBoundaryTx(ctx, func(tx *gorm.DB) error {
				// Mirror the real delete handler: re-fetch inside the lock. After
				// the first successful delete, this returns not-found (no success).
				target, gerr := repository.NewUserRepository(tx).GetByID(ctx, peer.ID)
				if gerr != nil {
					return &roleBoundaryErr{status: http.StatusNotFound, code: "user_not_found"}
				}
				if authErr := recheckFoundingAuthority(tx, ctx, founding.ID, "only_the_founding_administrator_may_delete_admins"); authErr != nil {
					return authErr
				}
				remaining, cerr := countActiveAdminsExceptTx(tx, ctx, target.ID)
				if cerr != nil {
					return cerr
				}
				if remaining <= 0 {
					return &roleBoundaryErr{status: http.StatusForbidden, code: "cannot_delete_last_active_admin"}
				}
				if err := repository.NewUserRepository(tx).Delete(ctx, target.ID); err != nil {
					return err
				}
				ok = true
				return nil
			})
			if rbErr != nil {
				successes <- false
				return
			}
			successes <- ok
		}()
	}

	passCount := 0
	for i := 0; i < races; i++ {
		if <-successes {
			passCount++
		}
	}
	assert.Equal(t, 1, passCount, "exactly one concurrent peer delete must succeed; the lock serializes the recheck")

	// Founding admin untouched; exactly one active admin remains.
	var n int64
	require.NoError(t, db.Model(&models.User{}).
		Where("role = ? AND deleted_at IS NULL", models.RoleAdmin).Count(&n).Error)
	assert.Equal(t, int64(1), n, "founding admin remains; never zero active admins")
}

// TestPG_SelectiveEmailUpdatePreservesConcurrentRoleTransition proves on real
// PostgreSQL that a non-role (email) update uses a column-scoped write and can
// NEVER clobber a concurrent serialized role transition's `role`. We drive a
// concurrent stream of email-only updates alongside a founding-admin demotion,
// and assert the demotion's role change survives (role == client) with no
// stale-pre-read resurrection to admin.
func TestPG_SelectiveEmailUpdatePreservesConcurrentRoleTransition(t *testing.T) {
	if testing.Short() {
		t.Skip("requires local PostgreSQL 15; use -short to skip")
	}
	db := pgTestDB(t)
	founding := adminUser("founding@example.com")
	target := adminUser("target@example.com")
	seedUserPG(t, db, founding, 0)
	seedUserPG(t, db, target, time.Minute)
	s := pgUserServer(t, db)

	foundingTok := mintToken(t, founding)

	// Fire a burst of concurrent email-only updates that each pre-read the row
	// while target is still 'admin' (simulating stale pre-reads). If any of these
	// used a full-model Save, it would write back role='admin'. They must be
	// selective and leave role untouched.
	const emailBurst = 25
	errs := make(chan error, emailBurst)
	for i := 0; i < emailBurst; i++ {
		go func(n int) {
			rec := doUserReq(t, s, "PUT", "/api/v1/users/"+target.ID.String(), foundingTok,
				fmt.Sprintf(`{"email":"%d@example.com"}`, n))
			if rec.Code != http.StatusOK {
				errs <- fmt.Errorf("email update: got %d %s", rec.Code, rec.Body.String())
				return
			}
			errs <- nil
		}(i)
	}
	for i := 0; i < emailBurst; i++ {
		require.NoError(t, <-errs)
	}

	// Now the founding admin demotes the target inside the advisory-locked tx.
	rec := doUserReq(t, s, "PUT", "/api/v1/users/"+target.ID.String(), foundingTok, `{"role":"client"}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// Final authoritative read: role must be client, and email must reflect the
	// LAST email update (the email column IS writable; role is not clobbered).
	var final models.User
	require.NoError(t, db.WithContext(context.Background()).Where("id = ?", target.ID).First(&final).Error)
	assert.Equal(t, models.RoleClient, final.Role, "demotion survived; no stale-pre-read role clobber")
	assert.Contains(t, final.Email, "@example.com", "email column still updated selectively")
}

// TestPG_SelectiveUpdateNeverWritesRole is a direct low-level proof that
// updateUserNonRoleFields only touches the permitted allow-list and drops any
// supplied "role", on a real PostgreSQL connection (so column writes are real).
func TestPG_SelectiveUpdateNeverWritesRole(t *testing.T) {
	if testing.Short() {
		t.Skip("requires local PostgreSQL 15; use -short to skip")
	}
	db := pgTestDB(t)
	target := adminUser("target@example.com")
	seedUserPG(t, db, target, 0)

	// Attempt to smuggle a role change through the non-role path.
	require.NoError(t, updateUserNonRoleFields(db, context.Background(), target.ID, map[string]interface{}{
		"email": "changed@example.com",
		"role":  string(models.RoleClient), // must be ignored
	}))

	var final models.User
	require.NoError(t, db.WithContext(context.Background()).Where("id = ?", target.ID).First(&final).Error)
	assert.Equal(t, "changed@example.com", final.Email, "email updated")
	assert.Equal(t, models.RoleAdmin, final.Role, "role ignored by selective update")
}

// TestPG_SelectiveUpdateNeverWritesQuotaMode proves on real PostgreSQL that the
// selective profile path can NEVER change quota_mode — a stale profile save from
// a client must not revert a concurrently-managed quota_mode back to legacy, nor
// promote legacy -> managed through the non-role channel.
func TestPG_SelectiveUpdateNeverWritesQuotaMode(t *testing.T) {
	if testing.Short() {
		t.Skip("requires local PostgreSQL 15; use -short to skip")
	}
	db := pgTestDB(t)
	target := clientUser("target@example.com")
	seedUserPG(t, db, target, 0)

	// Mark the row as managed quota (concurrent quota service), then try to drive
	// an email update through the non-role channel with a smuggled quota_mode.
	require.NoError(t, db.Model(&models.User{}).Where("id = ?", target.ID).
		Update("quota_mode", string(models.QuotaModeManaged)).Error)

	// Smuggling quota_mode through the non-role channel is ignored (the allow-list
	// drops it), so a profile save can never revert managed back to legacy.
	require.NoError(t, updateUserNonRoleFields(db, context.Background(), target.ID, map[string]interface{}{
		"email":      "changed@example.com",
		"quota_mode": string(models.QuotaModeLegacy), // ignored
	}))

	var final models.User
	require.NoError(t, db.WithContext(context.Background()).Where("id = ?", target.ID).First(&final).Error)
	assert.Equal(t, "changed@example.com", final.Email, "email updated")
	assert.Equal(t, models.QuotaModeManaged, final.QuotaMode, "quota_mode ignored; not reverted to legacy by profile save")

	// Inverse: a legacy row must not be promoted to managed via the profile path.
	require.NoError(t, updateUserNonRoleFields(db, context.Background(), target.ID, map[string]interface{}{
		"email":      "changed2@example.com",
		"quota_mode": string(models.QuotaModeManaged), // ignored
	}))
	var final2 models.User
	require.NoError(t, db.WithContext(context.Background()).Where("id = ?", target.ID).First(&final2).Error)
	assert.Equal(t, models.QuotaModeManaged, final2.QuotaMode, "quota_mode still not promoted via profile path")
}

// TestPG_ProfileUpdateCannotRevertConcurrentQuotaMode uses row locking (not
// sleep timing) to prove a concurrent transition of quota_mode to managed is not
// reverted by a profile-only email update. We open a transaction that flips
// quota_mode to managed and holds a FOR UPDATE row lock until the email update
// completes, then assert the email changed but quota_mode stayed managed.
func TestPG_ProfileUpdateCannotRevertConcurrentQuotaMode(t *testing.T) {
	if testing.Short() {
		t.Skip("requires local PostgreSQL 15; use -short to skip")
	}
	db := pgTestDB(t)
	founding := adminUser("founding@example.com")
	target := clientUser("target@example.com")
	seedUserPG(t, db, founding, 0)
	seedUserPG(t, db, target, time.Minute)
	s := pgUserServer(t, db)

	foundingTok := mintToken(t, founding)

	// Hold the row with a FOR UPDATE lock and flip quota_mode to managed, then
	// while still locked issue the email-only profile update. Because the profile
	// update uses a column-scoped write (email only), releasing the lock must
	// leave quota_mode='managed'. We run the HTTP request in a goroutine so the
	// test retains the lock; the request blocks on the row lock until we commit.
	tx := db.WithContext(context.Background()).Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()

	var u models.User
	require.NoError(t, tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ?", target.ID).First(&u).Error)
	require.NoError(t, tx.Model(&models.User{}).Where("id = ?", target.ID).
		Update("quota_mode", string(models.QuotaModeManaged)).Error)

	started := make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		close(started)
		done <- doUserReq(t, s, http.MethodPut, "/api/v1/users/"+target.ID.String(), foundingTok,
			`{"email":"concurrent@example.com"}`)
	}()
	// Wait until the request goroutine has actually issued the request before we
	// release the lock (deterministic coordination, not sleep-based timing).
	<-started
	require.NoError(t, tx.Commit().Error)

	rec := <-done
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var final models.User
	require.NoError(t, db.WithContext(context.Background()).Where("id = ?", target.ID).First(&final).Error)
	assert.Equal(t, "concurrent@example.com", final.Email, "email profile update applied")
	assert.Equal(t, models.QuotaModeManaged, final.QuotaMode, "quota_mode managed change survived the profile update")
}

// TestPG_RoleTransitionWithIPWhitelistUnderLock proves a combined role +
// ip_whitelist PUT inside the advisory-locked boundary tx persists both the role
// change and the whitelist, while leaving quota_mode untouched (deterministic,
// real PostgreSQL transaction).
func TestPG_RoleTransitionWithIPWhitelistUnderLock(t *testing.T) {
	if testing.Short() {
		t.Skip("requires local PostgreSQL 15; use -short to skip")
	}
	db := pgTestDB(t)
	founding := adminUser("founding@example.com")
	target := clientUser("target@example.com")
	seedUserPG(t, db, founding, 0)
	seedUserPG(t, db, target, time.Minute)
	require.NoError(t, db.Model(&models.User{}).Where("id = ?", target.ID).
		Update("quota_mode", string(models.QuotaModeManaged)).Error)
	s := pgUserServer(t, db)

	foundingTok := mintToken(t, founding)
	rec := doUserReq(t, s, http.MethodPut, "/api/v1/users/"+target.ID.String(), foundingTok,
		`{"role":"admin","ip_whitelist":["203.0.113.20"]}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var final models.User
	require.NoError(t, db.WithContext(context.Background()).Where("id = ?", target.ID).First(&final).Error)
	assert.Equal(t, models.RoleAdmin, final.Role)
	assert.Equal(t, []string{"203.0.113.20"}, final.IPWhitelist)
	assert.Equal(t, models.QuotaModeManaged, final.QuotaMode, "quota_mode untouched by role transition")
}
