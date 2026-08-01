package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/labstack/echo/v4"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
)

// roleBoundaryAdvisoryLockKey is the PostgreSQL advisory-lock key that serializes
// every admin role-removal/grant operation (create/promote/demote/delete admin).
// It is a single, fixed, well-known key so all such operations contend on one
// lock and therefore execute strictly one-at-a-time within their transaction.
//
// Locking design (Phase 1A Gate 1):
//   - The admin role boundary (any change that grants or removes the "admin"
//     role) is treated as ONE privileged operation.
//   - Each such operation runs inside a single database transaction. On
//     PostgreSQL we take a transaction-scoped exclusive advisory lock
//     (pg_advisory_xact_lock) BEFORE any read. Because the lock is
//     transaction-scoped it is held until the transaction commits/rolls back and
//     is released automatically — no manual unlock, no leaked locks.
//   - The locked section spans the recheck of caller active/role/founding
//     identity, the target role, the last-active-admin count, and the mutation,
//     so a concurrent operation cannot observe a stale intermediate state.
//   - On non-PostgreSQL dialects (SQLite, used by unit tests) the operation still
//     runs inside a transaction for atomicity; the real concurrency proof is the
//     PostgreSQL path (see user_containment_pg_test.go). Ordinary client changes
//     never enter this path, so no global lock is taken for them.
const roleBoundaryAdvisoryLockKey int64 = 0x524F4C4542445259 // "ROLEDBRY"

// roleBoundaryErr carries an HTTP status and a stable error code out of the
// transactional role-boundary function so handlers can render the same JSON
// shape the prior (raceable) handlers produced.
type roleBoundaryErr struct {
	status int
	code   string
}

func (e *roleBoundaryErr) Error() string { return e.code }

func writeRoleBoundaryErr(c echo.Context, err error) error {
	var rb *roleBoundaryErr
	if errors.As(err, &rb) {
		return c.JSON(rb.status, map[string]interface{}{"error": rb.code})
	}
	return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "internal_error"})
}

// dialectIsPostgres reports whether the dialector targets PostgreSQL, where the
// transaction advisory lock is available.
func dialectIsPostgres(db *gorm.DB) bool {
	return db != nil && db.Dialector != nil && db.Dialector.Name() == "postgres"
}

// execRoleBoundaryTx runs fn inside a single database transaction. On PostgreSQL
// it first acquires the role-boundary advisory lock so the recheck-and-mutate
// sequence in fn is atomic and serialized against every other admin role-boundary
// operation. The advisory lock is released automatically on commit/rollback.
func (s *Server) execRoleBoundaryTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if dialectIsPostgres(tx) {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", roleBoundaryAdvisoryLockKey).Error; err != nil {
				return fmt.Errorf("role boundary lock: %w", err)
			}
		}
		return fn(tx)
	})
}

// recheckFoundingAuthority re-reads the caller and the founding (earliest active)
// admin from the given transaction-scoped DB and enforces that the caller is an
// active admin AND is the founding administrator. The forbidCode is the JSON
// error code returned when authority is missing, so each operation keeps the
// stable code its callers assert on. Returns nil when authority is confirmed.
func recheckFoundingAuthority(db *gorm.DB, ctx context.Context, callerID uuid.UUID, forbidCode string) *roleBoundaryErr {
	var caller models.User
	if err := db.WithContext(ctx).Where("id = ? AND deleted_at IS NULL", callerID).First(&caller).Error; err != nil {
		return &roleBoundaryErr{status: http.StatusForbidden, code: "unauthorized"}
	}
	if caller.Role != models.RoleAdmin {
		return &roleBoundaryErr{status: http.StatusForbidden, code: "insufficient_permissions"}
	}
	if f := foundingAdminIDTx(db, ctx); f == uuid.Nil || caller.ID != f {
		return &roleBoundaryErr{status: http.StatusForbidden, code: forbidCode}
	}
	return nil
}

// foundingAdminIDTx returns the ID of the earliest-created active administrator
// (the founding admin), resolved deterministically by creation order from the
// users table. Returns uuid.Nil when no active admin exists. Operates on the
// supplied (transaction-scoped) DB so the resolution is consistent with the
// locked recheck.
func foundingAdminIDTx(db *gorm.DB, ctx context.Context) uuid.UUID {
	var user models.User
	err := db.WithContext(ctx).
		Where("role = ? AND deleted_at IS NULL", models.RoleAdmin).
		Order("created_at ASC, id ASC").
		First(&user).Error
	if err != nil {
		return uuid.Nil
	}
	return user.ID
}

// countActiveAdminsExceptTx returns the number of active (non-deleted)
// administrators excluding the given ID. Backs the last-admin guard. Operates on
// the supplied (transaction-scoped) DB so the count is consistent with the
// locked recheck + mutation.
func countActiveAdminsExceptTx(db *gorm.DB, ctx context.Context, exclude uuid.UUID) (int64, error) {
	var count int64
	err := db.WithContext(ctx).Model(&models.User{}).
		Where("role = ? AND deleted_at IS NULL AND id <> ?", models.RoleAdmin, exclude).
		Count(&count).Error
	return count, err
}

// isActiveAdminTx reports whether the given user ID is a currently active
// (non-deleted) administrator, read from the supplied DB.
func isActiveAdminTx(db *gorm.DB, ctx context.Context, id uuid.UUID) bool {
	var user models.User
	if err := db.WithContext(ctx).Where("id = ? AND deleted_at IS NULL", id).First(&user).Error; err != nil {
		return false
	}
	return user.Role == models.RoleAdmin
}

// permittedNonRoleColumns is the strict allow-list of User columns that a
// non-role update (e.g. email) may touch. role, quota_mode, permissions, status,
// password-derived, two_factor_secret, and every other security field are
// deliberately excluded so a selective update can never clobber a concurrent
// serialized role transition or revert a managed quota marker. This is the core
// fix for the Gate-1 follow-up: a full-model Save (which persists a stale
// pre-read `role`) is replaced by a column-scoped UPDATE.
//
// quota_mode is NEVER in this list. A stale profile save must not revert a
// concurrently managed quota_mode (legacy -> managed) back to legacy, nor
// promote legacy -> managed. The role-boundary transaction also keeps quota_mode
// untouched because it mutates only freshly-read target fields (Role/Email/
// IPWhitelist).
var permittedNonRoleColumns = []string{"email", "ip_whitelist"}

// updateUserNonRoleFields applies a SELECTIVE UPDATE of only the explicitly
// permitted non-role columns for the given user, using a column allow-list. Any
// security column present in the input (notably "role") is dropped, so this can
// never overwrite a concurrent role change. The UPDATE is a single
// statement-level write: role transitions remain the exclusive domain of the
// advisory-locked execRoleBoundaryTx.
//
// ip_whitelist is persisted via the repository's dedicated UpdateIPWhitelist
// helper (which uses a struct Select) rather than a raw map write, because on
// SQLite a map write of a JSON-serialized slice produces "row value misused".
// The repository path is dialect-agnostic and still only touches the
// ip_whitelist column — it cannot mutate role/quota_mode.
func updateUserNonRoleFields(db *gorm.DB, ctx context.Context, id uuid.UUID, fields map[string]interface{}) error {
	// Defense in depth: iterate ONLY the allow-list columns, copying the
	// caller-supplied value when present. Any other key (role, quota_mode,
	// password_hash, two_factor_secret, etc.) present in the input is simply
	// ignored — a caller can never smuggle a security mutation through here.
	for _, col := range permittedNonRoleColumns {
		v, ok := fields[col]
		if !ok {
			continue
		}
		switch col {
		case "email":
			// Column-scoped write; never touches role / quota_mode / security cols.
			if err := db.WithContext(ctx).Model(&models.User{}).Where("id = ?", id).
				Update("email", v).Error; err != nil {
				return err
			}
		case "ip_whitelist":
			// Persisted via the dedicated, allow-listed repository helper, which
			// only writes the ip_whitelist column (dialect-agnostic; avoids the
			// SQLite "row value misused" map-write issue).
			wl, _ := v.([]string)
			if err := repository.NewUserRepository(db).UpdateIPWhitelist(ctx, id, wl); err != nil {
				return err
			}
		}
	}
	return nil
}
