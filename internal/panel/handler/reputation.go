package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// ReputationHandler exposes what the outside world thinks of our address space.
type ReputationHandler struct {
	service *service.ReputationService
}

func NewReputationHandler(s *service.ReputationService) *ReputationHandler {
	return &ReputationHandler{service: s}
}

// RegisterReputationRoutes mounts the endpoints. Admin-only: this covers the
// whole fleet's addressing, including other tenants'.
func RegisterReputationRoutes(e *echo.Echo, h *ReputationHandler, db *gorm.DB) {
	g := e.Group("/api/v1/ip-reputation")
	g.Use(middleware.RequireAuth(db))
	g.Use(middleware.RequirePermission("admin:access"))

	g.GET("", h.List)
	g.POST("/check", h.CheckNow)
}

// List returns stored reputation. Defaults to flagged addresses only — a fleet
// of clean addresses buries the handful that are not.
func (h *ReputationHandler) List(c echo.Context) error {
	flaggedOnly := c.QueryParam("all") != "true"

	records, err := h.service.List(c.Request().Context(), flaggedOnly)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{
			"error": "Internal Server Error", "message": err.Error(),
		})
	}
	if records == nil {
		records = []models.IPReputation{}
	}
	return c.JSON(http.StatusOK, map[string]any{"data": records, "success": true})
}

// CheckNow runs a check immediately rather than waiting for the schedule.
//
// Bounded, because the blocklists and AbuseIPDB's free tier both have daily
// quotas: an unbounded "check everything" button would exhaust them and leave
// the rest of the fleet unchecked while reporting nothing wrong.
func (h *ReputationHandler) CheckNow(c echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), 2*time.Minute)
	defer cancel()

	checked := h.service.CheckDueAddresses(ctx, 0, 50)
	return c.JSON(http.StatusOK, map[string]any{
		"message": "Reputation check finished",
		"data":    map[string]any{"checked": checked},
	})
}
