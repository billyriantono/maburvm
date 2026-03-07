// Package handler provides HTTP handlers for import operations
package handler

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
)

// ImportHandler handles HTTP requests for VM import operations
type ImportHandler struct {
	service *service.ImportService
}

// NewImportHandler creates a new ImportHandler instance
func NewImportHandler(service *service.ImportService) *ImportHandler {
	return &ImportHandler{
		service: service,
	}
}

// ImportVirtualizorRequest represents the request body for importing Virtualizor VMs
// POST /api/nodes/:id/import/virtualizor
// Scans node for Virtualizor VM XML files and imports discovered VMs
type ImportVirtualizorRequest struct {
	UserID       string `json:"user_id" validate:"required,uuid"`                                   // Owner for imported VMs
	OSTemplateID string `json:"os_template_id" validate:"required,uuid"`                            // Template to associate
	CustomPath   string `json:"custom_path,omitempty"`                                              // Optional: custom path to scan
	StoragePool  string `json:"storage_pool,omitempty"`                                             // Target storage pool
	DiskAction   string `json:"disk_action,omitempty" validate:"omitempty,oneof=symlink copy move"` // How to handle disks
}

// ImportVirtualizorResponse represents the response for import operation
type ImportVirtualizorResponse struct {
	Message      string                 `json:"message"`
	NodeID       string                 `json:"node_id"`
	TotalFound   int                    `json:"total_found"`
	SuccessCount int                    `json:"success_count"`
	SkippedCount int                    `json:"skipped_count"`
	ErrorCount   int                    `json:"error_count"`
	Results      []service.ImportResult `json:"results"`
	CompletedAt  string                 `json:"completed_at"`
	DurationMs   int64                  `json:"duration_ms"`
}

// ImportVirtualizor handles POST /api/nodes/:id/import/virtualizor
// Scans node for Virtualizor VM XML files:
//   - Common paths: /etc/libvirt/qemu/, /var/lib/libvirt/images/
//   - Allows custom path specification
//
// For each discovered VM:
//   - Parses XML (Task 28)
//   - Checks for conflicts (UUID already exists?)
//   - Creates VM record in database with source_migration = "virtualizor"
//   - Re-maps disk image to new storage location
//   - Enqueues ImportJob for each VM
//
// Returns import report: success count, skipped count, errors
func (h *ImportHandler) ImportVirtualizor(c echo.Context) error {
	nodeID := c.Param("id")
	if nodeID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Node ID is required",
		})
	}

	var req ImportVirtualizorRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Invalid request body: " + err.Error(),
		})
	}

	// Validate required fields
	if req.UserID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "user_id is required",
		})
	}
	if req.OSTemplateID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "os_template_id is required",
		})
	}

	// Build service request
	serviceReq := &service.ImportVirtualizorRequest{
		NodeID:       nodeID,
		UserID:       req.UserID,
		OSTemplateID: req.OSTemplateID,
		CustomPath:   req.CustomPath,
		StoragePool:  req.StoragePool,
		DiskAction:   service.DiskAction(req.DiskAction),
	}

	// Execute import
	result, err := h.service.ImportVirtualizor(c.Request().Context(), serviceReq)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrNodeNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "Node not found",
			})
		case errors.Is(err, service.ErrTemplateNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "OS template not found",
			})
		case errors.Is(err, service.ErrNoVMsFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "No Virtualizor VMs found to import",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, ImportVirtualizorResponse{
		Message:      "Virtualizor import completed successfully",
		NodeID:       result.NodeID,
		TotalFound:   result.TotalFound,
		SuccessCount: result.SuccessCount,
		SkippedCount: result.SkippedCount,
		ErrorCount:   result.ErrorCount,
		Results:      result.Results,
		CompletedAt:  result.CompletedAt.Format("2006-01-02T15:04:05Z"),
		DurationMs:   result.DurationMs,
	})
}

// PreviewVirtualizorRequest represents the request for previewing importable VMs
// GET /api/nodes/:id/import/virtualizor/preview
type PreviewVirtualizorRequest struct {
	CustomPath string `json:"custom_path,omitempty"`
}

// PreviewVirtualizorResponse represents the response for preview operation
type PreviewVirtualizorResponse struct {
	NodeID     string          `json:"node_id"`
	TotalFound int             `json:"total_found"`
	VMs        []PreviewVMInfo `json:"vms"`
}

// PreviewVMInfo represents a VM that can be imported
type PreviewVMInfo struct {
	Name           string `json:"name"`
	UUID           string `json:"uuid"`
	CPU            int    `json:"cpu"`
	Memory         int    `json:"memory_mb"`
	DiskCount      int    `json:"disk_count"`
	NetworkCount   int    `json:"network_count"`
	HasVNC         bool   `json:"has_vnc"`
	SourcePath     string `json:"source_path"`
	Conflicts      bool   `json:"conflicts"`
	ConflictReason string `json:"conflict_reason,omitempty"`
}

// PreviewVirtualizor handles GET /api/nodes/:id/import/virtualizor/preview
// Returns a list of Virtualizor VMs that can be imported without actually importing them
// Allows users to preview what will be imported before starting the operation
func (h *ImportHandler) PreviewVirtualizor(c echo.Context) error {
	nodeID := c.Param("id")
	if nodeID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Node ID is required",
		})
	}

	customPath := c.QueryParam("custom_path")

	// Scan for importable VMs
	discoveredVMs, err := h.service.ListImportableVMs(c.Request().Context(), nodeID, customPath)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrNodeNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "Node not found",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	// Build preview response
	vms := make([]PreviewVMInfo, 0, len(discoveredVMs))
	for _, discovered := range discoveredVMs {
		if !discovered.Valid {
			continue
		}

		candidate := discovered.Candidate
		vmInfo := PreviewVMInfo{
			Name:         candidate.Name,
			UUID:         candidate.UUID,
			CPU:          candidate.CPU,
			Memory:       candidate.Memory,
			DiskCount:    len(candidate.Disks),
			NetworkCount: len(candidate.Networks),
			HasVNC:       candidate.HasVNC(),
			SourcePath:   discovered.XMLPath,
			Conflicts:    false,
		}

		// Check for conflicts - VM UUID or hostname already exists
		if existingVM, err := h.service.GetVMByID(c.Request().Context(), candidate.UUID); err == nil && existingVM != nil {
			vmInfo.Conflicts = true
			vmInfo.ConflictReason = fmt.Sprintf("VM with UUID %s already exists (hostname: %s)", candidate.UUID, existingVM.Hostname)
		}
		if existingVM, err := h.service.GetVMByHostname(c.Request().Context(), candidate.Name); err == nil && existingVM != nil {
			vmInfo.Conflicts = true
			vmInfo.ConflictReason = fmt.Sprintf("Hostname %s already exists", candidate.Name)
		}

		vms = append(vms, vmInfo)
	}

	return c.JSON(http.StatusOK, PreviewVirtualizorResponse{
		NodeID:     nodeID,
		TotalFound: len(vms),
		VMs:        vms,
	})
}

// RegisterImportRoutes registers all import routes with the Echo router
// Registers the following endpoints:
//   - POST /api/nodes/:id/import/virtualizor - Import VMs from Virtualizor
//   - GET  /api/nodes/:id/import/virtualizor/preview - Preview importable VMs
func RegisterImportRoutes(e *echo.Echo, handler *ImportHandler) {
	// Import routes group
	importGroup := e.Group("/api/v1/nodes/:id/import")

	// Apply authentication middleware
	importGroup.Use(middleware.RequireAuth(nil))

	// Apply permission middleware - requires import permission
	importGroup.Use(middleware.RequirePermission("vm:import"))

	// Preview importable VMs - requires vm:import permission
	importGroup.GET("/virtualizor/preview", handler.PreviewVirtualizor)

	// Import from Virtualizor - requires vm:import permission
	importGroup.POST("/virtualizor", handler.ImportVirtualizor)
}
