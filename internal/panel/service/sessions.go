package service

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// Sign-in history and ended sessions are kept this long.
const (
	signInHistoryKeep = 180 * 24 * time.Hour
	endedSessionKeep  = 30 * 24 * time.Hour
)

var lastSignInPrune atomic.Int64

// RecordSignIn adds one line to sign-in history. The account is found by
// email when the caller does not know it. Best-effort: a lost row is logged.
func RecordSignIn(ctx context.Context, db *gorm.DB, e models.SignInEvent) {
	if db == nil {
		return
	}
	db = db.WithContext(context.WithoutCancel(ctx))
	e.ID = uuid.New()
	e.Email = middleware.Clip(strings.ToLower(strings.TrimSpace(e.Email)), 255)
	e.Note = middleware.Clip(e.Note, 255)
	e.IPAddress = middleware.Clip(e.IPAddress, 64)
	e.UserAgent = middleware.Clip(e.UserAgent, 500)
	e.CreatedAt = time.Now()
	if e.UserID == nil && e.Email != "" {
		var row struct{ ID uuid.UUID }
		if db.Model(&models.User{}).Select("id").Where("LOWER(email) = ?", e.Email).Take(&row).Error == nil {
			e.UserID = &row.ID
		}
	}
	if err := db.Create(&e).Error; err != nil {
		slog.Default().Error("sign-in history: could not record an attempt", "email", e.Email, "method", e.Method, "error", err)
	}
	// ponytail: pruned from the sign-in path at most hourly per process; a job if the table grows past that.
	if now := time.Now(); now.Unix()-lastSignInPrune.Load() > 3600 {
		lastSignInPrune.Store(now.Unix())
		db.Where("created_at < ?", now.Add(-signInHistoryKeep)).Delete(&models.SignInEvent{})
		db.Where("expires_at < ? OR revoked_at < ?", now.Add(-endedSessionKeep), now.Add(-endedSessionKeep)).Delete(&models.UserSession{})
	}
}

// RecentFailures counts failed sign-ins and password reset requests from ip,
// or for email, since t: what the sign-in captcha is measured on.
func RecentFailures(ctx context.Context, db *gorm.DB, ip, email string, since time.Time) int64 {
	var n int64
	q := db.WithContext(ctx).Model(&models.SignInEvent{}).
		Where("created_at > ? AND (success = ? OR method = ?)", since, false, middleware.SignInPasswordReset)
	if email = strings.ToLower(strings.TrimSpace(email)); email != "" {
		q = q.Where("ip_address = ? OR LOWER(email) = ?", ip, email)
	} else {
		q = q.Where("ip_address = ?", ip)
	}
	if err := q.Count(&n).Error; err != nil {
		slog.Default().Warn("sign-in captcha: could not count failed attempts", "error", err)
	}
	return n
}

// SessionView is a session as its owner and staff see it.
type SessionView struct {
	ID         string    `json:"id"`
	Method     string    `json:"method"`
	IPAddress  string    `json:"ip_address"`
	UserAgent  string    `json:"user_agent"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	// Current is the session the request was made with.
	Current bool `json:"current"`
	// ImpersonatorEmail is the staff account behind a view as this customer.
	ImpersonatorEmail string `json:"impersonator_email,omitempty"`
}

// ActiveSessions lists the sessions of an account that still work, newest
// use first. Sessions cut off by a password change or "sign out everywhere"
// (users.token_revoked_at) are left out, as they no longer work.
func ActiveSessions(ctx context.Context, db *gorm.DB, userID uuid.UUID, currentSID string) ([]SessionView, error) {
	db = db.WithContext(ctx)
	var cutoff struct{ TokenRevokedAt *time.Time }
	if err := db.Model(&models.User{}).Select("token_revoked_at").Where("id = ?", userID).Take(&cutoff).Error; err != nil {
		return nil, err
	}
	var rows []models.UserSession
	if err := db.Where("user_id = ? AND revoked_at IS NULL", userID).Order("last_seen_at DESC").Limit(200).Find(&rows).Error; err != nil {
		return nil, err
	}
	type staffAccount struct {
		Email          string
		TokenRevokedAt *time.Time
	}
	staff := map[uuid.UUID]staffAccount{}
	now := time.Now()
	out := []SessionView{}
	for _, r := range rows {
		// Not used since the cutoff means its token predates it: dead.
		if now.After(r.ExpiresAt) || (cutoff.TokenRevokedAt != nil && r.LastSeenAt.Before(*cutoff.TokenRevokedAt)) {
			continue
		}
		v := SessionView{ID: r.ID.String(), Method: r.Method, IPAddress: r.IPAddress, UserAgent: r.UserAgent,
			CreatedAt: r.CreatedAt, LastSeenAt: r.LastSeenAt, ExpiresAt: r.ExpiresAt, Current: r.ID.String() == currentSID}
		if r.ImpersonatorID != nil {
			st, ok := staff[*r.ImpersonatorID]
			if !ok {
				_ = db.Unscoped().Model(&models.User{}).Select("email", "token_revoked_at").Where("id = ?", *r.ImpersonatorID).Take(&st).Error
				staff[*r.ImpersonatorID] = st
			}
			// The staff account's own cutoff ends its views too (middleware impersonatingStaff).
			if st.TokenRevokedAt != nil && r.CreatedAt.Before(*st.TokenRevokedAt) {
				continue
			}
			v.ImpersonatorEmail = st.Email
		}
		out = append(out, v)
	}
	return out, nil
}

// RevokeSession ends one session of an account. false when there was no
// such open session.
func RevokeSession(ctx context.Context, db *gorm.DB, userID uuid.UUID, sid string) (bool, error) {
	if _, err := uuid.Parse(sid); err != nil {
		return false, nil
	}
	res := db.WithContext(ctx).Model(&models.UserSession{}).
		Where("id = ? AND user_id = ? AND revoked_at IS NULL", sid, userID).Update("revoked_at", time.Now())
	return res.RowsAffected > 0, res.Error
}

// RevokeOtherSessions ends every session of an account but keepSID. Tokens
// from before sessions were recorded carry no id, so the account's token
// cutoff moves to now as well; the kept session needs a fresh token after
// this (middleware.ReissueSession).
func RevokeOtherSessions(ctx context.Context, db *gorm.DB, userID uuid.UUID, keepSID string) (int64, error) {
	now := time.Now()
	res := db.WithContext(ctx).Model(&models.UserSession{}).
		Where("user_id = ? AND id <> ? AND revoked_at IS NULL", userID, keepSID).Update("revoked_at", now)
	if res.Error != nil {
		return 0, res.Error
	}
	// Whole seconds: a token's issue time is, and the kept one is reissued now.
	if err := db.WithContext(ctx).Table("users").Where("id = ?", userID).
		Update("token_revoked_at", now.Truncate(time.Second)).Error; err != nil {
		return 0, err
	}
	return res.RowsAffected, db.WithContext(ctx).Model(&models.UserSession{}).Where("id = ?", keepSID).Update("last_seen_at", now).Error
}

// RevokeAllSessions signs an account out everywhere, its views as customers
// included. Recorded sessions end by their rows; the token cutoff, in whole
// seconds so a sign-in straight after still works, catches tokens from before
// sessions were recorded.
func RevokeAllSessions(ctx context.Context, db *gorm.DB, userID uuid.UUID) error {
	now := time.Now()
	if err := db.WithContext(ctx).Model(&models.UserSession{}).
		Where("(user_id = ? OR impersonator_id = ?) AND revoked_at IS NULL", userID, userID).Update("revoked_at", now).Error; err != nil {
		return err
	}
	return db.WithContext(ctx).Table("users").Where("id = ?", userID).Update("token_revoked_at", now.Truncate(time.Second)).Error
}

// SignInHistory is an account's latest sign-in attempts, newest first.
func SignInHistory(ctx context.Context, db *gorm.DB, userID uuid.UUID, limit int) ([]models.SignInEvent, error) {
	out := []models.SignInEvent{}
	err := db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC").Limit(limit).Find(&out).Error
	return out, err
}
