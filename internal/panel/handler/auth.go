package handler

import (
	"errors"
	"net/http"

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

	// Set the session cookie with the same hardened flags as the rest of the auth
	// layer (SetTokenCookies): HttpOnly + Secure + SameSite=Strict so the token
	// can't be read by JS, sent over plain HTTP, or attached to cross-site requests.
	cookie := new(http.Cookie)
	cookie.Name = "refreshToken"
	cookie.Value = resp.Token
	cookie.HttpOnly = true
	cookie.Secure = true
	cookie.SameSite = http.SameSiteStrictMode
	cookie.Path = "/"
	c.SetCookie(cookie)

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

func (h *AuthHandler) Logout(c echo.Context) error {
	// Clear refresh token cookie
	cookie := new(http.Cookie)
	cookie.Name = "refreshToken"
	cookie.Value = ""
	cookie.HttpOnly = true
	cookie.Path = "/"
	cookie.MaxAge = -1
	c.SetCookie(cookie)

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
