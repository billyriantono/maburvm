package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/authz"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// The root password the panel generated for a VM, for its owner to read on
// the VM's page. It is never emailed. See service/vm_initial_password.go.
//
//	GET    /api/v1/vms/:id/initial-password         is there one, and until when
//	POST   /api/v1/vms/:id/initial-password/reveal  read it (recorded in the audit log)
//	DELETE /api/v1/vms/:id/initial-password         erase it now
func RegisterVMInitialPasswordRoutes(e *echo.Echo, db *gorm.DB, authorizer *authz.Authorizer) {
	g := e.Group("/api/v1/vms/:id/initial-password", middleware.RequireAuth(db), middleware.RequirePermission("vm:read"))
	audit := repository.NewAuditRepository(db)
	record := func(c echo.Context, user *middleware.UserContext, vmID, action string) {
		uid := user.ID.String()
		_ = audit.Create(c.Request().Context(), &models.AuditLog{
			UserID: &uid, Action: action, ResourceType: "vm", ResourceID: &vmID,
			IPAddress: c.RealIP(), UserAgent: c.Request().UserAgent(),
		})
	}
	// A key that is narrower than its owner (read-only, or a downgraded role)
	// must not be able to fetch a credential.
	fullAccess := func(c echo.Context) (*middleware.UserContext, bool) {
		user, ok := middleware.GetUserContext(c)
		if !ok || user.APIKeyRestricted || user.APIKeyReadOnly {
			_ = c.JSON(http.StatusForbidden, map[string]interface{}{"error": "Forbidden", "message": "This needs a signed-in session or a full-access API key."})
			return nil, false
		}
		return user, true
	}

	g.GET("", func(c echo.Context) error {
		id := c.Param("id")
		if !authorizer.AuthorizeVM(c, id) {
			return nil
		}
		ctx := c.Request().Context()
		// Expired passwords are erased whenever anyone looks, so none outlives its time by long.
		_, _ = service.PurgeInitialPasswords(ctx, db)
		until, err := service.InitialPasswordExpiry(ctx, db, id)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Internal Server Error", "message": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{"available": until != nil, "expires_at": until}})
	})

	g.POST("/reveal", func(c echo.Context) error {
		id := c.Param("id")
		if !authorizer.AuthorizeVM(c, id) {
			return nil
		}
		user, ok := fullAccess(c)
		if !ok {
			return nil
		}
		password, until, err := service.RevealInitialPassword(c.Request().Context(), db, id)
		if errors.Is(err, service.ErrNoInitialPassword) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "There is no stored password for this VM. It was never generated, has expired, or was erased. Set a new one from the Console tab."})
		}
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Internal Server Error", "message": "The stored password could not be read."})
		}
		record(c, user, id, "vm.initial_password.view")
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{"password": password, "expires_at": until}})
	})

	g.DELETE("", func(c echo.Context) error {
		id := c.Param("id")
		if !authorizer.AuthorizeVM(c, id) {
			return nil
		}
		user, ok := fullAccess(c)
		if !ok {
			return nil
		}
		if err := service.ForgetInitialPassword(c.Request().Context(), db, id); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Internal Server Error", "message": err.Error()})
		}
		record(c, user, id, "vm.initial_password.erase")
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true})
	})
}
