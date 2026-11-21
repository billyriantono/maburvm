package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"gorm.io/gorm"
)

// RegisterDiskOpsRoutes registers growing and moving a VM's disks, the
// orphaned-disk finder and storage tiers.
func RegisterDiskOpsRoutes(e *echo.Echo, h *VMHandler, db *gorm.DB) {
	auth := middleware.RequireAuth(db)

	vms := e.Group("/api/v1/vms/:id", auth, middleware.RequirePermission("vm:read"))
	// Growing a disk is the owner's to do, as attaching one is.
	vms.POST("/disks/:device/resize", h.ResizeVMDisk, middleware.RequirePermission("vm:lifecycle"))
	// Moving one between pools is staff work.
	vms.POST("/disks/:device/move", h.MoveVMDisk, middleware.RequirePermission("vm:migrate"))
	vms.GET("/disk-moves", h.ListDiskMoves, middleware.RequirePermission("vm:migrate"))
	vms.POST("/disk-moves/cancel", h.CancelDiskMove, middleware.RequirePermission("vm:migrate"))

	storage := e.Group("/api/v1/storage", auth)
	storage.GET("/orphans", h.ListOrphanDisks, middleware.RequirePermission("node:read"))
	storage.POST("/orphans/delete", h.DeleteOrphanDisk, middleware.RequirePermission("storage:delete"))
	storage.GET("/tiers", h.ListStorageTiers, middleware.RequirePermission("node:read"))
	storage.PUT("/pools/:id/tier", h.SetPoolTier, middleware.RequirePermission("storage:update"))
}

func diskOpError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, service.ErrVMNotFound):
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "VM not found"})
	case errors.Is(err, service.ErrVMMigrating):
		return vmMoving(c, err)
	case errors.Is(err, service.ErrDiskMoveActive):
		return c.JSON(http.StatusConflict, map[string]interface{}{"error": "Conflict", "message": err.Error()})
	case errors.Is(err, service.ErrNodeCapacityExceeded):
		return c.JSON(http.StatusServiceUnavailable, map[string]interface{}{"error": "Node Capacity Exceeded", "message": capacityMessage(c, err)})
	case errors.Is(err, service.ErrDiskQuotaExceeded), errors.Is(err, service.ErrQuotaExceeded):
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Disk quota exceeded"})
	}
	return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": err.Error()})
}

// ResizeVMDisk handles POST /api/v1/vms/:id/disks/:device/resize {size_gb}:
// grow the system disk (vda) or an extra disk, running or stopped.
func (h *VMHandler) ResizeVMDisk(c echo.Context) error {
	id := c.Param("id")
	if !h.authorizeVMChange(c, id) {
		return nil
	}
	var req struct {
		SizeGB int `json:"size_gb"`
	}
	if err := c.Bind(&req); err != nil || req.SizeGB <= 0 {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "size_gb must be a positive integer"})
	}
	if isClientRole(c) && !h.service.SelfServiceAllowed(c.Request().Context(), service.SelfServiceExtraDisk) {
		return selfServiceDisabled(c, "Growing a disk")
	}
	device := c.Param("device")
	if device == "vda" && isClientRole(c) {
		// A customer's system disk is the size their plan sells; it changes
		// with the plan, through billing.
		return c.JSON(http.StatusForbidden, map[string]interface{}{"error": "Forbidden", "message": "The system disk grows with the server's plan; contact support or change the plan."})
	}
	res, err := h.service.ResizeDisk(c.Request().Context(), id, device, req.SizeGB, false)
	if err != nil {
		if refusedByAccount(c, err) {
			return nil
		}
		return diskOpError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": res})
}

// MoveVMDisk handles POST /api/v1/vms/:id/disks/:device/move {dest_pool_id}.
func (h *VMHandler) MoveVMDisk(c echo.Context) error {
	if isClientRole(c) {
		return c.JSON(http.StatusForbidden, map[string]interface{}{"error": "Forbidden", "message": "Only staff move disks"})
	}
	var req struct {
		DestPoolID string `json:"dest_pool_id"`
	}
	if err := c.Bind(&req); err != nil || req.DestPoolID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "dest_pool_id is required"})
	}
	userID := ""
	if u, ok := middleware.GetUserContext(c); ok {
		userID = u.ID.String()
	}
	move, err := h.service.StartDiskMove(c.Request().Context(), c.Param("id"), c.Param("device"), req.DestPoolID, userID)
	if err != nil {
		return diskOpError(c, err)
	}
	return c.JSON(http.StatusAccepted, map[string]interface{}{"success": true, "data": move})
}

// ListDiskMoves handles GET /api/v1/vms/:id/disk-moves.
func (h *VMHandler) ListDiskMoves(c echo.Context) error {
	if isClientRole(c) {
		return c.JSON(http.StatusForbidden, map[string]interface{}{"error": "Forbidden", "message": "Only staff move disks"})
	}
	moves, err := h.service.ListDiskMoves(c.Request().Context(), c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Internal Server Error", "message": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": moves})
}

// CancelDiskMove handles POST /api/v1/vms/:id/disk-moves/cancel.
func (h *VMHandler) CancelDiskMove(c echo.Context) error {
	if isClientRole(c) {
		return c.JSON(http.StatusForbidden, map[string]interface{}{"error": "Forbidden", "message": "Only staff move disks"})
	}
	move, err := h.service.CancelDiskMove(c.Request().Context(), c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusConflict, map[string]interface{}{"error": "Conflict", "message": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": move})
}

// ListOrphanDisks handles GET /api/v1/storage/orphans?node_id=.
func (h *VMHandler) ListOrphanDisks(c echo.Context) error {
	nodeID := c.QueryParam("node_id")
	if nodeID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "node_id is required"})
	}
	list, err := h.service.ListOrphanDisks(c.Request().Context(), nodeID)
	if err != nil {
		return c.JSON(http.StatusBadGateway, map[string]interface{}{"error": "Bad Gateway", "message": err.Error()})
	}
	if list == nil {
		list = []service.OrphanDisk{}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": list})
}

// DeleteOrphanDisk handles POST /api/v1/storage/orphans/delete: the disk as
// listed (node_id, path, size_bytes, modified_unix), confirm repeating its
// path, and allow_foreign for one MaburVM did not make.
func (h *VMHandler) DeleteOrphanDisk(c echo.Context) error {
	var req service.OrphanDeleteRequest
	if err := c.Bind(&req); err != nil || req.NodeID == "" || req.Path == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "node_id and path are required"})
	}
	if err := h.service.DeleteOrphanDisk(c.Request().Context(), req); err != nil {
		if errors.Is(err, service.ErrInvalidStoragePool) {
			return c.JSON(http.StatusConflict, map[string]interface{}{"error": "Conflict", "message": err.Error()})
		}
		return c.JSON(http.StatusBadGateway, map[string]interface{}{"error": "Bad Gateway", "message": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "Disk deleted"})
}

// ListStorageTiers handles GET /api/v1/storage/tiers.
func (h *VMHandler) ListStorageTiers(c echo.Context) error {
	tiers, err := service.StorageTiers(c.Request().Context(), h.service.DB())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Internal Server Error", "message": err.Error()})
	}
	if tiers == nil {
		tiers = []string{}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": tiers})
}

// SetPoolTier handles PUT /api/v1/storage/pools/:id/tier {tier}.
func (h *VMHandler) SetPoolTier(c echo.Context) error {
	var req struct {
		Tier string `json:"tier"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	pool, err := service.SetPoolTier(c.Request().Context(), h.service.DB(), c.Param("id"), req.Tier)
	switch {
	case errors.Is(err, service.ErrStoragePoolNotFound):
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": err.Error()})
	case errors.Is(err, service.ErrInvalidStorageTier):
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": err.Error()})
	case err != nil:
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Internal Server Error", "message": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": pool})
}
