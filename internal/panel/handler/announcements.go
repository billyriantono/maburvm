package handler

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// RegisterAnnouncementRoutes mounts announcements: staff write them, customers
// see the ones meant for them, and a marketing email's unsubscribe link lands
// on the public route.
func RegisterAnnouncementRoutes(e *echo.Echo, db *gorm.DB) {
	ok := func(c echo.Context, data any) error {
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": data})
	}
	fail := func(c echo.Context, err error) error {
		switch {
		case errors.Is(err, service.ErrAnnouncementNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "Announcement not found"})
		case errors.Is(err, service.ErrInvalidAnnouncement), errors.Is(err, service.ErrEmailNotConfigured), errors.Is(err, service.ErrNoUnsubscribeLink):
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": err.Error()})
		case errors.Is(err, service.ErrAnnouncementSending):
			return c.JSON(http.StatusConflict, map[string]interface{}{"error": "Conflict", "message": err.Error()})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Internal Server Error", "message": err.Error()})
	}
	bind := func(c echo.Context) (service.AnnouncementInput, error) {
		var in service.AnnouncementInput
		if err := c.Bind(&in); err != nil {
			return in, fmt.Errorf("%w: invalid request body", service.ErrInvalidAnnouncement)
		}
		return in, nil
	}

	g := e.Group("/api/v1/announcements", middleware.RequireAuth(db))
	write := []echo.MiddlewareFunc{middleware.RequirePermission(middleware.PermStaff), middleware.RequirePermission("announcement:update")}

	g.GET("/current", func(c echo.Context) error {
		u, _ := middleware.GetUserContext(c)
		out := []service.CustomerAnnouncement{}
		if u.Role != models.RoleClient {
			return ok(c, out)
		}
		rows, err := service.CurrentAnnouncements(c.Request().Context(), db, u.ID.String())
		if err != nil {
			return fail(c, err)
		}
		for _, a := range rows {
			out = append(out, a.ForCustomer())
		}
		return ok(c, out)
	})
	g.GET("", func(c echo.Context) error {
		rows, err := service.ListAnnouncements(c.Request().Context(), db)
		if err != nil {
			return fail(c, err)
		}
		return ok(c, rows)
	}, middleware.RequirePermission(middleware.PermStaff), middleware.RequirePermission("announcement:read"))
	g.POST("", func(c echo.Context) error {
		in, err := bind(c)
		if err != nil {
			return fail(c, err)
		}
		u, _ := middleware.GetUserContext(c)
		a, err := service.SaveAnnouncement(c.Request().Context(), db, "", in, u.ID.String())
		if err != nil {
			return fail(c, err)
		}
		return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": a})
	}, write...)
	g.PUT("/:id", func(c echo.Context) error {
		in, err := bind(c)
		if err != nil {
			return fail(c, err)
		}
		a, err := service.SaveAnnouncement(c.Request().Context(), db, c.Param("id"), in, "")
		if err != nil {
			return fail(c, err)
		}
		return ok(c, a)
	}, write...)
	g.DELETE("/:id", func(c echo.Context) error {
		if err := service.DeleteAnnouncement(c.Request().Context(), db, c.Param("id")); err != nil {
			return fail(c, err)
		}
		return ok(c, nil)
	}, write...)
	g.POST("/preview", func(c echo.Context) error {
		in, err := bind(c)
		if err != nil {
			return fail(c, err)
		}
		p, err := service.PreviewAnnouncement(c.Request().Context(), db, in)
		if err != nil {
			return fail(c, err)
		}
		return ok(c, p)
	}, write...)
	g.POST("/:id/email", func(c echo.Context) error {
		var req struct {
			PerMinute int `json:"per_minute"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
		}
		if req.PerMinute == 0 {
			req.PerMinute = 60
		}
		n, err := service.EmailAnnouncement(c.Request().Context(), db, c.Param("id"), req.PerMinute)
		if err != nil {
			return fail(c, err)
		}
		return ok(c, map[string]int{"queued": n, "per_minute": min(max(req.PerMinute, 1), 600)})
	}, write...)

	// Public: the link in a marketing email. The token is signed by the panel.
	e.POST("/api/v1/unsubscribe", func(c echo.Context) error {
		var req struct {
			Token string `json:"token"`
		}
		if err := c.Bind(&req); err != nil || service.Unsubscribe(c.Request().Context(), db, req.Token) != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": service.ErrBadUnsubscribeToken.Error()})
		}
		return ok(c, map[string]bool{"unsubscribed": true})
	})
	// The same choice from the account's own settings.
	e.PUT("/api/v1/settings/email-preferences", func(c echo.Context) error {
		var req struct {
			Marketing bool `json:"marketing"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
		}
		u, _ := middleware.GetUserContext(c)
		if err := service.SetMarketingOptOut(c.Request().Context(), db, u.ID.String(), !req.Marketing); err != nil {
			return fail(c, err)
		}
		return ok(c, map[string]bool{"marketing": req.Marketing})
	}, middleware.RequireAuth(db))
}
