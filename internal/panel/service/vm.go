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
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	panelclient "github.com/maburvm/panel/internal/panel/client"
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
	// ErrTemplateNotInstallable is returned when a template has no real base image
	// to provision from (e.g. the "/imported" placeholder created for imported VMs).
	ErrTemplateNotInstallable = errors.New("OS template has no installable image (it was created for imported VMs); choose or add a real template")
	// ErrNoUsablePool is returned when an auto-assigned VM can't get a reachable
	// public IP because no node-eligible pool has both a bridge and a gateway.
	ErrNoUsablePool = errors.New("no usable IP pool (with a bridge and gateway) is available on the selected node; an administrator must configure one before VMs can be ordered")
	// ErrPoolHasNoBridge is returned when a VM is created from an explicitly
	// selected IP pool that has no bridge configured — the VM would fall back to
	// the non-existent virbr0 and fail to start.
	ErrPoolHasNoBridge = errors.New("the selected IP pool has no bridge configured; set the pool's bridge (e.g. viifbr0) before creating VMs from it")
	// ErrVMLifecycleFailed is returned when a lifecycle operation fails
	ErrVMLifecycleFailed = errors.New("VM lifecycle operation failed")
	// ErrVMNodeInactive is returned when trying to execute an agent-backed VM
	// operation while the owning node is offline or in maintenance.
	ErrVMNodeInactive = errors.New("VM node is not active")
	// ErrVMCannotBeModified is returned when trying to modify a VM that's not stopped
	ErrVMCannotBeModified = errors.New("VM cannot be modified while running")
)

// isInstallableImagePath reports whether a template image path can seed a new VM.
// The "/imported" sentinel (and an empty path) mark templates that exist only to
// associate already-imported VMs; they have no provisionable base image.
func isInstallableImagePath(path string) bool {
	p := strings.TrimSpace(path)
	return p != "" && p != "/imported"
}

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
	networkRepo  *repository.NetworkRepository
	planRepo     *repository.PlanRepository
	ipamService  *IPAMService
	quotaService *QuotaService
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
		networkRepo:           repository.NewNetworkRepository(db),
		planRepo:              repository.NewPlanRepository(db),
		ipamService:           NewIPAMService(db, repository.NewIPAMRepository(db)),
		quotaService:          NewQuotaService(db, vmRepo),
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

// NodeSummary contains node fields needed to annotate VM list responses.
type NodeSummary struct {
	Name   string
	Status models.NodeStatus
}

// GetNodeSummaries returns a map of node ID to lightweight node info.
func (s *VMService) GetNodeSummaries(ctx context.Context) (map[string]NodeSummary, error) {
	nodes, err := s.nodeRepo.List(ctx, 0, 0)
	if err != nil {
		return nil, err
	}
	result := make(map[string]NodeSummary, len(nodes))
	for _, n := range nodes {
		status := n.Status
		// If the DB still says offline but the agent port is reachable, self-heal
		// the status before annotating VM list rows. This prevents active nodes from
		// being shown as inactive just because no node-health page was opened yet.
		if status == models.NodeStatusOffline && s.isAgentReachable(ctx, n.IPAddress) {
			status = models.NodeStatusActive
			_ = s.nodeRepo.UpdateStatus(ctx, n.ID, models.NodeStatusActive)
		}
		result[n.ID] = NodeSummary{Name: n.Name, Status: status}
	}
	return result, nil
}

// ============================================================================
// Create VM
// ============================================================================

// CreateVMRequest contains parameters for creating a new VM
type CreateVMRequest struct {
	UserID        string           `json:"user_id" validate:"required,uuid"`
	Hostname      string           `json:"hostname" validate:"required,max=100,hostname_rfc1123"`
	OSTemplateID  string           `json:"os_template_id" validate:"required,uuid"`
	Resources     models.Resources `json:"resources" validate:"required"`
	NodeID        string           `json:"node_id,omitempty" validate:"omitempty,uuid"` // Optional: specific node
	PlanID        string           `json:"plan_id,omitempty" validate:"omitempty,uuid"` // Optional: derive resources from a plan
	IPPoolID      string           `json:"ip_pool_id,omitempty" validate:"omitempty,uuid"`
	RequestedIP   string           `json:"requested_ip,omitempty" validate:"omitempty,ip"`
	BandwidthMbps int              `json:"bandwidth_mbps,omitempty" validate:"omitempty,min=0,max=10000"`
	VLANID        int              `json:"vlan_id,omitempty" validate:"omitempty,min=0,max=4094"`
	// CPUModel is the guest CPU model. Empty → the node defaults to a portable,
	// live-migratable model (kvm64). e.g. "host-passthrough", "host-model", "Haswell".
	CPUModel string `json:"cpu_model,omitempty" validate:"omitempty,max=64"`
	// UserData is an optional first-boot script/recipe (run once per instance via
	// cloud-init). Plain shell (#!/bin/bash …) or a cloud-init script.
	UserData string `json:"user_data,omitempty" validate:"omitempty,max=65536"`
	// ManagedNetworkID, when set, attaches the VM's NIC to that managed/private
	// network's bridge (VPC) instead of the public pool bridge.
	ManagedNetworkID string `json:"managed_network_id,omitempty" validate:"omitempty,uuid"`
	// CloneSourceRef, when set, is used as the disk source instead of the template
	// image — a "vm://<id>" (same node) or "vm://<srcNodeIP>/<id>" (cross-node)
	// reference the agent resolves by copying/pulling that VM's disk. Set by CloneVM.
	CloneSourceRef string `json:"-"`
	// AutoAssignIP, when true and IPPoolID is empty, makes the service pick the
	// first node-eligible pool with a free address and allocate a public IP from
	// it. Set for self-service (client) orders so they never get an unusable
	// NAT-only VM; if no pool has a free address the create fails with a clear error.
	AutoAssignIP bool `json:"-"`
	// Password, when set, becomes the new guest's root password (injected on
	// first boot). Empty + RegeneratePassword makes the service generate one.
	Password string `json:"-"`
	// RegeneratePassword, when true and Password is empty, generates a random root
	// password and returns it once in the response.
	RegeneratePassword bool `json:"-"`
	// SSHPublicKeys are authorized_keys lines to inject into the new guest
	// (resolved from the user's saved keys by the handler). Empty means none.
	SSHPublicKeys []string `json:"-"`
}

// CreateVMResponse contains the created VM and job information
type CreateVMResponse struct {
	VM     *models.VM `json:"vm"`
	JobID  int64      `json:"job_id"`
	Status string     `json:"status"`
	// RootPassword is present only when a password was generated for this VM
	// (RegeneratePassword with no explicit Password) — shown to the caller once.
	RootPassword string `json:"root_password,omitempty"`
}

// CreateVM creates a new VM and enqueues a creation job
func (s *VMService) CreateVM(ctx context.Context, req *CreateVMRequest) (*CreateVMResponse, error) {
	// If a plan (flavor) is selected, derive resources + bandwidth from it.
	if req.PlanID != "" {
		plan, err := s.planRepo.GetByID(ctx, req.PlanID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, fmt.Errorf("plan not found")
			}
			return nil, fmt.Errorf("failed to get plan: %w", err)
		}
		if !plan.IsActive {
			return nil, fmt.Errorf("plan is not active")
		}
		req.Resources = models.Resources{CPU: plan.CPU, RAM: plan.RAM, Disk: plan.Disk}
		if plan.BandwidthMbps > 0 && req.BandwidthMbps == 0 {
			req.BandwidthMbps = plan.BandwidthMbps
		}
	}

	// Validate resources
	if err := s.validateResources(&req.Resources); err != nil {
		return nil, err
	}

	// Enforce the owner's resource quota before allocating anything.
	if err := s.quotaService.CheckCanCreate(ctx, req.UserID, req.Resources); err != nil {
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
	// A real install needs a real base image. Imported templates carry the
	// "/imported" placeholder (their VMs already have disks) and can't seed a new
	// VM. Clones bypass this — they copy the source VM's disk via CloneSourceRef.
	if req.CloneSourceRef == "" && !isInstallableImagePath(template.ImagePath) {
		return nil, ErrTemplateNotInstallable
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

	// Create VM record and any requested IP/network allocation atomically.
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

	// Decide which pool(s) to allocate a public IP from. An explicit IPPoolID
	// wins; otherwise AutoAssignIP (client self-service) tries every pool eligible
	// for the node until one yields a free address. With neither, the VM gets no
	// managed public IP (an admin deliberately choosing a NAT/private setup).
	var poolCandidates []string
	if req.IPPoolID != "" {
		// Even an explicitly-chosen pool must have a bridge, or the VM gets the
		// node's default NAT bridge (virbr0) — which doesn't exist on most KVM
		// hosts, so the domain fails to START ("Cannot get interface MTU on
		// 'virbr0'"). Reject up front with a clear error instead of provisioning an
		// unstartable VM. (A managed/private network provides its own bridge, so
		// that path is exempt.)
		if req.ManagedNetworkID == "" {
			pool, perr := s.ipamService.GetPool(ctx, req.IPPoolID)
			if perr != nil {
				return nil, fmt.Errorf("failed to load selected IP pool: %w", perr)
			}
			if pool == nil || pool.Bridge == "" {
				return nil, ErrPoolHasNoBridge
			}
		}
		poolCandidates = []string{req.IPPoolID}
	} else if req.AutoAssignIP {
		pools, err := s.ipamService.ListPoolsForNode(ctx, nodeID)
		if err != nil {
			return nil, fmt.Errorf("failed to list IP pools for node: %w", err)
		}
		// Only auto-assign from pools that can actually produce a REACHABLE VM: a
		// pool needs a bridge (which host NIC the guest attaches to) and a gateway
		// (its default route). Allocating from a bridge-less pool would give the VM
		// an address but leave it on the node's NAT bridge (virbr0) — unreachable
		// from the internet — which is exactly the silent-failure we're avoiding.
		for i := range pools {
			if pools[i].Bridge != "" && pools[i].Gateway != "" {
				poolCandidates = append(poolCandidates, pools[i].ID)
			}
		}
		if len(poolCandidates) == 0 {
			return nil, ErrNoUsablePool
		}
	}

	var allocatedIP *models.IPAddress
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.vmRepo.WithDB(tx).Create(ctx, vm); err != nil {
			return fmt.Errorf("failed to create VM record: %w", err)
		}
		if req.RequestedIP != "" && req.IPPoolID == "" {
			return fmt.Errorf("ip_pool_id is required when requested_ip is set")
		}
		if len(poolCandidates) > 0 {
			nodeIDPtr := nodeID
			var allocated *models.IPAddress
			var lastErr error
			for _, pid := range poolCandidates {
				a, err := s.ipamService.AllocateAddressInTx(ctx, tx, &AllocateIPAddressRequest{
					PoolID:      pid,
					NodeID:      &nodeIDPtr,
					VMID:        &vm.ID,
					RequestedIP: req.RequestedIP,
				})
				if err == nil {
					allocated = a
					break
				}
				lastErr = err
				// In auto mode an exhausted/ineligible pool just means "try the
				// next one"; any other error (or an explicit-pool error) is fatal.
				if req.IPPoolID == "" && (errors.Is(err, ErrNoAvailableIPAddress) || errors.Is(err, ErrPoolNotAvailableOnNode)) {
					continue
				}
				return err
			}
			if allocated == nil {
				if req.IPPoolID == "" {
					return fmt.Errorf("no IP address available in any pool for the selected node")
				}
				return lastErr
			}
			allocatedIP = allocated
			network := &models.Network{VMID: vm.ID, IPAddress: allocated.Address}
			if req.BandwidthMbps > 0 {
				network.BandwidthLimit = int64(req.BandwidthMbps)
			}
			if req.VLANID > 0 {
				vlan := req.VLANID
				network.VLANID = &vlan
			}
			if err := s.networkRepo.WithDB(tx).Create(ctx, network); err != nil {
				return fmt.Errorf("failed to create network record: %w", err)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// Disk source: the template image, or — for a clone — a vm:// reference the
	// agent resolves by copying the source VM's disk (locally or pulled over SSH).
	imagePath := template.ImagePath
	if req.CloneSourceRef != "" {
		imagePath = req.CloneSourceRef
	}

	// Prepare VM creation params
	params := map[string]interface{}{
		"hostname":         req.Hostname,
		"os_template":      template.Name,
		"template_version": template.Version,
		"image_path":       imagePath,
		"resources":        req.Resources,
		"vnc_port":         vncConfig.Port,
		"vnc_password":     vncConfig.Password,
		"bandwidth_mbps":   req.BandwidthMbps,
		"vlan_id":          req.VLANID,
		"cpu_model":        req.CPUModel,
		"user_data":        req.UserData,
	}
	if allocatedIP != nil {
		params["ip_address"] = allocatedIP.Address
		params["ip_pool_id"] = allocatedIP.PoolID
		// Carry the pool's gateway and prefix length so the agent can configure
		// the guest NIC with a static address.
		if pool, perr := s.ipamService.GetPool(ctx, allocatedIP.PoolID); perr == nil && pool != nil {
			if pool.Gateway != "" {
				params["gateway"] = pool.Gateway
			}
			if prefix := cidrPrefixLen(pool.CIDR); prefix > 0 {
				params["netmask"] = prefix
			}
			if pool.Bridge != "" {
				params["bridge"] = pool.Bridge
			}
		}
	}

	// VPC attach: put the NIC on a managed/private network's bridge. Overrides the
	// pool bridge (a NIC lives on one bridge); VPC VMs typically omit a public pool.
	if req.ManagedNetworkID != "" {
		var mn models.ManagedNetwork
		if err := s.db.WithContext(ctx).Where("id = ?", req.ManagedNetworkID).First(&mn).Error; err != nil {
			return nil, fmt.Errorf("managed network not found: %w", err)
		}
		if mn.Bridge == "" {
			return nil, fmt.Errorf("managed network %q has no bridge (not provisioned on a node yet)", mn.Name)
		}
		params["bridge"] = mn.Bridge
	}

	// Diagnose a likely misconfiguration: a VM that was assigned a public IP but
	// has no bridge (its pool didn't set one, and it's not on a managed network)
	// will land on the node's default NAT bridge and be unreachable. Surface it in
	// the logs so an operator can fix the pool rather than chase a "VM has an IP
	// but isn't reachable" ticket.
	if allocatedIP != nil && req.ManagedNetworkID == "" {
		if _, ok := params["bridge"]; !ok {
			s.logger.WarnContext(ctx, "VM assigned a public IP but its pool has no bridge; it will use the node's default (NAT) bridge and may be unreachable",
				"vm_id", vm.ID, "ip", allocatedIP.Address, "pool_id", allocatedIP.PoolID)
		}
	}

	// Root password + SSH keys: without these a freshly-created guest has no
	// credentials and can't be logged into even when it's reachable. Inject the
	// caller's password (or a generated one) and any selected SSH keys so the VM
	// is actually usable on first boot — matching the rebuild flow.
	rootPassword := req.Password
	if rootPassword == "" && req.RegeneratePassword {
		rootPassword = generateRootPassword()
	}
	if rootPassword != "" {
		params["root_password"] = rootPassword
	}
	if len(req.SSHPublicKeys) > 0 {
		params["ssh_public_key"] = strings.Join(req.SSHPublicKeys, "\n")
	}
	// Only surface a password we generated ourselves; never echo back one the
	// caller supplied.
	generatedPassword := ""
	if req.Password == "" && req.RegeneratePassword {
		generatedPassword = rootPassword
	}

	paramsJSON, _ := json.Marshal(params)

	if s.riverClient == nil {
		s.logger.WarnContext(ctx, "VM creation queue disabled; VM record created without background job", "vm_id", vm.ID)
		return &CreateVMResponse{VM: vm, JobID: 0, Status: "pending", RootPassword: generatedPassword}, nil
	}

	// Enqueue VM creation job
	job := queue.VMOperationJob{
		VMID:      vm.ID,
		Operation: queue.VMOpCreate,
		NodeID:    nodeID,
		Params:    paramsJSON,
	}

	result, err := s.riverClient.Insert(ctx, job, nil)
	if err != nil {
		// Rollback VM creation on job enqueue failure. This also releases IPAM allocations
		// and removes network rows created for this VM.
		if delErr := s.cleanupVMAllocation(ctx, vm.ID, true); delErr != nil {
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
		VM:           vm,
		JobID:        result.Job.ID,
		Status:       "pending",
		RootPassword: generatedPassword,
	}, nil
}

// CloneVMRequest describes a VM-clone operation.
type CloneVMRequest struct {
	SourceVMID string `json:"-"`
	Hostname   string `json:"hostname"`     // new hostname; defaults to "<source>-clone"
	DestNodeID string `json:"dest_node_id"` // target node; defaults to the source's node
}

// CloneVM provisions a new VM as an independent copy of an existing one: same
// template/resources/owner, a fresh hostname/IP/MAC/VNC, and a disk copied from
// the source. The source must be stopped so the copy is consistent. The clone
// can target a different node (Virtualizor From → To server), in which case the
// destination node pulls the source disk over SSH. Reuses the create pipeline.
func (s *VMService) CloneVM(ctx context.Context, req *CloneVMRequest) (*CreateVMResponse, error) {
	src, err := s.vmRepo.GetByID(ctx, req.SourceVMID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get source VM: %w", err)
	}
	if src.Status == models.VMStatusRunning {
		return nil, fmt.Errorf("source VM must be stopped before cloning")
	}

	hostname := strings.TrimSpace(req.Hostname)
	if hostname == "" {
		hostname = src.Hostname + "-clone"
	}

	destNodeID := req.DestNodeID
	if destNodeID == "" {
		destNodeID = src.NodeID
	}

	// Same node → local disk copy. Different node → the destination agent pulls
	// the source disk over SSH (encode the source node's IP in the ref).
	cloneRef := "vm://" + src.ID
	if destNodeID != src.NodeID {
		srcNode, nerr := s.nodeRepo.GetByID(ctx, src.NodeID)
		if nerr != nil {
			return nil, fmt.Errorf("failed to resolve source node for cross-node clone: %w", nerr)
		}
		cloneRef = fmt.Sprintf("vm://%s/%s", srcNode.IPAddress, src.ID)
	}

	return s.CreateVM(ctx, &CreateVMRequest{
		UserID:         src.UserID,
		Hostname:       hostname,
		OSTemplateID:   src.OSTemplateID,
		Resources:      src.Resources,
		NodeID:         destNodeID,
		CloneSourceRef: cloneRef,
	})
}

// cidrPrefixLen returns the prefix length (e.g. 24) from a CIDR string such as
// "192.168.1.0/24". It returns 0 when the CIDR is empty or invalid.
func cidrPrefixLen(cidr string) int {
	if cidr == "" {
		return 0
	}
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil || ipnet == nil {
		return 0
	}
	ones, _ := ipnet.Mask.Size()
	return ones
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

	return &VNCConfig{
		Port:     port,
		Password: generateVNCPassword(),
	}
}

// generateVNCPassword returns an 8-char alphanumeric password. VNC's classic
// "VNC Auth" uses at most 8 bytes, so a longer or symbol-laden password only
// invites mismatches: the browser sends the literal value while QEMU stores a
// truncated/mangled one (it's set via the QEMU monitor). Keeping it 8 chars and
// free of shell/JSON-special characters makes the value the browser sends and
// the value QEMU enforces identical. Ambiguous chars (0/O, 1/l) are omitted.
func generateVNCPassword() string {
	const charset = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 8)
	for i := range b {
		b[i] = charset[randInt(int64(len(charset)))]
	}
	return string(b)
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

func (s *VMService) cleanupVMAllocation(ctx context.Context, vmID string, deleteVM bool) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.ipamService.ReleaseAddressesByVMIDInTx(ctx, tx, vmID); err != nil {
			return err
		}
		if err := s.networkRepo.WithDB(tx).DeleteByVMID(ctx, vmID); err != nil {
			return err
		}
		if !deleteVM {
			return nil
		}
		return s.vmRepo.WithDB(tx).Delete(ctx, vmID)
	})
}

// ============================================================================
// List VMs
// ============================================================================

// ListVMsRequest contains filtering and pagination parameters
type ListVMsRequest struct {
	UserID string          `json:"user_id,omitempty" validate:"omitempty,uuid"`
	NodeID string          `json:"node_id,omitempty" validate:"omitempty,uuid"`
	Status models.VMStatus `json:"status,omitempty" validate:"omitempty,oneof=running stopped suspended creating deleting error"`
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
// GetVMOwner returns the user ID that owns the given VM. It is a cheap lookup
// (no agent round-trip) used for per-resource authorization. Returns ErrVMNotFound
// if the VM does not exist.
func (s *VMService) GetVMOwner(ctx context.Context, vmID string) (string, error) {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrVMNotFound
		}
		return "", fmt.Errorf("failed to get VM: %w", err)
	}
	return vm.UserID, nil
}

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

	// Get agent status if requested and the VM is in a stable state. Skip while
	// creating or deleting: the hypervisor domain may still be up during a delete,
	// and syncing would flip the VM back to "running" and "resurrect" it in the UI
	// until the delete worker finishes.
	if includeAgentStatus && vm.Status != models.VMStatusCreating && vm.Status != models.VMStatusDeleting {
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

	authCtx, err := s.agentAuthContext(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	return client.GetVMStatus(authCtx, req)
}

// GetVMTrafficCounters returns the VM's cumulative network counters (total bytes
// rx/tx since boot) from the agent, for bandwidth accounting. Returns (0,0,nil)
// when the agent reports no resource usage yet.
func (s *VMService) GetVMTrafficCounters(ctx context.Context, nodeID, vmID string) (rxBytes, txBytes int64, err error) {
	status, err := s.getVMAgentStatus(ctx, vmID, nodeID)
	if err != nil {
		return 0, 0, err
	}
	if cr := status.GetCurrentResources(); cr != nil {
		return cr.GetNetworkRxBytes(), cr.GetNetworkTxBytes(), nil
	}
	return 0, 0, nil
}

// StopVMForEnforcement force-stops a VM on its node (used by bandwidth-quota
// enforcement) and reflects the stopped state in the DB.
func (s *VMService) StopVMForEnforcement(ctx context.Context, nodeID, vmID string) error {
	client, err := s.getAgentClient(ctx, nodeID)
	if err != nil {
		return err
	}
	authCtx, err := s.agentAuthContext(ctx, nodeID)
	if err != nil {
		return err
	}
	if _, err := client.ExecuteVMCommand(authCtx, &pb.VMCommandRequest{
		VmId:    vmID,
		Command: pb.VMCommandType_VM_COMMAND_TYPE_STOP,
		Async:   false,
	}); err != nil {
		return err
	}
	if err := s.vmRepo.UpdateStatus(ctx, vmID, models.VMStatusStopped); err != nil {
		s.logger.WarnContext(ctx, "failed to persist stopped status after bandwidth enforcement", "vm_id", vmID, "error", err)
	}
	return nil
}

// ============================================================================
// Additional Disks
// ============================================================================

// ListDisks returns the extra data disks attached to a VM (newest device last).
func (s *VMService) ListDisks(ctx context.Context, vmID string) ([]models.VMDisk, error) {
	var disks []models.VMDisk
	if err := s.db.WithContext(ctx).Where("vm_id = ?", vmID).Order("device ASC").Find(&disks).Error; err != nil {
		return nil, fmt.Errorf("failed to list disks: %w", err)
	}
	return disks, nil
}

// AttachDisk provisions and hot-plugs a new data disk of sizeGB onto the VM,
// then records it. The agent picks the next free virtio target.
func (s *VMService) AttachDisk(ctx context.Context, vmID string, sizeGB int) (*models.VMDisk, error) {
	if sizeGB <= 0 {
		return nil, fmt.Errorf("disk size must be a positive number of GB")
	}
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}
	client, err := s.getAgentClient(ctx, vm.NodeID)
	if err != nil {
		return nil, err
	}
	authCtx, err := s.agentAuthContext(ctx, vm.NodeID)
	if err != nil {
		return nil, err
	}
	resp, err := client.AttachDisk(authCtx, &pb.AttachDiskRequest{VmId: vmID, SizeGb: int64(sizeGB)})
	if err != nil {
		return nil, fmt.Errorf("agent attach disk failed: %w", err)
	}
	if !resp.Success {
		return nil, fmt.Errorf("attach disk failed: %s", agentErrorMessage(resp.Error))
	}

	disk := &models.VMDisk{VMID: vmID, Device: resp.Device, SizeGB: sizeGB, Path: resp.Path}
	if err := s.db.WithContext(ctx).Create(disk).Error; err != nil {
		return nil, fmt.Errorf("disk attached on node but failed to record it: %w", err)
	}
	return disk, nil
}

// DetachDisk detaches a data disk (by virtio device, e.g. "vdb") from the VM and
// optionally deletes its backing volume.
func (s *VMService) DetachDisk(ctx context.Context, vmID, device string, deleteVolume bool) error {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrVMNotFound
		}
		return fmt.Errorf("failed to get VM: %w", err)
	}
	var disk models.VMDisk
	if err := s.db.WithContext(ctx).Where("vm_id = ? AND device = ?", vmID, device).First(&disk).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("disk %s not found on this VM", device)
		}
		return err
	}
	client, err := s.getAgentClient(ctx, vm.NodeID)
	if err != nil {
		return err
	}
	authCtx, err := s.agentAuthContext(ctx, vm.NodeID)
	if err != nil {
		return err
	}
	resp, err := client.DetachDisk(authCtx, &pb.DetachDiskRequest{VmId: vmID, Device: device, Path: disk.Path, DeleteVolume: deleteVolume})
	if err != nil {
		return fmt.Errorf("agent detach disk failed: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("detach disk failed: %s", agentErrorMessage(resp.Error))
	}
	if err := s.db.WithContext(ctx).Delete(&disk).Error; err != nil {
		return fmt.Errorf("disk detached on node but failed to remove record: %w", err)
	}
	return nil
}

// agentErrorMessage extracts a human-readable message from an agent ErrorResponse.
func agentErrorMessage(e *pb.ErrorResponse) string {
	if e != nil && e.Message != "" {
		return e.Message
	}
	return "unknown agent error"
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
		// Disk can only grow — a qcow2/guest filesystem can't be safely shrunk, and
		// the agent's resize is grow-only, so reject shrink to keep DB == reality.
		if req.Resources.Disk < vm.Resources.Disk {
			return nil, fmt.Errorf("disk can only be grown (current %dGB, requested %dGB)", vm.Resources.Disk, req.Resources.Disk)
		}
		// Enforce quota on resize (VM count unchanged; usage already includes the old size).
		if err := s.quotaService.CheckCanResize(ctx, vm.UserID, vm.Resources, *req.Resources); err != nil {
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

	if s.riverClient != nil {
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

		// Do NOT release the IP/network or remove the row here: the hypervisor VM
		// is still running until the agent destroys it (async). Releasing the IP
		// now would let the next create hand the same live IP to another VM. The
		// delete worker performs the resource cleanup (IP release + row removal)
		// only after the agent confirms the domain is gone. We mark the VM as
		// "deleting" so it stops appearing as an active, operable VM in the UI.
		if err := s.vmRepo.UpdateStatus(ctx, vm.ID, models.VMStatusDeleting); err != nil {
			s.logger.WarnContext(ctx, "failed to mark VM as deleting", "vm_id", vm.ID, "error", err)
		}

		s.logger.InfoContext(ctx, "VM deletion job enqueued",
			"vm_id", vm.ID,
			"node_id", vm.NodeID,
		)
		return nil
	}

	// Queue disabled: there is no worker to finish the job, so clean up the
	// local records synchronously (IP release + network rows + VM row).
	s.logger.WarnContext(ctx, "VM deletion queue disabled; cleaning local VM records only", "vm_id", vm.ID)
	if err := s.cleanupVMAllocation(ctx, vm.ID, true); err != nil {
		return fmt.Errorf("failed to release VM IP/network allocation: %w", err)
	}

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
	// LifecycleSuspend pauses the VM (keeps it in memory)
	LifecycleSuspend LifecycleCommand = "suspend"
	// LifecycleUnsuspend resumes a paused VM
	LifecycleUnsuspend LifecycleCommand = "unsuspend"
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

	// A VM that is being deleted must not accept lifecycle commands: it is on its
	// way out and its disks/IP are about to be reclaimed, so starting/restarting
	// it would race the delete worker.
	if vm.Status == models.VMStatusDeleting {
		return nil, fmt.Errorf("VM is being deleted")
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
	case LifecycleSuspend:
		operation = queue.VMOpSuspend
		vmCommand = pb.VMCommandType_VM_COMMAND_TYPE_PAUSE
	case LifecycleUnsuspend:
		operation = queue.VMOpUnsuspend
		vmCommand = pb.VMCommandType_VM_COMMAND_TYPE_RESUME
	default:
		return nil, fmt.Errorf("invalid lifecycle command: %s", req.Command)
	}

	if err := s.ensureVMNodeActive(ctx, vm.NodeID); err != nil {
		return nil, err
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
	// On start, carry the pool's current bridge so the agent can self-heal a
	// stale NIC <source bridge> before booting (e.g. a VM defined against a
	// since-removed virbr0). Empty => the agent leaves the domain XML untouched.
	if req.Command == LifecycleStart {
		if _, _, bridge, _, _ := s.primaryNetworkConfig(ctx, vm.ID); bridge != "" {
			params["bridge"] = bridge
		}
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

func (s *VMService) ensureVMNodeActive(ctx context.Context, nodeID string) error {
	node, err := s.nodeRepo.GetByID(ctx, nodeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNodeNotFound
		}
		return fmt.Errorf("failed to get VM node: %w", err)
	}
	if node.Status == models.NodeStatusActive {
		return nil
	}
	if node.Status == models.NodeStatusMaintenance {
		return fmt.Errorf("%w: %s", ErrVMNodeInactive, node.Status)
	}
	if node.Status == models.NodeStatusOffline && s.isAgentReachable(ctx, node.IPAddress) {
		_ = s.nodeRepo.UpdateStatus(ctx, node.ID, models.NodeStatusActive)
		return nil
	}
	return fmt.Errorf("%w: %s", ErrVMNodeInactive, node.Status)
}

func (s *VMService) isAgentReachable(ctx context.Context, ipAddress string) bool {
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	dialer := net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(checkCtx, "tcp", fmt.Sprintf("%s:%d", ipAddress, DefaultAgentPort))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
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
	} else if req.Command == LifecycleStart {
		// Self-heal the NIC bridge: carry the pool's current bridge so the agent
		// rewrites a stale <source bridge> before booting. Only the bridge is
		// sent — starting an already-defined domain needs nothing else.
		if _, _, bridge, _, _ := s.primaryNetworkConfig(ctx, vm.ID); bridge != "" {
			config = &pb.VMConfig{
				NetworkConfig: &pb.VMNetworkConfig{
					Interfaces: []*pb.NetworkInterface{{
						Name:       "eth0",
						Type:       pb.NetworkInterfaceType_NETWORK_INTERFACE_TYPE_BRIDGE,
						BridgeName: bridge,
					}},
				},
			}
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

	ctx, err = s.agentAuthContext(ctx, vm.NodeID)
	if err != nil {
		return nil, err
	}
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

	// Sync the DB status from the agent's reported State, which is authoritative
	// about the ACTUAL domain state — even when the command itself "failed".
	// Starting an already-running domain returns Success=false with a "domain is
	// already running" message but State=RUNNING; without syncing here the panel
	// row would stay 'stopped' forever, out of sync with reality. syncVMStatus
	// no-ops on unknown/unspecified states, so this is safe to call always.
	s.syncVMStatus(ctx, vm, resp.State)

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

	// Create new connection (agent uses self-signed TLS)
	address := fmt.Sprintf("%s:50051", node.IPAddress)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	tlsCreds := panelclient.NodeTLSCredentials(node.ID, node.IPAddress)
	conn, err = grpc.DialContext(ctx, address,
		grpc.WithTransportCredentials(tlsCreds),
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

// agentAuthContext augments ctx with the node's Bearer token, which the agent's
// interceptor requires on every RPC. getAgentClient returns a raw client (no
// per-call credentials), so each caller must attach this — omitting it fails
// with "missing authorization header".
func (s *VMService) agentAuthContext(ctx context.Context, nodeID string) (context.Context, error) {
	node, err := s.nodeRepo.GetByID(ctx, nodeID)
	if err != nil {
		return ctx, fmt.Errorf("failed to load node for agent auth: %w", err)
	}
	md := metadata.New(map[string]string{"authorization": "Bearer " + node.Token})
	return metadata.NewOutgoingContext(ctx, md), nil
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
	// Password, when set, becomes the rebuilt guest's root password (applied via
	// cloud-init on first boot). Empty means no password is injected.
	Password string `json:"-"`
	// RegeneratePassword, when true and Password is empty, makes the service
	// generate a random root password and return it in the response.
	RegeneratePassword bool `json:"-"`
	// SSHPublicKeys are the authorized_keys lines to inject (resolved from the
	// user's saved keys by the handler). Empty means none.
	SSHPublicKeys []string `json:"-"`
}

// VMResetPasswordRequest contains parameters for resetting the guest root password.
type VMResetPasswordRequest struct {
	VMID     string
	Password string
}

// VMResetPasswordResponse is the result of a password reset request.
type VMResetPasswordResponse struct {
	VMID   string `json:"vm_id"`
	JobID  int64  `json:"job_id"`
	Status string `json:"status"`
}

// ResetPassword resets the guest root password. It is applied via the guest
// agent on the running VM (cloud images ship qemu-guest-agent).
func (s *VMService) ResetPassword(ctx context.Context, req *VMResetPasswordRequest) (*VMResetPasswordResponse, error) {
	vm, err := s.vmRepo.GetByID(ctx, req.VMID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}
	if vm.Status != models.VMStatusRunning {
		return nil, fmt.Errorf("VM must be running to reset the root password")
	}
	if s.riverClient == nil {
		return nil, fmt.Errorf("job queue unavailable")
	}
	paramsJSON, _ := json.Marshal(map[string]interface{}{"root_password": req.Password})
	result, err := s.riverClient.Insert(ctx, queue.VMOperationJob{
		VMID:      vm.ID,
		Operation: queue.VMOpResetPassword,
		NodeID:    vm.NodeID,
		Params:    paramsJSON,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to enqueue password reset job: %w", err)
	}
	return &VMResetPasswordResponse{VMID: vm.ID, JobID: result.Job.ID, Status: "pending"}, nil
}

// AttachISO enqueues attaching a bootable install/rescue ISO (by image URL or
// on-node path) to a stopped VM.
func (s *VMService) AttachISO(ctx context.Context, vmID, isoImage string) (int64, error) {
	if isoImage == "" {
		return 0, fmt.Errorf("iso image is required")
	}
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrVMNotFound
		}
		return 0, fmt.Errorf("failed to get VM: %w", err)
	}
	if vm.Status == models.VMStatusRunning {
		return 0, fmt.Errorf("stop the VM before attaching an ISO")
	}
	if s.riverClient == nil {
		return 0, fmt.Errorf("job queue unavailable")
	}
	paramsJSON, _ := json.Marshal(map[string]interface{}{"image_path": isoImage})
	res, err := s.riverClient.Insert(ctx, queue.VMOperationJob{
		VMID: vm.ID, Operation: queue.VMOpAttachISO, NodeID: vm.NodeID, Params: paramsJSON,
	}, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to enqueue attach-iso job: %w", err)
	}
	return res.Job.ID, nil
}

// DetachISO enqueues removing the install/rescue ISO from a VM.
func (s *VMService) DetachISO(ctx context.Context, vmID string) (int64, error) {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrVMNotFound
		}
		return 0, fmt.Errorf("failed to get VM: %w", err)
	}
	if s.riverClient == nil {
		return 0, fmt.Errorf("job queue unavailable")
	}
	res, err := s.riverClient.Insert(ctx, queue.VMOperationJob{
		VMID: vm.ID, Operation: queue.VMOpDetachISO, NodeID: vm.NodeID,
	}, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to enqueue detach-iso job: %w", err)
	}
	return res.Job.ID, nil
}

// rescueISOEnv is the env var holding a fallback rescue ISO URL.
const rescueISOEnv = "RESCUE_ISO_URL"

// RescueVM attaches a rescue ISO (which boots first) and marks the VM as in
// rescue mode. The VM must be stopped; starting it afterward boots into rescue.
// isoURL falls back to the RESCUE_ISO_URL env var when empty.
func (s *VMService) RescueVM(ctx context.Context, vmID, isoURL string) (int64, error) {
	if isoURL == "" {
		isoURL = os.Getenv(rescueISOEnv)
	}
	if isoURL == "" {
		return 0, fmt.Errorf("no rescue ISO configured: provide iso_url or set %s", rescueISOEnv)
	}
	// AttachISO validates the VM exists, is stopped, and enqueues the boot-first ISO.
	jobID, err := s.AttachISO(ctx, vmID, isoURL)
	if err != nil {
		return 0, err
	}
	if err := s.db.WithContext(ctx).Model(&models.VM{}).Where("id = ?", vmID).Update("rescue_mode", true).Error; err != nil {
		return 0, fmt.Errorf("failed to mark rescue mode: %w", err)
	}
	return jobID, nil
}

// UnrescueVM detaches the rescue ISO and clears rescue mode. Start the VM to
// boot from disk again.
func (s *VMService) UnrescueVM(ctx context.Context, vmID string) (int64, error) {
	jobID, err := s.DetachISO(ctx, vmID)
	if err != nil {
		return 0, err
	}
	if err := s.db.WithContext(ctx).Model(&models.VM{}).Where("id = ?", vmID).Update("rescue_mode", false).Error; err != nil {
		return 0, fmt.Errorf("failed to clear rescue mode: %w", err)
	}
	return jobID, nil
}

// MigrateVMRequest contains parameters for a live migration.
type MigrateVMRequest struct {
	VMID        string `json:"vm_id" validate:"required,uuid"`
	DestNodeID  string `json:"dest_node_id" validate:"required,uuid"`
	Live        bool   `json:"live"`
	CopyStorage bool   `json:"copy_storage"`
}

// MigrateVM live-migrates a VM to another node by driving libvirt migration on
// the source node's agent, then reassigns the VM to the destination node.
// Nodes do not share storage, so block migration (copy_storage) is the default.
func (s *VMService) MigrateVM(ctx context.Context, req *MigrateVMRequest) error {
	vm, err := s.vmRepo.GetByID(ctx, req.VMID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrVMNotFound
		}
		return fmt.Errorf("failed to get VM: %w", err)
	}
	if vm.NodeID == req.DestNodeID {
		return fmt.Errorf("VM is already on the target node")
	}

	destNode, err := s.nodeRepo.GetByID(ctx, req.DestNodeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNodeNotFound
		}
		return fmt.Errorf("failed to get destination node: %w", err)
	}
	if destNode.Status != models.NodeStatusActive {
		return fmt.Errorf("destination node is not active")
	}

	srcNode, err := s.nodeRepo.GetByID(ctx, vm.NodeID)
	if err != nil {
		return fmt.Errorf("failed to get source node: %w", err)
	}

	agentClient, err := s.getAgentClient(ctx, vm.NodeID)
	if err != nil {
		return fmt.Errorf("failed to connect to source agent: %w", err)
	}

	// Pre-create the destination disk: block migration (copy-storage-all) needs a
	// target file, and the destination node may not have a libvirt storage pool
	// covering the image directory. Best-effort — a pre-existing disk is fine.
	// (Validated live on 167->185: without this, libvirt errors "Storage pool not found".)
	if destAgent, derr := s.getAgentClient(ctx, destNode.ID); derr == nil {
		destCtx := metadata.NewOutgoingContext(ctx, metadata.New(map[string]string{
			"authorization": "Bearer " + destNode.Token,
			"x-node-id":     destNode.ID,
		}))
		_, _ = destAgent.CreateStorageVolume(destCtx, &pb.CreateStorageVolumeRequest{
			PoolType: "dir",
			PoolPath: "", // empty → the destination node's agent uses its own default image dir
			Name:     vm.ID,
			Format:   "qcow2",
			SizeGb:   int64(vm.Resources.Disk),
		})
	}

	// SSH transport so no libvirtd TCP/TLS listener is required on the nodes.
	destURI := fmt.Sprintf("qemu+ssh://root@%s/system", destNode.IPAddress)

	md := metadata.New(map[string]string{
		"authorization": "Bearer " + srcNode.Token,
		"x-node-id":     srcNode.ID,
	})
	authCtx := metadata.NewOutgoingContext(ctx, md)

	resp, err := agentClient.MigrateVM(authCtx, &pb.MigrateVMRequest{
		VmId:        vm.ID,
		DestUri:     destURI,
		Live:        req.Live,
		CopyStorage: req.CopyStorage,
	})
	if err != nil {
		return fmt.Errorf("migration RPC failed: %w", err)
	}
	if !resp.Success {
		msg := resp.Message
		if msg == "" && resp.Error != nil {
			msg = resp.Error.Message
		}
		return fmt.Errorf("migration failed: %s", msg)
	}

	// The domain now lives on the destination node.
	if err := s.vmRepo.UpdateNodeID(ctx, vm.ID, req.DestNodeID); err != nil {
		return fmt.Errorf("migration succeeded but failed to update node assignment: %w", err)
	}
	return nil
}

// RebuildVMResponse contains the result of a rebuild operation
type RebuildVMResponse struct {
	VMID    string `json:"vm_id"`
	Status  string `json:"status"`
	JobID   int64  `json:"job_id"`
	Message string `json:"message,omitempty"`
	// RootPassword is returned only when a password was generated for this
	// rebuild (regenerate requested with no explicit password). Shown once.
	RootPassword string `json:"root_password,omitempty"`
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

	// Resolve the template image path so the agent can fetch/clone it.
	rebuildTmpl, err := s.templateRepo.GetByID(ctx, templateID)
	if err != nil {
		return nil, fmt.Errorf("failed to load template for rebuild: %w", err)
	}

	// Resolve the root password: explicit wins, else generate one when asked.
	rootPassword := req.Password
	generatedPassword := ""
	if rootPassword == "" && req.RegeneratePassword {
		rootPassword = generateRootPassword()
		generatedPassword = rootPassword
	}

	// Preserve the VM's identity & static networking so cloud-init re-applies it
	// on the fresh disk (the regenerated seed replaces the create-time one), and
	// inject the (optional) new root password / selected SSH keys.
	ip, gateway, bridge, prefix, vlan := s.primaryNetworkConfig(ctx, vm.ID)

	// Prepare rebuild params
	params := map[string]interface{}{
		"template_id":    templateID,
		"image_path":     rebuildTmpl.ImagePath,
		"preserve_ip":    req.PreserveIP,
		"vnc_port":       vncConfig.Port,
		"vnc_password":   vncConfig.Password,
		"hostname":       vm.Hostname,
		"root_password":  rootPassword,
		"ssh_public_key": strings.Join(req.SSHPublicKeys, "\n"),
		"ip_address":     ip,
		"gateway":        gateway,
		"netmask":        prefix,
		"bridge":         bridge,
		"vlan_id":        vlan,
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
		VMID:         vm.ID,
		Status:       "pending",
		JobID:        result.Job.ID,
		Message:      "VM rebuild initiated",
		RootPassword: generatedPassword,
	}, nil
}

// generateRootPassword returns a random 16-char alphanumeric password for a
// freshly rebuilt guest's root account (ambiguous characters omitted).
func generateRootPassword() string {
	const charset = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 16)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return generateSecureToken(16)
		}
		b[i] = charset[n.Int64()]
	}
	return string(b)
}

// primaryNetworkConfig returns the static network parameters for a VM's first
// interface (IP, gateway, bridge, prefix length, VLAN), resolved from the owning
// IPAM pool. Zero values mean "not tracked" (the guest then falls back to DHCP).
func (s *VMService) primaryNetworkConfig(ctx context.Context, vmID string) (ip, gateway, bridge string, prefix, vlan int) {
	var netIface models.Network
	if err := s.db.WithContext(ctx).Where("vm_id = ?", vmID).Order("created_at ASC").First(&netIface).Error; err != nil {
		return
	}
	ip = hostOnlyIP(netIface.IPAddress)
	if netIface.VLANID != nil {
		vlan = *netIface.VLANID
	}
	// Resolve gateway/prefix/bridge from the IP's pool via the IPAM address record.
	var addr models.IPAddress
	if err := s.db.WithContext(ctx).Where("vm_id = ?", vmID).First(&addr).Error; err == nil {
		var pool models.IPPool
		if err := s.db.WithContext(ctx).Where("id = ?", addr.PoolID).First(&pool).Error; err == nil {
			gateway = pool.Gateway
			bridge = pool.Bridge
			prefix = prefixFromCIDR(pool.CIDR)
		}
	}
	return
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

	// Generate VNC password if empty
	if vm.VNCPassword == "" {
		vncConfig := s.generateVNCCredentials()
		vm.VNCPassword = vncConfig.Password
		if err := s.db.Model(&models.VM{}).Where("id = ?", vmID).Update("vnc_password", vm.VNCPassword).Error; err != nil {
			s.logger.Error("failed to save generated VNC password", "vm_id", vmID, "error", err)
		}
	}

	// Apply the password to the live domain so the browser (which authenticates
	// against QEMU with exactly this password) matches. Only meaningful while the
	// VM is running; a failure here would otherwise show up as a baffling
	// client-side "Authentication failed", so surface it instead of swallowing it.
	node, err := s.nodeRepo.GetByID(ctx, vm.NodeID)
	if vm.Status == models.VMStatusRunning {
		if syncErr := s.syncVNCPassword(ctx, vm.NodeID, vmID, vm.VNCPassword); syncErr != nil {
			return nil, fmt.Errorf("failed to apply VNC password to the running VM: %w", syncErr)
		}
	}

	response := &GetVNCResponse{
		VMID: vmID,
		Port: *vm.VNCPort,
	}

	if includePassword {
		response.Password = vm.VNCPassword
	}

	if err == nil {
		response.Host = node.IPAddress
	}

	return response, nil
}

// syncVNCPassword applies the VNC password to the live domain via the agent
// (QEMU monitor). Returns an error so callers can decide whether a failure is
// fatal — for the console flow it is, because the browser authenticates against
// QEMU with exactly this password, so a failed sync surfaces as a confusing
// client-side "Authentication failed".
func (s *VMService) syncVNCPassword(ctx context.Context, nodeID, vmID, password string) error {
	if password == "" {
		return fmt.Errorf("no VNC password to sync")
	}

	client, err := s.getAgentClient(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("agent unavailable: %w", err)
	}

	// getAgentClient returns a raw client, so every call must carry the node's
	// auth token itself. Send the Bearer token alongside the password — omitting
	// it is what made this call always fail with "missing authorization header".
	node, err := s.nodeRepo.GetByID(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("failed to load node for VNC sync: %w", err)
	}
	md := metadata.New(map[string]string{
		"authorization": "Bearer " + node.Token,
		"vnc-password":  password,
	})
	mdCtx := metadata.NewOutgoingContext(ctx, md)

	// Bound the RPC explicitly — the agent call goes on to run a libvirt/QEMU
	// monitor command that has no deadline of its own, so without this an
	// unresponsive node/QEMU can hang the console flow indefinitely instead of
	// surfacing an error.
	rpcCtx, cancel := context.WithTimeout(mdCtx, 10*time.Second)
	defer cancel()

	req := &pb.VNCProxyRequest{
		VmId:          vmID,
		ExpirySeconds: 60,
	}

	if _, err = client.StartVNCProxy(rpcCtx, req); err != nil {
		return fmt.Errorf("failed to apply VNC password on node: %w", err)
	}
	s.logger.Info("VNC password synced to agent", "vm_id", vmID)
	return nil
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

	// Apply the new password to the live domain via the agent (QEMU monitor)
	// when the VM is running, so it takes effect immediately on the next
	// console connect rather than only after a restart.
	if vm.Status == models.VMStatusRunning {
		if syncErr := s.syncVNCPassword(ctx, vm.NodeID, vmID, vncConfig.Password); syncErr != nil {
			return nil, fmt.Errorf("failed to apply new VNC password to the running VM: %w", syncErr)
		}
	}

	return vncConfig, nil
}

// SetConsoleEnabled toggles VNC console access for a VM. When disabling, the
// gate in the VNC service drops in-flight sessions and blocks new tokens; the
// underlying VNC password is left untouched so re-enabling restores access.
func (s *VMService) SetConsoleEnabled(ctx context.Context, vmID string, enabled bool) (*models.VM, error) {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}

	if err := s.db.WithContext(ctx).Model(&models.VM{}).
		Where("id = ?", vmID).
		Update("console_enabled", enabled).Error; err != nil {
		return nil, fmt.Errorf("failed to update console state: %w", err)
	}
	vm.ConsoleEnabled = enabled

	s.logger.InfoContext(ctx, "VM console access toggled", "vm_id", vmID, "enabled", enabled)
	return vm, nil
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

// ReconcileNodeVMStatuses queries the agent for the ACTUAL state of every VM on
// a node and syncs the DB status to match. This is what keeps the panel honest
// when a VM is started, stopped, or crashes out-of-band: without it the DB
// status only ever changes on an explicit lifecycle command, so a VM that is
// really running shows as 'stopped' (and vice-versa) indefinitely. Called each
// tick by the metrics collector for every online node.
//
// VMs mid-operation (creating/deleting) are skipped so a stale agent read can't
// clobber an in-flight provision or teardown. A per-VM agent error is logged and
// skipped, never treated as "stopped", so a transient network blip doesn't flip
// a healthy VM offline.
func (s *VMService) ReconcileNodeVMStatuses(ctx context.Context, nodeID string) {
	vms, err := s.vmRepo.ListByNodeID(ctx, nodeID, 0, 0)
	if err != nil {
		s.logger.WarnContext(ctx, "status reconcile: list node VMs failed", "node_id", nodeID, "error", err)
		return
	}
	for i := range vms {
		if vms[i].Status == models.VMStatusCreating || vms[i].Status == models.VMStatusDeleting {
			continue
		}
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		resp, err := s.getVMAgentStatus(sctx, vms[i].ID, nodeID)
		cancel()
		if err != nil {
			s.logger.WarnContext(ctx, "status reconcile: agent status failed", "vm_id", vms[i].ID, "error", err)
			continue
		}
		s.syncVMStatus(ctx, &vms[i], resp.GetState())
	}
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

// VMMetricsResult contains per-VM live metrics
type VMMetricsResult struct {
	CpuPercent           float64
	MemoryUsed           int64
	MemoryTotal          int64
	MemoryUsedPercent    float64
	DiskReadBytesPerSec  int64
	DiskWriteBytesPerSec int64
	NetworkRxBytesPerSec int64
	NetworkTxBytesPerSec int64
}

// GetVMMetrics gets live metrics for a specific VM from agent
func (s *VMService) GetVMMetrics(ctx context.Context, nodeID string, vmID string) (*VMMetricsResult, error) {
	agentClient, err := s.getAgentClient(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to agent: %w", err)
	}

	// Get node for auth token
	node, err := s.nodeRepo.GetByID(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to get node: %w", err)
	}

	// Create auth context
	md := metadata.New(map[string]string{
		"authorization": "Bearer " + node.Token,
	})
	authCtx := metadata.NewOutgoingContext(ctx, md)

	req := &pb.VMMetricsRequest{
		VmIds:      []string{vmID},
		IntervalMs: 1000,
	}

	stream, err := agentClient.StreamVMMetrics(authCtx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to start metrics stream: %w", err)
	}

	// Get first sample
	sample, err := stream.Recv()
	if err != nil {
		return nil, fmt.Errorf("failed to receive metrics: %w", err)
	}

	result := &VMMetricsResult{}
	if sample.Cpu != nil {
		result.CpuPercent = sample.Cpu.UsagePercent
	}
	if sample.Memory != nil {
		result.MemoryUsed = sample.Memory.UsedBytes
		result.MemoryTotal = sample.Memory.TotalBytes
		if result.MemoryTotal > 0 {
			result.MemoryUsedPercent = float64(result.MemoryUsed) / float64(result.MemoryTotal) * 100
		}
	}
	if sample.Disk != nil {
		result.DiskReadBytesPerSec = sample.Disk.ReadBytesPerSec
		result.DiskWriteBytesPerSec = sample.Disk.WriteBytesPerSec
	}
	if sample.Network != nil {
		result.NetworkRxBytesPerSec = sample.Network.RxBytesPerSec
		result.NetworkTxBytesPerSec = sample.Network.TxBytesPerSec
	}

	return result, nil
}
