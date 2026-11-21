package middleware

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// How a session started; also the method column of sign-in history.
const (
	SignInPassword      = "password"
	SignInSSO           = "sso"
	SignInImpersonation = "impersonation"
	// SignInBillingSSO is a one-click sign-in from the billing system.
	SignInBillingSSO = "billing_sso"
	// SignInPasswordReset marks a reset request in sign-in history.
	SignInPasswordReset = "password_reset"
)

// ImpersonationLifetime is how long a staff member may view as a customer
// before the view ends by itself.
const ImpersonationLifetime = time.Hour

// StaffCookieName holds the staff session while it views as a customer. Its
// path keeps it off every request except the one that ends the view.
const (
	StaffCookieName = "staffAccessToken"
	StaffCookiePath = "/api/v1/auth/impersonation"
)

// SessionMeta describes a new session.
type SessionMeta struct {
	Method         string
	IP             string
	UserAgent      string
	ImpersonatorID *uuid.UUID
}

// IssueSession records a session and mints its access token. When the row
// cannot be written the sign-in still goes ahead with a token that has no
// session id (as every token had before sessions were recorded); a view as a
// customer does not, since ending it needs the row.
func IssueSession(db *gorm.DB, user *models.User, meta SessionMeta, lifetime time.Duration) (string, error) {
	if lifetime <= 0 {
		lifetime = AccessTokenExpiry
	}
	now := time.Now()
	row := models.UserSession{
		ID: uuid.New(), UserID: user.ID, Method: meta.Method,
		IPAddress: Clip(meta.IP, 64), UserAgent: Clip(meta.UserAgent, 500),
		ImpersonatorID: meta.ImpersonatorID, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(lifetime),
	}
	sid, imp := "", ""
	if meta.ImpersonatorID != nil {
		imp = meta.ImpersonatorID.String()
	}
	if db != nil {
		if err := db.Create(&row).Error; err != nil {
			if imp != "" {
				return "", err
			}
			slog.Default().Error("session not recorded; signing in with a session that cannot be listed or revoked", "user_id", user.ID, "error", err)
		} else {
			sid = row.ID.String()
		}
	} else if imp != "" {
		return "", errors.New("no database to record the session")
	}
	return mintToken(user, sid, imp, lifetime)
}

// ReissueSession mints a fresh token for a recorded session, keeping when it
// ends: after the account's token cutoff moved, the kept session needs one.
func ReissueSession(db *gorm.DB, user *models.User, sid string) (string, time.Duration, error) {
	var row models.UserSession
	if err := db.Where("id = ? AND user_id = ? AND revoked_at IS NULL", sid, user.ID).First(&row).Error; err != nil {
		return "", 0, err
	}
	ttl := time.Until(row.ExpiresAt)
	if ttl <= 0 {
		return "", 0, errors.New("session has ended")
	}
	token, err := mintToken(user, sid, "", ttl)
	return token, ttl, err
}

// checkSession loads the session a token names and refuses one that was
// revoked or ran out. It notes when the session was last used, at most once a
// minute.
func checkSession(db *gorm.DB, claims *JWTClaims, userID uuid.UUID) (*models.UserSession, int, string) {
	var row models.UserSession
	if err := db.Where("id = ? AND user_id = ?", claims.SessionID, userID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, http.StatusUnauthorized, "Session has been revoked"
		}
		return nil, http.StatusInternalServerError, "Failed to validate session"
	}
	now := time.Now()
	if row.RevokedAt != nil || now.After(row.ExpiresAt) {
		return nil, http.StatusUnauthorized, "Session has been revoked"
	}
	if now.Sub(row.LastSeenAt) > time.Minute {
		db.Model(&models.UserSession{}).Where("id = ?", row.ID).Update("last_seen_at", now)
	}
	return &row, 0, ""
}

// impersonationForbidden is what staff may not do while viewing as a
// customer: change the customer's password, email, two-factor or sessions,
// make API keys, read initial passwords, or use MCP.
func impersonationForbidden(method, path string) bool {
	write := method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
	switch {
	case strings.HasPrefix(path, "/api/v1/settings"), strings.HasPrefix(path, "/api/v1/api-keys"):
		return write
	case strings.Contains(path, "/initial-password"):
		return write
	case strings.HasPrefix(path, "/api/v1/mcp"):
		return true
	}
	return false
}

// Clip shortens s to at most n bytes of valid UTF-8, for a varchar column.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// SetStaffCookie keeps the staff session while it views as a customer.
func SetStaffCookie(c echo.Context, token string, lifetime time.Duration) {
	c.SetCookie(&http.Cookie{Name: StaffCookieName, Value: token, Path: StaffCookiePath, MaxAge: int(lifetime.Seconds()),
		HttpOnly: true, Secure: requestIsHTTPS(c), SameSite: http.SameSiteStrictMode})
}

// TakeStaffCookie returns the kept staff session and removes the cookie.
func TakeStaffCookie(c echo.Context) string {
	ck, err := c.Cookie(StaffCookieName)
	c.SetCookie(&http.Cookie{Name: StaffCookieName, Value: "", Path: StaffCookiePath, MaxAge: -1,
		HttpOnly: true, Secure: requestIsHTTPS(c), SameSite: http.SameSiteStrictMode})
	if err != nil {
		return ""
	}
	return ck.Value
}
