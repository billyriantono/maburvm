package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"
	panelMiddleware "github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"gorm.io/gorm"
)

// QuotaHandler handles per-user resource quota endpoints.
type QuotaHandler struct {
	service *service.QuotaService
}

// NewQuotaHandler creates a new QuotaHandler.
func NewQuotaHandler(s *service.QuotaService) *QuotaHandler {
	return &QuotaHandler{service: s}
}

// GetMyQuota handles GET /api/v1/quota (current user's limits + usage).
func (h *QuotaHandler) GetMyQuota(c echo.Context) error {
	user, ok := panelMiddleware.GetUserContext(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized"})
	}
	status, err := h.service.GetStatus(c.Request().Context(), user.ID.String())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": status})
}

// GetUserQuota handles GET /api/v1/users/:id/quota (admin).
func (h *QuotaHandler) GetUserQuota(c echo.Context) error {
	status, err := h.service.GetStatus(c.Request().Context(), c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": status})
}

// SetUserQuota handles PUT /api/v1/users/:id/quota (admin).
func (h *QuotaHandler) SetUserQuota(c echo.Context) error {
	var req service.SetQuotaRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
	}
	quota, err := h.service.SetQuota(c.Request().Context(), c.Param("id"), &req)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": quota})
}

// RegisterQuotaRoutes registers quota routes. Self-quota is any authenticated
// user; per-user quota management requires admin.
func RegisterQuotaRoutes(e *echo.Echo, h *QuotaHandler, db *gorm.DB) {
	auth := panelMiddleware.RequireAuth(db)
	adminOnly := panelMiddleware.RequirePermission("admin:access")

	e.GET("/api/v1/quota", h.GetMyQuota, auth)

	g := e.Group("/api/v1/users/:id/quota")
	g.Use(auth)
	g.GET("", h.GetUserQuota, adminOnly)
	g.PUT("", h.SetUserQuota, adminOnly)
}
