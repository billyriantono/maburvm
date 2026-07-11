// Package handler provides HTTP handlers for VM management
package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/panel/vnc"
	"github.com/maburvm/panel/internal/shared/models"
)

// VMHandler handles HTTP requests for VM management
type VMHandler struct {
	service       *service.VMService
	vncService    *service.VNCService
	vncProxy      *vnc.ProxyServer
	sshKeyService *service.SSHKeyService
	recipeService *service.RecipeService
}

// NewVMHandler creates a new VMHandler instance
func NewVMHandler(service *service.VMService, vncService *service.VNCService, vncProxy *vnc.ProxyServer, sshKeyService *service.SSHKeyService, recipeService *service.RecipeService) *VMHandler {
	return &VMHandler{
		service:       service,
		vncService:    vncService,
		vncProxy:      vncProxy,
		sshKeyService: sshKeyService,
		recipeService: recipeService,
	}
}

// NewVMHandlerWithoutVNC creates a new VMHandler instance without VNC service (for backward compatibility)
func NewVMHandlerWithoutVNC(service *service.VMService) *VMHandler {
	return &VMHandler{
		service:    service,
		vncService: nil,
		vncProxy:   nil,
	}
}

// authorizeVM enforces per-resource tenant isolation for a VM-scoped request.
// Admins may act on any VM; every other user may act only on VMs they own.
//
// It returns true when the caller is authorized. When not, it writes the
// appropriate response (401 if unauthenticated, 404 otherwise — 404 rather than
// 403 so a client cannot probe which VM IDs exist) and returns false; the caller
// should then `return nil`, as the response has already been committed.
func (h *VMHandler) authorizeVM(c echo.Context, vmID string) bool {
	userCtx, ok := middleware.GetUserContext(c)
	if !ok {
		_ = c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error":   "Unauthorized",
			"message": "authentication required",
		})
		return false
	}
	// Admins bypass ownership checks.
	if userCtx.Role == models.RoleAdmin {
		return true
	}
	ownerID, err := h.service.GetVMOwner(c.Request().Context(), vmID)
	if err != nil || ownerID != userCtx.ID.String() {
		_ = c.JSON(http.StatusNotFound, map[string]interface{}{
			"error":   "Not Found",
			"message": "VM not found",
		})
		return false
	}
	return true
}

// ============================================================================
// Create VM
// ============================================================================

// CreateVMRequest represents a request to create a new VM
type CreateVMRequest struct {
	Hostname         string           `json:"hostname" validate:"required,max=100"`
	OSTemplateID     string           `json:"os_template_id" validate:"required,uuid"`
	Resources        models.Resources `json:"resources" validate:"required"`
	NodeID           string           `json:"node_id,omitempty" validate:"omitempty,uuid"`
	PlanID           string           `json:"plan_id,omitempty" validate:"omitempty,uuid"`
	IPPoolID         string           `json:"ip_pool_id,omitempty" validate:"omitempty,uuid"`
	RequestedIP      string           `json:"requested_ip,omitempty" validate:"omitempty,ip"`
	BandwidthMbps    int              `json:"bandwidth_mbps,omitempty" validate:"omitempty,min=0,max=10000"`
	VLANID           int              `json:"vlan_id,omitempty" validate:"omitempty,min=0,max=4094"`
	CPUModel         string           `json:"cpu_model,omitempty" validate:"omitempty,max=64"`
	UserData         string           `json:"user_data,omitempty" validate:"omitempty,max=65536"`
	ManagedNetworkID string           `json:"managed_network_id,omitempty" validate:"omitempty,uuid"`
	RecipeID         string           `json:"recipe_id,omitempty" validate:"omitempty,uuid"`
	// Password sets the new guest's root password. RegeneratePassword (with an
	// empty Password) asks the server to generate one and return it once.
	Password           string `json:"password,omitempty" validate:"omitempty,min=8,max=128"`
	RegeneratePassword bool   `json:"regenerate_password,omitempty"`
	// SSHKeyIDs are the user's saved SSH keys to inject into the new guest.
	SSHKeyIDs []string `json:"ssh_key_ids,omitempty"`
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
	// RootPassword is present only when the server generated a password for this
	// VM (regenerate_password with no explicit password) — shown to the user once.
	RootPassword string `json:"root_password,omitempty"`
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

	// Get the user from context (RequireAuth stores it under the "user" key).
	userCtx, ok := middleware.GetUserContext(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error":   "Unauthorized",
			"message": "User not found in context",
		})
	}
	userID := userCtx.ID.String()

	// Resolve a selected recipe into the first-boot user-data (ownership-enforced).
	// An explicit user_data wins; the recipe fills it only when none was supplied.
	userData := req.UserData
	if userData == "" && req.RecipeID != "" && h.recipeService != nil {
		script, rerr := h.recipeService.ResolveScript(c.Request().Context(), userID, req.RecipeID)
		if rerr != nil {
			if errors.Is(rerr, service.ErrRecipeNotFound) {
				return c.JSON(http.StatusNotFound, map[string]interface{}{
					"error":   "Not Found",
					"message": "Recipe not found",
				})
			}
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": "failed to resolve recipe: " + rerr.Error(),
			})
		}
		userData = script
	}

	// Resolve the selected SSH keys to authorized_keys lines (ownership-enforced),
	// so the new guest is actually loginable on first boot.
	var sshPublicKeys []string
	if len(req.SSHKeyIDs) > 0 && h.sshKeyService != nil {
		keys, kerr := h.sshKeyService.ResolvePublicKeys(c.Request().Context(), userID, req.SSHKeyIDs)
		if kerr != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": "failed to resolve SSH keys: " + kerr.Error(),
			})
		}
		sshPublicKeys = keys
	}

	// Create VM
	createReq := &service.CreateVMRequest{
		UserID:             userID,
		Hostname:           req.Hostname,
		OSTemplateID:       req.OSTemplateID,
		Resources:          req.Resources,
		NodeID:             req.NodeID,
		PlanID:             req.PlanID,
		IPPoolID:           req.IPPoolID,
		RequestedIP:        req.RequestedIP,
		BandwidthMbps:      req.BandwidthMbps,
		VLANID:             req.VLANID,
		CPUModel:           req.CPUModel,
		UserData:           userData,
		ManagedNetworkID:   req.ManagedNetworkID,
		Password:           req.Password,
		RegeneratePassword: req.RegeneratePassword,
		SSHPublicKeys:      sshPublicKeys,
	}

	// Production-grade default: a VM with neither a password nor an SSH key can't
	// be logged into. If the caller supplied neither, generate a root password and
	// return it once (what VirtFusion/Virtualizor do) so the VM is always usable.
	if req.Password == "" && len(sshPublicKeys) == 0 {
		createReq.RegeneratePassword = true
	}

	// Self-service (client) orders don't pick an IP pool — admins do. When a
	// non-admin creates a VM without specifying a pool or a private managed
	// network, auto-assign a public IP so the VM is actually reachable instead of
	// silently landing on NAT.
	if userCtx.Role != models.RoleAdmin && req.IPPoolID == "" && req.ManagedNetworkID == "" {
		createReq.AutoAssignIP = true
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
		case errors.Is(err, service.ErrTemplateNotInstallable):
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error":   "Bad Request",
				"message": err.Error(),
			})
		case errors.Is(err, service.ErrPoolNotAvailableOnNode):
			return c.JSON(http.StatusConflict, map[string]interface{}{
				"error":   "Conflict",
				"message": "The selected IP pool is not assigned to the chosen node. Pick a pool available on that node, or leave the pool empty to use DHCP.",
			})
		case errors.Is(err, service.ErrNoAvailableIPAddress):
			return c.JSON(http.StatusConflict, map[string]interface{}{
				"error":   "Conflict",
				"message": "The selected IP pool has no free addresses left.",
			})
		case errors.Is(err, service.ErrNoUsablePool):
			return c.JSON(http.StatusConflict, map[string]interface{}{
				"error":   "Conflict",
				"message": err.Error(),
			})
		case errors.Is(err, service.ErrPoolHasNoBridge):
			return c.JSON(http.StatusUnprocessableEntity, map[string]interface{}{
				"error":   "Unprocessable Entity",
				"message": err.Error(),
			})
		case errors.Is(err, service.ErrQuotaExceeded):
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error":   "Quota Exceeded",
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
		ID:           resp.VM.ID,
		Hostname:     resp.VM.Hostname,
		Status:       string(resp.VM.Status),
		NodeID:       resp.VM.NodeID,
		JobID:        resp.JobID,
		CreatedAt:    resp.VM.CreatedAt.Format("2006-01-02T15:04:05Z"),
		RootPassword: resp.RootPassword,
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
	ID           string      `json:"id"`
	Hostname     string      `json:"hostname"`
	Status       string      `json:"status"`
	NodeID       string      `json:"node_id"`
	NodeName     string      `json:"node_name"`
	NodeStatus   string      `json:"node_status"`
	UserID       string      `json:"user_id"`
	OSTemplateID string      `json:"os_template_id"`
	Resources    VMResources `json:"resources"`
	VNCPort      int         `json:"vnc_port,omitempty"`
	CreatedAt    string      `json:"created_at"`
	UpdatedAt    string      `json:"updated_at"`
}

// VMResources for list response
type VMResources struct {
	CPU  int `json:"cpu"`
	RAM  int `json:"ram"`
	Disk int `json:"disk"`
}

// ListVMs handles GET /api/vms - List VMs with filtering and pagination
func (h *VMHandler) ListVMs(c echo.Context) error {
	// Parse query parameters
	status := c.QueryParam("status")
	nodeID := c.QueryParam("node_id")
	userID := c.QueryParam("user_id")
	limitStr := c.QueryParam("limit")
	offsetStr := c.QueryParam("offset")
	pageStr := c.QueryParam("page")
	pageSizeStr := c.QueryParam("page_size")

	var limit, offset int
	if limitStr != "" {
		limit, _ = strconv.Atoi(limitStr)
	}
	if offsetStr != "" {
		offset, _ = strconv.Atoi(offsetStr)
	}

	// Support page/page_size params from frontend
	if pageStr != "" && pageSizeStr != "" {
		page, _ := strconv.Atoi(pageStr)
		pageSize, _ := strconv.Atoi(pageSizeStr)
		if page < 1 {
			page = 1
		}
		if pageSize > 0 {
			limit = pageSize
			offset = (page - 1) * pageSize
		}
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
	// Tenant isolation: a non-admin caller may only ever see their own VMs, so we
	// force the owner filter to their ID and ignore any client-supplied user_id
	// (which would otherwise let them enumerate other tenants' VMs). Admins may
	// filter by any user_id, or omit it to list all VMs.
	if userCtx, ok := middleware.GetUserContext(c); ok && userCtx.Role != models.RoleAdmin {
		listReq.UserID = userCtx.ID.String()
	} else if userID != "" {
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

	// Build node lookup map so list rows can show whether the owning node is
	// offline/maintenance. VMs are still inventory records even when their node is
	// inactive, but the UI must not present them as operable.
	nodeSummaries := make(map[string]service.NodeSummary)
	if len(resp.VMs) > 0 {
		nodes, _ := h.service.GetNodeSummaries(c.Request().Context())
		nodeSummaries = nodes
	}

	// Map to response format
	items := make([]VMListItem, len(resp.VMs))
	for i, vm := range resp.VMs {
		node := nodeSummaries[vm.NodeID]
		items[i] = VMListItem{
			ID:           vm.ID,
			Hostname:     vm.Hostname,
			Status:       string(vm.Status),
			NodeID:       vm.NodeID,
			NodeName:     node.Name,
			NodeStatus:   string(node.Status),
			UserID:       vm.UserID,
			OSTemplateID: vm.OSTemplateID,
			Resources: VMResources{
				CPU:  vm.Resources.CPU,
				RAM:  vm.Resources.RAM,
				Disk: vm.Resources.Disk,
			},
			CreatedAt: vm.CreatedAt.Format("2006-01-02T15:04:05Z"),
			UpdatedAt: vm.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		}
		if vm.VNCPort != nil {
			items[i].VNCPort = *vm.VNCPort
		}
	}

	// Calculate page info for frontend
	currentPage := 1
	pageSize := resp.Limit
	if pageSize > 0 {
		currentPage = (resp.Offset / pageSize) + 1
	}
	totalPages := 1
	if resp.Total > 0 && pageSize > 0 {
		totalPages = int((resp.Total + int64(pageSize) - 1) / int64(pageSize))
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":    "VMs retrieved successfully",
		"data":       items,
		"total":      resp.Total,
		"page":       currentPage,
		"limit":      resp.Limit,
		"totalPages": totalPages,
		"offset":     resp.Offset,
		"has_more":   resp.HasMore,
	})
}

// ============================================================================
// Get VM Details
// ============================================================================

// VMDetailResponse represents detailed VM information
type VMDetailResponse struct {
	ID             string                 `json:"id"`
	Hostname       string                 `json:"hostname"`
	Status         string                 `json:"status"`
	NodeID         string                 `json:"node_id"`
	UserID         string                 `json:"user_id"`
	OSTemplateID   string                 `json:"os_template_id"`
	Resources      models.Resources       `json:"resources"`
	VNCPort        int                    `json:"vnc_port,omitempty"`
	ConsoleEnabled bool                   `json:"console_enabled"`
	RescueMode     bool                   `json:"rescue_mode"`
	AgentStatus    map[string]interface{} `json:"agent_status,omitempty"`
	CreatedAt      string                 `json:"created_at"`
	UpdatedAt      string                 `json:"updated_at"`
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

	if !h.authorizeVM(c, id) {
		return nil
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
		ID:             vm.VM.ID,
		Hostname:       vm.VM.Hostname,
		Status:         string(vm.VM.Status),
		NodeID:         vm.VM.NodeID,
		UserID:         vm.VM.UserID,
		OSTemplateID:   vm.VM.OSTemplateID,
		Resources:      vm.VM.Resources,
		ConsoleEnabled: vm.VM.ConsoleEnabled,
		RescueMode:     vm.VM.RescueMode,
		CreatedAt:      vm.VM.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:      vm.VM.UpdatedAt.Format("2006-01-02T15:04:05Z"),
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

	if !h.authorizeVM(c, id) {
		return nil
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
		case errors.Is(err, service.ErrQuotaExceeded):
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error":   "Quota Exceeded",
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

	if !h.authorizeVM(c, id) {
		return nil
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

// SuspendVM handles POST /api/vms/:id/suspend - Pause a VM (keep in memory)
func (h *VMHandler) SuspendVM(c echo.Context) error {
	return h.handleLifecycleCommand(c, service.LifecycleSuspend)
}

// UnsuspendVM handles POST /api/vms/:id/unsuspend - Resume a paused VM
func (h *VMHandler) UnsuspendVM(c echo.Context) error {
	return h.handleLifecycleCommand(c, service.LifecycleUnsuspend)
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

	if !h.authorizeVM(c, id) {
		return nil
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
		case errors.Is(err, service.ErrVMLifecycleFailed), errors.Is(err, service.ErrVMNodeInactive):
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
	// Password sets the rebuilt guest's root password. RegeneratePassword asks
	// the server to generate one (returned once in the response).
	Password           string `json:"password,omitempty"`
	RegeneratePassword bool   `json:"regenerate_password,omitempty"`
	// SSHKeyIDs are the user's saved SSH keys to inject into the rebuilt guest.
	SSHKeyIDs []string `json:"ssh_key_ids,omitempty"`
}

// RebuildVMResponse represents the response after rebuilding a VM
type RebuildVMResponse struct {
	VMID    string `json:"vm_id"`
	Status  string `json:"status"`
	JobID   int64  `json:"job_id"`
	Message string `json:"message,omitempty"`
	// RootPassword is present only when a password was generated for this rebuild.
	RootPassword string `json:"root_password,omitempty"`
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

	if !h.authorizeVM(c, id) {
		return nil
	}

	var req RebuildVMRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Invalid request body",
		})
	}

	// Resolve the selected SSH keys to authorized_keys lines (ownership-enforced).
	var sshPublicKeys []string
	if len(req.SSHKeyIDs) > 0 && h.sshKeyService != nil {
		if user, ok := middleware.GetUserContext(c); ok {
			keys, kerr := h.sshKeyService.ResolvePublicKeys(c.Request().Context(), user.ID.String(), req.SSHKeyIDs)
			if kerr != nil {
				return c.JSON(http.StatusInternalServerError, map[string]interface{}{
					"error":   "Internal Server Error",
					"message": "failed to resolve SSH keys: " + kerr.Error(),
				})
			}
			sshPublicKeys = keys
		}
	}

	// Rebuild VM
	rebuildReq := &service.RebuildVMRequest{
		VMID:               id,
		TemplateID:         req.TemplateID,
		PreserveIP:         req.PreserveIP,
		Password:           req.Password,
		RegeneratePassword: req.RegeneratePassword,
		SSHPublicKeys:      sshPublicKeys,
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
			VMID:         resp.VMID,
			Status:       resp.Status,
			JobID:        resp.JobID,
			Message:      resp.Message,
			RootPassword: resp.RootPassword,
		},
	})
}

// CloneVM handles POST /api/vms/:id/clone - clone an existing (stopped) VM.
func (h *VMHandler) CloneVM(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}
	if !h.authorizeVM(c, id) {
		return nil
	}
	var req struct {
		Hostname   string `json:"hostname"`
		DestNodeID string `json:"dest_node_id"`
	}
	_ = c.Bind(&req) // both optional (hostname → "<source>-clone", node → source's)

	resp, err := h.service.CloneVM(c.Request().Context(), &service.CloneVMRequest{SourceVMID: id, Hostname: req.Hostname, DestNodeID: req.DestNodeID})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrVMNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "VM not found"})
		case errors.Is(err, service.ErrTemplateNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "OS template not found"})
		default:
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": err.Error()})
		}
	}

	return c.JSON(http.StatusCreated, map[string]interface{}{
		"message": "VM clone initiated",
		"data": map[string]interface{}{
			"vm":     resp.VM,
			"job_id": resp.JobID,
			"status": resp.Status,
		},
	})
}

// ListVMDisks handles GET /api/vms/:id/disks - list extra data disks.
func (h *VMHandler) ListVMDisks(c echo.Context) error {
	if !h.authorizeVM(c, c.Param("id")) {
		return nil
	}
	disks, err := h.service.ListDisks(c.Request().Context(), c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Internal Server Error", "message": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": disks})
}

// AttachVMDisk handles POST /api/vms/:id/disks - provision + attach a data disk.
func (h *VMHandler) AttachVMDisk(c echo.Context) error {
	if !h.authorizeVM(c, c.Param("id")) {
		return nil
	}
	var req struct {
		SizeGB int `json:"size_gb"`
	}
	if err := c.Bind(&req); err != nil || req.SizeGB <= 0 {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "size_gb must be a positive integer"})
	}
	disk, err := h.service.AttachDisk(c.Request().Context(), c.Param("id"), req.SizeGB)
	if err != nil {
		if errors.Is(err, service.ErrVMNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "VM not found"})
		}
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": err.Error()})
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": disk})
}

// DetachVMDisk handles DELETE /api/vms/:id/disks/:device?delete_volume=true.
func (h *VMHandler) DetachVMDisk(c echo.Context) error {
	if !h.authorizeVM(c, c.Param("id")) {
		return nil
	}
	deleteVolume := c.QueryParam("delete_volume") == "true"
	err := h.service.DetachDisk(c.Request().Context(), c.Param("id"), c.Param("device"), deleteVolume)
	if err != nil {
		if errors.Is(err, service.ErrVMNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "VM not found"})
		}
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "Disk detached"})
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

	if !h.authorizeVM(c, id) {
		return nil
	}

	// Get user ID from context (RequireAuth stores the user under the "user" key).
	userID := "system"
	if userCtx, ok := middleware.GetUserContext(c); ok {
		userID = userCtx.ID.String()
	}

	// The caller is the VM owner (or an admin), verified above, so it is safe to
	// include the VNC password in the response.
	includePassword := true

	// Get VNC config (host + port from DB). Bounded so a slow/unreachable agent
	// or QEMU monitor can't leave the browser stuck on "Connecting..." forever.
	ctx, cancel := context.WithTimeout(c.Request().Context(), 10*time.Second)
	defer cancel()
	vncConfig, err := h.service.GetVNCConfig(ctx, id, includePassword)
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

	// Generate VNC proxy token with host and port embedded
	var wsURL string
	if h.vncProxy != nil && vncConfig.Host != "" && vncConfig.Port > 0 {
		token, _, err := h.vncProxy.GenerateVNCToken(
			id, userID, "", // nodeID not needed for direct connection
			vncConfig.Host, vncConfig.Port,
			vnc.TokenExpiry,
		)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": fmt.Sprintf("failed to generate VNC token: %v", err),
			})
		}

		// Build WebSocket URL pointing to panel's proxy endpoint
		// Use the request's scheme and host so it works behind reverse proxy
		scheme := "ws"
		if c.Request().TLS != nil || c.Request().Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "wss"
		}
		host := c.Request().Host
		wsURL = fmt.Sprintf("%s://%s/ws/vnc?token=%s", scheme, host, token)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "VNC configuration retrieved",
		"data": VNCConfigResponse{
			VMID:         vncConfig.VMID,
			Host:         vncConfig.Host,
			Port:         vncConfig.Port,
			Password:     vncConfig.Password,
			WebSocketURL: wsURL,
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

	if !h.authorizeVM(c, id) {
		return nil
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

// EnableConsole handles POST /api/vms/:id/console/enable
func (h *VMHandler) EnableConsole(c echo.Context) error { return h.setConsole(c, true) }

// DisableConsole handles POST /api/vms/:id/console/disable
func (h *VMHandler) DisableConsole(c echo.Context) error { return h.setConsole(c, false) }

// setConsole toggles VNC console access for a VM.
func (h *VMHandler) setConsole(c echo.Context, enabled bool) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	if !h.authorizeVM(c, id) {
		return nil
	}

	vm, err := h.service.SetConsoleEnabled(c.Request().Context(), id, enabled)
	if err != nil {
		if errors.Is(err, service.ErrVMNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "VM not found",
			})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Console state updated",
		"data": map[string]interface{}{
			"console_enabled": vm.ConsoleEnabled,
		},
	})
}

// ============================================================================
// Register Routes
// ============================================================================

// ResetPasswordVM handles POST /api/vms/:id/reset-password - Reset guest root password
func (h *VMHandler) ResetPasswordVM(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "VM ID is required"})
	}
	if !h.authorizeVM(c, id) {
		return nil
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := c.Bind(&req); err != nil || req.Password == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "password is required"})
	}
	resp, err := h.service.ResetPassword(c.Request().Context(), &service.VMResetPasswordRequest{VMID: id, Password: req.Password})
	if err != nil {
		if errors.Is(err, service.ErrVMNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "VM not found"})
		}
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"message": "Password reset enqueued", "data": resp})
}

// AttachISOVM handles POST /api/vms/:id/iso/attach - Attach a bootable ISO
func (h *VMHandler) AttachISOVM(c echo.Context) error {
	id := c.Param("id")
	if !h.authorizeVM(c, id) {
		return nil
	}
	var req struct {
		ISOURL string `json:"iso_url"`
	}
	if err := c.Bind(&req); err != nil || req.ISOURL == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "iso_url is required"})
	}
	jobID, err := h.service.AttachISO(c.Request().Context(), id, req.ISOURL)
	if err != nil {
		if errors.Is(err, service.ErrVMNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "VM not found"})
		}
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"message": "ISO attach enqueued", "data": map[string]interface{}{"job_id": jobID, "status": "pending"}})
}

// DetachISOVM handles POST /api/vms/:id/iso/detach - Detach the install/rescue ISO
func (h *VMHandler) DetachISOVM(c echo.Context) error {
	id := c.Param("id")
	if !h.authorizeVM(c, id) {
		return nil
	}
	jobID, err := h.service.DetachISO(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, service.ErrVMNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "VM not found"})
		}
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"message": "ISO detach enqueued", "data": map[string]interface{}{"job_id": jobID, "status": "pending"}})
}

// RescueVM handles POST /api/vms/:id/rescue - Boot the VM from a rescue ISO.
// Body: optional {iso_url}; falls back to the RESCUE_ISO_URL env var.
func (h *VMHandler) RescueVM(c echo.Context) error {
	id := c.Param("id")
	if !h.authorizeVM(c, id) {
		return nil
	}
	var req struct {
		ISOURL string `json:"iso_url"`
	}
	_ = c.Bind(&req) // body is optional
	jobID, err := h.service.RescueVM(c.Request().Context(), id, req.ISOURL)
	if err != nil {
		if errors.Is(err, service.ErrVMNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "VM not found"})
		}
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Rescue ISO attached — start the VM to boot into rescue",
		"data":    map[string]interface{}{"job_id": jobID, "status": "pending"},
	})
}

// UnrescueVM handles POST /api/vms/:id/unrescue - Detach the rescue ISO.
func (h *VMHandler) UnrescueVM(c echo.Context) error {
	id := c.Param("id")
	if !h.authorizeVM(c, id) {
		return nil
	}
	jobID, err := h.service.UnrescueVM(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, service.ErrVMNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "VM not found"})
		}
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Rescue ISO detached — start the VM to boot from disk",
		"data":    map[string]interface{}{"job_id": jobID, "status": "pending"},
	})
}

// MigrateVM handles POST /api/vms/:id/migrate - Live-migrate a VM to another node.
// Body: {dest_node_id, live?, copy_storage?}. copy_storage defaults to true
// (the nodes do not share storage).
func (h *VMHandler) MigrateVM(c echo.Context) error {
	id := c.Param("id")
	if !h.authorizeVM(c, id) {
		return nil
	}
	var req struct {
		DestNodeID  string `json:"dest_node_id"`
		Live        *bool  `json:"live"`
		CopyStorage *bool  `json:"copy_storage"`
	}
	if err := c.Bind(&req); err != nil || req.DestNodeID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "dest_node_id is required"})
	}
	live := true
	if req.Live != nil {
		live = *req.Live
	}
	copyStorage := true
	if req.CopyStorage != nil {
		copyStorage = *req.CopyStorage
	}

	err := h.service.MigrateVM(c.Request().Context(), &service.MigrateVMRequest{
		VMID:        id,
		DestNodeID:  req.DestNodeID,
		Live:        live,
		CopyStorage: copyStorage,
	})
	if err != nil {
		switch {
		case errors.Is(err, service.ErrVMNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "VM not found"})
		case errors.Is(err, service.ErrNodeNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "Destination node not found"})
		default:
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Migration Failed", "message": err.Error()})
		}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "VM migrated to destination node"})
}

// RegisterVMRoutes registers all VM routes with the Echo router
func RegisterVMRoutes(e *echo.Echo, handler *VMHandler, db interface{}) {
	// Create VM routes group
	vms := e.Group("/api/v1/vms")

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
		lifecycle.POST("/suspend", handler.SuspendVM)
		lifecycle.POST("/unsuspend", handler.UnsuspendVM)
		lifecycle.POST("/reset-password", handler.ResetPasswordVM)
		lifecycle.POST("/iso/attach", handler.AttachISOVM)
		lifecycle.POST("/iso/detach", handler.DetachISOVM)
		lifecycle.POST("/rescue", handler.RescueVM)
		lifecycle.POST("/unrescue", handler.UnrescueVM)
		lifecycle.POST("/migrate", handler.MigrateVM)
		lifecycle.POST("/clone", handler.CloneVM)
	}

	// Additional data disks - require vm:lifecycle (attach/detach is management)
	disks := vms.Group("/:id/disks")
	disks.Use(middleware.RequirePermission("vm:lifecycle"))
	{
		disks.GET("", handler.ListVMDisks)
		disks.POST("", handler.AttachVMDisk)
		disks.DELETE("/:device", handler.DetachVMDisk)
	}

	// VNC operations - require vm:console
	vnc := vms.Group("/:id/vnc")
	vnc.Use(middleware.RequirePermission("vm:console"))
	{
		vnc.GET("", handler.GetVNCConfig)
		vnc.POST("/refresh", handler.RefreshVNCPassword)
	}

	// VM metrics - require vm:read
	vms.GET("/:id/metrics", handler.GetVMMetrics)

	// Console token operations - require vm:console
	consoleToken := vms.Group("/:id/console")
	consoleToken.Use(middleware.RequirePermission("vm:console"))
	{
		consoleToken.POST("/token", handler.GenerateConsoleToken)
		consoleToken.DELETE("/token", handler.RevokeConsoleToken)
		consoleToken.POST("/enable", handler.EnableConsole)
		consoleToken.POST("/disable", handler.DisableConsole)
	}
}

func (h *VMHandler) GenerateConsoleToken(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	if h.vncService == nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": "VNC service not configured",
		})
	}

	userCtx, ok := c.Get("user").(*middleware.UserContext)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error":   "Unauthorized",
			"message": "User context not found",
		})
	}

	resp, err := h.vncService.GenerateConsoleToken(c.Request().Context(), id, userCtx.ID.String())
	if err != nil {
		switch {
		case errors.Is(err, service.ErrConsoleTokenVMNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "VM not found",
			})
		case errors.Is(err, service.ErrConsoleTokenUnauthorized):
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error":   "Forbidden",
				"message": "User not authorized to access this VM",
			})
		case errors.Is(err, service.ErrConsoleDisabled):
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error":   "Forbidden",
				"message": "Console is disabled for this VM",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Console token generated",
		"data": map[string]interface{}{
			"token":      resp.Token,
			"expires_at": resp.ExpiresAt.Format("2006-01-02T15:04:05Z"),
			"ws_url":     resp.WSURL,
		},
	})
}

func (h *VMHandler) RevokeConsoleToken(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	var req struct {
		JTI string `json:"jti"`
	}
	if err := c.Bind(&req); err != nil || req.JTI == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Token JTI is required",
		})
	}

	if h.vncService == nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": "VNC service not configured",
		})
	}

	if err := h.vncService.RevokeConsoleToken(c.Request().Context(), req.JTI); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Console token revoked",
	})
}

// GetVMMetrics handles GET /api/v1/vms/:id/metrics - Get VM resource metrics
func (h *VMHandler) GetVMMetrics(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	if !h.authorizeVM(c, id) {
		return nil
	}

	// Get VM to find its node
	vm, err := h.service.GetVM(c.Request().Context(), id, false)
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

	// Try to get live metrics from agent via streaming (1 sample)
	ctx, cancel := context.WithTimeout(c.Request().Context(), 5*time.Second)
	defer cancel()

	metrics, err := h.service.GetVMMetrics(ctx, vm.VM.NodeID, id)
	if err == nil {
		return c.JSON(http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{
				"cpu_percent":              metrics.CpuPercent,
				"memory_used":              metrics.MemoryUsed,
				"memory_total":             metrics.MemoryTotal,
				"memory_used_percent":      metrics.MemoryUsedPercent,
				"disk_read_bytes_per_sec":  metrics.DiskReadBytesPerSec,
				"disk_write_bytes_per_sec": metrics.DiskWriteBytesPerSec,
				"network_rx_bytes_per_sec": metrics.NetworkRxBytesPerSec,
				"network_tx_bytes_per_sec": metrics.NetworkTxBytesPerSec,
			},
		})
	}

	// Fallback: return allocated resources as static metrics
	ramMB := vm.VM.Resources.RAM
	return c.JSON(http.StatusOK, map[string]interface{}{
		"data": map[string]interface{}{
			"cpu_percent":              float64(0),
			"memory_used":              int64(0),
			"memory_total":             int64(ramMB) * 1024 * 1024,
			"memory_used_percent":      float64(0),
			"disk_read_bytes_per_sec":  int64(0),
			"disk_write_bytes_per_sec": int64(0),
			"network_rx_bytes_per_sec": int64(0),
			"network_tx_bytes_per_sec": int64(0),
		},
	})
}
