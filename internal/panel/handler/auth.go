package handler

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

type AuthHandler struct {
	userService *service.UserService
}

func NewAuthHandler(userService *service.UserService) *AuthHandler {
	return &AuthHandler{
		userService: userService,
	}
}

type LoginRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required"`
	TOTPCode string `json:"totpCode"`
	// CaptchaToken is the passed check, once the panel asks for one.
	CaptchaToken string `json:"captchaToken"`
}

type LoginResponse struct {
	User        interface{} `json:"user,omitempty"`
	Token       string      `json:"token,omitempty"`
	Requires2FA bool        `json:"requires2fa,omitempty"`
	TempToken   string      `json:"tempToken,omitempty"`
	// ExpiresIn is how many seconds the token lasts, so the page keeps it as long.
	ExpiresIn int `json:"expiresIn,omitempty"`
	// Redirect is the page an SSO sign-in lands on, when it names one.
	Redirect string `json:"redirect,omitempty"`
}

// accountLocked answers a sign-in or a code check refused because the account
// is locked. true when err was that.
func accountLocked(c echo.Context, err error) (bool, error) {
	var locked *service.AccountLockedError
	if !errors.As(err, &locked) {
		return false, nil
	}
	c.Response().Header().Set("Retry-After", strconv.Itoa(max(int(time.Until(locked.Until).Seconds()), 1)))
	return true, c.JSON(http.StatusTooManyRequests, map[string]string{
		"code":  "account_locked",
		"error": "Too many failed sign-in attempts. Try again in " + locked.Wait() + ", or reset your password.",
	})
}

// ClientIP returns the caller's public-facing IP address as seen by the panel.
// Used by the frontend to suggest the current IP when configuring IP
// whitelists. No auth required — it reveals nothing beyond what the request
// itself already exposes to any server.
func (h *AuthHandler) ClientIP(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]interface{}{
		"success": true,
		"data": map[string]string{
			"ip": c.RealIP(),
		},
	})
}

func (h *AuthHandler) Login(c echo.Context) error {
	var req LoginRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error": "Invalid request body",
		})
	}

	if blocked, out := captchaGate(c, h.userService.DB(), req.Email, req.CaptchaToken); blocked {
		return out
	}

	// Call service
	loginReq := &service.LoginRequest{
		Email:     req.Email,
		Password:  req.Password,
		TOTPCode:  req.TOTPCode,
		ClientIP:  c.RealIP(),
		UserAgent: c.Request().UserAgent(),
	}

	resp, err := h.userService.Login(loginReq)
	if err != nil {
		h.recordSignIn(c, nil, req.Email, middleware.SignInPassword, signInFailure(err))
		if locked, out := accountLocked(c, err); locked {
			return out
		}
		switch {
		case errors.Is(err, service.ErrInvalidCredentials):
			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error": "Invalid email or password",
			})
		case errors.Is(err, service.ErrInvalidTOTPCode):
			return c.JSON(http.StatusUnauthorized, map[string]string{
				"error": "Invalid 2FA code",
			})
		case errors.Is(err, service.ErrIPNotWhitelisted):
			return c.JSON(http.StatusForbidden, map[string]string{
				"error": "IP address not whitelisted",
			})
		case errors.Is(err, service.ErrAccountSuspended):
			return c.JSON(http.StatusForbidden, map[string]string{
				"code":  "account_suspended",
				"error": "This account is suspended. Contact support.",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]string{
				"error": "Failed to login",
			})
		}
	}

	// Check if 2FA is required
	if resp.Requires2FA {
		return c.JSON(http.StatusOK, LoginResponse{
			Requires2FA: true,
			TempToken:   resp.TempToken,
		})
	}

	h.recordSignIn(c, resp.User, req.Email, middleware.SignInPassword, "")
	// The browser keeps the session in an HttpOnly cookie no script can read.
	// The token stays in the body for scripts that sign in this way.
	middleware.SetSessionCookie(c, resp.Token, h.userService.SessionLifetime())
	return c.JSON(http.StatusOK, LoginResponse{
		User:      resp.User,
		Token:     resp.Token,
		ExpiresIn: int(h.userService.SessionLifetime().Seconds()),
	})
}

// Register is the public self-enrollment endpoint. Secure v1 is invite-only:
// until Phase 1B invitation acceptance + prerequisites are activated, public
// self-registration fails closed with a stable, non-enumerating response that
// creates no user and leaks no configuration state. Callers cannot use this to
// probe whether an email is already registered, nor to enroll a client.
func (h *AuthHandler) Register(c echo.Context) error {
	// We still decode the body so malformed requests are reported consistently,
	// but the result is discarded — no user is ever created via this path while
	// enrollment is gated, and we never echo validation differences that could
	// enumerate existing accounts.
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	_ = c.Bind(&req)

	return c.JSON(http.StatusForbidden, map[string]string{
		"error": "enrollment_unavailable",
	})
}

// ForgotPassword handles POST /auth/forgot-password. It always responds 200 with
// the same generic message regardless of whether the email maps to an account,
// so it can't be used to enumerate registered users.
func (h *AuthHandler) ForgotPassword(c echo.Context) error {
	var req struct {
		Email        string `json:"email"`
		CaptchaToken string `json:"captchaToken"`
	}
	_ = c.Bind(&req)
	if blocked, out := captchaGate(c, h.userService.DB(), req.Email, req.CaptchaToken); blocked {
		return out
	}
	// On the account's history, and counted towards the captcha like a failed sign-in.
	h.recordSignIn(c, nil, req.Email, middleware.SignInPasswordReset, "")

	// resetURLBase is the public frontend reset page. PANEL_PUBLIC_URL is the
	// panel origin (e.g. https://panel.example.com); fall back to a relative path.
	base := service.PublicURL()
	resetURLBase := base + "/reset-password"

	if err := h.userService.RequestPasswordReset(c.Request().Context(), req.Email, resetURLBase); err != nil {
		// Genuine server error — but still don't reveal account existence.
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to process request",
		})
	}
	return c.JSON(http.StatusOK, map[string]string{
		"message": "If an account exists for that email, a reset link has been sent.",
	})
}

// ResetPassword handles POST /auth/reset-password. It consumes a reset token and
// sets a new password.
func (h *AuthHandler) ResetPassword(c echo.Context) error {
	var req struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
	}
	if err := h.userService.ResetPassword(c.Request().Context(), req.Token, req.Password); err != nil {
		switch {
		case errors.Is(err, service.ErrInvalidResetToken):
			return c.JSON(http.StatusBadRequest, map[string]string{
				"error": "This reset link is invalid or has expired. Please request a new one.",
			})
		case errors.Is(err, service.ErrWeakPassword):
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to reset password"})
		}
	}
	return c.JSON(http.StatusOK, map[string]string{"message": "Password reset successfully. You can now sign in."})
}

func (h *AuthHandler) Logout(c echo.Context) error {
	// Route is behind RequireAuth, so the user context is always populated.
	userCtx, ok := c.Get("user").(*middleware.UserContext)
	if !ok || userCtx == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"error": "Not authenticated",
		})
	}
	middleware.TakeStaffCookie(c)
	// This session ends; the account's others stay signed in. A token from
	// before sessions were recorded can only be ended with all of them.
	var err error
	if userCtx.SessionID != "" {
		_, err = service.RevokeSession(c.Request().Context(), h.userService.DB(), userCtx.ID, userCtx.SessionID)
	} else {
		err = h.userService.RevokeUserTokens(userCtx.ID)
	}
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to log out",
		})
	}
	// The cookie itself is cleared by DropSessionCookie on the route.
	return c.JSON(http.StatusOK, map[string]string{
		"message": "Logged out successfully",
	})
}

func (h *AuthHandler) Me(c echo.Context) error {
	// Get user from context (set by auth middleware)
	userCtx, ok := c.Get("user").(*middleware.UserContext)
	if !ok || userCtx == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"error": "Not authenticated",
		})
	}

	data := map[string]interface{}{
		"id":                 userCtx.ID,
		"email":              userCtx.Email,
		"role":               userCtx.Role,
		"two_factor_enabled": userCtx.TwoFactorEnabled,
		// Set while an administrator must turn on two-factor before anything else.
		"two_factor_setup_required": userCtx.TwoFactorSetupRequired,
		// What the pages may offer; the panel checks each request regardless.
		"permissions": userCtx.Permissions,
	}
	if userCtx.StaffRole.ID != "" {
		data["staff_role"] = userCtx.StaffRole.ID
		data["staff_role_name"] = userCtx.StaffRole.Name
	}
	if userCtx.ImpersonatorID != "" {
		data["impersonator"] = map[string]string{"id": userCtx.ImpersonatorID, "email": userCtx.ImpersonatorEmail}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": data})
}

// EndImpersonation ends a staff member's view as a customer and gives the
// browser its staff session back (kept in a cookie only this route sees).
func (h *AuthHandler) EndImpersonation(c echo.Context) error {
	userCtx, ok := middleware.GetUserContext(c)
	if !ok || userCtx.ImpersonatorID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "You are not viewing the panel as a customer"})
	}
	ctx := c.Request().Context()
	db := h.userService.DB()
	if _, err := service.RevokeSession(ctx, db, userCtx.ID, userCtx.SessionID); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "The view could not be ended. Try again."})
	}
	service.AuditUserEvent(ctx, db, "user.impersonate.end", userCtx.ImpersonatorID, userCtx.ID.String(), c.RealIP(), c.Request().UserAgent(),
		map[string]any{"email": userCtx.Email})
	redirect := "/login"
	staff := middleware.TakeStaffCookie(c)
	if claims, err := middleware.ParseAndValidateToken(staff); err == nil && claims.UserID == userCtx.ImpersonatorID && claims.ExpiresAt != nil {
		middleware.SetSessionCookie(c, staff, time.Until(claims.ExpiresAt.Time))
		redirect = "/users/" + userCtx.ID.String()
	} else {
		middleware.ClearSessionCookie(c)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]string{"redirect": redirect}})
}

// recordSignIn adds a line to the account's sign-in history.
func (h *AuthHandler) recordSignIn(c echo.Context, user *models.User, email, method, failure string) {
	e := models.SignInEvent{Email: email, Method: method, Success: failure == "", Note: failure,
		IPAddress: c.RealIP(), UserAgent: c.Request().UserAgent()}
	if user != nil {
		e.UserID, e.Email = &user.ID, user.Email
	}
	service.RecordSignIn(c.Request().Context(), h.userService.DB(), e)
}

// signInFailure names why a sign-in failed, for the history.
func signInFailure(err error) string {
	var locked *service.AccountLockedError
	switch {
	case errors.As(err, &locked):
		return "account_locked"
	case errors.Is(err, service.ErrInvalidCredentials), errors.Is(err, service.ErrNoSSOAccount):
		return "wrong_email_or_password"
	case errors.Is(err, service.ErrInvalidTOTPCode):
		return "wrong_two_factor_code"
	case errors.Is(err, service.ErrIPNotWhitelisted):
		return "ip_not_allowed"
	case errors.Is(err, service.ErrAccountSuspended):
		return "account_suspended"
	}
	return "error"
}
