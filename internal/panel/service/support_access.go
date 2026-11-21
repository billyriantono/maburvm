package service

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// FoundingAdminID is the earliest-created permanent administrator: the one
// account that may create other administrators and open support access.
// A temporary (support) account is never it, however the others come and go.
// Returns uuid.Nil when there is none.
func FoundingAdminID(ctx context.Context, db *gorm.DB) uuid.UUID {
	return middleware.FoundingAdminID(ctx, db)
}

// IsTemporaryAccount reports whether the user is a support account. An
// account that cannot be read counts as one: the callers refuse on true.
func IsTemporaryAccount(ctx context.Context, db *gorm.DB, id uuid.UUID) bool {
	var user models.User
	if err := db.WithContext(ctx).Select("expires_at").First(&user, "id = ?", id).Error; err != nil {
		return true
	}
	return user.ExpiresAt != nil
}

// auditSupportSignIn puts a support account's sign-in on record, whichever
// way it signed in. A support account is somebody else holding full control.
func auditSupportSignIn(db *gorm.DB, u *models.User, ip string) {
	if u.ExpiresAt == nil {
		return
	}
	id := u.ID.String()
	AuditSupportAccess(context.Background(), db, "support_access.sign_in", id, id, ip, "", map[string]any{"email": u.Email})
}

// AuditSupportAccess records one event in the life of a support account:
// support_access.create, .revoke or .sign_in. actorID is who did it, grantID
// the support account. Never silent: a lost row is logged.
func AuditSupportAccess(ctx context.Context, db *gorm.DB, action, actorID, grantID, ip, userAgent string, details map[string]any) {
	entry := &models.AuditLog{
		UserID:       &actorID,
		Action:       action,
		ResourceType: "USER",
		ResourceID:   &grantID,
		IPAddress:    ip,
		UserAgent:    userAgent,
		Details:      details,
	}
	if err := db.WithContext(ctx).Create(entry).Error; err != nil {
		slog.Default().Error("audit: could not record support access event", "action", action, "account", grantID, "actor", actorID, "error", err)
	}
}
