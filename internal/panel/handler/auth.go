package handler

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
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
}

type LoginResponse struct {
	User        interface{} `json:"user,omitempty"`
	Token       string      `json:"token,omitempty"`
	Requires2FA bool        `json:"requires2fa,omitempty"`
	TempToken   string      `json:"tempToken,omitempty"`
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

	// Get client IP
	clientIP := c.RealIP()

	// Call service
	loginReq := &service.LoginRequest{
		Email:    req.Email,
		Password: req.Password,
		TOTPCode: req.TOTPCode,
		ClientIP: clientIP,
	}

	resp, err := h.userService.Login(loginReq)
	if err != nil {
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

	// The token is returned in the body; the web client stores it and sends it as a
	// Bearer header. No server-set auth cookie (the panel is header-authenticated).
	return c.JSON(http.StatusOK, LoginResponse{
		User:  resp.User,
		Token: resp.Token,
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
		Email string `json:"email"`
	}
	_ = c.Bind(&req)

	// resetURLBase is the public frontend reset page. PANEL_PUBLIC_URL is the
	// panel origin (e.g. https://mabur.gopek.id); fall back to a relative path.
	base := strings.TrimRight(os.Getenv("PANEL_PUBLIC_URL"), "/")
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
	// Revoke every outstanding token for this user server-side.
	userCtx, ok := c.Get("user").(*middleware.UserContext)
	if !ok || userCtx == nil {
		return c.JSON(http.StatusUnauthorized, map[string]string{
			"error": "Not authenticated",
		})
	}
	if err := h.userService.RevokeUserTokens(userCtx.ID); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to log out",
		})
	}
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

	return c.JSON(http.StatusOK, map[string]interface{}{
		"success": true,
		"data": map[string]interface{}{
			"id":    userCtx.ID,
			"email": userCtx.Email,
			"role":  userCtx.Role,
		},
	})
}
