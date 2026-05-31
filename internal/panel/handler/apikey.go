package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	panelMiddleware "github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// APIKeyHandler handles per-user API key endpoints.
type APIKeyHandler struct {
	service *service.APIKeyService
}

// NewAPIKeyHandler creates a new APIKeyHandler.
func NewAPIKeyHandler(s *service.APIKeyService) *APIKeyHandler {
	return &APIKeyHandler{service: s}
}

// createAPIKeyResponse is returned once on creation; Token is the plaintext key
// and is never retrievable again.
type createAPIKeyResponse struct {
	models.APIKey
	Token string `json:"token"`
}

// ListAPIKeys handles GET /api/v1/api-keys (current user's keys).
func (h *APIKeyHandler) ListAPIKeys(c echo.Context) error {
	user, ok := panelMiddleware.GetUserContext(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized"})
	}
	keys, err := h.service.ListAPIKeys(c.Request().Context(), user.ID.String())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to list API keys"})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": keys})
}

// CreateAPIKey handles POST /api/v1/api-keys. The plaintext token is returned
// exactly once in the response and cannot be retrieved later.
func (h *APIKeyHandler) CreateAPIKey(c echo.Context) error {
	user, ok := panelMiddleware.GetUserContext(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized"})
	}
	var req service.CreateAPIKeyRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
	}
	key, token, err := h.service.CreateAPIKey(c.Request().Context(), user.ID.String(), req)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{
		"success": true,
		"data":    createAPIKeyResponse{APIKey: *key, Token: token},
	})
}

// RevokeAPIKey handles DELETE /api/v1/api-keys/:id (current user's key only).
func (h *APIKeyHandler) RevokeAPIKey(c echo.Context) error {
	user, ok := panelMiddleware.GetUserContext(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized"})
	}
	if err := h.service.RevokeAPIKey(c.Request().Context(), c.Param("id"), user.ID.String()); err != nil {
		if errors.Is(err, service.ErrAPIKeyNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "API key not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "API key revoked"})
}

// RegisterAPIKeyRoutes registers per-user API key routes (all require auth).
func RegisterAPIKeyRoutes(e *echo.Echo, h *APIKeyHandler, db *gorm.DB) {
	g := e.Group("/api/v1/api-keys")
	g.Use(panelMiddleware.RequireAuth(db))
	g.GET("", h.ListAPIKeys)
	g.POST("", h.CreateAPIKey)
	g.DELETE("/:id", h.RevokeAPIKey)
}
