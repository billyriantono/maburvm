package handler

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/riverqueue/river"
	"gorm.io/gorm"
)

// RegisterBillingDeliveryRoutes serves the log of events sent to the billing
// system's webhook. Staff only: the events name customers and their servers.
func RegisterBillingDeliveryRoutes(e *echo.Echo, db *gorm.DB, rc *river.Client[pgx.Tx]) {
	auth := middleware.RequireAuth(db)

	e.GET("/api/v1/billing/deliveries", func(c echo.Context) error {
		size := clampInt(c.QueryParam("page_size"), 25, 5, 100)
		page := clampInt(c.QueryParam("page"), 1, 1, 100000)
		rows, total, err := service.ListBillingDeliveries(c.Request().Context(), db, c.QueryParam("status"), size, (page-1)*size)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Internal Server Error", "message": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{
			"deliveries": rows, "total": total, "page": page, "page_size": size,
		}})
	}, auth, middleware.RequirePermission("billing:read"))

	e.POST("/api/v1/billing/deliveries/:id/retry", func(c echo.Context) error {
		err := service.ErrDeliveryNotFound
		if _, bad := uuid.Parse(c.Param("id")); bad == nil {
			err = service.RetryBillingDelivery(c.Request().Context(), db, rc, c.Param("id"))
		}
		if errors.Is(err, service.ErrDeliveryNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "That event is not in the log, or was already delivered"})
		}
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Internal Server Error", "message": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{"queued": true}})
	}, auth, middleware.RequirePermission("billing:update"))
}
