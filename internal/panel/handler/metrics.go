package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	panelMiddleware "github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"gorm.io/gorm"
)

// MetricsHandler serves persisted metric history.
type MetricsHandler struct {
	service *service.MetricsService
}

// NewMetricsHandler creates a new MetricsHandler.
func NewMetricsHandler(s *service.MetricsService) *MetricsHandler {
	return &MetricsHandler{service: s}
}

// GetNodeMetricsHistory handles GET /api/v1/nodes/:id/metrics/history.
// Query params: minutes (window, default 60, max 1440), limit (default 500, max 5000).
func (h *MetricsHandler) GetNodeMetricsHistory(c echo.Context) error {
	id := c.Param("id")

	minutes := clampInt(c.QueryParam("minutes"), 60, 1, 1440)
	limit := clampInt(c.QueryParam("limit"), 500, 1, 5000)

	samples, err := h.service.NodeHistory(c.Request().Context(), id, time.Duration(minutes)*time.Minute, limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": samples})
}

// GetVMMetricsHistory handles GET /api/v1/vms/:id/metrics/history.
// Query params: minutes (window, default 60, max 1440), limit (default 500, max 5000).
func (h *MetricsHandler) GetVMMetricsHistory(c echo.Context) error {
	id := c.Param("id")

	minutes := clampInt(c.QueryParam("minutes"), 60, 1, 1440)
	limit := clampInt(c.QueryParam("limit"), 500, 1, 5000)

	samples, err := h.service.VMHistory(c.Request().Context(), id, time.Duration(minutes)*time.Minute, limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": samples})
}

// clampInt parses a query value, falling back to def, and clamps to [min, max].
func clampInt(raw string, def, min, max int) int {
	v := def
	if raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			v = parsed
		}
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// RegisterMetricsRoutes registers metric history routes.
func RegisterMetricsRoutes(e *echo.Echo, h *MetricsHandler, db *gorm.DB) {
	auth := panelMiddleware.RequireAuth(db)

	nodes := e.Group("/api/v1/nodes/:id/metrics")
	nodes.Use(auth)
	nodes.GET("/history", h.GetNodeMetricsHistory, panelMiddleware.RequirePermission("node:read"))

	vms := e.Group("/api/v1/vms/:id/metrics")
	vms.Use(auth)
	vms.GET("/history", h.GetVMMetricsHistory, panelMiddleware.RequirePermission("vm:read"))
}
