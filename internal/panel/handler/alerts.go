package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"gorm.io/gorm"
)

// RegisterAlertRoutes serves the operator alert history and its settings.
// Staff only: an alert names nodes, pools and other customers' servers.
func RegisterAlertRoutes(e *echo.Echo, db *gorm.DB, alerts *service.AlertService) {
	auth := middleware.RequireAuth(db)
	perm := middleware.RequirePermission
	refused := func(c echo.Context, err error) error {
		if errors.Is(err, service.ErrAlertSettings) {
			msg := strings.TrimPrefix(err.Error(), service.ErrAlertSettings.Error()+": ")
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": msg})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Internal Server Error", "message": err.Error()})
	}

	e.GET("/api/v1/alerts", func(c echo.Context) error {
		size := clampInt(c.QueryParam("page_size"), 25, 5, 100)
		page := clampInt(c.QueryParam("page"), 1, 1, 100000)
		rows, total, err := alerts.List(c.Request().Context(), c.QueryParam("state") == "open", size, (page-1)*size)
		if err != nil {
			return refused(c, err)
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "meta": PageMeta(page, size, total), "data": map[string]interface{}{
			"alerts": rows, "total": total, "page": page, "page_size": size,
		}})
	}, auth, perm("ops:read"))

	e.POST("/api/v1/alerts/:id/close", func(c echo.Context) error {
		err := gorm.ErrRecordNotFound // an id that is no id names no alert
		if _, bad := uuid.Parse(c.Param("id")); bad == nil {
			err = alerts.Close(c.Request().Context(), c.Param("id"))
		}
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "That alert is not open"})
			}
			return refused(c, err)
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{"closed": true}})
	}, auth, perm("ops:update"))

	e.GET("/api/v1/settings/system/alerts", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": alerts.Status(c.Request().Context())})
	}, auth, perm("settings:read"))

	e.PUT("/api/v1/settings/system/alerts", func(c echo.Context) error {
		var in service.AlertSettingsInput
		if err := c.Bind(&in); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
		}
		if err := alerts.Save(c.Request().Context(), in); err != nil {
			return refused(c, err)
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": alerts.Status(c.Request().Context())})
	}, auth, perm("settings:update"))

	// Sends with the form as it stands, saved or not, and says how each channel answered.
	e.POST("/api/v1/settings/system/alerts/test", func(c echo.Context) error {
		var in service.AlertSettingsInput
		if err := c.Bind(&in); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
		}
		results, err := alerts.Test(c.Request().Context(), in)
		if err != nil {
			return refused(c, err)
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{"results": results}})
	}, auth, perm("settings:update"))
}
