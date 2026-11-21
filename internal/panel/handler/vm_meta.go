package handler

import (
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/authz"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// vmDeleteLocked answers a removal or reinstall a delete lock refused.
func vmDeleteLocked(c echo.Context, err error) error {
	return c.JSON(http.StatusConflict, map[string]interface{}{
		"error": "Conflict", "code": "delete_locked", "message": err.Error(),
	})
}

// RegisterVMMetaRoutes mounts a VM's delete lock and tags (owner and staff)
// and the staff notes on VMs and accounts (staff only). Changes are recorded
// by the audit middleware like every other write.
func RegisterVMMetaRoutes(e *echo.Echo, vms *service.VMService, db *gorm.DB) {
	az := authz.NewAuthorizer(db)
	bad := func(c echo.Context, msg string) error {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": msg})
	}
	vmErr := func(c echo.Context, err error) error {
		switch {
		case errors.Is(err, service.ErrVMNotFound):
			authz.NotFound(c)
			return nil
		case errors.Is(err, service.ErrVMDeleteLocked):
			return vmDeleteLocked(c, err)
		case errors.Is(err, service.ErrInvalidTags):
			return bad(c, err.Error())
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Internal Server Error", "message": err.Error()})
	}

	g := e.Group("/api/v1/vms", middleware.RequireAuth(db), middleware.RequirePermission("vm:read"))
	g.PUT("/:id/lock", func(c echo.Context) error {
		id := c.Param("id")
		if !az.AuthorizeVM(c, id) {
			return nil
		}
		var req struct {
			Locked bool `json:"locked"`
		}
		if err := c.Bind(&req); err != nil {
			return bad(c, "Invalid request body")
		}
		user, _ := middleware.GetUserContext(c)
		lock, err := vms.SetDeleteLock(c.Request().Context(), id, req.Locked, user.Role == models.RoleAdmin)
		if err != nil {
			return vmErr(c, err)
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]string{"delete_lock": lock}})
	}, middleware.RequirePermission("vm:update"))
	g.PUT("/:id/tags", func(c echo.Context) error {
		id := c.Param("id")
		if !az.AuthorizeVM(c, id) {
			return nil
		}
		var req struct {
			Tags []string `json:"tags"`
		}
		if err := c.Bind(&req); err != nil {
			return bad(c, "Invalid request body")
		}
		tags, err := vms.SetTags(c.Request().Context(), id, req.Tags)
		if err != nil {
			return vmErr(c, err)
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string][]string{"tags": tags}})
	}, middleware.RequirePermission("vm:update"))

	// Staff notes: the admin area only, whatever a customer's permissions say.
	staff := middleware.RequirePermission(middleware.PermStaff)
	notes := func(subject, read, write string, group *echo.Group) {
		ok := func(c echo.Context, data any) error {
			return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": data})
		}
		subjectID := func(c echo.Context) (string, bool) {
			id := c.Param("id")
			_, err := uuid.Parse(id)
			if note := c.Param("note_id"); err == nil && note != "" {
				_, err = uuid.Parse(note)
			}
			return id, err == nil
		}
		noteErr := func(c echo.Context, err error) error {
			if errors.Is(err, service.ErrNoteNotFound) {
				return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "Note not found"})
			}
			return bad(c, err.Error())
		}
		group.GET("/:id/notes", func(c echo.Context) error {
			id, valid := subjectID(c)
			if !valid {
				return bad(c, "Invalid id")
			}
			rows, err := service.ListStaffNotes(c.Request().Context(), db, subject, id)
			if err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Could not read the notes"})
			}
			return ok(c, rows)
		}, staff, middleware.RequirePermission(read))
		group.POST("/:id/notes", func(c echo.Context) error {
			id, valid := subjectID(c)
			if !valid {
				return bad(c, "Invalid id")
			}
			var req struct {
				Body string `json:"body"`
			}
			if err := c.Bind(&req); err != nil {
				return bad(c, "Invalid request body")
			}
			user, _ := middleware.GetUserContext(c)
			n, err := service.AddStaffNote(c.Request().Context(), db, subject, id, req.Body, user.ID.String(), user.Email)
			if err != nil {
				return noteErr(c, err)
			}
			return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": n})
		}, staff, middleware.RequirePermission(read), middleware.RequirePermission(write))
		group.PUT("/:id/notes/:note_id", func(c echo.Context) error {
			id, valid := subjectID(c)
			if !valid {
				return bad(c, "Invalid id")
			}
			var req struct {
				Body string `json:"body"`
			}
			if err := c.Bind(&req); err != nil {
				return bad(c, "Invalid request body")
			}
			n, err := service.UpdateStaffNote(c.Request().Context(), db, subject, id, c.Param("note_id"), req.Body)
			if err != nil {
				return noteErr(c, err)
			}
			return ok(c, n)
		}, staff, middleware.RequirePermission(read), middleware.RequirePermission(write))
		group.DELETE("/:id/notes/:note_id", func(c echo.Context) error {
			id, valid := subjectID(c)
			if !valid {
				return bad(c, "Invalid id")
			}
			if err := service.DeleteStaffNote(c.Request().Context(), db, subject, id, c.Param("note_id")); err != nil {
				return noteErr(c, err)
			}
			return ok(c, nil)
		}, staff, middleware.RequirePermission(read), middleware.RequirePermission(write))
	}
	notes(service.NoteOnVM, "vm:read", "vm:update", g)
	notes(service.NoteOnUser, "user:read", "user:update", e.Group("/api/v1/users", middleware.RequireAuth(db)))
}
