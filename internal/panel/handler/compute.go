package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/authz"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// ComputeHandler serves a VM's hardware, nodes' pools of dedicated cores and
// VMs' snapshot schedules.
type ComputeHandler struct {
	vms   *service.VMService
	snaps *service.SnapshotService
	authz *authz.Authorizer
	db    *gorm.DB
}

// NewComputeHandler builds the handler.
func NewComputeHandler(vms *service.VMService, snaps *service.SnapshotService, a *authz.Authorizer, db *gorm.DB) *ComputeHandler {
	return &ComputeHandler{vms: vms, snaps: snaps, authz: a, db: db}
}

func computeError(c echo.Context, err error) error {
	status := http.StatusInternalServerError
	var allowance *service.AllowanceError
	switch {
	case errors.Is(err, service.ErrVMNotFound), errors.Is(err, service.ErrNodeNotFound), errors.Is(err, service.ErrSnapshotScheduleNotFound):
		status = http.StatusNotFound
	case errors.Is(err, service.ErrInvalidHardware), errors.Is(err, service.ErrInvalidSchedule):
		status = http.StatusBadRequest
	case errors.Is(err, service.ErrVMLimitsBusy), errors.Is(err, service.ErrNoDedicatedCPUs), errors.Is(err, service.ErrVMMigrating):
		status = http.StatusConflict
	case errors.As(err, &allowance):
		status = http.StatusForbidden
	case errors.Is(err, service.ErrHardwareUnavailable), errors.Is(err, service.ErrSnapshotOptionsUnsupported),
		errors.Is(err, service.ErrSnapshotsNeedQcow2):
		status = http.StatusUnprocessableEntity
	}
	return c.JSON(status, map[string]interface{}{"error": http.StatusText(status), "message": err.Error()})
}

// SetVMHardware handles PUT /api/v1/vms/:id/hardware {"hardware": {...}}:
// staff change a VM's CPU model, topology, dedicated cores, huge pages,
// watchdog and restart on crash. Its node applies them at the VM's next power
// off and on.
func (h *ComputeHandler) SetVMHardware(c echo.Context) error {
	if !h.authz.AuthorizeVM(c, c.Param("id")) {
		return nil
	}
	if u, ok := middleware.GetUserContext(c); !ok || u.Role != models.RoleAdmin {
		return c.JSON(http.StatusForbidden, map[string]interface{}{"error": "Forbidden", "message": "Only an administrator can change a VM's hardware."})
	}
	var req struct {
		Hardware *models.Hardware `json:"hardware"`
	}
	if err := c.Bind(&req); err != nil || req.Hardware == nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "hardware is required"})
	}
	res, err := h.vms.SetVMHardware(c.Request().Context(), c.Param("id"), *req.Hardware)
	if err != nil {
		return computeError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": res})
}

// NodeCPUPool handles GET /api/v1/nodes/:id/dedicated-cpus.
func (h *ComputeHandler) NodeCPUPool(c echo.Context) error {
	out, err := service.GetNodeCPUPool(c.Request().Context(), h.db, c.Param("id"))
	if err != nil {
		return computeError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": out})
}

// SetNodeCPUPool handles PUT /api/v1/nodes/:id/dedicated-cpus {"pool": "4-15"}.
func (h *ComputeHandler) SetNodeCPUPool(c echo.Context) error {
	var req struct {
		Pool string `json:"pool"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	if _, err := service.GetNodeCPUPool(c.Request().Context(), h.db, c.Param("id")); err != nil {
		return computeError(c, err)
	}
	out, err := service.SetNodeCPUPool(c.Request().Context(), h.db, c.Param("id"), req.Pool)
	if err != nil {
		return computeError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": out})
}

// GetSnapshotSchedule handles GET /api/v1/vms/:id/snapshot-schedule; data is
// null for a VM without one.
func (h *ComputeHandler) GetSnapshotSchedule(c echo.Context) error {
	if !h.authz.AuthorizeVM(c, c.Param("id")) {
		return nil
	}
	sch, err := h.snaps.GetSnapshotSchedule(c.Request().Context(), c.Param("id"))
	if errors.Is(err, service.ErrSnapshotScheduleNotFound) {
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": nil})
	}
	if err != nil {
		return computeError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": sch})
}

// SetSnapshotSchedule handles PUT /api/v1/vms/:id/snapshot-schedule.
func (h *ComputeHandler) SetSnapshotSchedule(c echo.Context) error {
	if !h.authz.AuthorizeVM(c, c.Param("id")) {
		return nil
	}
	var req service.SnapshotScheduleRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	sch, err := h.snaps.SetSnapshotSchedule(c.Request().Context(), c.Param("id"), req)
	if err != nil {
		return computeError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": sch})
}

// DeleteSnapshotSchedule handles DELETE /api/v1/vms/:id/snapshot-schedule.
// The snapshots it took are kept.
func (h *ComputeHandler) DeleteSnapshotSchedule(c echo.Context) error {
	if !h.authz.AuthorizeVM(c, c.Param("id")) {
		return nil
	}
	if err := h.snaps.DeleteSnapshotSchedule(c.Request().Context(), c.Param("id")); err != nil {
		return computeError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "Snapshot schedule removed; the snapshots it took are kept"})
}

// RegisterComputeRoutes registers the routes.
func RegisterComputeRoutes(e *echo.Echo, h *ComputeHandler, db *gorm.DB) {
	vms := e.Group("/api/v1/vms")
	vms.Use(middleware.RequireAuth(db), middleware.RequirePermission("vm:read"))
	vms.PUT("/:id/hardware", h.SetVMHardware, middleware.RequirePermission("vm:update"))
	vms.GET("/:id/snapshot-schedule", h.GetSnapshotSchedule)
	vms.PUT("/:id/snapshot-schedule", h.SetSnapshotSchedule, middleware.RequirePermission("snapshot:create"))
	vms.DELETE("/:id/snapshot-schedule", h.DeleteSnapshotSchedule, middleware.RequirePermission("snapshot:delete"))

	nodes := e.Group("/api/v1/nodes")
	nodes.Use(middleware.RequireAuth(db))
	nodes.GET("/:id/dedicated-cpus", h.NodeCPUPool, middleware.RequirePermission("node:read"))
	nodes.PUT("/:id/dedicated-cpus", h.SetNodeCPUPool, middleware.RequirePermission("node:update"))
}
