// Package handler provides HTTP handlers for VM management
package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// VMHandler handles HTTP requests for VM management
type VMHandler struct {
	service *service.VMService
}

// NewVMHandler creates a new VMHandler instance
func NewVMHandler(service *service.VMService) *VMHandler {
	return &VMHandler{
		service: service,
	}
}

// ============================================================================
// Create VM
// ============================================================================

// CreateVMRequest represents a request to create a new VM
type CreateVMRequest struct {
	Hostname     string           `json:"hostname" validate:"required,max=100"`
	OSTemplateID string           `json:"os_template_id" validate:"required,uuid"`
	Resources    models.Resources `json:"resources" validate:"required"`
	NodeID       string           `json:"node_id,omitempty" validate:"omitempty,uuid"`
}

// CreateVMResponse represents the response after creating a VM
type CreateVMResponse struct {
	ID        string `json:"id"`
	Hostname  string `json:"hostname"`
	Status    string `json:"status"`
	NodeID    string `json:"node_id"`
	JobID     int64  `json:"job_id"`
	VNCPort   int    `json:"vnc_port,omitempty"`
	CreatedAt string `json:"created_at"`
}

// CreateVM handles POST /api/vms - Create a new VM
func (h *VMHandler) CreateVM(c echo.Context) error {
	var req CreateVMRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Invalid request body",
		})
	}

	// Validate required fields
	if req.Hostname == "" || req.OSTemplateID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Hostname and OS template ID are required",
		})
	}

	// Get user ID from context (set by auth middleware)
	userID, ok := c.Get("user_id").(string)
	if !ok || userID == "" {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error":   "Unauthorized",
			"message": "User ID not found in token",
		})
	}

	// Create VM
	createReq := &service.CreateVMRequest{
		UserID:       userID,
		Hostname:     req.Hostname,
		OSTemplateID: req.OSTemplateID,
		Resources:    req.Resources,
		NodeID:       req.NodeID,
	}

	resp, err := h.service.CreateVM(c.Request().Context(), createReq)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrHostnameExists):
			return c.JSON(http.StatusConflict, map[string]interface{}{
				"error":   "Conflict",
				"message": "Hostname already exists",
			})
		case errors.Is(err, service.ErrTemplateNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "OS template not found",
			})
		case errors.Is(err, service.ErrNodeNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "Node not found",
			})
		case errors.Is(err, service.ErrNoAvailableNodes):
			return c.JSON(http.StatusServiceUnavailable, map[string]interface{}{
				"error":   "Service Unavailable",
				"message": "No available nodes for VM placement",
			})
		case errors.Is(err, service.ErrInvalidResources):
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error":   "Bad Request",
				"message": err.Error(),
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	response := CreateVMResponse{
		ID:        resp.VM.ID,
		Hostname:  resp.VM.Hostname,
		Status:    string(resp.VM.Status),
		NodeID:    resp.VM.NodeID,
		JobID:     resp.JobID,
		CreatedAt: resp.VM.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}

	if resp.VM.VNCPort != nil {
		response.VNCPort = *resp.VM.VNCPort
	}

	return c.JSON(http.StatusCreated, map[string]interface{}{
		"message": "VM created successfully",
		"data":    response,
	})
}

// ============================================================================
// List VMs
// ============================================================================

// VMListItem represents a VM in the list response
type VMListItem struct {
	ID           string `json:"id"`
	Hostname     string `json:"hostname"`
	Status       string `json:"status"`
	NodeID       string `json:"node_id"`
	UserID       string `json:"user_id"`
	OSTemplateID string `json:"os_template_id"`
	CPU          int    `json:"cpu"`
	RAM          int    `json:"ram_mb"`
	Disk         int    `json:"disk_gb"`
	VNCPort      int    `json:"vnc_port,omitempty"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// ListVMs handles GET /api/vms - List VMs with filtering and pagination
func (h *VMHandler) ListVMs(c echo.Context) error {
	// Parse query parameters
	status := c.QueryParam("status")
	nodeID := c.QueryParam("node_id")
	userID := c.QueryParam("user_id")
	limitStr := c.QueryParam("limit")
	offsetStr := c.QueryParam("offset")

	var limit, offset int
	if limitStr != "" {
		limit, _ = strconv.Atoi(limitStr)
	}
	if offsetStr != "" {
		offset, _ = strconv.Atoi(offsetStr)
	}

	// Build request
	listReq := &service.ListVMsRequest{
		Limit:  limit,
		Offset: offset,
	}

	if status != "" {
		listReq.Status = models.VMStatus(status)
	}
	if nodeID != "" {
		listReq.NodeID = nodeID
	}
	if userID != "" {
		listReq.UserID = userID
	}

	// List VMs
	resp, err := h.service.ListVMs(c.Request().Context(), listReq)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": err.Error(),
		})
	}

	// Map to response format
	items := make([]VMListItem, len(resp.VMs))
	for i, vm := range resp.VMs {
		items[i] = VMListItem{
			ID:           vm.ID,
			Hostname:     vm.Hostname,
			Status:       string(vm.Status),
			NodeID:       vm.NodeID,
			UserID:       vm.UserID,
			OSTemplateID: vm.OSTemplateID,
			CPU:          vm.Resources.CPU,
			RAM:          vm.Resources.RAM,
			Disk:         vm.Resources.Disk,
			CreatedAt:    vm.CreatedAt.Format("2006-01-02T15:04:05Z"),
			UpdatedAt:    vm.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		}
		if vm.VNCPort != nil {
			items[i].VNCPort = *vm.VNCPort
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":  "VMs retrieved successfully",
		"data":     items,
		"total":    resp.Total,
		"limit":    resp.Limit,
		"offset":   resp.Offset,
		"has_more": resp.HasMore,
	})
}

// ============================================================================
// Get VM Details
// ============================================================================

// VMDetailResponse represents detailed VM information
type VMDetailResponse struct {
	ID           string                 `json:"id"`
	Hostname     string                 `json:"hostname"`
	Status       string                 `json:"status"`
	NodeID       string                 `json:"node_id"`
	UserID       string                 `json:"user_id"`
	OSTemplateID string                 `json:"os_template_id"`
	Resources    models.Resources       `json:"resources"`
	VNCPort      int                    `json:"vnc_port,omitempty"`
	AgentStatus  map[string]interface{} `json:"agent_status,omitempty"`
	CreatedAt    string                 `json:"created_at"`
	UpdatedAt    string                 `json:"updated_at"`
}

// GetVM handles GET /api/vms/:id - Get VM details and status
func (h *VMHandler) GetVM(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	// Check if we should include agent status
	includeAgent := c.QueryParam("include_status") == "true"

	// Get VM
	vm, err := h.service.GetVM(c.Request().Context(), id, includeAgent)
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

	resp := VMDetailResponse{
		ID:           vm.VM.ID,
		Hostname:     vm.VM.Hostname,
		Status:       string(vm.VM.Status),
		NodeID:       vm.VM.NodeID,
		UserID:       vm.VM.UserID,
		OSTemplateID: vm.VM.OSTemplateID,
		Resources:    vm.VM.Resources,
		CreatedAt:    vm.VM.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:    vm.VM.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}

	if vm.VM.VNCPort != nil {
		resp.VNCPort = *vm.VM.VNCPort
	}

	// Include agent status if available
	if vm.Status != nil {
		resp.AgentStatus = map[string]interface{}{
			"state":        vm.Status.State.String(),
			"uptime":       vm.Status.UptimeSeconds,
			"pid":          vm.Status.Pid,
			"ip_addresses": vm.Status.IpAddresses,
			"vnc_port":     vm.Status.VncPort,
		}
		if vm.Status.CurrentResources != nil {
			resp.AgentStatus["resources"] = map[string]interface{}{
				"cpu_percent":      vm.Status.CurrentResources.CpuPercent,
				"memory_used_mb":   vm.Status.CurrentResources.MemoryUsedMb,
				"memory_total_mb":  vm.Status.CurrentResources.MemoryTotalMb,
				"disk_read_bytes":  vm.Status.CurrentResources.DiskReadBytes,
				"disk_write_bytes": vm.Status.CurrentResources.DiskWriteBytes,
			}
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "VM retrieved successfully",
		"data":    resp,
	})
}

// ============================================================================
// Update VM
// ============================================================================

// UpdateVMRequest represents a request to update a VM
type UpdateVMRequest struct {
	Hostname  string            `json:"hostname,omitempty"`
	Resources *models.Resources `json:"resources,omitempty"`
}

// UpdateVM handles PUT /api/vms/:id - Update VM hostname and resources
func (h *VMHandler) UpdateVM(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	var req UpdateVMRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Invalid request body",
		})
	}

	// Build update request
	updateReq := &service.UpdateVMRequest{
		VMID: id,
	}
	if req.Hostname != "" {
		updateReq.Hostname = req.Hostname
	}
	if req.Resources != nil {
		updateReq.Resources = req.Resources
	}

	// Update VM
	vm, err := h.service.UpdateVM(c.Request().Context(), updateReq)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrVMNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "VM not found",
			})
		case errors.Is(err, service.ErrHostnameExists):
			return c.JSON(http.StatusConflict, map[string]interface{}{
				"error":   "Conflict",
				"message": "Hostname already exists",
			})
		case errors.Is(err, service.ErrVMCannotBeModified):
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error":   "Bad Request",
				"message": "VM must be stopped to modify resources",
			})
		case errors.Is(err, service.ErrInvalidResources):
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error":   "Bad Request",
				"message": err.Error(),
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "VM updated successfully",
		"data": map[string]interface{}{
			"id":         vm.ID,
			"hostname":   vm.Hostname,
			"status":     string(vm.Status),
			"resources":  vm.Resources,
			"updated_at": vm.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		},
	})
}

// ============================================================================
// Delete VM
// ============================================================================

// DeleteVM handles DELETE /api/vms/:id - Delete a VM
func (h *VMHandler) DeleteVM(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	// Delete VM
	if err := h.service.DeleteVM(c.Request().Context(), id); err != nil {
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

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "VM deletion initiated",
	})
}

// ============================================================================
// VM Lifecycle Operations
// ============================================================================

// LifecycleRequest represents a request for VM lifecycle operations
type LifecycleRequest struct {
	Async      bool   `json:"async,omitempty"`
	TemplateID string `json:"template_id,omitempty"`
}

// LifecycleResponse represents the response from a lifecycle operation
type LifecycleResponse struct {
	VMID     string `json:"vm_id"`
	Command  string `json:"command"`
	Success  bool   `json:"success"`
	Message  string `json:"message,omitempty"`
	NewState string `json:"new_state,omitempty"`
	JobID    int64  `json:"job_id,omitempty"`
}

// StartVM handles POST /api/vms/:id/start - Start a VM
func (h *VMHandler) StartVM(c echo.Context) error {
	return h.handleLifecycleCommand(c, service.LifecycleStart)
}

// StopVM handles POST /api/vms/:id/stop - Stop a VM gracefully
func (h *VMHandler) StopVM(c echo.Context) error {
	return h.handleLifecycleCommand(c, service.LifecycleStop)
}

// ForceStopVM handles POST /api/vms/:id/force-stop - Force stop a VM
func (h *VMHandler) ForceStopVM(c echo.Context) error {
	return h.handleLifecycleCommand(c, service.LifecycleForceStop)
}

// RestartVM handles POST /api/vms/:id/restart - Restart a VM
func (h *VMHandler) RestartVM(c echo.Context) error {
	return h.handleLifecycleCommand(c, service.LifecycleRestart)
}

// handleLifecycleCommand handles lifecycle commands with common logic
func (h *VMHandler) handleLifecycleCommand(c echo.Context, command service.LifecycleCommand) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	var req LifecycleRequest
	if err := c.Bind(&req); err != nil {
		// Body is optional, ignore errors
	}

	// Execute lifecycle command
	lifecycleReq := &service.LifecycleRequest{
		VMID:    id,
		Command: command,
		Async:   req.Async,
	}

	resp, err := h.service.ExecuteLifecycleCommand(c.Request().Context(), lifecycleReq)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrVMNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "VM not found",
			})
		case errors.Is(err, service.ErrVMLifecycleFailed):
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error":   "Bad Request",
				"message": err.Error(),
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Lifecycle command executed",
		"data": LifecycleResponse{
			VMID:     resp.VMID,
			Command:  resp.Command,
			Success:  resp.Success,
			Message:  resp.Message,
			NewState: resp.NewState,
			JobID:    resp.JobID,
		},
	})
}

// ============================================================================
// Rebuild VM
// ============================================================================

// RebuildVMRequest represents a request to rebuild a VM
type RebuildVMRequest struct {
	TemplateID string `json:"template_id,omitempty"`
	PreserveIP bool   `json:"preserve_ip,omitempty"`
}

// RebuildVMResponse represents the response after rebuilding a VM
type RebuildVMResponse struct {
	VMID    string `json:"vm_id"`
	Status  string `json:"status"`
	JobID   int64  `json:"job_id"`
	Message string `json:"message,omitempty"`
}

// RebuildVM handles POST /api/vms/:id/rebuild - Rebuild a VM (reinstall OS)
func (h *VMHandler) RebuildVM(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	var req RebuildVMRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Invalid request body",
		})
	}

	// Rebuild VM
	rebuildReq := &service.RebuildVMRequest{
		VMID:       id,
		TemplateID: req.TemplateID,
		PreserveIP: req.PreserveIP,
	}

	resp, err := h.service.RebuildVM(c.Request().Context(), rebuildReq)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrVMNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "VM not found",
			})
		case errors.Is(err, service.ErrTemplateNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "OS template not found",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "VM rebuild initiated",
		"data": RebuildVMResponse{
			VMID:    resp.VMID,
			Status:  resp.Status,
			JobID:   resp.JobID,
			Message: resp.Message,
		},
	})
}

// ============================================================================
// VNC Operations
// ============================================================================

// VNCConfigResponse represents VNC connection details
type VNCConfigResponse struct {
	VMID         string `json:"vm_id"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	Password     string `json:"password,omitempty"`
	WebSocketURL string `json:"websocket_url,omitempty"`
}

// GetVNCConfig handles GET /api/vms/:id/vnc - Get VNC configuration
func (h *VMHandler) GetVNCConfig(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	// Check if user is admin or VM owner to include password
	// For now, include password for simplicity
	includePassword := true

	// Get VNC config
	vnc, err := h.service.GetVNCConfig(c.Request().Context(), id, includePassword)
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

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "VNC configuration retrieved",
		"data": VNCConfigResponse{
			VMID:         vnc.VMID,
			Host:         vnc.Host,
			Port:         vnc.Port,
			Password:     vnc.Password,
			WebSocketURL: vnc.WebSocketURL,
		},
	})
}

// RefreshVNCPassword handles POST /api/vms/:id/vnc/refresh - Generate new VNC password
func (h *VMHandler) RefreshVNCPassword(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	// Generate new password
	vnc, err := h.service.RefreshVNCPassword(c.Request().Context(), id)
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

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "VNC password refreshed",
		"data": map[string]interface{}{
			"vnc_port":     vnc.Port,
			"vnc_password": vnc.Password,
		},
	})
}

// ============================================================================
// Register Routes
// ============================================================================

// RegisterVMRoutes registers all VM routes with the Echo router
func RegisterVMRoutes(e *echo.Echo, handler *VMHandler, db interface{}) {
	// Create VM routes group
	vms := e.Group("/api/vms")

	// Apply authentication middleware
	vms.Use(middleware.RequireAuth(nil))

	// Apply permission middleware for VM management
	vms.Use(middleware.RequirePermission("vm:read"))

	// List VMs - requires vm:read
	vms.GET("", handler.ListVMs)

	// Get VM details - requires vm:read
	vms.GET("/:id", handler.GetVM)

	// Create VM - requires vm:create
	vms.POST("", handler.CreateVM, middleware.RequirePermission("vm:create"))

	// Update VM - requires vm:update
	vms.PUT("/:id", handler.UpdateVM, middleware.RequirePermission("vm:update"))

	// Delete VM - requires vm:delete
	vms.DELETE("/:id", handler.DeleteVM, middleware.RequirePermission("vm:delete"))

	// VM Lifecycle operations - require vm:lifecycle
	lifecycle := vms.Group("/:id")
	lifecycle.Use(middleware.RequirePermission("vm:lifecycle"))
	{
		lifecycle.POST("/start", handler.StartVM)
		lifecycle.POST("/stop", handler.StopVM)
		lifecycle.POST("/force-stop", handler.ForceStopVM)
		lifecycle.POST("/restart", handler.RestartVM)
		lifecycle.POST("/rebuild", handler.RebuildVM)
	}

	// VNC operations - require vm:console
	vnc := vms.Group("/:id/vnc")
	vnc.Use(middleware.RequirePermission("vm:console"))
	{
		vnc.GET("", handler.GetVNCConfig)
		vnc.POST("/refresh", handler.RefreshVNCPassword)
	}
}
