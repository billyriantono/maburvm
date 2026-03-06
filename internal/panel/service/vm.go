// Package service provides business logic for VM operations
package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"

	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
)

var (
	// ErrVMNotFound is returned when a VM is not found
	ErrVMNotFound = errors.New("VM not found")
	// ErrNoAvailableNodes is returned when no active nodes are available
	ErrNoAvailableNodes = errors.New("no available nodes for VM placement")
	// ErrInvalidResources is returned when resource requirements are invalid
	ErrInvalidResources = errors.New("invalid resource configuration")
	// ErrHostnameExists is returned when a hostname is already in use
	ErrHostnameExists = errors.New("hostname already exists")
	// ErrVMLifecycleFailed is returned when a lifecycle operation fails
	ErrVMLifecycleFailed = errors.New("VM lifecycle operation failed")
	// ErrVMCannotBeModified is returned when trying to modify a VM that's not stopped
	ErrVMCannotBeModified = errors.New("VM cannot be modified while running")
)

// NodeSelectionStrategy determines how nodes are selected for VM placement
type NodeSelectionStrategy string

const (
	// NodeSelectionRoundRobin assigns VMs to nodes in round-robin fashion
	NodeSelectionRoundRobin NodeSelectionStrategy = "round_robin"
	// NodeSelectionResourceBased assigns VMs to nodes with most available resources
	NodeSelectionResourceBased NodeSelectionStrategy = "resource_based"
)

// VNCConfig holds VNC connection details
type VNCConfig struct {
	Port     int    `json:"port"`
	Password string `json:"password"`
}

// VMService handles VM-related business operations
type VMService struct {
	db           *gorm.DB
	vmRepo       *repository.VMRepository
	nodeRepo     *repository.NodeRepository
	templateRepo *repository.TemplateRepository
	riverClient  *river.Client[pgx.Tx]
	logger       *slog.Logger

	// Node selection state
	nodeSelectionStrategy NodeSelectionStrategy
	lastNodeIndex         int
	nodeMutex             sync.Mutex

	// gRPC connections cache
	grpcConns map[string]*grpc.ClientConn
	connMutex sync.RWMutex
}

// NewVMService creates a new VMService instance
func NewVMService(
	db *gorm.DB,
	vmRepo *repository.VMRepository,
	nodeRepo *repository.NodeRepository,
	templateRepo *repository.TemplateRepository,
	riverClient *river.Client[pgx.Tx],
	logger *slog.Logger,
) *VMService {
	return &VMService{
		db:                    db,
		vmRepo:                vmRepo,
		nodeRepo:              nodeRepo,
		templateRepo:          templateRepo,
		riverClient:           riverClient,
		logger:                logger,
		nodeSelectionStrategy: NodeSelectionRoundRobin,
		lastNodeIndex:         -1,
		grpcConns:             make(map[string]*grpc.ClientConn),
	}
}

// SetNodeSelectionStrategy sets the node selection strategy
func (s *VMService) SetNodeSelectionStrategy(strategy NodeSelectionStrategy) {
	s.nodeMutex.Lock()
	defer s.nodeMutex.Unlock()
	s.nodeSelectionStrategy = strategy
}

// ============================================================================
// Create VM
// ============================================================================

// CreateVMRequest contains parameters for creating a new VM
type CreateVMRequest struct {
	UserID       string           `json:"user_id" validate:"required,uuid"`
	Hostname     string           `json:"hostname" validate:"required,max=100"`
	OSTemplateID string           `json:"os_template_id" validate:"required,uuid"`
	Resources    models.Resources `json:"resources" validate:"required"`
	NodeID       string           `json:"node_id,omitempty" validate:"omitempty,uuid"` // Optional: specific node
}

// CreateVMResponse contains the created VM and job information
type CreateVMResponse struct {
	VM     *models.VM `json:"vm"`
	JobID  int64      `json:"job_id"`
	Status string     `json:"status"`
}

// CreateVM creates a new VM and enqueues a creation job
func (s *VMService) CreateVM(ctx context.Context, req *CreateVMRequest) (*CreateVMResponse, error) {
	// Validate resources
	if err := s.validateResources(&req.Resources); err != nil {
		return nil, err
	}

	// Check hostname uniqueness
	exists, err := s.vmRepo.HostnameExists(ctx, req.Hostname)
	if err != nil {
		return nil, fmt.Errorf("failed to check hostname: %w", err)
	}
	if exists {
		return nil, ErrHostnameExists
	}

	// Get OS template
	template, err := s.templateRepo.GetByID(ctx, req.OSTemplateID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTemplateNotFound
		}
		return nil, fmt.Errorf("failed to get template: %w", err)
	}
	if !template.IsActive {
		return nil, fmt.Errorf("OS template is not active")
	}

	// Select node for VM placement
	nodeID := req.NodeID
	if nodeID == "" {
		nodeID, err = s.selectNode(ctx)
		if err != nil {
			return nil, err
		}
	} else {
		// Validate specified node exists and is active
		node, err := s.nodeRepo.GetByID(ctx, nodeID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrNodeNotFound
			}
			return nil, fmt.Errorf("failed to get node: %w", err)
		}
		if node.Status != models.NodeStatusActive {
			return nil, fmt.Errorf("selected node is not active")
		}
	}

	// Generate VNC credentials
	vncConfig := s.generateVNCCredentials()

	// Create VM record
	vm := &models.VM{
		UserID:       req.UserID,
		NodeID:       nodeID,
		Hostname:     req.Hostname,
		OSTemplateID: req.OSTemplateID,
		Resources:    req.Resources,
		Status:       models.VMStatusCreating,
		VNCPort:      &vncConfig.Port,
		VNCPassword:  vncConfig.Password,
	}

	if err := s.vmRepo.Create(ctx, vm); err != nil {
		return nil, fmt.Errorf("failed to create VM record: %w", err)
	}

	// Prepare VM creation params
	params := map[string]interface{}{
		"hostname":         req.Hostname,
		"os_template":      template.Name,
		"template_version": template.Version,
		"image_path":       template.ImagePath,
		"resources":        req.Resources,
		"vnc_port":         vncConfig.Port,
		"vnc_password":     vncConfig.Password,
	}
	paramsJSON, _ := json.Marshal(params)

	// Enqueue VM creation job
	job := queue.VMOperationJob{
		VMID:      vm.ID,
		Operation: queue.VMOpCreate,
		NodeID:    nodeID,
		Params:    paramsJSON,
	}

	result, err := s.riverClient.Insert(ctx, job, nil)
	if err != nil {
		// Rollback VM creation on job enqueue failure
		if delErr := s.vmRepo.Delete(ctx, vm.ID); delErr != nil {
			s.logger.ErrorContext(ctx, "failed to rollback VM creation after job enqueue failure",
				"vm_id", vm.ID, "error", delErr)
		}
		return nil, fmt.Errorf("failed to enqueue VM creation job: %w", err)
	}

	s.logger.InfoContext(ctx, "VM creation job enqueued",
		"vm_id", vm.ID,
		"job_id", result.Job.ID,
		"node_id", nodeID,
	)

	return &CreateVMResponse{
		VM:     vm,
		JobID:  result.Job.ID,
		Status: "pending",
	}, nil
}

// validateResources validates VM resource requirements
func (s *VMService) validateResources(resources *models.Resources) error {
	if resources.CPU < 1 || resources.CPU > 128 {
		return fmt.Errorf("%w: CPU must be between 1 and 128", ErrInvalidResources)
	}
	if resources.RAM < 128 || resources.RAM > 131072 {
		return fmt.Errorf("%w: RAM must be between 128 MB and 131072 MB", ErrInvalidResources)
	}
	if resources.Disk < 1 || resources.Disk > 1048576 {
		return fmt.Errorf("%w: Disk must be between 1 GB and 1048576 GB", ErrInvalidResources)
	}
	if resources.IOPS != nil && (*resources.IOPS < 100 || *resources.IOPS > 100000) {
		return fmt.Errorf("%w: IOPS must be between 100 and 100000", ErrInvalidResources)
	}
	if resources.Swap != nil && (*resources.Swap < 0 || *resources.Swap > 65536) {
		return fmt.Errorf("%w: Swap must be between 0 and 65536 MB", ErrInvalidResources)
	}
	return nil
}

// selectNode selects an available node for VM placement
func (s *VMService) selectNode(ctx context.Context) (string, error) {
	nodes, err := s.nodeRepo.ListActive(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list active nodes: %w", err)
	}
	if len(nodes) == 0 {
		return "", ErrNoAvailableNodes
	}

	s.nodeMutex.Lock()
	defer s.nodeMutex.Unlock()

	switch s.nodeSelectionStrategy {
	case NodeSelectionResourceBased:
		// For resource-based selection, we'd need node metrics
		// For now, fall back to round-robin
		return s.selectNodeRoundRobin(nodes), nil
	default:
		return s.selectNodeRoundRobin(nodes), nil
	}
}

// selectNodeRoundRobin selects a node using round-robin
func (s *VMService) selectNodeRoundRobin(nodes []models.Node) string {
	s.lastNodeIndex = (s.lastNodeIndex + 1) % len(nodes)
	return nodes[s.lastNodeIndex].ID
}

// generateVNCCredentials generates VNC port and password
func (s *VMService) generateVNCCredentials() *VNCConfig {
	// Generate random port between 5900 and 5999
	port := 5900 + int(randInt(100))

	// Generate random 12-character password
	password := s.generateRandomPassword(12)

	return &VNCConfig{
		Port:     port,
		Password: password,
	}
}

// randInt generates a random integer between 0 and max-1 using crypto/rand
func randInt(max int64) int64 {
	n, _ := rand.Int(rand.Reader, big.NewInt(max))
	return n.Int64()
}

// generateRandomPassword generates a cryptographically secure random password
func (s *VMService) generateRandomPassword(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*"
	password := make([]byte, length)
	for i := range password {
		n := randInt(int64(len(charset)))
		password[i] = charset[n]
	}
	return string(password)
}

// ============================================================================
// List VMs
// ============================================================================

// ListVMsRequest contains filtering and pagination parameters
type ListVMsRequest struct {
	UserID string          `json:"user_id,omitempty" validate:"omitempty,uuid"`
	NodeID string          `json:"node_id,omitempty" validate:"omitempty,uuid"`
	Status models.VMStatus `json:"status,omitempty" validate:"omitempty,oneof=running stopped suspended creating error"`
	Limit  int             `json:"limit,omitempty" validate:"omitempty,min=1,max=100"`
	Offset int             `json:"offset,omitempty" validate:"omitempty,min=0"`
}

// ListVMsResponse contains the list of VMs and pagination info
type ListVMsResponse struct {
	VMs     []models.VM `json:"vms"`
	Total   int64       `json:"total"`
	Limit   int         `json:"limit"`
	Offset  int         `json:"offset"`
	HasMore bool        `json:"has_more"`
}

// ListVMs retrieves VMs with filtering and pagination
func (s *VMService) ListVMs(ctx context.Context, req *ListVMsRequest) (*ListVMsResponse, error) {
	// Set default pagination
	limit := req.Limit
	if limit == 0 {
		limit = 20
	}

	var vms []models.VM
	var total int64
	var err error

	// Apply filters
	switch {
	case req.UserID != "":
		vms, err = s.vmRepo.ListByUserID(ctx, req.UserID, limit, req.Offset)
		if err != nil {
			return nil, fmt.Errorf("failed to list VMs by user: %w", err)
		}
		total, err = s.vmRepo.CountByUserID(ctx, req.UserID)
	case req.NodeID != "":
		vms, err = s.vmRepo.ListByNodeID(ctx, req.NodeID, limit, req.Offset)
		if err != nil {
			return nil, fmt.Errorf("failed to list VMs by node: %w", err)
		}
		total, err = s.vmRepo.CountByNodeID(ctx, req.NodeID)
	case req.Status != "":
		vms, err = s.vmRepo.ListByStatus(ctx, req.Status, limit, req.Offset)
		if err != nil {
			return nil, fmt.Errorf("failed to list VMs by status: %w", err)
		}
		total, err = s.vmRepo.CountByStatus(ctx, req.Status)
	default:
		vms, err = s.vmRepo.List(ctx, limit, req.Offset)
		if err != nil {
			return nil, fmt.Errorf("failed to list VMs: %w", err)
		}
		total, err = s.vmRepo.Count(ctx)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to count VMs: %w", err)
	}

	return &ListVMsResponse{
		VMs:     vms,
		Total:   total,
		Limit:   limit,
		Offset:  req.Offset,
		HasMore: int64(req.Offset+limit) < total,
	}, nil
}

// ============================================================================
// Get VM
// ============================================================================

// GetVMResponse contains VM details with status
type GetVMResponse struct {
	VM       *models.VM           `json:"vm"`
	Node     *models.Node         `json:"node,omitempty"`
	Template *models.OSTemplate   `json:"template,omitempty"`
	Status   *pb.VMStatusResponse `json:"agent_status,omitempty"`
	VNC      *VNCConfig           `json:"vnc,omitempty"`
}

// GetVM retrieves a VM by ID with details and status
func (s *VMService) GetVM(ctx context.Context, vmID string, includeAgentStatus bool) (*GetVMResponse, error) {
	vm, err := s.vmRepo.GetByIDWithRelations(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}

	response := &GetVMResponse{
		VM: &vm.VM,
	}

	// Get node info
	node, err := s.nodeRepo.GetByID(ctx, vm.NodeID)
	if err == nil {
		response.Node = node
	}

	// Get template info
	template, err := s.templateRepo.GetByID(ctx, vm.OSTemplateID)
	if err == nil {
		response.Template = template
	}

	// Include VNC config (password only for authorized requests)
	if vm.VNCPort != nil {
		response.VNC = &VNCConfig{
			Port: *vm.VNCPort,
		}
	}

	// Get agent status if requested and VM is not being created
	if includeAgentStatus && vm.Status != models.VMStatusCreating {
		status, err := s.getVMAgentStatus(ctx, vm.ID, vm.NodeID)
		if err != nil {
			s.logger.WarnContext(ctx, "failed to get VM status from agent",
				"vm_id", vm.ID, "error", err)
		} else {
			response.Status = status
			// Sync VM status with agent
			s.syncVMStatus(ctx, &vm.VM, status.State)
		}
	}

	return response, nil
}

// getVMAgentStatus retrieves VM status from the agent via gRPC
func (s *VMService) getVMAgentStatus(ctx context.Context, vmID, nodeID string) (*pb.VMStatusResponse, error) {
	client, err := s.getAgentClient(ctx, nodeID)
	if err != nil {
		return nil, err
	}

	req := &pb.VMStatusRequest{
		VmId:           vmID,
		IncludeMetrics: true,
	}

	return client.GetVMStatus(ctx, req)
}

// syncVMStatus updates the VM status in the database based on agent state
func (s *VMService) syncVMStatus(ctx context.Context, vm *models.VM, agentState pb.VMState) {
	var newStatus models.VMStatus

	switch agentState {
	case pb.VMState_VM_STATE_RUNNING:
		newStatus = models.VMStatusRunning
	case pb.VMState_VM_STATE_STOPPED, pb.VMState_VM_STATE_PENDING:
		newStatus = models.VMStatusStopped
	case pb.VMState_VM_STATE_PAUSED:
		newStatus = models.VMStatusSuspended
	case pb.VMState_VM_STATE_ERROR:
		newStatus = models.VMStatusError
	default:
		return // Don't update for unknown states
	}

	if vm.Status != newStatus {
		s.logger.InfoContext(ctx, "syncing VM status",
			"vm_id", vm.ID,
			"old_status", vm.Status,
			"new_status", newStatus,
		)
		if err := s.vmRepo.UpdateStatus(ctx, vm.ID, newStatus); err != nil {
			s.logger.ErrorContext(ctx, "failed to sync VM status",
				"vm_id", vm.ID, "error", err)
		} else {
			vm.Status = newStatus
		}
	}
}

// ============================================================================
// Update VM
// ============================================================================

// UpdateVMRequest contains parameters for updating a VM
type UpdateVMRequest struct {
	VMID      string            `json:"vm_id" validate:"required,uuid"`
	Hostname  string            `json:"hostname,omitempty" validate:"omitempty,max=100"`
	Resources *models.Resources `json:"resources,omitempty" validate:"omitempty"`
}

// UpdateVM updates VM hostname and/or resources
func (s *VMService) UpdateVM(ctx context.Context, req *UpdateVMRequest) (*models.VM, error) {
	vm, err := s.vmRepo.GetByID(ctx, req.VMID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}

	// VM must be stopped to modify resources
	if req.Resources != nil && vm.Status != models.VMStatusStopped {
		return nil, ErrVMCannotBeModified
	}

	// Update hostname if provided
	if req.Hostname != "" && req.Hostname != vm.Hostname {
		exists, err := s.vmRepo.HostnameExists(ctx, req.Hostname)
		if err != nil {
			return nil, fmt.Errorf("failed to check hostname: %w", err)
		}
		if exists {
			return nil, ErrHostnameExists
		}
		vm.Hostname = req.Hostname
	}

	// Update resources if provided
	if req.Resources != nil {
		if err := s.validateResources(req.Resources); err != nil {
			return nil, err
		}
		vm.Resources = *req.Resources

		// Enqueue resize job
		params := map[string]interface{}{
			"resources": *req.Resources,
		}
		paramsJSON, _ := json.Marshal(params)

		job := queue.VMOperationJob{
			VMID:      vm.ID,
			Operation: queue.VMOpResize,
			NodeID:    vm.NodeID,
			Params:    paramsJSON,
		}

		_, err := s.riverClient.Insert(ctx, job, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to enqueue VM resize job: %w", err)
		}
	}

	// Save changes
	if err := s.vmRepo.Update(ctx, vm); err != nil {
		return nil, fmt.Errorf("failed to update VM: %w", err)
	}

	return vm, nil
}

// ============================================================================
// Delete VM
// ============================================================================

// DeleteVM deletes a VM and enqueues a deletion job
func (s *VMService) DeleteVM(ctx context.Context, vmID string) error {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrVMNotFound
		}
		return fmt.Errorf("failed to get VM: %w", err)
	}

	// Enqueue delete job
	job := queue.VMOperationJob{
		VMID:      vm.ID,
		Operation: queue.VMOpDelete,
		NodeID:    vm.NodeID,
	}

	_, err = s.riverClient.Insert(ctx, job, nil)
	if err != nil {
		return fmt.Errorf("failed to enqueue VM delete job: %w", err)
	}

	s.logger.InfoContext(ctx, "VM deletion job enqueued",
		"vm_id", vm.ID,
		"node_id", vm.NodeID,
	)

	return nil
}

// ============================================================================
// VM Lifecycle Operations
// ============================================================================

// LifecycleCommand represents a VM lifecycle command
type LifecycleCommand string

const (
	// LifecycleStart starts the VM
	LifecycleStart LifecycleCommand = "start"
	// LifecycleStop gracefully stops the VM
	LifecycleStop LifecycleCommand = "stop"
	// LifecycleForceStop force stops the VM
	LifecycleForceStop LifecycleCommand = "force_stop"
	// LifecycleRestart restarts the VM
	LifecycleRestart LifecycleCommand = "restart"
	// LifecycleRebuild rebuilds the VM (reinstall OS)
	LifecycleRebuild LifecycleCommand = "rebuild"
)

// LifecycleRequest contains parameters for lifecycle operations
type LifecycleRequest struct {
	VMID       string           `json:"vm_id" validate:"required,uuid"`
	Command    LifecycleCommand `json:"command" validate:"required"`
	Async      bool             `json:"async"`
	TemplateID string           `json:"template_id,omitempty" validate:"omitempty,uuid"` // For rebuild
}

// LifecycleResponse contains the result of a lifecycle operation
type LifecycleResponse struct {
	VMID     string `json:"vm_id"`
	Command  string `json:"command"`
	Success  bool   `json:"success"`
	Message  string `json:"message,omitempty"`
	NewState string `json:"new_state,omitempty"`
	JobID    int64  `json:"job_id,omitempty"`
}

// ExecuteLifecycleCommand executes a VM lifecycle command
func (s *VMService) ExecuteLifecycleCommand(ctx context.Context, req *LifecycleRequest) (*LifecycleResponse, error) {
	vm, err := s.vmRepo.GetByID(ctx, req.VMID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}

	// Map lifecycle command to VM operation
	var operation queue.VMOperationType
	var vmCommand pb.VMCommandType

	switch req.Command {
	case LifecycleStart:
		operation = queue.VMOpStart
		vmCommand = pb.VMCommandType_VM_COMMAND_TYPE_START
	case LifecycleStop:
		operation = queue.VMOpStop
		vmCommand = pb.VMCommandType_VM_COMMAND_TYPE_STOP
	case LifecycleForceStop:
		operation = queue.VMOpStop
		vmCommand = pb.VMCommandType_VM_COMMAND_TYPE_FORCE_STOP
	case LifecycleRestart:
		operation = queue.VMOpRestart
		vmCommand = pb.VMCommandType_VM_COMMAND_TYPE_RESTART
	case LifecycleRebuild:
		operation = queue.VMOpRebuild
		vmCommand = pb.VMCommandType_VM_COMMAND_TYPE_CREATE
	default:
		return nil, fmt.Errorf("invalid lifecycle command: %s", req.Command)
	}

	// For rebuild, validate template
	if req.Command == LifecycleRebuild {
		if req.TemplateID != "" {
			template, err := s.templateRepo.GetByID(ctx, req.TemplateID)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, ErrTemplateNotFound
				}
				return nil, fmt.Errorf("failed to get template: %w", err)
			}
			if !template.IsActive {
				return nil, fmt.Errorf("OS template is not active")
			}
		}
		// VM must be stopped for rebuild
		if vm.Status == models.VMStatusRunning {
			return nil, fmt.Errorf("VM must be stopped before rebuilding")
		}
	}

	// For synchronous execution, call agent directly
	if !req.Async {
		return s.executeSyncLifecycle(ctx, vm, vmCommand, req)
	}

	// For async execution, enqueue job
	params := map[string]interface{}{}
	if req.TemplateID != "" {
		params["template_id"] = req.TemplateID
	}
	if req.Command == LifecycleForceStop {
		params["force"] = true
	}
	paramsJSON, _ := json.Marshal(params)

	job := queue.VMOperationJob{
		VMID:      vm.ID,
		Operation: operation,
		NodeID:    vm.NodeID,
		Params:    paramsJSON,
	}

	result, err := s.riverClient.Insert(ctx, job, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to enqueue lifecycle job: %w", err)
	}

	s.logger.InfoContext(ctx, "VM lifecycle job enqueued",
		"vm_id", vm.ID,
		"command", req.Command,
		"job_id", result.Job.ID,
	)

	return &LifecycleResponse{
		VMID:    vm.ID,
		Command: string(req.Command),
		Success: true,
		Message: "Job enqueued",
		JobID:   result.Job.ID,
	}, nil
}

// executeSyncLifecycle executes a lifecycle command synchronously via gRPC
func (s *VMService) executeSyncLifecycle(
	ctx context.Context,
	vm *models.VM,
	command pb.VMCommandType,
	req *LifecycleRequest,
) (*LifecycleResponse, error) {
	client, err := s.getAgentClient(ctx, vm.NodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to agent: %w", err)
	}

	// Build VM config for rebuild operations
	var config *pb.VMConfig
	if req.Command == LifecycleRebuild {
		config, err = s.buildVMConfig(ctx, vm, req.TemplateID)
		if err != nil {
			return nil, err
		}
	}

	grpcReq := &pb.VMCommandRequest{
		VmId:    vm.ID,
		Command: command,
		Config:  config,
		Async:   false,
	}

	// Set timeout based on command
	timeout := 30 * time.Second
	if command == pb.VMCommandType_VM_COMMAND_TYPE_CREATE {
		timeout = 5 * time.Minute // Rebuild may take longer
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, err := client.ExecuteVMCommand(ctx, grpcReq)
	if err != nil {
		s.logger.ErrorContext(ctx, "lifecycle command failed",
			"vm_id", vm.ID,
			"command", command,
			"error", err,
		)
		return &LifecycleResponse{
			VMID:    vm.ID,
			Command: string(req.Command),
			Success: false,
			Message: err.Error(),
		}, ErrVMLifecycleFailed
	}

	// Update VM status based on response
	if resp.Success {
		s.syncVMStatus(ctx, vm, resp.State)
	}

	return &LifecycleResponse{
		VMID:     vm.ID,
		Command:  string(req.Command),
		Success:  resp.Success,
		Message:  resp.Message,
		NewState: resp.State.String(),
	}, nil
}

// buildVMConfig builds a VM configuration for agent communication
func (s *VMService) buildVMConfig(ctx context.Context, vm *models.VM, templateID string) (*pb.VMConfig, error) {
	// Use current template or specified template
	osTemplateID := vm.OSTemplateID
	if templateID != "" {
		osTemplateID = templateID
	}

	template, err := s.templateRepo.GetByID(ctx, osTemplateID)
	if err != nil {
		return nil, fmt.Errorf("failed to get template: %w", err)
	}

	config := &pb.VMConfig{
		Resources: &pb.VMResources{
			Vcpus:    int32(vm.Resources.CPU),
			MemoryMb: int64(vm.Resources.RAM),
			DiskGb:   int64(vm.Resources.Disk),
		},
		ImageId:    template.ImagePath,
		VncEnabled: true,
	}

	if vm.VNCPort != nil {
		// VNC password is already set on VM
		config.VncPassword = vm.VNCPassword
	}

	if vm.Resources.Swap != nil {
		config.Resources.SwapMb = int64(*vm.Resources.Swap)
	}

	if vm.Resources.IOPS != nil {
		config.Resources.IopsLimit = int32(*vm.Resources.IOPS)
	}

	// Add metadata
	config.Metadata = map[string]string{
		"vm_id":       vm.ID,
		"hostname":    vm.Hostname,
		"template_id": template.ID,
	}

	return config, nil
}

// ============================================================================
// gRPC Client Management
// ============================================================================

// getAgentClient returns a gRPC client for the specified node
func (s *VMService) getAgentClient(ctx context.Context, nodeID string) (pb.NodeAgentClient, error) {
	// Check connection cache
	s.connMutex.RLock()
	conn, exists := s.grpcConns[nodeID]
	s.connMutex.RUnlock()

	if exists {
		return pb.NewNodeAgentClient(conn), nil
	}

	// Get node details
	node, err := s.nodeRepo.GetByID(ctx, nodeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("failed to get node: %w", err)
	}

	// Create new connection
	address := fmt.Sprintf("%s:50051", node.IPAddress)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn, err = grpc.DialContext(ctx, address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to agent: %w", err)
	}

	// Cache connection
	s.connMutex.Lock()
	s.grpcConns[nodeID] = conn
	s.connMutex.Unlock()

	return pb.NewNodeAgentClient(conn), nil
}

// Close closes all gRPC connections
func (s *VMService) Close() error {
	s.connMutex.Lock()
	defer s.connMutex.Unlock()

	var lastErr error
	for nodeID, conn := range s.grpcConns {
		if err := conn.Close(); err != nil {
			s.logger.Error("failed to close gRPC connection",
				"node_id", nodeID,
				"error", err,
			)
			lastErr = err
		}
		delete(s.grpcConns, nodeID)
	}

	return lastErr
}

// ============================================================================
// Rebuild VM (Reinstall OS)
// ============================================================================

// RebuildVMRequest contains parameters for rebuilding a VM
type RebuildVMRequest struct {
	VMID       string `json:"vm_id" validate:"required,uuid"`
	TemplateID string `json:"template_id,omitempty" validate:"omitempty,uuid"` // Optional: new template
	PreserveIP bool   `json:"preserve_ip"`                                     // Keep the same IP addresses
}

// RebuildVMResponse contains the result of a rebuild operation
type RebuildVMResponse struct {
	VMID    string `json:"vm_id"`
	Status  string `json:"status"`
	JobID   int64  `json:"job_id"`
	Message string `json:"message,omitempty"`
}

// RebuildVM rebuilds a VM by reinstalling the OS
func (s *VMService) RebuildVM(ctx context.Context, req *RebuildVMRequest) (*RebuildVMResponse, error) {
	vm, err := s.vmRepo.GetByID(ctx, req.VMID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}

	// VM must be stopped for rebuild
	if vm.Status == models.VMStatusRunning {
		return nil, fmt.Errorf("VM must be stopped before rebuilding")
	}

	// Validate template if specified
	templateID := vm.OSTemplateID
	if req.TemplateID != "" {
		template, err := s.templateRepo.GetByID(ctx, req.TemplateID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrTemplateNotFound
			}
			return nil, fmt.Errorf("failed to get template: %w", err)
		}
		if !template.IsActive {
			return nil, fmt.Errorf("OS template is not active")
		}
		templateID = req.TemplateID
	}

	// Update VM template if changed
	if templateID != vm.OSTemplateID {
		vm.OSTemplateID = templateID
		if err := s.vmRepo.Update(ctx, vm); err != nil {
			return nil, fmt.Errorf("failed to update VM template: %w", err)
		}
	}

	// Generate new VNC password for security
	vncConfig := s.generateVNCCredentials()
	vm.VNCPassword = vncConfig.Password
	vm.VNCPort = &vncConfig.Port
	if err := s.db.Model(vm).Updates(map[string]interface{}{
		"vnc_password": vncConfig.Password,
		"vnc_port":     vncConfig.Port,
	}).Error; err != nil {
		return nil, fmt.Errorf("failed to update VNC credentials: %w", err)
	}

	// Prepare rebuild params
	params := map[string]interface{}{
		"template_id":  templateID,
		"preserve_ip":  req.PreserveIP,
		"vnc_port":     vncConfig.Port,
		"vnc_password": vncConfig.Password,
	}
	paramsJSON, _ := json.Marshal(params)

	// Enqueue rebuild job
	job := queue.VMOperationJob{
		VMID:      vm.ID,
		Operation: queue.VMOpRebuild,
		NodeID:    vm.NodeID,
		Params:    paramsJSON,
	}

	result, err := s.riverClient.Insert(ctx, job, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to enqueue VM rebuild job: %w", err)
	}

	s.logger.InfoContext(ctx, "VM rebuild job enqueued",
		"vm_id", vm.ID,
		"template_id", templateID,
		"job_id", result.Job.ID,
	)

	return &RebuildVMResponse{
		VMID:    vm.ID,
		Status:  "pending",
		JobID:   result.Job.ID,
		Message: "VM rebuild initiated",
	}, nil
}

// ============================================================================
// VNC Operations
// ============================================================================

// GetVNCResponse contains VNC connection details
type GetVNCResponse struct {
	VMID         string `json:"vm_id"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	Password     string `json:"password,omitempty"`
	WebSocketURL string `json:"websocket_url,omitempty"`
}

// GetVNCConfig retrieves VNC configuration for a VM
func (s *VMService) GetVNCConfig(ctx context.Context, vmID string, includePassword bool) (*GetVNCResponse, error) {
	vm, err := s.vmRepo.GetByIDWithNode(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}

	if vm.VNCPort == nil {
		return nil, fmt.Errorf("VNC is not configured for this VM")
	}

	response := &GetVNCResponse{
		VMID: vmID,
		Port: *vm.VNCPort,
	}

	if includePassword {
		response.Password = vm.VNCPassword
	}

	// Get node IP for connection
	node, err := s.nodeRepo.GetByID(ctx, vm.NodeID)
	if err == nil {
		response.Host = node.IPAddress
		response.WebSocketURL = fmt.Sprintf("wss://%s/vnc/%s", node.IPAddress, vmID)
	}

	return response, nil
}

// RefreshVNCPassword generates a new VNC password for a VM
func (s *VMService) RefreshVNCPassword(ctx context.Context, vmID string) (*VNCConfig, error) {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}

	// Generate new credentials
	vncConfig := s.generateVNCCredentials()

	// Update VM
	if err := s.vmRepo.UpdateVNCPassword(ctx, vmID, vncConfig.Password); err != nil {
		return nil, fmt.Errorf("failed to update VNC password: %w", err)
	}
	if err := s.vmRepo.UpdateVNCPort(ctx, vmID, vncConfig.Port); err != nil {
		return nil, fmt.Errorf("failed to update VNC port: %w", err)
	}

	// Notify agent of password change if VM is running
	if vm.Status == models.VMStatusRunning {
		// This would require an agent API to update VNC password dynamically
		// For now, password will take effect on next VM restart
		s.logger.InfoContext(ctx, "VNC password updated, will take effect on next restart",
			"vm_id", vmID)
	}

	return vncConfig, nil
}

// ============================================================================
// Status Sync from Agent Heartbeat
// ============================================================================

// SyncVMStatusFromHeartbeat updates VM status based on agent heartbeat data
func (s *VMService) SyncVMStatusFromHeartbeat(ctx context.Context, nodeID string, activeVMIDs []string) error {
	// Get all VMs on this node
	vms, err := s.vmRepo.ListByNodeID(ctx, nodeID, 0, 0)
	if err != nil {
		return fmt.Errorf("failed to list VMs for node: %w", err)
	}

	// Build set of active VM IDs
	activeSet := make(map[string]bool)
	for _, id := range activeVMIDs {
		activeSet[id] = true
	}

	// Update each VM's status
	for _, vm := range vms {
		shouldBeRunning := activeSet[vm.ID]
		currentlyRunning := vm.Status == models.VMStatusRunning

		if shouldBeRunning && !currentlyRunning {
			// VM is running on agent but marked as stopped
			s.logger.InfoContext(ctx, "updating VM status to running from heartbeat",
				"vm_id", vm.ID)
			if err := s.vmRepo.UpdateStatus(ctx, vm.ID, models.VMStatusRunning); err != nil {
				s.logger.ErrorContext(ctx, "failed to update VM status",
					"vm_id", vm.ID, "error", err)
			}
		} else if !shouldBeRunning && currentlyRunning {
			// VM is stopped on agent but marked as running
			s.logger.InfoContext(ctx, "updating VM status to stopped from heartbeat",
				"vm_id", vm.ID)
			if err := s.vmRepo.UpdateStatus(ctx, vm.ID, models.VMStatusStopped); err != nil {
				s.logger.ErrorContext(ctx, "failed to update VM status",
					"vm_id", vm.ID, "error", err)
			}
		}
	}

	return nil
}

// GetVMStatusMetrics retrieves current metrics for a VM from the agent
func (s *VMService) GetVMStatusMetrics(ctx context.Context, vmID string) (*pb.VMStatusResponse, error) {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}

	return s.getVMAgentStatus(ctx, vmID, vm.NodeID)
}
