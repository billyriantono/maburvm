package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
)

// MigrationHandler serves moving VMs between nodes and draining nodes. Staff
// only: no customer role holds vm:migrate or node:drain.
type MigrationHandler struct {
	svc *service.MigrationService
}

// NewMigrationHandler builds the handler.
func NewMigrationHandler(svc *service.MigrationService) *MigrationHandler {
	return &MigrationHandler{svc: svc}
}

func migrationError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, service.ErrVMNotFound):
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "VM not found"})
	case errors.Is(err, service.ErrNodeNotFound):
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "Node not found"})
	case errors.Is(err, service.ErrMigrationActive), errors.Is(err, service.ErrDrainActive):
		return c.JSON(http.StatusConflict, map[string]interface{}{"error": "Conflict", "message": err.Error()})
	case errors.Is(err, service.ErrNoActiveMigration), errors.Is(err, service.ErrNoActiveDrain):
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": err.Error()})
	case errors.Is(err, service.ErrMigrationBlocked):
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": err.Error()})
	default:
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Internal Server Error", "message": err.Error()})
	}
}

func requesterID(c echo.Context) string {
	if u, ok := middleware.GetUserContext(c); ok {
		return u.ID.String()
	}
	return ""
}

// CheckMigration handles GET /vms/:id/migration/check: what a move would do
// and what stops it, changing nothing.
func (h *MigrationHandler) CheckMigration(c echo.Context) error {
	chk, err := h.svc.Check(c.Request().Context(), service.MigrationRequest{
		VMID: c.Param("id"), DestNodeID: c.QueryParam("dest_node_id"), DestPoolID: c.QueryParam("dest_pool_id"), Mode: c.QueryParam("mode"),
	})
	if err != nil {
		return migrationError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": chk})
}

// StartMigration handles POST /vms/:id/migrate.
func (h *MigrationHandler) StartMigration(c echo.Context) error {
	var req service.MigrationRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	req.VMID, req.RequestedBy = c.Param("id"), requesterID(c)
	row, chk, err := h.svc.Start(c.Request().Context(), req)
	if errors.Is(err, service.ErrMigrationBlocked) && chk != nil {
		return c.JSON(http.StatusConflict, map[string]interface{}{"error": "Conflict", "message": "The VM cannot be moved", "data": chk})
	}
	if err != nil {
		return migrationError(c, err)
	}
	return c.JSON(http.StatusAccepted, map[string]interface{}{"success": true, "data": row})
}

// GetMigration handles GET /vms/:id/migration: the VM's newest move, or null.
func (h *MigrationHandler) GetMigration(c echo.Context) error {
	row, err := h.svc.Latest(c.Request().Context(), c.Param("id"))
	if err != nil {
		return migrationError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": row})
}

// CancelMigration handles POST /vms/:id/migration/cancel.
func (h *MigrationHandler) CancelMigration(c echo.Context) error {
	row, err := h.svc.Cancel(c.Request().Context(), c.Param("id"))
	if err != nil {
		return migrationError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": row})
}

// GetDrain handles GET /nodes/:id/drain: the node's latest drain, and its
// side of the migration link.
func (h *MigrationHandler) GetDrain(c echo.Context) error {
	ctx := c.Request().Context()
	rep, err := h.svc.LatestDrain(ctx, c.Param("id"))
	if err != nil {
		return migrationError(c, err)
	}
	link, err := h.svc.Link(ctx, c.Param("id"))
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return migrationError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{"report": rep, "link": link}})
}

// PlanDrain handles POST /nodes/:id/drain/plan: where each VM would go.
func (h *MigrationHandler) PlanDrain(c echo.Context) error {
	var req service.DrainRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	plan, err := h.svc.PlanDrain(c.Request().Context(), c.Param("id"), req.Destinations)
	if err != nil {
		return migrationError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": plan})
}

// StartDrain handles POST /nodes/:id/drain.
func (h *MigrationHandler) StartDrain(c echo.Context) error {
	var req service.DrainRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	req.RequestedBy = requesterID(c)
	d, err := h.svc.StartDrain(c.Request().Context(), c.Param("id"), req)
	if err != nil {
		return migrationError(c, err)
	}
	return c.JSON(http.StatusAccepted, map[string]interface{}{"success": true, "data": d})
}

// AbortDrain handles POST /nodes/:id/drain/abort.
func (h *MigrationHandler) AbortDrain(c echo.Context) error {
	d, err := h.svc.AbortDrain(c.Request().Context(), c.Param("id"))
	if err != nil {
		return migrationError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": d})
}

// RegisterMigrationRoutes registers the move and drain routes.
func RegisterMigrationRoutes(e *echo.Echo, h *MigrationHandler, db *gorm.DB) {
	auth := middleware.RequireAuth(db)
	migrate := middleware.RequirePermission("vm:migrate")
	e.GET("/api/v1/vms/:id/migration/check", h.CheckMigration, auth, migrate)
	e.GET("/api/v1/vms/:id/migration", h.GetMigration, auth, migrate)
	e.POST("/api/v1/vms/:id/migrate", h.StartMigration, auth, migrate)
	e.POST("/api/v1/vms/:id/migration/cancel", h.CancelMigration, auth, migrate)

	drain := middleware.RequirePermission("node:drain")
	e.GET("/api/v1/nodes/:id/drain", h.GetDrain, auth, middleware.RequirePermission("node:read"))
	e.POST("/api/v1/nodes/:id/drain/plan", h.PlanDrain, auth, drain)
	e.POST("/api/v1/nodes/:id/drain", h.StartDrain, auth, drain)
	e.POST("/api/v1/nodes/:id/drain/abort", h.AbortDrain, auth, drain)
}
