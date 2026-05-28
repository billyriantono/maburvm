package handler

import (
	"errors"
	"net/http"
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

	// Set refresh token cookie
	cookie := new(http.Cookie)
	cookie.Name = "refreshToken"
	cookie.Value = resp.Token
	cookie.HttpOnly = true
	cookie.Secure = false // Set to true in production with HTTPS
	cookie.Path = "/"
	c.SetCookie(cookie)

	return c.JSON(http.StatusOK, LoginResponse{
		User:  resp.User,
		Token: resp.Token,
	})
}

func (h *AuthHandler) Register(c echo.Context) error {
	var req struct {
		Email    string `json:"email" validate:"required,email"`
		Password string `json:"password" validate:"required"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error": "Invalid request body",
		})
	}

	registerReq := &service.RegisterRequest{
		Email:    req.Email,
		Password: req.Password,
	}

	user, err := h.userService.Register(registerReq)
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "already exists") {
			return c.JSON(http.StatusConflict, map[string]string{
				"error": "Email already registered",
			})
		}
		if errors.Is(err, service.ErrWeakPassword) {
			return c.JSON(http.StatusBadRequest, map[string]string{
				"error": errMsg,
			})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "Failed to register user",
		})
	}

	return c.JSON(http.StatusCreated, map[string]interface{}{
		"message": "User registered successfully",
		"user":    user,
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
