package handler

// One-click sign-in from the billing system. The billing module asks for a
// link over the signed billing webhook (user.sso); the link carries a token
// signed under the panel's secret that lasts a minute, works once and names
// one customer (and the VM to open). Opening it signs the browser in like the
// SSO callback does, the panel's own two-factor step included.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	billingSSOTTL     = 60 * time.Second
	billingSSOPurpose = "billing-sso"
	billingSSOPath    = "/api/v1/auth/billing-sso"
)

// errBillingSSORefused is a sign-in link the account may not have.
var errBillingSSORefused = errors.New("single sign-on refused")

type billingSSOToken struct {
	UserID uuid.UUID `json:"u"`
	VMID   string    `json:"v,omitempty"`
	Page   string    `json:"p,omitempty"`
	Exp    int64     `json:"e"`
	Nonce  string    `json:"n"`
}

// billingSSOLink is the path of a one-time sign-in link for a customer.
func billingSSOLink(userID uuid.UUID, vmID, page string, now time.Time) (string, error) {
	token, err := signJSON(billingSSOPurpose, billingSSOToken{
		UserID: userID, VMID: vmID, Page: page, Exp: now.Add(billingSSOTTL).Unix(), Nonce: randomString(18),
	})
	if err != nil {
		return "", err
	}
	return billingSSOPath + "?token=" + url.QueryEscape(token), nil
}

// billingSSOLanding is the page a customer lands on: the VM the link names
// (its console when page is "console"), else their dashboard.
func billingSSOLanding(vmID, page string) string {
	if _, err := uuid.Parse(vmID); err == nil {
		if page == "console" {
			return "/client/vms/" + vmID + "/console"
		}
		return "/client/vms/" + vmID
	}
	return "/client/dashboard"
}

// spendBillingSSO marks a token used, false when it was used before. The mark
// outlives the token, after which its expiry refuses it by itself.
func spendBillingSSO(ctx context.Context, db *gorm.DB, t billingSSOToken) (bool, error) {
	if db == nil {
		return false, errors.New("no database to record the sign-in link")
	}
	now := time.Now().UTC()
	res := db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&billingIdempotencyRow{
		IdempotencyKey: replayKeyPrefix + "sso:" + t.Nonce,
		RequestID:      uuid.New().String(),
		Event:          "sso-guard",
		Response:       []byte("{}"),
		ProcessedAt:    now,
		ExpiresAt:      time.Unix(t.Exp, 0).UTC().Add(time.Minute),
	})
	return res.RowsAffected == 1, res.Error
}

// BillingSSO handles GET /api/v1/auth/billing-sso?token=…
func (h *OIDCHandler) BillingSSO(c echo.Context) error {
	var t billingSSOToken
	if verifyJSON(billingSSOPurpose, c.QueryParam("token"), &t) != nil || time.Now().Unix() > t.Exp || t.Nonce == "" {
		return c.Redirect(http.StatusFound, "/login?sso=billing")
	}
	fresh, err := spendBillingSSO(c.Request().Context(), h.users.DB(), t)
	if err != nil {
		slog.Default().Error("billing sign-in link could not be recorded", "error", err)
		return c.Redirect(http.StatusFound, "/login?sso=error")
	}
	if !fresh {
		return c.Redirect(http.StatusFound, "/login?sso=billing")
	}

	user, token, needs2FA, err := h.users.BillingSSOLogin(t.UserID, c.RealIP(), c.Request().UserAgent())
	if err != nil || !needs2FA {
		email := ""
		if user != nil {
			email = user.Email
		}
		h.recordSignIn(c, user, email, middleware.SignInBillingSSO, err)
	}
	next := billingSSOLanding(t.VMID, t.Page)
	var locked *service.AccountLockedError
	switch {
	case errors.Is(err, service.ErrNoSSOAccount):
		return c.Redirect(http.StatusFound, "/login?sso=billing")
	case errors.Is(err, service.ErrIPNotWhitelisted):
		return c.Redirect(http.StatusFound, "/login?sso=ip")
	case errors.Is(err, service.ErrAccountSuspended):
		return c.Redirect(http.StatusFound, "/login?sso=suspended")
	case errors.As(err, &locked):
		return c.Redirect(http.StatusFound, "/login?sso=locked")
	case err != nil:
		slog.Default().Error("billing sign-in failed", "error", err)
		return c.Redirect(http.StatusFound, "/login?sso=error")
	case needs2FA:
		ticket, err := signJSON("sso-2fa", sso2FATicket{UserID: user.ID, Exp: time.Now().Add(sso2FATicketTTL).Unix(),
			Method: middleware.SignInBillingSSO, Next: next})
		if err != nil {
			return c.Redirect(http.StatusFound, "/login?sso=error")
		}
		// Another account's session in this browser would send the sign-in
		// page straight to that account's dashboard.
		middleware.ClearSessionCookie(c)
		return c.Redirect(http.StatusFound, "/login?sso=2fa#ticket="+url.QueryEscape(ticket))
	}
	slog.Default().Info("billing sign-in", "user_id", user.ID, "ip", c.RealIP())
	h.auditBillingSSO(c, user, next, false)
	middleware.SetSessionCookie(c, token, h.users.SessionLifetime())
	return c.Redirect(http.StatusFound, next)
}

// auditBillingSSO records a finished sign-in from the billing system.
func (h *OIDCHandler) auditBillingSSO(c echo.Context, u *models.User, landing string, twoFactor bool) {
	id := u.ID.String()
	service.AuditUserEvent(c.Request().Context(), h.users.DB(), "user.sign_in", id, id, c.RealIP(), c.Request().UserAgent(),
		map[string]any{"method": middleware.SignInBillingSSO, "email": u.Email, "landing": landing, "two_factor": twoFactor})
}

// handleUserSSO gives the billing module a one-time sign-in link for a
// customer, landing on vm_id when it names one of theirs (HandleWebhook has
// checked it). The link lasts a minute and works once.
func (h *BillingHandler) handleUserSSO(ctx context.Context, data *WebhookData, requestID string) (WebhookResponse, error) {
	if h.db == nil {
		return WebhookResponse{}, errors.New("accounts are not available")
	}
	id, err := uuid.Parse(strings.TrimSpace(data.UserID))
	if err != nil {
		return WebhookResponse{}, service.ErrUserNotFound
	}
	var u models.User
	if err := h.db.WithContext(ctx).First(&u, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return WebhookResponse{}, service.ErrUserNotFound
		}
		return WebhookResponse{}, err
	}
	if err := service.BillingSSOAccountError(&u); err != nil {
		why := "the panel user is a staff account or has expired; single sign-on is for current customer accounts"
		var locked *service.AccountLockedError
		switch {
		case errors.Is(err, service.ErrAccountSuspended):
			why = "the panel account is suspended"
		case errors.As(err, &locked):
			why = "the panel account is locked after failed sign-in attempts; try again in " + locked.Wait()
		}
		return WebhookResponse{}, fmt.Errorf("%w: %s", errBillingSSORefused, why)
	}
	page := ""
	if data.Page == "console" && data.VMID != "" {
		page = "console"
	}
	path, err := billingSSOLink(u.ID, data.VMID, page, time.Now())
	if err != nil {
		return WebhookResponse{}, fmt.Errorf("the sign-in link could not be made: %w", err)
	}
	meta := map[string]string{"path": path, "expires_in": fmt.Sprintf("%d", int(billingSSOTTL.Seconds()))}
	if base := service.PublicURL(); base != "" {
		meta["url"] = base + path
	}
	return WebhookResponse{
		Success: true, Message: "Sign-in link", VMID: data.VMID, ExternalRef: data.ExternalRef,
		Timestamp: time.Now().UTC(), RequestID: requestID, Metadata: meta,
	}, nil
}
