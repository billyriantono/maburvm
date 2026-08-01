// Package handler provides HTTP handlers for snapshot management
package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/authz"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
)

// SnapshotHandler handles HTTP requests for snapshot management
type SnapshotHandler struct {
	service *service.SnapshotService
	authz   *authz.Authorizer
}

// NewSnapshotHandler creates a new SnapshotHandler instance. The authorizer
// enforces owner-or-admin access (reusing the domain authz contract) and
// anti-enumeration (non-owner/nonexistent → 404, missing identity → 401).
func NewSnapshotHandler(service *service.SnapshotService, authorizer *authz.Authorizer) *SnapshotHandler {
	return &SnapshotHandler{
		service: service,
		authz:   authorizer,
	}
}

// ============================================================================
// Create Snapshot
// ============================================================================

// CreateSnapshotRequest represents a request to create a new snapshot
type CreateSnapshotRequest struct {
	Name string `json:"name" validate:"required,max=100"`
}

// CreateSnapshotResponse represents the response after creating a snapshot
type CreateSnapshotResponse struct {
	ID        string `json:"id"`
	VMID      string `json:"vm_id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	DiskPath  string `json:"disk_path,omitempty"`
	JobID     int64  `json:"job_id"`
	CreatedAt string `json:"created_at"`
}

// CreateSnapshot handles POST /api/vms/:id/snapshots - Create a new VM snapshot
func (h *SnapshotHandler) CreateSnapshot(c echo.Context) error {
	vmID := c.Param("id")
	if vmID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	var req CreateSnapshotRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Invalid request body",
		})
	}

	// Validate required fields
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Snapshot name is required",
		})
	}

	// Enforce owner-or-admin access to the route VM (401 missing auth, 404
	// non-owner/nonexistent). Admin support is preserved.
	if !h.authz.AuthorizeVM(c, vmID) {
		return nil
	}

	// Get user ID from context (set by auth middleware)
	userCtx, ok := middleware.GetUserContext(c)
	if !ok || userCtx == nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error":   "Unauthorized",
			"message": "User ID not found in token",
		})
	}
	userID := userCtx.ID.String()

	// Create snapshot
	createReq := &service.CreateSnapshotRequest{
		VMID:   vmID,
		Name:   req.Name,
		UserID: userID,
	}

	resp, err := h.service.CreateSnapshot(c.Request().Context(), createReq)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrVMNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "VM not found",
			})
		case errors.Is(err, service.ErrSnapshotNameExists):
			return c.JSON(http.StatusConflict, map[string]interface{}{
				"error":   "Conflict",
				"message": "Snapshot name already exists for this VM",
			})
		case errors.Is(err, service.ErrVMSnapshotLimitReached):
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error":   "Forbidden",
				"message": "VM snapshot limit reached (max 10 snapshots per VM)",
			})
		case errors.Is(err, service.ErrSnapshotInProgress):
			return c.JSON(http.StatusConflict, map[string]interface{}{
				"error":   "Conflict",
				"message": "A snapshot operation is already in progress for this VM",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusCreated, map[string]interface{}{
		"message": "Snapshot creation initiated",
		"data": CreateSnapshotResponse{
			ID:        resp.Snapshot.ID,
			VMID:      resp.Snapshot.VMID,
			Name:      resp.Snapshot.Name,
			Status:    string(resp.Snapshot.Status),
			DiskPath:  resp.Snapshot.DiskPath,
			JobID:     resp.JobID,
			CreatedAt: resp.Snapshot.CreatedAt.Format("2006-01-02T15:04:05Z"),
		},
	})
}

// ============================================================================
// List Snapshots
// ============================================================================

// SnapshotListItem represents a snapshot in the list response
type SnapshotListItem struct {
	ID        string `json:"id"`
	VMID      string `json:"vm_id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	DiskPath  string `json:"disk_path,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ListSnapshots handles GET /api/vms/:id/snapshots - List VM snapshots
func (h *SnapshotHandler) ListSnapshots(c echo.Context) error {
	vmID := c.Param("id")
	if vmID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	// Parse query parameters
	status := c.QueryParam("status")
	limitStr := c.QueryParam("limit")
	offsetStr := c.QueryParam("offset")

	var limit, offset int
	if limitStr != "" {
		limit, _ = strconv.Atoi(limitStr)
	}
	if offsetStr != "" {
		offset, _ = strconv.Atoi(offsetStr)
	}

	// Enforce owner-or-admin access to the route VM (401 missing auth, 404
	// non-owner/nonexistent). Admin support is preserved.
	if !h.authz.AuthorizeVM(c, vmID) {
		return nil
	}

	// Get user ID from context (set by auth middleware)
	userCtx, ok := middleware.GetUserContext(c)
	if !ok || userCtx == nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error":   "Unauthorized",
			"message": "User ID not found in token",
		})
	}
	userID := userCtx.ID.String()

	// Build request
	listReq := &service.ListSnapshotsRequest{
		VMID:   vmID,
		UserID: userID,
		Limit:  limit,
		Offset: offset,
	}

	if status != "" {
		listReq.Status = status
	}

	// List snapshots
	resp, err := h.service.ListSnapshots(c.Request().Context(), listReq)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrVMNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "VM not found",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	// Map to response format
	items := make([]SnapshotListItem, len(resp.Snapshots))
	for i, snapshot := range resp.Snapshots {
		items[i] = SnapshotListItem{
			ID:        snapshot.ID,
			VMID:      snapshot.VMID,
			Name:      snapshot.Name,
			Status:    string(snapshot.Status),
			DiskPath:  snapshot.DiskPath,
			CreatedAt: snapshot.CreatedAt.Format("2006-01-02T15:04:05Z"),
			UpdatedAt: snapshot.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":  "Snapshots retrieved successfully",
		"data":     items,
		"total":    resp.Total,
		"limit":    resp.Limit,
		"offset":   resp.Offset,
		"has_more": resp.HasMore,
	})
}

// ============================================================================
// Get Snapshot
// ============================================================================

// SnapshotDetailResponse represents detailed snapshot information
type SnapshotDetailResponse struct {
	ID        string `json:"id"`
	VMID      string `json:"vm_id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	DiskPath  string `json:"disk_path,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// GetSnapshot handles GET /api/vms/:id/snapshots/:snapshot_id - Get snapshot details
func (h *SnapshotHandler) GetSnapshot(c echo.Context) error {
	vmID := c.Param("id")
	snapshotID := c.Param("snapshot_id")

	if vmID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}
	if snapshotID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Snapshot ID is required",
		})
	}

	// Enforce owner-or-admin access to the route VM (401 missing auth, 404
	// non-owner/nonexistent). Admin support is preserved.
	if !h.authz.AuthorizeVM(c, vmID) {
		return nil
	}

	// Get snapshot (service validates snapshot→route-VM membership → 404 on mismatch)
	snapshot, err := h.service.GetSnapshot(c.Request().Context(), snapshotID, vmID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrSnapshotNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "Snapshot not found",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Snapshot retrieved successfully",
		"data": SnapshotDetailResponse{
			ID:        snapshot.ID,
			VMID:      snapshot.VMID,
			Name:      snapshot.Name,
			Status:    string(snapshot.Status),
			DiskPath:  snapshot.DiskPath,
			CreatedAt: snapshot.CreatedAt.Format("2006-01-02T15:04:05Z"),
			UpdatedAt: snapshot.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		},
	})
}

// ============================================================================
// Restore Snapshot
// ============================================================================

// RestoreSnapshotResponse represents the response from a restore operation
type RestoreSnapshotResponse struct {
	SnapshotID string `json:"snapshot_id"`
	VMID       string `json:"vm_id"`
	Status     string `json:"status"`
	JobID      int64  `json:"job_id"`
	Message    string `json:"message,omitempty"`
}

// RestoreSnapshot handles POST /api/vms/:id/snapshots/:snapshot_id/restore - Restore to snapshot
func (h *SnapshotHandler) RestoreSnapshot(c echo.Context) error {
	vmID := c.Param("id")
	snapshotID := c.Param("snapshot_id")

	if vmID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}
	if snapshotID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Snapshot ID is required",
		})
	}

	// Enforce owner-or-admin access to the route VM (401 missing auth, 404
	// non-owner/nonexistent). Admin support is preserved.
	if !h.authz.AuthorizeVM(c, vmID) {
		return nil
	}

	// Get user ID from context (set by auth middleware)
	userCtx, ok := middleware.GetUserContext(c)
	if !ok || userCtx == nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error":   "Unauthorized",
			"message": "User ID not found in token",
		})
	}
	userID := userCtx.ID.String()

	// Restore snapshot
	restoreReq := &service.RestoreSnapshotRequest{
		SnapshotID: snapshotID,
		VMID:       vmID,
		UserID:     userID,
	}

	resp, err := h.service.RestoreSnapshot(c.Request().Context(), restoreReq)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrSnapshotNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "Snapshot not found",
			})
		case errors.Is(err, service.ErrVMNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "VM not found",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Snapshot restore initiated",
		"data": RestoreSnapshotResponse{
			SnapshotID: resp.SnapshotID,
			VMID:       resp.VMID,
			Status:     resp.Status,
			JobID:      resp.JobID,
			Message:    resp.Message,
		},
	})
}

// ============================================================================
// Delete Snapshot
// ============================================================================

// DeleteSnapshot handles DELETE /api/vms/:id/snapshots/:snapshot_id - Delete a snapshot
func (h *SnapshotHandler) DeleteSnapshot(c echo.Context) error {
	vmID := c.Param("id")
	snapshotID := c.Param("snapshot_id")

	if vmID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}
	if snapshotID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Snapshot ID is required",
		})
	}

	// Enforce owner-or-admin access to the route VM (401 missing auth, 404
	// non-owner/nonexistent). Admin support is preserved.
	if !h.authz.AuthorizeVM(c, vmID) {
		return nil
	}

	// Get user ID from context (set by auth middleware)
	userCtx, ok := middleware.GetUserContext(c)
	if !ok || userCtx == nil {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error":   "Unauthorized",
			"message": "User ID not found in token",
		})
	}
	userID := userCtx.ID.String()

	// Delete snapshot
	deleteReq := &service.DeleteSnapshotRequest{
		SnapshotID: snapshotID,
		VMID:       vmID,
		UserID:     userID,
	}

	if err := h.service.DeleteSnapshot(c.Request().Context(), deleteReq); err != nil {
		switch {
		case errors.Is(err, service.ErrSnapshotNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "Snapshot not found",
			})
		case errors.Is(err, service.ErrVMNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "VM not found",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Snapshot deletion initiated",
	})
}

// ============================================================================
// Register Routes
// ============================================================================

// RegisterSnapshotRoutes registers all snapshot routes with the Echo router
func RegisterSnapshotRoutes(e *echo.Echo, handler *SnapshotHandler, db *gorm.DB) {
	// Create snapshot routes group
	snapshots := e.Group("/api/v1/vms/:id/snapshots")

	// Apply authentication middleware
	snapshots.Use(middleware.RequireAuth(db))

	// Apply permission middleware for snapshot management
	snapshots.Use(middleware.RequirePermission("vm:read"))

	// List snapshots - requires vm:read
	snapshots.GET("", handler.ListSnapshots)

	// Get snapshot details - requires vm:read
	snapshots.GET("/:snapshot_id", handler.GetSnapshot)

	// Create snapshot - requires vm:snapshot
	snapshots.POST("", handler.CreateSnapshot, middleware.RequirePermission("vm:snapshot"))

	// Restore snapshot - requires vm:snapshot
	snapshots.POST("/:snapshot_id/restore", handler.RestoreSnapshot, middleware.RequirePermission("vm:snapshot"))

	// Delete snapshot - requires vm:snapshot
	snapshots.DELETE("/:snapshot_id", handler.DeleteSnapshot, middleware.RequirePermission("vm:snapshot"))
}
