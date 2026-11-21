package handler

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/riverqueue/river"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
)

// MaintenanceView is maintenance mode as a signed-in account sees it. Staff
// also see who switched it on and what is still running.
type MaintenanceView struct {
	Enabled     bool                 `json:"enabled"`
	Message     string               `json:"message"`
	Since       any                  `json:"since,omitempty"`
	By          string               `json:"by,omitempty"`
	HoldJobs    bool                 `json:"hold_jobs"`
	RunningJobs []service.RunningJob `json:"running_jobs,omitempty"`
	CanSwitch   bool                 `json:"can_switch"`
}

// ownerOnly refuses anyone but the founding administrator, signed in or with
// an unrestricted key of theirs.
func ownerOnly(db *gorm.DB) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			u, ok := middleware.GetUserContext(c)
			if !ok || u.APIKeyRestricted || u.ImpersonatorID != "" || u.ID != service.FoundingAdminID(c.Request().Context(), db) {
				return c.JSON(http.StatusForbidden, map[string]interface{}{"error": "Forbidden", "message": "Only the Owner can do this."})
			}
			return next(c)
		}
	}
}

// RegisterMaintenanceRoutes mounts panel maintenance mode.
func RegisterMaintenanceRoutes(e *echo.Echo, db *gorm.DB, rc *river.Client[pgx.Tx]) {
	g := e.Group("/api/v1/maintenance", middleware.RequireAuth(db))
	view := func(c echo.Context, m middleware.Maintenance) error {
		u, _ := middleware.GetUserContext(c)
		out := MaintenanceView{Enabled: m.Enabled, Message: m.Notice()}
		if !m.Enabled {
			out.Message = ""
		}
		if m.Since != nil {
			out.Since = m.Since
		}
		if u.Can(middleware.PermStaff) {
			out.Message, out.By, out.HoldJobs = m.Message, m.By, m.HoldJobs
			out.CanSwitch = !u.APIKeyRestricted && u.ImpersonatorID == "" && u.ID == service.FoundingAdminID(c.Request().Context(), db)
			if m.Enabled && u.Can("ops:read") {
				out.RunningJobs, _ = service.RunningJobs(c.Request().Context(), db)
			}
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": out})
	}
	g.GET("", func(c echo.Context) error {
		return view(c, middleware.LoadMaintenance(db))
	})
	g.PUT("", func(c echo.Context) error {
		var req struct {
			Enabled  bool   `json:"enabled"`
			Message  string `json:"message"`
			HoldJobs bool   `json:"hold_jobs"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
		}
		u, _ := middleware.GetUserContext(c)
		m, err := service.SetMaintenance(c.Request().Context(), db, rc, req.Enabled, req.Message, req.HoldJobs, u.Email)
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": err.Error()})
		}
		return view(c, m)
	}, ownerOnly(db))
}

// RegisterUpdateCheckRoutes mounts the update notice: staff who see settings
// read it; the Owner turns it on, checks now and dismisses a release.
func RegisterUpdateCheckRoutes(e *echo.Echo, db *gorm.DB) {
	g := e.Group("/api/v1/system/update", middleware.RequireAuth(db), middleware.RequirePermission(middleware.PermStaff))
	ok := func(c echo.Context, st service.UpdateStatus) error {
		u, _ := middleware.GetUserContext(c)
		owner := !u.APIKeyRestricted && u.ImpersonatorID == "" && u.ID == service.FoundingAdminID(c.Request().Context(), db)
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{
			"status": st, "can_manage": owner,
		}})
	}
	bad := func(c echo.Context, err error) error {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": err.Error()})
	}
	g.GET("", func(c echo.Context) error {
		return ok(c, service.GetUpdateStatus(c.Request().Context(), db))
	}, middleware.RequirePermission("settings:read"))
	g.PUT("", func(c echo.Context) error {
		var req service.UpdateCheckSettings
		if err := c.Bind(&req); err != nil {
			return bad(c, err)
		}
		if err := service.SaveUpdateCheckSettings(c.Request().Context(), db, req); err != nil {
			return bad(c, err)
		}
		return ok(c, service.GetUpdateStatus(c.Request().Context(), db))
	}, ownerOnly(db))
	g.POST("/check", func(c echo.Context) error {
		st, err := service.CheckForUpdate(c.Request().Context(), db)
		if errors.Is(err, service.ErrUpdateCheckOff) {
			return bad(c, err)
		}
		if err != nil {
			// The failure is recorded in the status, which says what went wrong.
			st = service.GetUpdateStatus(c.Request().Context(), db)
		}
		return ok(c, st)
	}, ownerOnly(db))
	g.POST("/dismiss", func(c echo.Context) error {
		var req struct {
			Tag string `json:"tag"`
		}
		if err := c.Bind(&req); err != nil {
			return bad(c, err)
		}
		if err := service.DismissUpdate(c.Request().Context(), db, req.Tag); err != nil {
			return bad(c, err)
		}
		return ok(c, service.GetUpdateStatus(c.Request().Context(), db))
	}, ownerOnly(db))
}
