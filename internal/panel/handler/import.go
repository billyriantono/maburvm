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
	UserID       string   `json:"user_id" validate:"required,uuid"`                                   // Owner for imported VMs
	OSTemplateID string   `json:"os_template_id" validate:"required,uuid"`                            // Template to associate
	CustomPath   string   `json:"custom_path,omitempty"`                                              // Optional: custom path to scan
	StoragePool  string   `json:"storage_pool,omitempty"`                                             // Target storage pool
	DiskAction   string   `json:"disk_action,omitempty" validate:"omitempty,oneof=symlink copy move"` // How to handle disks
	VMUUIDs      []string `json:"vm_uuids,omitempty"`                                                 // Optional: only import these VMs
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
		VMUUIDs:      req.VMUUIDs,
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

	// Wrap in the standard {success, data} envelope so the frontend (which reads
	// response.data.data) gets the payload — matches the rest of the API.
	return c.JSON(http.StatusOK, map[string]interface{}{
		"success": true,
		"data": ImportVirtualizorResponse{
			Message:      "Virtualizor import completed successfully",
			NodeID:       result.NodeID,
			TotalFound:   result.TotalFound,
			SuccessCount: result.SuccessCount,
			SkippedCount: result.SkippedCount,
			ErrorCount:   result.ErrorCount,
			Results:      result.Results,
			CompletedAt:  result.CompletedAt.Format("2006-01-02T15:04:05Z"),
			DurationMs:   result.DurationMs,
		},
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

// PreviewDiskInfo is the per-disk shape the preview UI renders.
type PreviewDiskInfo struct {
	SourceFile string `json:"source_file"`
	Format     string `json:"format"`
	Device     string `json:"device"`
}

// PreviewNetworkInfo is the per-NIC shape the preview UI renders.
type PreviewNetworkInfo struct {
	MACAddress string `json:"mac_address"`
	Bridge     string `json:"bridge"`
	Model      string `json:"model"`
	IPAddress  string `json:"ip_address"`
}

// PreviewVMInfo represents a VM that can be imported. Field shape matches the
// frontend ScannedVM type so the preview table renders fully populated rows.
type PreviewVMInfo struct {
	Name           string               `json:"name"`
	UUID           string               `json:"uuid"`
	Hostname       string               `json:"hostname"`
	CPU            int                  `json:"cpu"`
	Memory         int                  `json:"memory_mb"`
	VNCPort        int                  `json:"vnc_port"`
	Status         string               `json:"status,omitempty"`
	Disks          []PreviewDiskInfo    `json:"disks"`
	Networks       []PreviewNetworkInfo `json:"networks"`
	XMLPath        string               `json:"xml_path"`
	Conflicts      bool                 `json:"conflicts"`
	ConflictReason string               `json:"conflict_reason,omitempty"`
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

		hostname := candidate.Name
		if candidate.Metadata != nil && candidate.Metadata.Hostname != "" {
			hostname = candidate.Metadata.Hostname
		}
		vncPort := 0
		if candidate.VNC != nil {
			vncPort = candidate.VNC.Port
		}
		disks := make([]PreviewDiskInfo, 0, len(candidate.Disks))
		for _, d := range candidate.Disks {
			disks = append(disks, PreviewDiskInfo{SourceFile: d.SourceFile, Format: d.Format, Device: d.Device})
		}
		networks := make([]PreviewNetworkInfo, 0, len(candidate.Networks))
		for _, n := range candidate.Networks {
			ip := ""
			if n.IPConfig != nil {
				ip = n.IPConfig.Address
			}
			networks = append(networks, PreviewNetworkInfo{MACAddress: n.MACAddress, Bridge: n.Bridge, Model: n.Model, IPAddress: ip})
		}

		vmInfo := PreviewVMInfo{
			Name:      candidate.Name,
			UUID:      candidate.UUID,
			Hostname:  hostname,
			CPU:       candidate.CPU,
			Memory:    candidate.Memory,
			VNCPort:   vncPort,
			Status:    candidate.Status,
			Disks:     disks,
			Networks:  networks,
			XMLPath:   discovered.XMLPath,
			Conflicts: false,
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

	// Wrap in the standard {success, data} envelope so the frontend (which reads
	// response.data.data) gets the payload — matches the rest of the API.
	return c.JSON(http.StatusOK, map[string]interface{}{
		"success": true,
		"data": PreviewVirtualizorResponse{
			NodeID:     nodeID,
			TotalFound: len(vms),
			VMs:        vms,
		},
	})
}

// RegisterImportRoutes registers all import routes with the Echo router
// Registers the following endpoints:
//   - POST /api/nodes/:id/import/virtualizor - Import VMs from Virtualizor
//   - GET  /api/nodes/:id/import/virtualizor/preview - Preview importable VMs
//   - POST /api/nodes/:id/import/sync - Sync VM info (hostname + IP) from agent
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

	// Sync VM info from agent - updates hostname and IP for existing VMs
	importGroup.POST("/sync", handler.SyncNodeVMs)
}

// SyncNodeVMs handles POST /api/nodes/:id/import/sync
// Syncs VM hostname and IP addresses from agent guest-agent for all VMs on the node
func (h *ImportHandler) SyncNodeVMs(c echo.Context) error {
	nodeID := c.Param("id")
	if nodeID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Node ID is required",
		})
	}

	results, err := h.service.SyncNodeVMs(c.Request().Context(), nodeID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": err.Error(),
		})
	}

	// Count results
	var updated, unchanged, skipped, errCount int
	for _, r := range results {
		switch r.Status {
		case "updated":
			updated++
		case "unchanged":
			unchanged++
		case "skipped":
			skipped++
		case "error":
			errCount++
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Sync completed",
		"data": map[string]interface{}{
			"total":     len(results),
			"updated":   updated,
			"unchanged": unchanged,
			"skipped":   skipped,
			"errors":    errCount,
			"results":   results,
		},
	})
}
