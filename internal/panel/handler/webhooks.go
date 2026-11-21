package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/riverqueue/river"
	"gorm.io/gorm"
)

// webhookEndpointList is GET /api/v1/webhooks: the endpoints and every event
// one can subscribe to.
type webhookEndpointList struct {
	Success bool                        `json:"success"`
	Data    []service.WebhookEndpoint   `json:"data"`
	Events  []service.WebhookEventGroup `json:"events"`
}

// RegisterWebhookRoutes serves the operator's outbound webhooks: add, change,
// test and remove them, and read and retry each one's deliveries.
func RegisterWebhookRoutes(e *echo.Echo, db *gorm.DB, rc *river.Client[pgx.Tx]) {
	g := e.Group("/api/v1/webhooks", middleware.RequireAuth(db))
	read, write := middleware.RequirePermission("webhook:read"), middleware.RequirePermission("webhook:update")

	fail := func(c echo.Context, err error) error {
		switch {
		case errors.Is(err, service.ErrWebhookEndpointNotFound):
			return c.JSON(http.StatusNotFound, map[string]any{"error": "Not Found", "message": "No such webhook"})
		case errors.Is(err, service.ErrWebhookEndpointInvalid):
			return c.JSON(http.StatusBadRequest, map[string]any{"error": "Bad Request",
				"message": strings.TrimPrefix(err.Error(), service.ErrWebhookEndpointInvalid.Error()+": ")})
		case errors.Is(err, service.ErrDeliveryNotFound):
			return c.JSON(http.StatusNotFound, map[string]any{"error": "Not Found", "message": "That event is not in this webhook's log, or was already delivered"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "Internal Server Error", "message": err.Error()})
	}
	bind := func(c echo.Context) (service.WebhookEndpointRequest, error) {
		var req service.WebhookEndpointRequest
		if err := c.Bind(&req); err != nil {
			return req, fmt.Errorf("%w: the body is not valid JSON", service.ErrWebhookEndpointInvalid)
		}
		return req, nil
	}

	g.GET("", func(c echo.Context) error {
		rows, err := service.ListWebhookEndpoints(c.Request().Context(), db)
		if err != nil {
			return fail(c, err)
		}
		return c.JSON(http.StatusOK, webhookEndpointList{Success: true, Data: rows, Events: service.WebhookEventCatalog})
	}, read)

	g.POST("", func(c echo.Context) error {
		req, err := bind(c)
		if err == nil {
			var w *service.WebhookEndpoint
			if w, err = service.CreateWebhookEndpoint(c.Request().Context(), db, req); err == nil {
				return c.JSON(http.StatusCreated, map[string]any{"success": true, "data": w})
			}
		}
		return fail(c, err)
	}, write)

	g.PUT("/:id", func(c echo.Context) error {
		req, err := bind(c)
		if err == nil {
			var w *service.WebhookEndpoint
			if w, err = service.UpdateWebhookEndpoint(c.Request().Context(), db, c.Param("id"), req); err == nil {
				return c.JSON(http.StatusOK, map[string]any{"success": true, "data": w})
			}
		}
		return fail(c, err)
	}, write)

	g.DELETE("/:id", func(c echo.Context) error {
		if err := service.DeleteWebhookEndpoint(c.Request().Context(), db, c.Param("id")); err != nil {
			return fail(c, err)
		}
		return c.JSON(http.StatusOK, map[string]any{"success": true, "message": "Webhook removed"})
	}, write)

	g.POST("/:id/test", func(c echo.Context) error {
		d, err := service.SendTestEvent(c.Request().Context(), db, c.Param("id"))
		if err != nil {
			return fail(c, err)
		}
		return c.JSON(http.StatusOK, map[string]any{"success": true, "data": d})
	}, write)

	g.GET("/:id/deliveries", func(c echo.Context) error {
		if _, err := service.GetWebhookEndpoint(c.Request().Context(), db, c.Param("id")); err != nil {
			return fail(c, err)
		}
		size := clampInt(c.QueryParam("page_size"), 25, 5, 100)
		page := clampInt(c.QueryParam("page"), 1, 1, 100000)
		rows, total, err := service.ListEndpointDeliveries(c.Request().Context(), db, c.Param("id"), c.QueryParam("status"), size, (page-1)*size)
		if err != nil {
			return fail(c, err)
		}
		return c.JSON(http.StatusOK, map[string]any{"success": true, "meta": PageMeta(page, size, total), "data": map[string]any{
			"deliveries": rows, "total": total, "page": page, "page_size": size,
		}})
	}, read)

	g.POST("/:id/deliveries/:delivery_id/retry", func(c echo.Context) error {
		err := service.ErrDeliveryNotFound
		if _, bad := uuid.Parse(c.Param("delivery_id")); bad == nil {
			if _, bad = uuid.Parse(c.Param("id")); bad == nil {
				err = service.RetryEndpointDelivery(c.Request().Context(), db, rc, c.Param("id"), c.Param("delivery_id"))
			}
		}
		if err != nil {
			return fail(c, err)
		}
		return c.JSON(http.StatusOK, map[string]any{"success": true, "data": map[string]any{"queued": true}})
	}, write)
}
