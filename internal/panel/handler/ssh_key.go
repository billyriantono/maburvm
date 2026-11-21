package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	panelMiddleware "github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"gorm.io/gorm"
)

// SSHKeyHandler handles per-user SSH public key endpoints.
type SSHKeyHandler struct {
	service *service.SSHKeyService
	db      *gorm.DB
}

// NewSSHKeyHandler creates a new SSHKeyHandler.
func NewSSHKeyHandler(s *service.SSHKeyService) *SSHKeyHandler {
	return &SSHKeyHandler{service: s}
}

// ListSSHKeys handles GET /api/v1/ssh-keys: the keys of the caller's account,
// or of a team account with ?account_id=.
func (h *SSHKeyHandler) ListSSHKeys(c echo.Context) error {
	_, account, _, ok := actingAccount(c, h.db)
	if !ok {
		return nil
	}
	keys, err := h.service.ListSSHKeys(c.Request().Context(), account)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to list SSH keys"})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": keys})
}

// CreateSSHKey handles POST /api/v1/ssh-keys.
func (h *SSHKeyHandler) CreateSSHKey(c echo.Context) error {
	_, account, _, ok := actingAccount(c, h.db)
	if !ok {
		return nil
	}
	var req service.CreateSSHKeyRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
	}
	key, err := h.service.CreateSSHKey(c.Request().Context(), account, req)
	if err != nil {
		if errors.Is(err, service.ErrSSHKeyDuplicate) {
			return c.JSON(http.StatusConflict, map[string]interface{}{"error": err.Error()})
		}
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": key})
}

// GenerateSSHKey handles POST /api/v1/ssh-keys/generate. It stores only the
// public key and returns the private key PEM exactly once.
func (h *SSHKeyHandler) GenerateSSHKey(c echo.Context) error {
	_, account, _, ok := actingAccount(c, h.db)
	if !ok {
		return nil
	}
	var req service.GenerateSSHKeyRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
	}
	generated, err := h.service.GenerateSSHKey(c.Request().Context(), account, req)
	if err != nil {
		if errors.Is(err, service.ErrSSHKeyDuplicate) {
			return c.JSON(http.StatusConflict, map[string]interface{}{"error": err.Error()})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to generate SSH key"})
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": generated})
}

// DeleteSSHKey handles DELETE /api/v1/ssh-keys/:id (the acting account's key only).
func (h *SSHKeyHandler) DeleteSSHKey(c echo.Context) error {
	_, account, _, ok := actingAccount(c, h.db)
	if !ok {
		return nil
	}
	if err := h.service.DeleteSSHKey(c.Request().Context(), c.Param("id"), account); err != nil {
		if errors.Is(err, service.ErrSSHKeyNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "SSH key not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "SSH key deleted"})
}

// RegisterSSHKeyRoutes registers per-user SSH key routes (all require auth).
func RegisterSSHKeyRoutes(e *echo.Echo, h *SSHKeyHandler, db *gorm.DB) {
	h.db = db
	g := e.Group("/api/v1/ssh-keys")
	g.Use(panelMiddleware.RequireAuth(db))
	g.GET("", h.ListSSHKeys)
	g.POST("", h.CreateSSHKey)
	g.POST("/generate", h.GenerateSSHKey)
	g.DELETE("/:id", h.DeleteSSHKey)
}
