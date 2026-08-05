// Package handler provides HTTP handlers for VM management
package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/authz"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/panel/vnc"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// VMHandler handles HTTP requests for VM management
type VMHandler struct {
	service       *service.VMService
	vncService    *service.VNCService
	vncProxy      *vnc.ProxyServer
	sshKeyService *service.SSHKeyService
	recipeService *service.RecipeService
	authz         *authz.Authorizer
	// audit records VM lifecycle actions to audit_logs. Set by RegisterVMRoutes
	// (which has the db handle). May be nil in tests/constructors that don't wire
	// it; logVMActivity is a no-op in that case.
	audit *repository.AuditRepository
	// imageService resolves a create-from-image source. Set by SetImageService
	// from the server wiring; nil in tests, in which case source_image_id is
	// ignored.
	imageService *service.ImageService
}

// SetImageService injects the image service used to resolve a create-from-image
// source. Called from server wiring after construction.
func (h *VMHandler) SetImageService(s *service.ImageService) { h.imageService = s }

// NewVMHandler creates a new VMHandler instance
func NewVMHandler(service *service.VMService, vncService *service.VNCService, vncProxy *vnc.ProxyServer, sshKeyService *service.SSHKeyService, recipeService *service.RecipeService, authorizer *authz.Authorizer) *VMHandler {
	return &VMHandler{
		service:       service,
		vncService:    vncService,
		vncProxy:      vncProxy,
		sshKeyService: sshKeyService,
		recipeService: recipeService,
		authz:         authorizer,
	}
}

// NewVMHandlerWithoutVNC creates a new VMHandler instance without VNC service (for backward compatibility)
func NewVMHandlerWithoutVNC(service *service.VMService, authorizer *authz.Authorizer) *VMHandler {
	return &VMHandler{
		service:    service,
		vncService: nil,
		vncProxy:   nil,
		authz:      authorizer,
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

// clientNetworkSelectionDenied writes a generic, non-topology-leaking 4xx
// response when a RoleClient attempts a prohibited infrastructure/network
// selection (node, IP pool, requested IP, managed network, CPU model, nonzero
// bandwidth/VLAN, custom NIC, VLAN/anti-spoof changes, or a destination node for
// clone/migration). It deliberately does NOT reveal which field or which node/
// pool/VLAN exists so a client cannot probe topology.
func clientNetworkSelectionDenied(c echo.Context) error {
	return c.JSON(http.StatusForbidden, map[string]interface{}{
		"error":   "Forbidden",
		"message": "clients may not select VM network or infrastructure placement; this is assigned automatically",
	})
}

// isClientRole reports whether the caller is an authenticated non-admin (client).
// Unauthenticated callers return false so the dedicated 401 path elsewhere owns
// that case.
func isClientRole(c echo.Context) bool {
	u, ok := middleware.GetUserContext(c)
	return ok && u.Role != models.RoleAdmin
}

// applyClientVMPolicy enforces the Gate-1 client networking policy on VM
// creation. For a RoleClient it rejects any explicit infrastructure/network
// selection (node, IP pool, requested IP, managed network, CPU model, nonzero
// bandwidth, nonzero VLAN) and otherwise forces AutoAssignIP so the VM lands on a
// reachable public address. Admins pass through unchanged. It returns true when
// it has committed a response (the caller must `return nil`); false to continue.
func (h *VMHandler) applyClientVMPolicy(c echo.Context, userCtx *middleware.UserContext, req *CreateVMRequest, createReq *service.CreateVMRequest) bool {
	// Admins retain full control; nothing to enforce.
	if userCtx.Role == models.RoleAdmin {
		return false
	}
	// Reject an explicit client-supplied infrastructure/network choice. Default
	// (empty/zero) values are allowed; only a concrete, nonzero choice blocks.
	if req.NodeID != "" || req.IPPoolID != "" || req.RequestedIP != "" ||
		req.ManagedNetworkID != "" || req.CPUModel != "" ||
		req.BandwidthMbps != 0 || req.VLANID != 0 {
		_ = clientNetworkSelectionDenied(c)
		return true
	}
	// Permitted auto-only request: clients cannot pick a pool or network, so
	// force automatic public IP assignment.
	createReq.AutoAssignIP = true
	return false
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
	// VPCID places the VM in one of the caller's own VPCs. Ownership is verified
	// in the service, so another tenant's id simply fails to resolve.
	VPCID string `json:"vpc_id,omitempty" validate:"omitempty,uuid"`
	// Region is the location the customer chose (id or slug).
	Region   string `json:"region,omitempty"`
	RecipeID string `json:"recipe_id,omitempty" validate:"omitempty,uuid"`
	// Password sets the new guest's root password. RegeneratePassword (with an
	// empty Password) asks the server to generate one and return it once.
	Password           string `json:"password,omitempty" validate:"omitempty,min=8,max=128"`
	RegeneratePassword bool   `json:"regenerate_password,omitempty"`
	// SSHKeyIDs are the user's saved SSH keys to inject into the new guest.
	SSHKeyIDs []string `json:"ssh_key_ids,omitempty"`
	// SSHPublicKeys are raw authorized_keys lines pasted at create time (in
	// addition to any saved SSHKeyIDs) — lets an admin add a key without saving it.
	SSHPublicKeys []string `json:"ssh_public_keys,omitempty"`
	// SourceImageID, when set, seeds the new VM's disk from a stored image
	// (Vultr/DO-style create-from-snapshot) instead of the OS template. The OS
	// template is derived from the image, so os_template_id is not required then.
	SourceImageID string `json:"source_image_id,omitempty" validate:"omitempty,uuid"`
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

	// Validate required fields. Either an OS template or a source image is
	// required — a create-from-image derives the template from the image.
	if req.Hostname == "" || (req.OSTemplateID == "" && req.SourceImageID == "") {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Hostname and either an OS template or a source image are required",
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
	// Append raw keys pasted at create time (trimmed, non-empty).
	for _, raw := range req.SSHPublicKeys {
		if k := strings.TrimSpace(raw); k != "" {
			sshPublicKeys = append(sshPublicKeys, k)
		}
	}

	// Create VM
	createReq := &service.CreateVMRequest{
		UserID:           userID,
		Hostname:         req.Hostname,
		OSTemplateID:     req.OSTemplateID,
		Resources:        req.Resources,
		NodeID:           req.NodeID,
		PlanID:           req.PlanID,
		IPPoolID:         req.IPPoolID,
		RequestedIP:      req.RequestedIP,
		BandwidthMbps:    req.BandwidthMbps,
		VLANID:           req.VLANID,
		CPUModel:         req.CPUModel,
		UserData:         userData,
		ManagedNetworkID: req.ManagedNetworkID,
		VPCID:            req.VPCID,
		Region:           req.Region,
		// A customer must choose their location; placing them silently is not the
		// platform's call. Admin and integration callers may omit it and fall back
		// to the configured default, which is what keeps WHMCS provisioning working.
		RegionRequired:     userCtx.Role != models.RoleAdmin,
		Password:           req.Password,
		RegeneratePassword: req.RegeneratePassword,
		SSHPublicKeys:      sshPublicKeys,
	}

	// Production-grade default: a VM with neither a password nor an SSH key can't
	// be logged into. If the caller supplied neither, generate a root password and
	// return it once (what commercial VM panels do) so the VM is always usable.
	if req.Password == "" && len(sshPublicKeys) == 0 {
		createReq.RegeneratePassword = true
	}

	// Create-from-image: resolve the stored image to a disk source (s3://<key>)
	// and derive the OS template from it. Ownership + readiness are enforced in
	// the service. This makes the new VM a full copy of the captured image.
	if req.SourceImageID != "" && h.imageService != nil {
		sourceRef, tmplID, ierr := h.imageService.ResolveSource(
			c.Request().Context(), req.SourceImageID, userCtx.ID, userCtx.Role == models.RoleAdmin)
		if ierr != nil {
			status, msg := http.StatusBadRequest, ierr.Error()
			if errors.Is(ierr, service.ErrImageNotFound) {
				status, msg = http.StatusNotFound, "Source image not found"
			} else if errors.Is(ierr, service.ErrImageNotReady) {
				msg = "Source image is not available yet"
			}
			return c.JSON(status, map[string]interface{}{"error": "Bad Request", "message": msg})
		}
		createReq.CloneSourceRef = sourceRef
		createReq.OSTemplateID = tmplID
	}

	// Gate-1 client networking policy: enforce AutoAssign/IP restriction for
	// clients and reject any explicit infrastructure/network selection before the
	// create reaches the service layer. Admins are unaffected.
	if h.applyClientVMPolicy(c, userCtx, &req, createReq) {
		return nil
	}

	resp, err := h.service.CreateVM(c.Request().Context(), createReq)
	if err != nil {
		switch {
		// A VPC the caller does not own must read as "not found", never as a
		// server fault, and must not confirm the id exists.
		case errors.Is(err, service.ErrRegionRequired), errors.Is(err, service.ErrRegionNoCapacity):
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error": "Bad Request", "message": err.Error(),
			})
		case errors.Is(err, service.ErrRegionNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error": "Not Found", "message": "region not found",
			})
		case errors.Is(err, service.ErrVPCWrongRegion):
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error": "Bad Request", "message": err.Error(),
			})
		case errors.Is(err, service.ErrVPCNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "VPC not found",
			})
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
		case errors.Is(err, service.ErrIPInUseOnNetwork):
			return c.JSON(http.StatusConflict, map[string]interface{}{
				"error":   "Conflict",
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

	h.logVMActivity(c, resp.VM.ID, "vm.create", map[string]any{
		"hostname": resp.VM.Hostname,
	})

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
	// Where the VM physically runs. Customers reason in regions, never in nodes,
	// so this is what pairs a VM with the private networks and floating IPs it
	// can actually use.
	RegionID      string `json:"region_id,omitempty"`
	RegionName    string `json:"region_name,omitempty"`
	RegionCountry string `json:"region_country,omitempty"`
	VNCPort       int    `json:"vnc_port,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
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
			RegionID:      vm.RegionID,
			RegionName:    vm.RegionName,
			RegionCountry: vm.RegionCountry,
			ID:            vm.ID,
			Hostname:      vm.Hostname,
			Status:        string(vm.Status),
			NodeID:        vm.NodeID,
			NodeName:      node.Name,
			NodeStatus:    string(node.Status),
			UserID:        vm.UserID,
			OSTemplateID:  vm.OSTemplateID,
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
	UserID    string            `json:"user_id,omitempty"`
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
	if req.UserID != "" {
		// Owner reassignment is admin-only.
		userCtx, ok := c.Get("user").(*middleware.UserContext)
		if !ok || userCtx.Role != models.RoleAdmin {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error": "Forbidden", "message": "Only admins can reassign VM ownership",
			})
		}
		updateReq.UserID = req.UserID
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
		case errors.Is(err, service.ErrTargetUserNotFound):
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error":   "Bad Request",
				"message": "Target user not found",
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
// GetVMOperation returns the latest tracked multi-step operation for a VM (e.g.
// a delete: destroy on host → release IP/network → remove records) so the UI can
// show progress and whether it actually succeeded. Returns {data: null} when the
// VM has no tracked operation.
func (h *VMHandler) GetVMOperation(c echo.Context) error {
	id := c.Param("id")
	if !h.authorizeVM(c, id) {
		return nil
	}
	op, err := h.service.GetLatestVMOperation(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": err.Error(),
		})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"data": op})
}

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

	// Capture the hostname before the VM is gone so the audit entry is readable.
	deletedHostname := h.vmHostname(c, id)

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

	h.logVMActivity(c, id, "vm.delete", map[string]any{"hostname": deletedHostname})

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

	h.logVMActivity(c, id, "vm."+string(command), map[string]any{
		"new_state": resp.NewState,
		"hostname":  h.vmHostname(c, id),
	})

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

	h.logVMActivity(c, id, "vm.rebuild", map[string]any{"hostname": h.vmHostname(c, id)})

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

	// Gate-1: clients may not choose a destination node for a clone; the VM is
	// cloned onto the source's node (or auto-placement). Reject before the
	// service runs. Admins pass through unchanged.
	if isClientRole(c) && req.DestNodeID != "" {
		return clientNetworkSelectionDenied(c)
	}

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
		// Disk admission rejection is a client-side quota condition, not a server
		// fault: map it to 400 without leaking policy/cap detail or a generic 500.
		if errors.Is(err, service.ErrDiskQuotaExceeded) || errors.Is(err, service.ErrQuotaExceeded) {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Disk quota exceeded"})
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

// VNCConfigResponse represents VNC connection details.
//
// The WebSocket endpoint is returned as a relative, token-bearing path
// (ws_path) that stays same-origin. The browser derives the absolute
// ws:// or wss:// URL from window.location so the internal panel host
// (e.g. panel:8080) is never exposed to the client bundle. No absolute
// panel WebSocket URL is emitted here.
type VNCConfigResponse struct {
	VMID     string `json:"vm_id"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Password string `json:"password,omitempty"`
	WSPath   string `json:"ws_path,omitempty"`
}

// vncWSPathPrefix is the same-origin relative path the browser turns into an
// absolute ws:// or wss:// URL. The token query value is always escaped.
const vncWSPathPrefix = "/ws/vnc"

// buildVNCWSPath returns a relative, token-bearing WebSocket path. It never
// includes a scheme or host, so the internal panel host cannot leak to the
// client. The token is URL-escaped to keep the query string well-formed.
func buildVNCWSPath(token string) string {
	return vncWSPathPrefix + "?token=" + url.QueryEscape(token)
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
	var wsPath string
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

		// Return a relative, token-bearing path so the browser builds the
		// absolute ws:// or wss:// URL from its own origin. This keeps the
		// WebSocket same-origin (routed via the Next /ws rewrite) and prevents
		// leaking the internal panel host (e.g. panel:8080) to the client.
		// The token is URL-escaped so it cannot break the query string.
		wsPath = buildVNCWSPath(token)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "VNC configuration retrieved",
		"data": VNCConfigResponse{
			VMID:     vncConfig.VMID,
			Host:     vncConfig.Host,
			Port:     vncConfig.Port,
			Password: vncConfig.Password,
			WSPath:   wsPath,
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

// RepairConsole handles POST /api/vms/:id/console/repair. It injects a VNC
// graphics device into a domain that lacks one (imported VMs). This
// RESTARTS the VM if it is running, so it is gated behind an explicit confirm.
func (h *VMHandler) RepairConsole(c echo.Context) error {
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

	// Restarting a VM is disruptive — require an explicit confirm flag.
	var body struct {
		Confirm bool `json:"confirm"`
	}
	_ = c.Bind(&body)
	if !body.Confirm && c.QueryParam("confirm") != "true" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Confirmation Required",
			"message": "Repairing the console restarts the VM. Retry with {\"confirm\": true}.",
		})
	}

	vm, err := h.service.RepairConsole(c.Request().Context(), id)
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

	port := 0
	if vm.VNCPort != nil {
		port = *vm.VNCPort
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Console repaired (VNC enabled); the VM was restarted if it was running",
		"data": map[string]interface{}{
			"console_enabled": vm.ConsoleEnabled,
			"vnc_port":        port,
		},
	})
}

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

	// Gate-1: migration is an admin-only operation. A client cannot choose a
	// destination node (or initiate a migration at all); reject before the
	// service runs.
	if isClientRole(c) {
		return clientNetworkSelectionDenied(c)
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
func RegisterVMRoutes(e *echo.Echo, handler *VMHandler, db *gorm.DB) {
	// Wire the audit repo so lifecycle actions are recorded to audit_logs and the
	// activity endpoint can read them back.
	if handler.audit == nil {
		handler.audit = repository.NewAuditRepository(db)
	}

	// Create VM routes group
	vms := e.Group("/api/v1/vms")

	// Apply authentication middleware (DB-backed so user existence/state/IP
	// whitelist are validated, not just JWT signature)
	vms.Use(middleware.RequireAuth(db))

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
	vms.GET("/:id/operation", handler.GetVMOperation)

	// VM activity log (audit_logs for this VM) - require vm:read
	vms.GET("/:id/activity", handler.GetVMActivity)

	// Console token operations - require vm:console
	consoleToken := vms.Group("/:id/console")
	consoleToken.Use(middleware.RequirePermission("vm:console"))
	{
		consoleToken.POST("/token", handler.GenerateConsoleToken)
		consoleToken.DELETE("/token", handler.RevokeConsoleToken)
		consoleToken.POST("/enable", handler.EnableConsole)
		consoleToken.POST("/disable", handler.DisableConsole)
		consoleToken.POST("/repair", handler.RepairConsole)
	}
}

// GenerateConsoleToken handles POST /api/v1/vms/:id/console/token.
//
// This is the legacy console-token endpoint. It emitted an absolute WebSocket
// URL built from the internal bind address and used an incompatible VNC token
// claim. It is explicitly contained (disabled) until a later phase unifies the
// proxy path/token contracts with the primary GetVNCConfig endpoint. The route
// shape is preserved, but every request returns a stable 503 without exposing
// any internal endpoint, host, or token detail.
func (h *VMHandler) GenerateConsoleToken(c echo.Context) error {
	return c.JSON(http.StatusServiceUnavailable, map[string]interface{}{
		"error":   "Service Unavailable",
		"code":    "vnc_console_legacy_unavailable",
		"message": "The legacy VNC console endpoint is unavailable; use the primary VNC console endpoint.",
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

	// Authorize the route VM (owner-or-admin). Missing identity → 401; a
	// non-owner or nonexistent VM → 404 (anti-enumeration). This prevents a
	// caller from revoking a token on a VM they do not control.
	if !h.authz.AuthorizeVM(c, id) {
		return nil
	}

	if h.vncService == nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": "VNC service not configured",
		})
	}

	// Revoke is scoped to the route VM ID AND JTI, so a token belonging to
	// another tenant's VM cannot be revoked via a caller-supplied JTI. No token
	// details are exposed on success or failure.
	if err := h.vncService.RevokeConsoleTokenForVM(c.Request().Context(), id, req.JTI); err != nil {
		// Both "JTI not found" and "JTI belongs to a different VM" map to 404.
		return c.JSON(http.StatusNotFound, map[string]interface{}{
			"error":   "Not Found",
			"message": "Console token not found",
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
