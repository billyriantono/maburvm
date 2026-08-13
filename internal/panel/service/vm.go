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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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
	// ErrTargetUserNotFound is returned when reassigning a VM to a nonexistent user.
	ErrTargetUserNotFound = errors.New("target user not found")
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
	// ErrIPInUseOnNetwork is returned when the requested IP already answers ARP on
	// the node (it's live — used by a VM the panel doesn't manage), so assigning it
	// would collide with an existing host.
	ErrIPInUseOnNetwork = errors.New("the requested IP is already in use on the network (it answers ARP); pick a different address")
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

	vpcService    *VPCService
	regionService *RegionService

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
	UserID       string           `json:"user_id" validate:"required,uuid"`
	Hostname     string           `json:"hostname" validate:"required,max=100,hostname_rfc1123"`
	OSTemplateID string           `json:"os_template_id" validate:"required,uuid"`
	Resources    models.Resources `json:"resources" validate:"required"`
	NodeID       string           `json:"node_id,omitempty" validate:"omitempty,uuid"` // Optional: specific node
	PlanID       string           `json:"plan_id,omitempty" validate:"omitempty,uuid"` // Optional: derive resources from a plan
	IPPoolID     string           `json:"ip_pool_id,omitempty" validate:"omitempty,uuid"`
	// VPCID places the VM inside a tenant VPC. It resolves to that VPC's private
	// address pool, so a customer never has to know pool or bridge names — and
	// cannot reach another tenant's.
	VPCID string `json:"vpc_id,omitempty" validate:"omitempty,uuid"`
	// Region is the location the customer chose, by id or slug. Required for
	// customer-initiated orders; integrations that predate regions (the WHMCS
	// webhook) may omit it and fall back to the configured default.
	Region string `json:"region,omitempty"`
	// RegionRequired marks a customer-initiated order, where silently choosing a
	// physical location on their behalf is not acceptable.
	RegionRequired bool   `json:"-"`
	RequestedIP    string `json:"requested_ip,omitempty" validate:"omitempty,ip"`
	BandwidthMbps  int    `json:"bandwidth_mbps,omitempty" validate:"omitempty,min=0,max=10000"`
	// Monthly data quota (GB) + over-quota policy, normally inherited from the
	// plan. 0 quota = unlimited. Populated from the plan in CreateVM.
	DataQuotaGB       int64  `json:"data_quota_gb,omitempty" validate:"omitempty,min=0"`
	OverQuotaPolicy   string `json:"over_quota_policy,omitempty" validate:"omitempty,oneof=throttle overage suspend"`
	ThrottleSpeedMbps int    `json:"throttle_speed_mbps,omitempty" validate:"omitempty,min=0"`
	VLANID            int    `json:"vlan_id,omitempty" validate:"omitempty,min=0,max=4094"`
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
		// Inherit the plan's monthly data quota + over-quota policy so the
		// enforcer can act on this VM. Explicit request values (if any) win.
		if req.DataQuotaGB == 0 {
			req.DataQuotaGB = plan.DataQuotaGB
		}
		if req.OverQuotaPolicy == "" {
			req.OverQuotaPolicy = plan.OverQuotaPolicy
		}
		if req.ThrottleSpeedMbps == 0 {
			req.ThrottleSpeedMbps = plan.ThrottleSpeedMbps
		}
	}
	if req.OverQuotaPolicy == "" {
		req.OverQuotaPolicy = models.OverQuotaThrottle
	}

	// Validate resources
	if err := s.validateResources(&req.Resources); err != nil {
		return nil, err
	}

	// Quota admission is enforced inside the create transaction below (Lane D:
	// AdmitVMCreateTx), so it serializes with the VM-row insert and can't
	// double-spend. A cheap non-authoritative precheck here fails fast before we do
	// template/node/IP work for an obviously-over-quota request.
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
	// Resolve the region BEFORE node selection: it constrains which nodes are
	// eligible, and a customer choosing "Jakarta" must not land in Purwokerto.
	var orderRegion *models.Region
	if s.regionService != nil {
		orderRegion, err = s.regionService.ResolveOrderRegion(ctx, req.Region, req.RegionRequired)
		if err != nil {
			return nil, err
		}
	}

	// Resolve a VPC placement BEFORE node selection: a VPC lives in a router
	// namespace on ONE node, so it dictates where the VM goes. Doing this after
	// selection let the scheduler pick a different host, and the VM was then
	// rejected for using a pool that node cannot reach.
	if req.VPCID != "" {
		poolID, vpcNode, verr := s.vpcPoolForUser(ctx, req.UserID, req.VPCID)
		if verr != nil {
			return nil, verr
		}
		// A private network does NOT span regions, so a VM cannot be in one region
		// and in a network that lives in another. Refuse rather than silently
		// honouring one and discarding the other — the VM would come up in a city
		// the customer did not choose, and nothing would say so.
		if orderRegion != nil {
			var vpcNodeRegion *string
			if err := s.db.WithContext(ctx).Model(&models.Node{}).
				Select("region_id").Where("id = ?", vpcNode).Scan(&vpcNodeRegion).Error; err == nil &&
				vpcNodeRegion != nil && *vpcNodeRegion != orderRegion.ID {
				return nil, ErrVPCWrongRegion
			}
		}
		req.IPPoolID = poolID
		req.AutoAssignIP = false
		req.NodeID = vpcNode
	}

	nodeID := req.NodeID
	if nodeID == "" && orderRegion != nil {
		nodeID, err = s.selectNodeInRegion(ctx, orderRegion.ID, req.Resources.Disk)
		if err != nil {
			return nil, err
		}
	}
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

	// Create VM record and any requested IP/network allocation atomically. VNC
	// port 0 means auto-assign — store NULL (not 0, which fails the DB range
	// check); the real port is filled in once the domain starts.
	vm := &models.VM{
		UserID:       req.UserID,
		NodeID:       nodeID,
		Hostname:     req.Hostname,
		OSTemplateID: req.OSTemplateID,
		Resources:    req.Resources,
		Status:       models.VMStatusCreating,
		VNCPassword:  vncConfig.Password,
	}
	if vncConfig.Port > 0 {
		vm.VNCPort = &vncConfig.Port
	}

	// A VPC placement resolves to that VPC's own private pool. Ownership is
	// checked here, so passing another tenant's VPC id simply fails to resolve.
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
			// If a specific IP was requested, make sure it isn't already live on the
			// wire (in use by a VM we don't manage) before handing it out. Best-effort:
			// a probe error (agent down) doesn't block creation — the IPAM reconciler
			// is the backstop.
			if req.RequestedIP != "" {
				if live, perr := s.probeIPsOnNode(ctx, nodeID, pool.Bridge, []string{req.RequestedIP}); perr == nil {
					for _, ip := range live {
						if ip == req.RequestedIP {
							return nil, ErrIPInUseOnNetwork
						}
					}
				}
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
		// Lane D admission: take the per-user lock and re-check quota against
		// authoritative in-tx usage BEFORE inserting the VM row, so concurrent
		// same-user creates serialize here and cannot overcommit.
		if err := s.quotaService.AdmitVMCreateTx(ctx, tx, req.UserID, req.Resources); err != nil {
			return err
		}
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
			// Snapshot the monthly data quota + over-quota policy onto the
			// interface so the bandwidth enforcer can act without re-reading the plan.
			network.BandwidthQuotaGB = req.DataQuotaGB
			network.OverQuotaPolicy = req.OverQuotaPolicy
			network.ThrottleSpeedMbps = req.ThrottleSpeedMbps
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

	// Tell the node which pool to provision into. Without it the agent falls back
	// to /var/lib/libvirt/images — the node's ROOT filesystem — so every VM would
	// land on the OS volume no matter what storage the operator provisioned, and
	// filling that volume takes libvirt and the agent down with it.
	if pool := PrimaryPool(ctx, s.db, nodeID); pool != nil {
		params["pool_path"] = pool.Path
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
// can target a different node (a From → To server migration), in which case the
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

// selectNodeInRegion picks an active node inside one region. A region with no
// active node fails loudly here rather than silently spilling the order into a
// different city.
func (s *VMService) selectNodeInRegion(ctx context.Context, regionID string, diskGB int) (string, error) {
	var nodes []models.Node
	if err := s.db.WithContext(ctx).
		Where("status = ? AND region_id = ?", models.NodeStatusActive, regionID).
		Find(&nodes).Error; err != nil {
		return "", err
	}
	if len(nodes) == 0 {
		return "", ErrRegionNoCapacity
	}
	nodes = s.nodesWithRoom(ctx, nodes, diskGB)
	if len(nodes) == 0 {
		return "", ErrRegionNoCapacity
	}
	s.nodeMutex.Lock()
	defer s.nodeMutex.Unlock()
	return s.selectNodeRoundRobin(nodes), nil
}

// nodesWithRoom drops nodes whose provisioning pool cannot hold the disk.
//
// Placement previously ignored storage entirely — round-robin would happily put
// a VM on a node with nothing left, and the failure surfaced as a provisioning
// error after the customer had already been charged. Nodes whose storage the
// panel has not measured are kept: absence of a reading is not evidence of a
// full disk, and excluding them would stop orders on a healthy node.
func (s *VMService) nodesWithRoom(ctx context.Context, nodes []models.Node, diskGB int) []models.Node {
	if diskGB <= 0 {
		return nodes
	}
	out := make([]models.Node, 0, len(nodes))
	for i := range nodes {
		pool := PrimaryPool(ctx, s.db, nodes[i].ID)
		if PoolFits(pool, diskGB) {
			out = append(out, nodes[i])
			continue
		}
		s.logger.WarnContext(ctx, "node skipped: provisioning pool has no room",
			"node_id", nodes[i].ID, "pool", pool.Path,
			"available_bytes", pool.AvailableSpace, "requested_gb", diskGB)
	}
	return out
}

// SetRegionService wires region resolution for order placement.
func (s *VMService) SetRegionService(r *RegionService) { s.regionService = r }

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

// generateVNCCredentials generates VNC port and password. Port 0 means
// "auto-assign": the agent lets libvirt pick a free VNC port at start time. The
// previous behaviour (a random 5900-5999 port with no collision check) failed to
// start VMs on nodes that already host other VMs — libvirt's "Failed to reserve
// port" — which is every real KVM host. The actual port is read back from the
// agent (GetVMStatus) once the domain is running.
func (s *VMService) generateVNCCredentials() *VNCConfig {
	return &VNCConfig{
		Port:     0, // 0 → agent auto-assigns a free port (autoport)
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
	// Floating IPs survive their VM, so they are not returned to the pool below —
	// but their host-side NAT rules must go, or the address keeps answering and
	// forwarding to a dead VM's address. Done before the transaction because it
	// needs the VM's node and an agent round-trip.
	if vm, err := s.vmRepo.GetByID(ctx, vmID); err == nil && vm != nil {
		s.detachFloatingIPsOnAgentForVM(ctx, vmID, vm.NodeID)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.ipamService.ReleaseAddressesByVMIDInTx(ctx, tx, vmID); err != nil {
			return err
		}
		if err := s.networkRepo.WithDB(tx).DeleteByVMID(ctx, vmID); err != nil {
			return err
		}
		// Release any pending disk-quota reservations for this VM so a removed/soft
		// VM cannot leave orphaned capacity that would over-count forever. This
		// runs in the same local transaction as the VM soft deletion (or cleanup).
		if err := s.quotaService.DeleteDiskReservationsByVMTx(ctx, tx, vmID); err != nil {
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
	// Search matches hostname or VM id across the whole result set, not just the
	// page being returned.
	Search string `json:"search,omitempty" validate:"omitempty,max=253"`
	Limit  int    `json:"limit,omitempty" validate:"omitempty,min=1,max=100"`
	Offset int    `json:"offset,omitempty" validate:"omitempty,min=0"`
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

	// Every supplied filter applies together. The previous switch honoured only
	// the first match, so a customer — whose owner filter is always set for
	// tenant isolation — had their status and node filters silently ignored,
	// and searching returned results that did not match what was asked for.
	filter := repository.VMFilter{
		UserID: req.UserID,
		NodeID: req.NodeID,
		Status: req.Status,
		Search: req.Search,
	}

	vms, err := s.vmRepo.ListFiltered(ctx, filter, limit, req.Offset)
	if err != nil {
		return nil, fmt.Errorf("failed to list VMs: %w", err)
	}
	total, err := s.vmRepo.CountFiltered(ctx, filter)

	if err != nil {
		return nil, fmt.Errorf("failed to count VMs: %w", err)
	}

	s.fillVMRegions(ctx, vms)

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
// fillVMRegions annotates VMs with where they physically run. Customers reason
// in regions, never in nodes, so this is what lets them tell which private
// networks and floating IPs a given VM can actually use.
func (s *VMService) fillVMRegions(ctx context.Context, vms []models.VM) {
	if len(vms) == 0 {
		return
	}
	byNode := RegionsByNode(ctx, s.db)
	for i := range vms {
		if reg, ok := byNode[vms[i].NodeID]; ok {
			vms[i].RegionID, vms[i].RegionName, vms[i].RegionCountry = reg.ID, reg.Name, reg.Country
		}
	}
}

// SetVPCService wires tenant VPC lookups for VM placement.
func (s *VMService) SetVPCService(v *VPCService) { s.vpcService = v }

// vpcPoolForUser resolves a tenant's VPC to the private IP pool its VMs draw
// from, refusing a VPC the user does not own.
func (s *VMService) vpcPoolForUser(ctx context.Context, userID, vpcID string) (string, string, error) {
	if s.vpcService == nil {
		return "", "", fmt.Errorf("VPC support is not configured")
	}
	vpc, err := s.vpcService.Get(ctx, userID, vpcID)
	if err != nil {
		return "", "", err
	}
	if vpc.Bridge == "" || vpc.NodeID == nil || *vpc.NodeID == "" {
		return "", "", fmt.Errorf("VPC %q is not provisioned on a node yet", vpc.Name)
	}
	var pool models.IPPool
	if err := s.db.WithContext(ctx).Where("bridge = ?", vpc.Bridge).First(&pool).Error; err != nil {
		return "", "", fmt.Errorf("VPC %q has no address pool", vpc.Name)
	}
	return pool.ID, *vpc.NodeID, nil
}

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

	one := []models.VM{vm.VM}
	s.fillVMRegions(ctx, one)
	vm.VM = one[0]

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
//
// Gate-1 disk admission: capacity is reserved BEFORE the agent AttachDisk RPC so
// a concurrent extra-disk admission cannot double-spend the user's disk quota.
// The reservation is counted against quota (alongside boot disks and active
// vm_disks) until it is consumed. On agent failure the reservation is released
// (capacity returned). On agent success the reservation is atomically consumed
// and the vm_disks row is created in the SAME transaction; if that final DB
// recording fails, the reservation is RETAINED fail-closed (NOT released) so the
// capacity is never leaked or bypassed. No TTL/automatic expiry exists; a pending
// reservation intentionally overcounts until explicitly consumed or released.
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

	// 1) Reserve capacity (per-user admission lock + disk quota evaluation +
	//    pending reservation insert) BEFORE driving the agent.
	res, err := s.quotaService.ReserveDiskQuota(ctx, vm.UserID, vm.ID, sizeGB)
	if err != nil {
		return nil, err
	}

	// 2) Drive the agent AttachDisk RPC (outside any DB transaction).
	client, err := s.getAgentClient(ctx, vm.NodeID)
	if err != nil {
		// Agent unreachable: release the reservation, capacity is returned.
		_ = s.quotaService.ReleaseDiskReservation(ctx, res.ID)
		return nil, err
	}
	authCtx, err := s.agentAuthContext(ctx, vm.NodeID)
	if err != nil {
		_ = s.quotaService.ReleaseDiskReservation(ctx, res.ID)
		return nil, err
	}
	resp, err := client.AttachDisk(authCtx, &pb.AttachDiskRequest{VmId: vmID, SizeGb: int64(sizeGB)})
	if err != nil {
		_ = s.quotaService.ReleaseDiskReservation(ctx, res.ID)
		return nil, fmt.Errorf("agent attach disk failed: %w", err)
	}
	if !resp.Success {
		_ = s.quotaService.ReleaseDiskReservation(ctx, res.ID)
		return nil, fmt.Errorf("attach disk failed: %s", agentErrorMessage(resp.Error))
	}

	// 3) Agent succeeded: atomically consume the reservation AND record the
	//    vm_disks row in the same transaction. If this recording fails, we retain
	//    the reservation fail-closed (do NOT release) so capacity cannot be leaked
	//    to a later quota-bypassing attach.
	disk := &models.VMDisk{VMID: vmID, Device: resp.Device, SizeGB: sizeGB, Path: resp.Path}
	recErr := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Canonical finalize: re-locks the pending reservation under the owner's
		// admit lock, derives vm_id/size_gb from it, creates the vm_disks row from
		// the agent's device/path, and consumes the reservation — all atomically.
		_, ferr := s.quotaService.LockAndFinalizeReservationTx(ctx, tx, res.ID, disk)
		return ferr
	})
	if recErr != nil {
		return nil, fmt.Errorf("disk attached on node but failed to record it (reservation retained): %w", recErr)
	}
	return disk, nil
}

// DetachDisk detaches a data disk (by virtio device, e.g. "vdb") from the VM and
// optionally deletes its backing volume.
//
// Gate-1 accounting: the disk's usage is released ONLY after the agent confirms
// the detach. If the agent call fails, the vm_disks row is left intact so the
// quota accounting stays correct (we never release capacity the disk still holds
// on the node). On agent success the vm_disks row is deleted; that row IS the
// accounting for an attached (consumed) disk, so pending reservations are left
// untouched — clearing them here would race a concurrent AttachDisk's in-flight
// reservation for the same VM.
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
	// Agent succeeded: release the disk's accounting by deleting its vm_disks row.
	// An attached disk is a CONSUMED reservation already turned into this row, so the
	// row IS the accounting — there is no pending reservation to touch here. We must
	// NOT bulk-clear pending reservations for the VM: a concurrent AttachDisk may hold
	// an in-flight pending reservation for the same VM and deleting it would corrupt
	// that attach's finalize. Deleting a consumed row is a monotonic capacity release,
	// so it needs no admission lock.
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
	// UserID, when set, reassigns VM ownership to another user (admin-only;
	// enforced at the handler). Empty leaves ownership unchanged.
	UserID string `json:"user_id,omitempty" validate:"omitempty,uuid"`
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

	// Owner reassignment (admin-only; enforced at the handler). Validate the
	// target user exists before transferring ownership. Persisted by whichever
	// update path runs below (both write the full VM row).
	if req.UserID != "" && req.UserID != vm.UserID {
		var cnt int64
		if err := s.db.WithContext(ctx).Model(&models.User{}).Where("id = ?", req.UserID).Count(&cnt).Error; err != nil {
			return nil, fmt.Errorf("failed to validate target user: %w", err)
		}
		if cnt == 0 {
			return nil, ErrTargetUserNotFound
		}
		vm.UserID = req.UserID
	}

	// Resource change → Lane D admission + persistence in one transaction.
	if req.Resources != nil {
		if err := s.validateResources(req.Resources); err != nil {
			return nil, err
		}
		// Disk can only grow — a qcow2/guest filesystem can't be safely shrunk, and
		// the agent's resize is grow-only, so reject shrink to keep DB == reality.
		if req.Resources.Disk < vm.Resources.Disk {
			return nil, fmt.Errorf("disk can only be grown (current %dGB, requested %dGB)", vm.Resources.Disk, req.Resources.Disk)
		}
		oldRes := vm.Resources
		// Admit + persist atomically: the per-user lock + authoritative in-tx quota
		// check serialize with the resource write, so a resize can't overcommit.
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := s.quotaService.AdmitVMResizeTx(ctx, tx, vm.UserID, oldRes, *req.Resources); err != nil {
				return err
			}
			vm.Resources = *req.Resources
			return s.vmRepo.WithDB(tx).Update(ctx, vm)
		}); err != nil {
			return nil, err
		}

		// Enqueue the resize job only after the new resources are committed.
		paramsJSON, _ := json.Marshal(map[string]interface{}{"resources": *req.Resources})
		job := queue.VMOperationJob{VMID: vm.ID, Operation: queue.VMOpResize, NodeID: vm.NodeID, Params: paramsJSON}
		if _, err := s.riverClient.Insert(ctx, job, nil); err != nil {
			return nil, fmt.Errorf("failed to enqueue VM resize job: %w", err)
		}
		return vm, nil
	}

	// Hostname-only (or no-op) update.
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
	case LifecycleSuspend:
		operation = queue.VMOpSuspend
		vmCommand = pb.VMCommandType_VM_COMMAND_TYPE_PAUSE
	case LifecycleUnsuspend:
		operation = queue.VMOpUnsuspend
		vmCommand = pb.VMCommandType_VM_COMMAND_TYPE_RESUME
	default:
		// Rebuild is intentionally NOT a lifecycle command — it goes through
		// RebuildVM, which carries the root password + SSH keys the guest needs.
		return nil, fmt.Errorf("invalid lifecycle command: %s", req.Command)
	}

	if err := s.ensureVMNodeActive(ctx, vm.NodeID); err != nil {
		return nil, err
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

	var config *pb.VMConfig
	if req.Command == LifecycleStart {
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

	// A command the node refused is a failure, and must reach the caller as one.
	// It used to be returned with a nil error, so the handler answered 200, the
	// UI showed nothing, and the audit log recorded the attempt as though it had
	// worked — a VM that never booted left a "vm.start … new_state: RUNNING"
	// entry behind it.
	//
	// The exception is a command that was already satisfied: asking a running
	// domain to start returns Success=false with "already running", and treating
	// that as an error would make an idempotent action look broken.
	if !resp.Success && !stateSatisfiesCommand(command, resp.State) {
		s.logger.WarnContext(ctx, "node refused lifecycle command",
			"vm_id", vm.ID, "command", command, "message", resp.Message, "state", resp.State.String())
		return &LifecycleResponse{
			VMID:     vm.ID,
			Command:  string(req.Command),
			Success:  false,
			Message:  resp.Message,
			NewState: resp.State.String(),
		}, fmt.Errorf("%w: %s", ErrVMLifecycleFailed, resp.Message)
	}

	return &LifecycleResponse{
		VMID:     vm.ID,
		Command:  string(req.Command),
		Success:  resp.Success,
		Message:  resp.Message,
		NewState: resp.State.String(),
	}, nil
}

// stateSatisfiesCommand reports whether the domain is already in the state the
// command was asking for, which makes a refusal harmless rather than a failure.
func stateSatisfiesCommand(command pb.VMCommandType, state pb.VMState) bool {
	switch command {
	case pb.VMCommandType_VM_COMMAND_TYPE_START, pb.VMCommandType_VM_COMMAND_TYPE_RESUME:
		return state == pb.VMState_VM_STATE_RUNNING
	case pb.VMCommandType_VM_COMMAND_TYPE_STOP, pb.VMCommandType_VM_COMMAND_TYPE_FORCE_STOP:
		return state == pb.VMState_VM_STATE_STOPPED
	case pb.VMCommandType_VM_COMMAND_TYPE_PAUSE:
		return state == pb.VMState_VM_STATE_PAUSED
	default:
		return false
	}
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

	// The VNC port is auto-assigned by libvirt at start time (see
	// generateVNCCredentials), so the stored value may be NULL (not started yet)
	// or stale. When the VM is running, read the real port from the agent and
	// persist it — otherwise the console would connect to the wrong port or, for a
	// freshly-created VM, have no port at all.
	if vm.Status == models.VMStatusRunning {
		if st, serr := s.getVMAgentStatus(ctx, vmID, vm.NodeID); serr == nil && st.GetVncPort() > 0 {
			livePort := int(st.GetVncPort())
			if vm.VNCPort == nil || *vm.VNCPort != livePort {
				if uerr := s.vmRepo.UpdateVNCPort(ctx, vmID, livePort); uerr != nil {
					s.logger.WarnContext(ctx, "failed to persist live VNC port", "vm_id", vmID, "error", uerr)
				}
			}
			vm.VNCPort = &livePort
		}
	}

	if vm.VNCPort == nil {
		return nil, fmt.Errorf("VNC is not available yet (the VM must be running to assign a port)")
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
	// VM is running.
	//
	// This is best-effort, NOT fatal. Imported (e.g. from another platform) domains often
	// run VNC with no password auth (QEMU security type "None"); QMP set_password
	// then fails because auth can't be enabled at runtime — but the console still
	// works fine without a password (noVNC just skips the auth handshake). Hard-
	// failing here is what made GetVNCConfig return 500 "Failed to get VNC config"
	// for every imported VM. Freshly-created MaburVM domains are defined with a
	// passwd attribute, so the sync succeeds and the browser password matches; a
	// rare failure there degrades to a client-side "Authentication failed" (same
	// as before this endpoint existed) instead of blocking the whole console.
	node, err := s.nodeRepo.GetByID(ctx, vm.NodeID)
	if vm.Status == models.VMStatusRunning {
		if syncErr := s.syncVNCPassword(ctx, vm.NodeID, vmID, vm.VNCPassword); syncErr != nil {
			s.logger.WarnContext(ctx, "failed to apply VNC password to running VM; serving console without runtime password sync (normal for imported open-VNC domains)",
				"vm_id", vmID, "error", syncErr)
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

	// Enforce it on the domain, not just in this table. The flag alone gates the
	// panel's proxy while qemu keeps listening on 0.0.0.0, so a console reported
	// as disabled stayed reachable by anyone who could route to the node.
	s.applyConsoleAccess(ctx, vm, enabled)

	s.logger.InfoContext(ctx, "VM console access toggled", "vm_id", vmID, "enabled", enabled)
	return vm, nil
}

// applyConsoleAccess pushes the console decision down to the node and records
// the port it reports back.
//
// Best-effort: a node that cannot be reached leaves the panel's flag in place,
// which still blocks the panel's own console route. The gap that leaves — direct
// access to the node's VNC socket — is logged rather than hidden, because an
// operator who disabled a console needs to know it did not fully take.
func (s *VMService) applyConsoleAccess(ctx context.Context, vm *models.VM, enabled bool) {
	client, err := s.getAgentClient(ctx, vm.NodeID)
	if err != nil {
		s.logger.ErrorContext(ctx, "console access not enforced on node; the VNC socket is unchanged",
			"vm_id", vm.ID, "enabled", enabled, "error", err)
		return
	}
	authCtx, err := s.agentAuthContext(ctx, vm.NodeID)
	if err != nil {
		s.logger.ErrorContext(ctx, "console access not enforced on node", "vm_id", vm.ID, "error", err)
		return
	}
	authCtx, cancel := context.WithTimeout(authCtx, 20*time.Second)
	defer cancel()

	resp, err := client.SetConsoleAccess(authCtx, &pb.SetConsoleAccessRequest{
		VmId:        vm.ID,
		Enabled:     enabled,
		VncPassword: vm.VNCPassword,
	})
	if err != nil {
		s.logger.ErrorContext(ctx, "console access not enforced on node; the VNC socket is unchanged",
			"vm_id", vm.ID, "enabled", enabled, "error", err)
		return
	}
	if !resp.GetSuccess() {
		s.logger.ErrorContext(ctx, "node refused the console change; the VNC socket is unchanged",
			"vm_id", vm.ID, "enabled", enabled, "message", resp.GetError().GetMessage())
		return
	}

	if resp.GetVncPort() > 0 {
		port := int(resp.GetVncPort())
		if uerr := s.vmRepo.UpdateVNCPort(ctx, vm.ID, port); uerr != nil {
			s.logger.WarnContext(ctx, "failed to persist VNC port", "vm_id", vm.ID, "error", uerr)
		} else {
			vm.VNCPort = &port
		}
	} else if enabled {
		// Enabled but the domain has no console device: clear the port rather
		// than keep a number that will not connect.
		if uerr := s.vmRepo.ClearVNCPort(ctx, vm.ID); uerr == nil {
			vm.VNCPort = nil
		}
	}

	if resp.GetRestartRequired() {
		s.logger.InfoContext(ctx, "console listen address applied to the stored definition; takes effect on next boot",
			"vm_id", vm.ID, "enabled", enabled)
	}
}

// RepairConsole makes the VNC console work for a VM whose libvirt domain has no
// <graphics> device at all — the case for many imported VMs, whose
// stored vnc_port is fictional and which `virsh vncdisplay` reports as having no
// VNC. It asks the agent to inject a VNC graphics device into the persistent
// domain XML; because graphics is not hot-pluggable, a RUNNING VM is RESTARTED by
// the agent. Callers MUST gate this behind an explicit confirm. Afterwards the
// live autoport VNC port is read from the agent and persisted, and console access
// is enabled.
func (s *VMService) RepairConsole(ctx context.Context, vmID string) (*models.VM, error) {
	vm, err := s.vmRepo.GetByIDWithNode(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}

	client, err := s.getAgentClient(ctx, vm.NodeID)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to agent: %w", err)
	}
	authCtx, err := s.agentAuthContext(ctx, vm.NodeID)
	if err != nil {
		return nil, err
	}
	// The agent restarts the VM if it was running, so allow a generous deadline.
	authCtx, cancel := context.WithTimeout(authCtx, 60*time.Second)
	defer cancel()

	resp, err := client.ExecuteVMCommand(authCtx, &pb.VMCommandRequest{
		VmId:    vmID,
		Command: pb.VMCommandType_VM_COMMAND_TYPE_REPAIR_CONSOLE,
		Async:   false,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to repair console on node: %w", err)
	}
	if !resp.GetSuccess() {
		return nil, fmt.Errorf("failed to repair console: %s", resp.GetMessage())
	}
	s.syncVMStatus(ctx, vm, resp.GetState())

	// Enable console access (was likely off for an imported VM) and refresh the
	// stored VNC port from the live autoport so the console connects to the right
	// place without waiting for the reconcile loop.
	if err := s.db.WithContext(ctx).Model(&models.VM{}).
		Where("id = ?", vmID).Update("console_enabled", true).Error; err != nil {
		s.logger.WarnContext(ctx, "failed to enable console after repair", "vm_id", vmID, "error", err)
	} else {
		vm.ConsoleEnabled = true
	}

	if st, serr := s.getVMAgentStatus(ctx, vmID, vm.NodeID); serr == nil && st.GetVncPort() > 0 {
		livePort := int(st.GetVncPort())
		if uerr := s.vmRepo.UpdateVNCPort(ctx, vmID, livePort); uerr != nil {
			s.logger.WarnContext(ctx, "failed to persist VNC port after repair", "vm_id", vmID, "error", uerr)
		}
		vm.VNCPort = &livePort
	}

	s.logger.InfoContext(ctx, "VM console repaired (VNC graphics injected)", "vm_id", vmID)
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

// GetLatestVMOperation returns the most recent tracked operation (e.g. a delete)
// for a VM, or (nil, nil) when none exists. The row outlives the VM on delete, so
// it stays readable through completion.
func (s *VMService) GetLatestVMOperation(ctx context.Context, vmID string) (*models.VMOperation, error) {
	var op models.VMOperation
	err := s.db.WithContext(ctx).Where("vm_id = ?", vmID).Order("started_at DESC").First(&op).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &op, nil
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
			// The node is reachable but has no such domain — the VM was removed
			// or failed to provision out-of-band. Reconcile it to stopped (unless
			// it's already a terminal stopped/error) and don't warn: this is a
			// steady state, not a transient failure worth logging every tick.
			if status.Code(err) == codes.NotFound {
				if vms[i].Status != models.VMStatusStopped && vms[i].Status != models.VMStatusError {
					if uerr := s.vmRepo.UpdateStatus(ctx, vms[i].ID, models.VMStatusStopped); uerr != nil {
						s.logger.WarnContext(ctx, "status reconcile: mark stopped failed", "vm_id", vms[i].ID, "error", uerr)
					}
				}
				continue
			}
			s.logger.WarnContext(ctx, "status reconcile: agent status failed", "vm_id", vms[i].ID, "error", err)
			continue
		}
		s.syncVMStatus(ctx, &vms[i], resp.GetState())
		// Keep the stored VNC port in sync with libvirt's auto-assigned one so the
		// console connects to the right port (and the UI shows it) without waiting
		// for someone to open the console.
		if lp := int(resp.GetVncPort()); lp > 0 && (vms[i].VNCPort == nil || *vms[i].VNCPort != lp) {
			if uerr := s.vmRepo.UpdateVNCPort(ctx, vms[i].ID, lp); uerr != nil {
				s.logger.WarnContext(ctx, "status reconcile: persist VNC port failed", "vm_id", vms[i].ID, "error", uerr)
			}
		}
	}
}

// stuckVMDeadline is how long a VM may sit in a transient creating/deleting
// state before the reaper assumes its job was lost (panel crashed between the
// row commit and the River insert, or the job died) and resolves it. Must exceed
// the worst-case legitimate create/delete time (create includes a ~2-minute
// post-provision reboot window + River retry backoff).
const stuckVMDeadline = 15 * time.Minute

// ReapStuckVMs resolves VMs wedged in a transient state past stuckVMDeadline,
// closing the durability gap where a VM row is committed but its River job never
// got enqueued (crash window in CreateVM) or a delete job was lost:
//   - creating: probe the agent. Domain present -> adopt real status. Domain
//     absent -> the create never landed; mark error so the row is operable
//     (retry/delete) instead of a permanent ghost.
//   - deleting: re-enqueue an idempotent, unique-gated delete so cleanup
//     eventually completes and the IP/network allocation is released.
func (s *VMService) ReapStuckVMs(ctx context.Context, nodeID string) {
	vms, err := s.vmRepo.ListByNodeID(ctx, nodeID, 0, 0)
	if err != nil {
		s.logger.WarnContext(ctx, "reaper: list node VMs failed", "node_id", nodeID, "error", err)
		return
	}
	cutoff := time.Now().Add(-stuckVMDeadline)
	for i := range vms {
		vm := &vms[i]
		if vm.UpdatedAt.After(cutoff) {
			continue
		}
		switch vm.Status {
		case models.VMStatusCreating:
			sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			resp, aerr := s.getVMAgentStatus(sctx, vm.ID, nodeID)
			cancel()
			if aerr != nil {
				if status.Code(aerr) == codes.NotFound {
					s.logger.WarnContext(ctx, "reaper: create never completed, marking error", "vm_id", vm.ID, "stuck_since", vm.UpdatedAt)
					if uerr := s.vmRepo.UpdateStatus(ctx, vm.ID, models.VMStatusError); uerr != nil {
						s.logger.WarnContext(ctx, "reaper: mark error failed", "vm_id", vm.ID, "error", uerr)
					}
				}
				// Agent unreachable/other error: leave it; retry next tick.
				continue
			}
			s.syncVMStatus(ctx, vm, resp.GetState())
		case models.VMStatusDeleting:
			if s.riverClient == nil {
				continue
			}
			// UniqueOpts skips this if a delete is still in-flight; the worker is
			// idempotent (domain-not-found == success).
			if _, ierr := s.riverClient.Insert(ctx, queue.VMOperationJob{
				VMID:      vm.ID,
				Operation: queue.VMOpDelete,
				NodeID:    vm.NodeID,
			}, nil); ierr != nil {
				s.logger.WarnContext(ctx, "reaper: re-enqueue delete failed", "vm_id", vm.ID, "error", ierr)
			} else {
				s.logger.WarnContext(ctx, "reaper: re-enqueued stuck delete", "vm_id", vm.ID, "stuck_since", vm.UpdatedAt)
			}
		}
	}
}

// externalIPNote marks an IPAddress the panel auto-reserved because an ARP probe
// found it live on the wire (in use by a VM the panel doesn't manage). The IP
// reconciler only ever flips IPs bearing this note back to available, so it never
// disturbs admin-reserved or assigned addresses.
const externalIPNote = "auto-detected in use on the network"

// probeIPsOnNode asks the node agent to ARP-probe ips on a bridge and returns the
// subset that are live (in use) on the wire.
func (s *VMService) probeIPsOnNode(ctx context.Context, nodeID, bridge string, ips []string) ([]string, error) {
	if bridge == "" || len(ips) == 0 {
		return nil, nil
	}
	client, err := s.getAgentClient(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	authCtx, err := s.agentAuthContext(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	resp, err := client.ProbeIPs(authCtx, &pb.ProbeIPsRequest{
		Bridge:      bridge,
		IpAddresses: ips,
		TimeoutMs:   1000,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetInUse(), nil
}

// ReconcileNodePoolIPs ARP-probes each of the node's pool IPs and keeps IPAM in
// sync with reality: an available IP that answers ARP (used by an unmanaged VM,
// e.g. a pre-existing imported guest) is auto-reserved so it's never
// allocated, and a previously auto-reserved IP that has gone quiet is released
// back to available. Only 'available' and self-reserved (externalIPNote) rows
// are ever touched — assigned/disabled/admin-reserved addresses are left alone.
func (s *VMService) ReconcileNodePoolIPs(ctx context.Context, nodeID string) {
	pools, err := s.ipamService.ListPoolsForNode(ctx, nodeID)
	if err != nil {
		s.logger.WarnContext(ctx, "IP reconcile: list pools failed", "node_id", nodeID, "error", err)
		return
	}
	for i := range pools {
		pool := pools[i]
		if pool.Bridge == "" {
			continue
		}
		var addrs []models.IPAddress
		if err := s.db.WithContext(ctx).
			Where("pool_id = ? AND (status = ? OR (status = ? AND note = ?))",
				pool.ID, models.IPAddressStatusAvailable, models.IPAddressStatusReserved, externalIPNote).
			Find(&addrs).Error; err != nil {
			s.logger.WarnContext(ctx, "IP reconcile: list addresses failed", "pool_id", pool.ID, "error", err)
			continue
		}
		if len(addrs) == 0 {
			continue
		}
		ips := make([]string, len(addrs))
		for j := range addrs {
			ips[j] = addrs[j].Address
		}
		live, err := s.probeIPsOnNode(ctx, nodeID, pool.Bridge, ips)
		if err != nil {
			s.logger.WarnContext(ctx, "IP reconcile: probe failed", "node_id", nodeID, "bridge", pool.Bridge, "error", err)
			continue
		}
		liveSet := make(map[string]bool, len(live))
		for _, ip := range live {
			liveSet[ip] = true
		}
		for j := range addrs {
			a := addrs[j]
			switch {
			case liveSet[a.Address] && a.Status == models.IPAddressStatusAvailable:
				// Guard on status so we never clobber an IP that was just assigned.
				s.db.WithContext(ctx).Model(&models.IPAddress{}).
					Where("id = ? AND status = ?", a.ID, models.IPAddressStatusAvailable).
					Updates(map[string]interface{}{"status": models.IPAddressStatusReserved, "note": externalIPNote})
			case !liveSet[a.Address] && a.Status == models.IPAddressStatusReserved && a.Note == externalIPNote:
				s.db.WithContext(ctx).Model(&models.IPAddress{}).
					Where("id = ? AND status = ? AND note = ?", a.ID, models.IPAddressStatusReserved, externalIPNote).
					Updates(map[string]interface{}{"status": models.IPAddressStatusAvailable, "note": ""})
			}
		}
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
