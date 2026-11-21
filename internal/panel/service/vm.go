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
	"slices"
	"strconv"
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
	// ErrTemplateNotOffered is returned when a template's availability rules
	// (its plan list, or its licence) do not cover the order.
	ErrTemplateNotOffered = errors.New("this operating system is not offered on this plan")
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
	// ErrRebuildNotPossible is returned when a VM cannot take the system asked for
	ErrRebuildNotPossible = errors.New("reinstall not possible")
	// ErrVMCannotBeModified is returned when trying to modify a VM that's not stopped
	ErrVMCannotBeModified = errors.New("VM cannot be modified while running")
	// ErrUEFINotSupported refuses a template that boots with UEFI on a node
	// whose agent does not boot UEFI guests (models.FeatureUEFI).
	ErrUEFINotSupported = errors.New("this template boots with UEFI, which this node's agent cannot boot yet; use a BIOS template, or a node whose agent boots UEFI guests")
)

// isInstallableImagePath reports whether a template image path can seed a new VM.
// The "/imported" sentinel (and an empty path) mark templates that exist only to
// associate already-imported VMs; they have no provisionable base image.
func isInstallableImagePath(path string) bool {
	p := strings.TrimSpace(path)
	return p != "" && p != "/imported"
}

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

	vpcService    *VPCService
	regionService *RegionService

	// sendLimits stands in for the node in tests; nil sends to the node.
	sendLimits func(ctx context.Context, nodeID string, req *pb.SetVMLimitsRequest) (*pb.SetVMLimitsResponse, error)

	// ensureLink sets up the migration link between nodes; a cross-node
	// clone copies the disk over it. Nil refuses cross-node clones.
	ensureLink func(ctx context.Context, nodeIDs ...string) error

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
		db:           db,
		vmRepo:       vmRepo,
		nodeRepo:     nodeRepo,
		templateRepo: templateRepo,
		networkRepo:  repository.NewNetworkRepository(db),
		planRepo:     repository.NewPlanRepository(db),
		ipamService:  NewIPAMService(db, repository.NewIPAMRepository(db)),
		quotaService: NewQuotaService(db, vmRepo),
		riverClient:  riverClient,
		logger:       logger,
		grpcConns:    make(map[string]*grpc.ClientConn),
	}
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
	// The status is as the collector last set it. Dialling here made every VM
	// list wait three seconds for each node that is down.
	for _, n := range nodes {
		result[n.ID] = NodeSummary{Name: n.Name, Status: n.Status}
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
	// IPv6PoolID, when set, delegates one routed prefix (a /64) from that pool
	// to the VM alongside its IPv4 address. Requires an IPv4 pool.
	IPv6PoolID string `json:"ipv6_pool_id,omitempty" validate:"omitempty,uuid"`
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
	RegionRequired bool `json:"-"`
	// PlanRequired marks a customer-initiated order. Its size, speed cap and
	// transfer quota come from the plan alone; whatever else the request says
	// about them is dropped.
	PlanRequired bool `json:"-"`
	// Billed marks an order the billing system placed. The account quota limits
	// what a customer adds on their own, not what they have paid for.
	Billed bool `json:"-"`
	// BillingRef is the billing system's reference for the service this VM is
	// provisioned for. Kept on the VM; empty for anything not ordered through billing.
	BillingRef string `json:"-"`
	// AnyHostname skips the "unique among the owner's VMs" check. Set for
	// orders from the billing system, which sends whatever name the customer
	// typed: a paid order is never refused over a name.
	AnyHostname   bool   `json:"-"`
	RequestedIP   string `json:"requested_ip,omitempty" validate:"omitempty,ip"`
	BandwidthMbps int    `json:"bandwidth_mbps,omitempty" validate:"omitempty,min=0,max=10000"`
	// Monthly data quota (GB) + over-quota policy, normally inherited from the
	// plan. 0 quota = unlimited. Populated from the plan in CreateVM.
	DataQuotaGB       int64  `json:"data_quota_gb,omitempty" validate:"omitempty,min=0"`
	OverQuotaPolicy   string `json:"over_quota_policy,omitempty" validate:"omitempty,oneof=throttle overage suspend"`
	ThrottleSpeedMbps int    `json:"throttle_speed_mbps,omitempty" validate:"omitempty,min=0"`
	VLANID            int    `json:"vlan_id,omitempty" validate:"omitempty,min=0,max=4094"`
	// CPUModel is the guest CPU model. Empty → host-passthrough, which moves live
	// only between nodes with the same CPU. e.g. "host-model", "Haswell".
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
	// Restore, when set, says CloneSourceRef is a backup in object storage:
	// the node uses it once, as the new disk, and checks it first.
	Restore *RestoreSource `json:"-"`
	// BlockSMTP blocks outbound mail whatever the plan says: a copy of a VM
	// whose mail was blocked stays blocked.
	BlockSMTP bool `json:"-"`
	// Limits are the VM's CPU and disk caps, set by staff. Nil takes the plan's.
	Limits *models.VMLimits `json:"-"`
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
	// AutoIPv6 delegates a /64 from a routed IPv6 pool on the chosen node
	// when IPv6PoolID is empty: what a customer gets by ticking IPv6.
	AutoIPv6 bool `json:"-"`
	// StoragePoolID puts the VM's disk in that pool (staff only); its node is
	// then the VM's node. Empty takes the plan's storage tier, else the node's
	// primary pool.
	StoragePoolID string `json:"-"`
	// DriversISO, for a VM installed from an ISO, also puts the drivers ISO
	// staff chose (System settings) in the second CD drive.
	DriversISO bool `json:"-"`
}

// ErrNoIPv6Here refuses an order for IPv6 where the location has none.
var ErrNoIPv6Here = errors.New("IPv6 is not offered in this location yet: order without it, or pick another location")

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
	if req.PlanRequired {
		if req.PlanID == "" {
			return nil, ErrPlanRequired
		}
		req.BandwidthMbps, req.DataQuotaGB, req.OverQuotaPolicy, req.ThrottleSpeedMbps = 0, 0, "", 0
		req.Limits = nil
	}
	// The VM's CPU and disk caps: the request's own (staff), else the plan's,
	// else the IOPS figure the API has always accepted with the resources.
	var limits models.VMLimits
	if req.Limits != nil {
		limits = *req.Limits
	} else if req.Resources.IOPS != nil {
		limits.IOPS = *req.Resources.IOPS
	}
	// If a plan (flavor) is selected, derive resources + bandwidth from it.
	blockSMTP := req.BlockSMTP
	// natSize > 0 makes this a NAT VM: a private address and that many public
	// ports on its node. defaultGroup replaces the built-in default rules.
	natSize, defaultGroup := 0, ""
	if req.PlanID != "" {
		plan, err := s.planRepo.GetByID(ctx, req.PlanID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrPlanNotFound
			}
			return nil, fmt.Errorf("failed to get plan: %w", err)
		}
		if !plan.IsActive {
			return nil, ErrPlanInactive
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
		blockSMTP = blockSMTP || plan.SMTPPolicy == models.SMTPBlocked
		if req.Limits == nil {
			limits = plan.Limits
		}
		if plan.NetworkType == models.PlanNetworkNAT {
			natSize = natPorts(plan)
		}
		if plan.DefaultFirewallGroupID != nil {
			defaultGroup = *plan.DefaultFirewallGroupID
		}
		if req.AutoIPv6 && plan.MaxExtraIPv6 != nil && *plan.MaxExtraIPv6 == 0 {
			return nil, &AllowanceError{Message: "This plan does not include IPv6."}
		}
	}
	if req.OverQuotaPolicy == "" {
		req.OverQuotaPolicy = models.OverQuotaThrottle
	}

	// Validate resources
	if err := s.validateResources(&req.Resources); err != nil {
		return nil, err
	}
	if err := limits.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidResources, err)
	}

	// Quota admission is enforced inside the create transaction below (Lane D:
	// AdmitVMCreateTx), so it serializes with the VM-row insert and can't
	// double-spend. A cheap non-authoritative precheck here fails fast before we do
	// template/node/IP work for an obviously-over-quota request.
	if err := s.quotaService.CheckCanCreate(ctx, req.UserID, req.Resources); err != nil && !billedOverQuota(req.Billed, err) {
		return nil, err
	}

	// A name is unique among the owner's own VMs, not across customers.
	if !req.AnyHostname {
		exists, err := s.vmRepo.HostnameExistsForUser(ctx, req.UserID, req.Hostname, "")
		if err != nil {
			return nil, fmt.Errorf("failed to check hostname: %w", err)
		}
		if exists {
			return nil, ErrHostnameExists
		}
	}

	// Get OS template
	template, err := s.templateRepo.GetByID(ctx, req.OSTemplateID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTemplateNotFound
		}
		return nil, fmt.Errorf("failed to get template: %w", err)
	}
	// An inactive template only bars a fresh install. When the disk is cloned —
	// from a captured image or another VM — the template is a label recording
	// what the machine runs, and the bytes come from somewhere else entirely.
	//
	// Deactivating a template means "stop offering this for new installs". Taking
	// it to mean "images captured from it can no longer be used" makes every such
	// image unusable, which is what happened: creating from an image resolves the
	// image's own template, and an imported VM's template is never active.
	if req.CloneSourceRef == "" && !template.IsActive {
		return nil, fmt.Errorf("OS template is not active")
	}
	// A real install needs a real base image. Imported templates carry the
	// "/imported" placeholder (their VMs already have disks) and can't seed a new
	// VM. Clones bypass this — they copy the source VM's disk via CloneSourceRef.
	if req.CloneSourceRef == "" && !isInstallableImagePath(template.ImagePath) {
		return nil, ErrTemplateNotInstallable
	}
	// Staff install anything. A customer needs the template offered on their
	// plan; a billed order is already priced, so only a plan list refuses it.
	if (req.PlanRequired && !template.OfferedOn(req.PlanID)) || (req.Billed && template.RestrictedFrom(req.PlanID)) {
		return nil, ErrTemplateNotOffered
	}
	// An ISO is installed from onto an empty disk; it is never a disk itself.
	fromISO := template.IsISO()
	if fromISO && (req.CloneSourceRef != "" || req.Restore != nil) {
		return nil, fmt.Errorf("%w: an ISO cannot be the source of a copy", ErrTemplateNotInstallable)
	}

	// The plan's storage tier, or the pool staff named, decides where the disk
	// goes; a named pool also names the node.
	storageTier := planStorageTier(ctx, s.db, req.PlanID)
	if req.StoragePoolID != "" {
		var p models.StoragePool
		if err := s.db.WithContext(ctx).Where("id = ?", req.StoragePoolID).First(&p).Error; err != nil {
			return nil, fmt.Errorf("%w: storage pool %s not found", ErrInvalidStoragePool, req.StoragePoolID)
		}
		if req.NodeID != "" && req.NodeID != p.NodeID {
			return nil, fmt.Errorf("%w: storage pool %s is on another node than the one chosen", ErrInvalidStoragePool, p.Name)
		}
		req.NodeID, storageTier = p.NodeID, ""
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

	// The plan's hypervisor group narrows every automatic choice of node; a
	// node chosen by staff or a VPC is taken as given.
	group := planNodeGroup(ctx, s.db, req.PlanID)

	// A NAT VM needs a node with a NAT pool to take its address from.
	if natSize > 0 && req.NodeID == "" && req.IPPoolID == "" {
		regionID := ""
		if orderRegion != nil {
			regionID = orderRegion.ID
		}
		if req.NodeID, err = s.selectNATNode(ctx, regionID, group, storageTier, req.Resources); err != nil {
			return nil, err
		}
	}

	nodeID := req.NodeID
	if nodeID == "" && orderRegion != nil {
		nodeID, err = s.selectNodeInRegion(ctx, orderRegion.ID, group, storageTier, req.Resources)
		if err != nil {
			return nil, err
		}
	}
	if nodeID == "" {
		nodeID, err = s.selectNode(ctx, group, storageTier, req.Resources)
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
		if node.CreationPaused {
			return nil, fmt.Errorf("selected node is not taking new VMs (creation is paused on it)")
		}
	}

	// A UEFI image boots only on a node whose agent boots UEFI guests.
	uefi := req.CloneSourceRef == "" && template.Firmware == models.FirmwareUEFI
	if uefi && !s.nodeHasFeature(ctx, nodeID, models.FeatureUEFI) {
		return nil, ErrUEFINotSupported
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
			if natSize > 0 && !pool.NAT {
				return nil, fmt.Errorf("%w: a NAT plan's VMs take their address from a NAT pool", ErrNoNATPool)
			}
			if pool.NAT && natSize == 0 {
				natSize = DefaultNATPorts
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
	} else if natSize > 0 {
		pools, err := s.ipamService.ListPoolsForNode(ctx, nodeID)
		if err != nil {
			return nil, fmt.Errorf("failed to list IP pools for node: %w", err)
		}
		if poolCandidates = usableNATPools(pools); len(poolCandidates) == 0 {
			return nil, ErrNoNATPool
		}
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
			if pools[i].Bridge != "" && pools[i].Gateway != "" && !pools[i].NAT {
				poolCandidates = append(poolCandidates, pools[i].ID)
			}
		}
		if len(poolCandidates) == 0 {
			return nil, ErrNoUsablePool
		}
	}

	if req.AutoIPv6 && req.IPv6PoolID == "" && len(poolCandidates) > 0 {
		pools, perr := s.ipamService.ListPoolsForNode(ctx, nodeID)
		if perr != nil {
			return nil, fmt.Errorf("failed to list IP pools for node: %w", perr)
		}
		for i := range pools {
			if pools[i].Family == models.IPFamilyIPv6 && pools[i].IsDelegated() {
				req.IPv6PoolID = pools[i].ID
				break
			}
		}
		if req.IPv6PoolID == "" {
			return nil, ErrNoIPv6Here
		}
	}

	// A delegated IPv6 prefix travels on the IPv4 interface's config, so it
	// needs one to travel on. Checked before the transaction so a bad pool id
	// fails fast instead of after the VM row exists.
	if req.IPv6PoolID != "" {
		if len(poolCandidates) == 0 {
			return nil, fmt.Errorf("ipv6_pool_id requires an IPv4 pool (ip_pool_id)")
		}
		v6pool, perr := s.ipamService.GetPool(ctx, req.IPv6PoolID)
		if perr != nil {
			return nil, fmt.Errorf("failed to load selected IPv6 pool: %w", perr)
		}
		if !v6pool.IsDelegated() || v6pool.Family != models.IPFamilyIPv6 {
			return nil, ErrPoolNotDelegated
		}
	}

	var allocatedIP *models.IPAddress
	var allocatedV6 *models.VMIPv6Prefix
	var diskPool *models.StoragePool
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lane D admission: take the per-user lock and re-check quota against
		// authoritative in-tx usage BEFORE inserting the VM row, so concurrent
		// same-user creates serialize here and cannot overcommit.
		if err := s.quotaService.AdmitVMCreateTx(ctx, tx, req.UserID, req.Resources); err != nil && !billedOverQuota(req.Billed, err) {
			return err
		}
		// Node vCPU and RAM overcommit, checked in the same transaction for the
		// same reason.
		if err := AdmitNodeVCPU(ctx, tx, vm.NodeID, req.Resources.CPU); err != nil {
			return err
		}
		if err := s.quotaService.AdmitNodeRAMTx(ctx, tx, vm.NodeID, req.Resources.RAM); err != nil {
			return err
		}
		// Storage is admitted here, not only at placement: a node the caller
		// named, or one picked without a region, never went through placement.
		var perr error
		if req.StoragePoolID != "" {
			diskPool, perr = chosenPool(ctx, tx, req.StoragePoolID, vm.NodeID)
		} else {
			diskPool, perr = NodeDiskPool(ctx, tx, vm.NodeID, storageTier)
		}
		if perr != nil {
			return perr
		}
		if err := lvmPoolUsable(diskPool); err != nil {
			return err
		}
		if err := AdmitPoolDisk(ctx, tx, diskPool, req.Resources.Disk); err != nil {
			return err
		}
		if err := s.vmRepo.WithDB(tx).Create(ctx, vm); err != nil {
			return fmt.Errorf("failed to create VM record: %w", err)
		}
		if diskPool != nil {
			if err := tx.Exec("UPDATE vms SET storage_pool_id = ? WHERE id = ?", diskPool.ID, vm.ID).Error; err != nil {
				return fmt.Errorf("failed to record the VM's storage pool: %w", err)
			}
		}
		if req.PlanID != "" || req.BillingRef != "" {
			if err := writeBillingLink(tx, vm, strPtrOrNil(req.PlanID), strPtrOrNil(req.BillingRef)); err != nil {
				return err
			}
		}
		if blockSMTP {
			if err := tx.Exec("UPDATE vms SET smtp_blocked = TRUE WHERE id = ?", vm.ID).Error; err != nil {
				return fmt.Errorf("failed to record the plan's mail block: %w", err)
			}
			vm.SMTPBlocked = true
		}
		if !limits.IsZero() {
			if err := writeVMLimits(tx, vm.ID, limits); err != nil {
				return err
			}
			vm.Limits = limits
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
					return fmt.Errorf("no IP address available in any pool for the selected node: %w", ErrNoAvailableIPAddress)
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
			// A NAT VM's block of public ports, the first one forwarded to the
			// way in (SSH, or Remote Desktop on Windows).
			if natSize > 0 {
				block, err := allocateNATPortBlockTx(ctx, tx, nodeID, vm.ID, natSize)
				if err != nil {
					return err
				}
				first := &models.PortForward{VMID: vm.ID, NetworkID: network.ID, NodeID: nodeID, ExternalPort: block.StartPort,
					InternalPort: remotePort(template.OSType), Protocol: "tcp", SourceIP: "0.0.0.0/0"}
				if err := tx.Create(first).Error; err != nil {
					return fmt.Errorf("failed to forward the VM's first port: %w", err)
				}
			}
		}
		if req.IPv6PoolID != "" {
			p, err := s.ipamService.AllocateIPv6PrefixInTx(ctx, tx, req.IPv6PoolID, vm.ID, nodeID)
			if err != nil {
				return err
			}
			allocatedV6 = p
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
	// Windows does not finish installing on less, and the node keeps a disk
	// at least as large as the image, so a smaller plan would be given more
	// than it pays for. Clones are exempt: their disk already exists.
	if template.OSType == models.OSTypeWindows && req.CloneSourceRef == "" &&
		(req.Resources.RAM < models.WindowsMinRAMMB || req.Resources.Disk < models.WindowsMinDiskGB) {
		return nil, fmt.Errorf("%w: Windows Server needs at least %d GB of memory and a %d GB disk; choose a larger size",
			ErrInvalidResources, models.WindowsMinRAMMB/1024, models.WindowsMinDiskGB)
	}
	// What the node checks the download against, and the ISOs an install
	// from an ISO starts with.
	metadata := map[string]string{}
	if fromISO {
		metadata[models.InstallISOKey], imagePath = template.ImagePath, models.BlankImageRef
		if template.ChecksumSHA256 != "" {
			metadata[models.InstallISOSHA256Key] = template.ChecksumSHA256
		}
		if req.DriversISO {
			drivers, derr := DriversISOTemplate(ctx, s.db)
			if derr != nil {
				return nil, derr
			}
			metadata[models.DriversISOKey] = drivers.ImagePath
			if drivers.ChecksumSHA256 != "" {
				metadata[models.DriversISOSHA256Key] = drivers.ChecksumSHA256
			}
		}
	} else if req.CloneSourceRef == "" && template.ChecksumSHA256 != "" {
		metadata[models.ImageSHA256Key] = template.ChecksumSHA256
	}
	if uefi {
		metadata[models.FirmwareKey] = models.FirmwareUEFI
	}
	// The node is told what kind of system this is with the image itself.
	imagePath = models.GuestImageRef(template.OSType, imagePath)
	linuxGuest := template.OSType == "" || template.OSType == models.OSTypeLinux
	if fromISO {
		// Nothing is installed to take a password, keys or a script: whoever
		// installs it from the console sets it up.
		req.UserData, req.SSHPublicKeys, req.Password, req.RegeneratePassword = "", nil, "", false
	} else if !linuxGuest {
		// First-boot scripts and SSH keys are delivered through cloud-init and
		// the Linux filesystem. Another kind of system has neither, and an
		// order that carries them (billing sends a fixed set) must not fail.
		req.UserData = ""
		req.SSHPublicKeys = nil
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
	if allocatedV6 != nil {
		params["ipv6_prefix"] = allocatedV6.Prefix
		params["ipv6_address"] = allocatedV6.Address()
		params["ipv6_gateway"] = models.IPv6LinkLocalGateway
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
	// A router's image has an empty admin password, a Windows one a password
	// nobody knows, and neither has keys to fall back on: both always get one.
	generatePassword := (req.RegeneratePassword || !linuxGuest) && !fromISO
	if rootPassword == "" && generatePassword {
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
	if req.Password == "" && generatePassword {
		generatedPassword = rootPassword
	}

	// Tell the node which pool to provision into. Without it the agent falls back
	// to /var/lib/libvirt/images — the node's ROOT filesystem — so every VM would
	// land on the OS volume no matter what storage the operator provisioned, and
	// filling that volume takes libvirt and the agent down with it.
	if diskPool != nil {
		params["pool_path"] = diskPool.Path
		if models.IsLVMPoolType(diskPool.Type) {
			params["pool_type"] = diskPool.Type
		}
	}
	if len(metadata) > 0 {
		params["metadata"] = metadata
	}
	if req.Restore != nil {
		req.Restore.apply(params)
	}

	sealJobSecrets(params)
	paramsJSON, _ := json.Marshal(params)

	// Keep a generated password, encrypted and for a short time, so the owner
	// can read it from the VM's page: whoever ordered through billing never
	// sees this response. A password the caller chose is theirs and is not kept.
	if generatedPassword != "" {
		if err := StoreInitialPassword(ctx, s.db, vm.ID, generatedPassword); err != nil {
			s.logger.WarnContext(ctx, "could not keep the generated root password for its owner", "vm_id", vm.ID, "error", err)
		}
	}

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

	// Seed the default inbound firewall for the new VM. Best-effort: a VM
	// without its seed is no worse off than before (fully open), so never fail
	// the create for it.
	// A router is not given one: it carries tunnels and routing protocols no
	// short list of ports can anticipate, and it has a firewall of its own.
	if template.OSType != models.OSTypeRouterOS {
		if defaultGroup != "" {
			s.attachDefaultGroup(ctx, vm.ID, defaultGroup, template.OSType)
		} else {
			s.seedDefaultFirewall(ctx, vm.ID, template.OSType)
		}
	}

	return &CreateVMResponse{
		VM:           vm,
		JobID:        result.Job.ID,
		Status:       "pending",
		RootPassword: generatedPassword,
	}, nil
}

// sealJobSecrets encrypts the passwords a job carries, so river_job rows never
// hold them in clear; the worker opens them (queue.openJobSecret).
func sealJobSecrets(params map[string]interface{}) {
	for _, k := range []string{"root_password", "vnc_password"} {
		if v, ok := params[k].(string); ok {
			if sealed, err := models.SealSecret(v); err == nil {
				params[k] = sealed
			}
		}
	}
}

// seedDefaultFirewall inserts the default inbound ruleset for a NEWLY created
// VM: allow SSH (tcp/22), ICMP, HTTP (tcp/80) and HTTPS (tcp/443) from
// anywhere. Everything else inbound is dropped by the agent: once a VM has ANY
// firewall rules, its applier appends a per-VM default DROP, and return
// traffic for outbound connections passes via the chain's conntrack
// ESTABLISHED/RELATED accept. Existing VMs (no rules) are untouched — an empty
// rule set still means "everything open".
//
// The rules are enforced on the host by the post-create network-config apply
// in the create worker (and by any later firewall resync).
func (s *VMService) seedDefaultFirewall(ctx context.Context, vmID, osType string) {
	// The way in: SSH, or Remote Desktop on Windows.
	remote := "22"
	if osType == models.OSTypeWindows {
		remote = "3389"
	}
	rules := []models.FirewallRule{
		{VMID: vmID, Protocol: "tcp", PortRange: remote, Action: "allow", Direction: "inbound", SourceIP: "0.0.0.0/0", Priority: 10},
		{VMID: vmID, Protocol: "icmp", Action: "allow", Direction: "inbound", SourceIP: "0.0.0.0/0", Priority: 20},
		{VMID: vmID, Protocol: "tcp", PortRange: "80", Action: "allow", Direction: "inbound", SourceIP: "0.0.0.0/0", Priority: 30},
		{VMID: vmID, Protocol: "tcp", PortRange: "443", Action: "allow", Direction: "inbound", SourceIP: "0.0.0.0/0", Priority: 40},
	}
	if err := s.db.WithContext(ctx).Create(&rules).Error; err != nil {
		s.logger.WarnContext(ctx, "failed to seed default firewall rules; VM inbound traffic is unrestricted",
			"vm_id", vmID, "error", err)
	}
}

// attachDefaultGroup gives a new VM its plan's firewall group in place of the
// built-in default rules, then opens the new system's way in if the group does
// not. Best-effort like the seed: a VM without it is open, not shut.
func (s *VMService) attachDefaultGroup(ctx context.Context, vmID, groupID, osType string) {
	link := models.VMFirewallGroup{VMID: vmID, GroupID: groupID, Position: models.FirewallGroupAfter, SortOrder: 1}
	if err := s.db.WithContext(ctx).Create(&link).Error; err != nil {
		s.logger.WarnContext(ctx, "failed to attach the plan's firewall group; VM inbound traffic is unrestricted",
			"vm_id", vmID, "group_id", groupID, "error", err)
		return
	}
	s.openRemoteAccess(ctx, vmID, osType)
}

// selectNATNode places a NAT VM on an active node (in the region, when given)
// that offers NAT ports and has a NAT pool to take its address from.
func (s *VMService) selectNATNode(ctx context.Context, regionID, group, tier string, res models.Resources) (string, error) {
	q := s.db.WithContext(ctx).Where("status = ? AND nat_port_end > 0", models.NodeStatusActive)
	if regionID != "" {
		q = q.Where("region_id = ?", regionID)
	}
	var nodes, withPool []models.Node
	if err := q.Find(&nodes).Error; err != nil {
		return "", err
	}
	for _, n := range nodes {
		if pools, err := s.ipamService.ListPoolsForNode(ctx, n.ID); err == nil && len(usableNATPools(pools)) > 0 {
			withPool = append(withPool, n)
		}
	}
	if len(withPool) == 0 {
		return "", ErrNoNATPool
	}
	withPool, err := inNodeGroup(withPool, group)
	if err != nil {
		return "", err
	}
	return s.placeVMTier(ctx, withPool, res, true, tier)
}

// portInSpec reports whether a rule's port spec ("22", "20-30", "22,80")
// covers port; an empty spec covers every port.
func portInSpec(spec string, port int) bool {
	if strings.TrimSpace(spec) == "" {
		return true
	}
	for _, part := range strings.Split(spec, ",") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(part), "-")
		a, err := strconv.Atoi(lo)
		if err != nil {
			continue
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(hi); err != nil {
				continue
			}
		}
		if port >= a && port <= b {
			return true
		}
	}
	return false
}

// openRemoteAccess makes sure a VM that changes its kind of system can still
// be reached once it has. A VM with firewall rules drops everything they do
// not allow, and the rules it was given at creation open the old system's way
// in: SSH for Linux, Remote Desktop for Windows. A VM with no rules is open
// already and is left that way. Rules from attached groups count.
func (s *VMService) openRemoteAccess(ctx context.Context, vmID, osType string) {
	ports := []string{"22"}
	switch osType {
	case models.OSTypeWindows:
		ports = []string{"3389"}
	case models.OSTypeRouterOS:
		ports = []string{"22", "8291"} // SSH and WinBox
	}
	rules, err := repository.NewFirewallRepository(s.db).ListEffectiveByVMID(ctx, vmID)
	if err != nil || len(rules) == 0 {
		return
	}
	var own []models.FirewallRule
	if err := s.db.WithContext(ctx).Where("vm_id = ?", vmID).Find(&own).Error; err != nil {
		return
	}
	priority := 0
	for _, r := range own {
		priority = max(priority, r.Priority)
	}
	for _, port := range ports {
		n, _ := strconv.Atoi(port)
		if slices.ContainsFunc(rules, func(r models.FirewallRule) bool {
			return r.Direction == "inbound" && r.Action == "allow" && (r.Protocol == "tcp" || r.Protocol == "all") && portInSpec(r.PortRange, n)
		}) {
			continue
		}
		priority = min(priority+10, 1000)
		rule := models.FirewallRule{VMID: vmID, Protocol: "tcp", PortRange: port, Action: "allow", Direction: "inbound", SourceIP: "0.0.0.0/0", Priority: priority}
		if err := s.db.WithContext(ctx).Create(&rule).Error; err != nil {
			s.logger.WarnContext(ctx, "could not open the new system's remote access port", "vm_id", vmID, "port", port, "error", err)
		}
	}
}

// CloneVMRequest describes a VM-clone operation.
type CloneVMRequest struct {
	SourceVMID string `json:"-"`
	Hostname   string `json:"hostname"`     // new hostname; defaults to "<source>-clone"
	DestNodeID string `json:"dest_node_id"` // target node; defaults to the source's node
	// ByCustomer applies the template's availability rules: a copy of a
	// licensed system is one more licence.
	ByCustomer bool `json:"-"`
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
	if VMMigrating(ctx, s.db, src.ID) {
		return nil, ErrVMMigrating
	}

	if req.ByCustomer {
		planID := ""
		if src.PlanID != nil {
			planID = *src.PlanID
		}
		if tmpl, terr := s.templateRepo.GetByID(ctx, src.OSTemplateID); terr == nil && !tmpl.OfferedOn(planID) {
			return nil, ErrTemplateNotOffered
		}
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
	// the source disk over the migration link (the source node's IP in the ref).
	cloneRef := "vm://" + src.ID
	if destNodeID != src.NodeID {
		srcNode, nerr := s.nodeRepo.GetByID(ctx, src.NodeID)
		if nerr != nil {
			return nil, fmt.Errorf("failed to resolve source node for cross-node clone: %w", nerr)
		}
		if s.ensureLink == nil {
			return nil, fmt.Errorf("copying a VM to another node is not available here")
		}
		if err := s.ensureLink(ctx, src.NodeID, destNodeID); err != nil {
			return nil, fmt.Errorf("the VM cannot be copied to that node: %w", err)
		}
		cloneRef = fmt.Sprintf("vm://%s/%s", hostOnlyIP(srcNode.IPAddress), src.ID)
	}

	create := &CreateVMRequest{
		UserID:         src.UserID,
		Hostname:       hostname,
		OSTemplateID:   src.OSTemplateID,
		Resources:      src.Resources,
		NodeID:         destNodeID,
		CloneSourceRef: cloneRef,
	}
	s.inheritNetworkLimits(ctx, src.ID, create)
	return s.CreateVM(ctx, create)
}

// inheritNetworkLimits gives a copy the speed cap, transfer quota and mail
// block its source has, and its CPU and disk limits with any throttle folded
// in: copying a VM must not be a way around either.
func (s *VMService) inheritNetworkLimits(ctx context.Context, srcVMID string, req *CreateVMRequest) {
	var blocked []bool
	if err := s.db.WithContext(ctx).Table("vms").Where("id = ?", srcVMID).Pluck("smtp_blocked", &blocked).Error; err == nil && len(blocked) == 1 {
		req.BlockSMTP = blocked[0]
	}
	if src, err := s.vmRepo.GetByID(ctx, srcVMID); err == nil {
		limits := src.Limits.WithThrottle(src.Throttle)
		req.Limits = &limits
	}
	n, err := s.networkRepo.GetByVMID(ctx, srcVMID)
	if err != nil || n == nil {
		return
	}
	req.BandwidthMbps = int(n.BandwidthLimit)
	req.DataQuotaGB = n.BandwidthQuotaGB
	req.OverQuotaPolicy = n.OverQuotaPolicy
	req.ThrottleSpeedMbps = n.ThrottleSpeedMbps
}

// billedOverQuota reports that err is only the account quota saying no to
// something the billing system ordered. Every other refusal still stands,
// including a managed account whose quota cannot be resolved.
func billedOverQuota(billed bool, err error) bool {
	return billed && errors.Is(err, ErrQuotaExceeded)
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

// selectNodeInRegion picks a node inside one region. A region with no node
// that can take the VM fails loudly here rather than silently spilling the
// order into a different city.
func (s *VMService) selectNodeInRegion(ctx context.Context, regionID, group, tier string, res models.Resources) (string, error) {
	var nodes []models.Node
	if err := s.db.WithContext(ctx).
		Where("status = ? AND region_id = ?", models.NodeStatusActive, regionID).
		Find(&nodes).Error; err != nil {
		return "", err
	}
	nodes, err := inNodeGroup(nodes, group)
	if err != nil {
		return "", err
	}
	id, err := s.placeVMTier(ctx, nodes, res, true, tier)
	if errors.Is(err, ErrNoAvailableNodes) {
		return "", ErrRegionNoCapacity
	}
	return id, err
}

// placeVM picks the node for a new VM: only nodes taking VMs, preferring the
// ones that answered their last check, then only those where the VM fits (RAM
// and vCPUs under their ratios, the disk in the provisioning pool, the same
// checks admission makes), then the best of those by rankNodes.
//
// When no node fits, strict refuses; otherwise the best node is returned
// anyway, so admission refuses with the reason, which says more than "no nodes".
func (s *VMService) placeVM(ctx context.Context, nodes []models.Node, res models.Resources, strict bool) (string, error) {
	return s.placeVMTier(ctx, nodes, res, strict, "")
}

// placeVMTier is placeVM for a disk that must go in a pool of the storage
// tier ("" for the node's primary pool): a node with no room there does not fit.
func (s *VMService) placeVMTier(ctx context.Context, nodes []models.Node, res models.Resources, strict bool, tier string) (string, error) {
	nodes = nodesTakingVMs(nodes)
	if len(nodes) == 0 {
		return "", ErrNoAvailableNodes
	}
	loads, err := loadNodes(ctx, s.db, nodesReporting(nodes))
	if err != nil {
		return "", err
	}
	fit := make([]nodeLoad, 0, len(loads))
	for _, n := range loads {
		reason := ""
		switch {
		case !n.ramFits(res.RAM):
			reason = "RAM allocation would pass the overcommit ratio"
		case !n.vcpuFits(res.CPU):
			reason = "vCPU allocation would pass the overcommit ratio"
		case res.Disk > 0 || tier != "":
			pool, err := NodeDiskPool(ctx, s.db, n.Node.ID, tier)
			if err == nil {
				err = AdmitPoolDisk(ctx, s.db, pool, res.Disk)
			}
			if err != nil {
				reason = err.Error()
			}
		}
		if reason != "" {
			s.logger.WarnContext(ctx, "node skipped for a new VM", "node_id", n.Node.ID, "reason", reason)
			continue
		}
		fit = append(fit, n)
	}
	if len(fit) == 0 {
		if strict {
			return "", ErrNoAvailableNodes
		}
		fit = loads
	}
	return rankNodes(fit, res.RAM)[0].Node.ID, nil
}

// SetLinkEnsurer wires the migration link a cross-node clone copies over.
func (s *VMService) SetLinkEnsurer(f func(ctx context.Context, nodeIDs ...string) error) {
	s.ensureLink = f
}

// SetRegionService wires region resolution for order placement.
func (s *VMService) SetRegionService(r *RegionService) { s.regionService = r }

// nodesTakingVMs drops nodes where an operator has paused VM creation.
// Filtered here rather than in SQL so every placement path shares one rule.
func nodesTakingVMs(nodes []models.Node) []models.Node {
	out := nodes[:0:0]
	for _, n := range nodes {
		if !n.CreationPaused {
			out = append(out, n)
		}
	}
	return out
}

// nodesReporting drops nodes that missed their last check when another node
// answered it: an active node whose agent has stopped answering would take the
// order and fail it. When none answered, all are kept and the order is tried.
func nodesReporting(nodes []models.Node) []models.Node {
	out := nodes[:0:0]
	for _, n := range nodes {
		if n.FailedPolls == 0 {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nodes
	}
	return out
}

// selectNode picks a node among all active ones for an order with no region.
func (s *VMService) selectNode(ctx context.Context, group, tier string, res models.Resources) (string, error) {
	nodes, err := s.nodeRepo.ListActive(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list active nodes: %w", err)
	}
	if nodes, err = inNodeGroup(nodes, group); err != nil {
		return "", err
	}
	return s.placeVMTier(ctx, nodes, res, false, tier)
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
		if err := s.ipamService.ReleaseIPv6PrefixByVMInTx(ctx, tx, vmID); err != nil {
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
	// Tag keeps VMs carrying this tag.
	Tag string `json:"tag,omitempty" validate:"omitempty,max=32"`
	// Access lists a customer's own VMs and those their team memberships
	// cover, instead of UserID.
	Access *repository.VMAccessScope `json:"-"`
	// ProjectID keeps the VMs in one project.
	ProjectID string `json:"project_id,omitempty" validate:"omitempty,uuid"`
	Limit     int    `json:"limit,omitempty" validate:"omitempty,min=1,max=100"`
	Offset    int    `json:"offset,omitempty" validate:"omitempty,min=0"`
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
		UserID:    req.UserID,
		NodeID:    req.NodeID,
		Status:    req.Status,
		Search:    req.Search,
		Tag:       req.Tag,
		Access:    req.Access,
		ProjectID: req.ProjectID,
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
	// IPv6 is the VM's delegated routed prefix, nil when it has none.
	IPv6 *VMIPv6 `json:"ipv6,omitempty"`
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

// GetVMStatus returns a VM's stored status.
func (s *VMService) GetVMStatus(ctx context.Context, vmID string) (models.VMStatus, error) {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrVMNotFound
		}
		return "", fmt.Errorf("failed to get VM: %w", err)
	}
	if vm.Suspended {
		return models.VMStatusSuspended, nil
	}
	return vm.Status, nil
}

// setSuspended writes the suspension flag. It is the only writer.
func (s *VMService) setSuspended(ctx context.Context, vm *models.VM, on bool) error {
	// By table name, not by model: the field is read-only on the model (so a
	// full-row save cannot overwrite it), and an update through the model is
	// silently dropped for a read-only field.
	if err := s.db.WithContext(ctx).Table("vms").Where("id = ?", vm.ID).Update("suspended", on).Error; err != nil {
		return fmt.Errorf("failed to update suspension: %w", err)
	}
	vm.Suspended = on
	return nil
}

// SuspendVM is the billing suspend: flag the VM, then power it off. The flag
// goes first so the owner cannot start it again in between, and it holds
// across node reboots, which a paused domain does not.
func (s *VMService) SuspendVM(ctx context.Context, vmID string) error {
	if VMMigrating(ctx, s.db, vmID) {
		return ErrVMMigrating
	}
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrVMNotFound
		}
		return fmt.Errorf("failed to get VM: %w", err)
	}
	was := vm.Suspended
	if err := s.setSuspended(ctx, vm, true); err != nil {
		return err
	}
	s.emitVMEvent(ctx, BillingEventVMSuspended, vmID)
	if !was {
		s.emailOwner(ctx, vmID, EmailVMSuspended)
	}
	if vm.Status == models.VMStatusRunning || vm.Status == models.VMStatusSuspended {
		if _, err := s.ExecuteLifecycleCommand(ctx, &LifecycleRequest{VMID: vmID, Command: LifecycleForceStop}); err != nil {
			return fmt.Errorf("VM flagged suspended but could not be powered off: %w", err)
		}
	}
	return s.vmRepo.UpdateStatus(ctx, vmID, models.VMStatusSuspended)
}

// UnsuspendVM lifts a suspension and boots the VM. The flag is cleared first:
// if the node cannot start the VM right now, the owner still gets it back and
// can start it themselves.
func (s *VMService) UnsuspendVM(ctx context.Context, vmID string) error {
	if VMMigrating(ctx, s.db, vmID) {
		return ErrVMMigrating
	}
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrVMNotFound
		}
		return fmt.Errorf("failed to get VM: %w", err)
	}
	was := vm.Suspended
	if err := s.setSuspended(ctx, vm, false); err != nil {
		return err
	}
	if was {
		s.emailOwner(ctx, vmID, EmailVMUnsuspended)
	}
	if vm.Status == models.VMStatusSuspended {
		if err := s.vmRepo.UpdateStatus(ctx, vmID, models.VMStatusStopped); err != nil {
			return err
		}
	}
	s.emitVMEvent(ctx, BillingEventVMUnsuspended, vmID)
	if _, err := s.ExecuteLifecycleCommand(ctx, &LifecycleRequest{VMID: vmID, Command: LifecycleStart}); err != nil {
		return fmt.Errorf("suspension lifted, but the VM did not start: %w", err)
	}
	return nil
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
	if s.ipamService != nil {
		response.IPv6, _ = s.ipamService.GetVMIPv6(ctx, vm.ID)
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
// then records it. The agent picks the next free virtio target. The disk is
// created in the VM's own storage pool.
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
	if VMMigrating(ctx, s.db, vmID) {
		return nil, ErrVMMigrating
	}
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

	if err := checkExtraDiskAllowance(ctx, s.db, vm); err != nil {
		return nil, err
	}

	// The disk goes beside the VM's own, never onto the node's root filesystem,
	// and its pool must have room for it as it would for a new VM.
	pool := VMPool(ctx, s.db, vm.ID, vm.NodeID)
	if err := lvmPoolUsable(pool); err != nil {
		return nil, err
	}
	if !PoolFits(pool, sizeGB) {
		return nil, fmt.Errorf("%w: storage pool %s has %d GB free, and a %d GB disk needs that much plus %d GB kept free",
			ErrNodeCapacityExceeded, pool.Name, pool.AvailableSpace/bytesPerGB, sizeGB, diskHeadroomBytes/bytesPerGB)
	}
	poolPath, poolType := "", ""
	if pool != nil {
		poolPath = pool.Path
		if models.IsLVMPoolType(pool.Type) {
			poolType = pool.Type
		}
	}

	// 1) Reserve capacity (per-user admission lock + disk quota evaluation +
	//    pending reservation insert) BEFORE driving the agent.
	res, err := s.quotaService.ReserveDiskQuota(ctx, vm.UserID, vm.ID, sizeGB)
	if err != nil {
		return nil, err
	}
	// The reservation now counts toward the pool's allocation, so two attaches
	// racing for the last room both see each other here.
	if err := admitPoolRatio(ctx, s.db, pool, 0); err != nil {
		_ = s.quotaService.ReleaseDiskReservation(ctx, res.ID)
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
	resp, err := client.AttachDisk(authCtx, &pb.AttachDiskRequest{VmId: vmID, SizeGb: int64(sizeGB), PoolPath: poolPath, PoolType: poolType})
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

// ErrDetachKeepsNoVolume refuses a detach that would leave the disk's file on
// the node with nothing in the panel pointing at it.
var ErrDetachKeepsNoVolume = errors.New("a detached disk is not kept: detaching deletes the disk and everything on it. Confirm with delete_volume=true")

// DetachDisk detaches a data disk (by virtio device, e.g. "vdb") from the VM and
// deletes its backing volume. deleteVolume is the caller's confirmation of
// that; without it nothing is done, because a volume left on the node would
// be a file no one can reattach, delete or bill.
//
// Gate-1 accounting: the disk's usage is released ONLY after the agent confirms
// the detach. If the agent call fails, the vm_disks row is left intact so the
// quota accounting stays correct (we never release capacity the disk still holds
// on the node). On agent success the vm_disks row is deleted; that row IS the
// accounting for an attached (consumed) disk, so pending reservations are left
// untouched — clearing them here would race a concurrent AttachDisk's in-flight
// reservation for the same VM.
func (s *VMService) DetachDisk(ctx context.Context, vmID, device string, deleteVolume bool) error {
	if VMMigrating(ctx, s.db, vmID) {
		return ErrVMMigrating
	}
	if diskMoveActive(ctx, s.db, vmID) {
		return ErrDiskMoveActive
	}
	if !deleteVolume {
		return ErrDetachKeepsNoVolume
	}
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
	// A disk on a logical volume is deleted inside the pool that holds it. One
	// MaburVM did not create (an imported VM's) is detached and kept, listed
	// under its pool for an operator to delete on purpose; one outside every
	// registered pool is not touched at all.
	req := &pb.DetachDiskRequest{VmId: vmID, Device: device, Path: disk.Path, DeleteVolume: true}
	var keepIn *models.StoragePool
	if models.IsBlockDevicePath(disk.Path) {
		pool := DiskPool(ctx, s.db, vm.ID, vm.NodeID, disk.Path)
		if pool == nil || !models.IsLVMPoolType(pool.Type) {
			return fmt.Errorf("disk %s is the block device %s, which is not in a storage pool registered with the panel; register its volume group as an LVM pool first", device, disk.Path)
		}
		req.PoolType, req.PoolPath = pool.Type, pool.Path
		if !models.MaburVMVolumePath(disk.Path) {
			req.DeleteVolume, keepIn = false, pool
		}
	}
	client, err := s.getAgentClient(ctx, vm.NodeID)
	if err != nil {
		return err
	}
	authCtx, err := s.agentAuthContext(ctx, vm.NodeID)
	if err != nil {
		return err
	}
	resp, err := client.DetachDisk(authCtx, req)
	if err != nil {
		return fmt.Errorf("agent detach disk failed: %w", err)
	}
	if !resp.Success {
		return fmt.Errorf("detach disk failed: %s", agentErrorMessage(resp.Error))
	}
	if keepIn != nil {
		kept := &models.StorageVolume{Name: keptDiskName(vm.Hostname, device), PoolID: keepIn.ID, Size: int64(disk.SizeGB) * bytesPerGB, Format: "raw", Path: disk.Path}
		if err := s.db.WithContext(ctx).Create(kept).Error; err != nil {
			s.logger.ErrorContext(ctx, "an imported disk was detached and kept but could not be listed under its pool", "vm_id", vm.ID, "path", disk.Path, "error", err)
		}
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

// keptDiskName names a detached disk kept in its pool after its VM.
func keptDiskName(hostname, device string) string {
	name := strings.TrimSpace(hostname + " " + device + " (kept)")
	if len(name) > 100 {
		name = name[:100]
	}
	return name
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
	// A suspended VM that is powered off still reads as suspended.
	if vm.Suspended && newStatus == models.VMStatusStopped {
		newStatus = models.VMStatusSuspended
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
	// StartAfter boots the VM once the resize has been applied. Set by callers
	// that stopped a running VM to resize it; never read from a request body.
	StartAfter bool `json:"-"`
	// Billed marks a package change from the billing system; see CreateVMRequest.
	Billed bool `json:"-"`
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

	// VM must be stopped to modify resources, except to grow its disk, which
	// a running VM takes at once. A caller that stopped the VM to have the
	// resize job boot it again (StartAfter) keeps that path: a VM still
	// shutting down would otherwise be grown live and left off.
	if req.Resources != nil && vm.Status != models.VMStatusStopped {
		if vm.Status != models.VMStatusRunning || req.StartAfter || !diskOnlyGrowth(vm.Resources, *req.Resources) {
			return nil, ErrVMCannotBeModified
		}
		if _, err := s.ResizeDisk(ctx, vm.ID, "vda", req.Resources.Disk, req.Billed); err != nil {
			return nil, err
		}
		if vm, err = s.vmRepo.GetByID(ctx, req.VMID); err != nil {
			return nil, fmt.Errorf("failed to get VM: %w", err)
		}
		req.Resources = nil
	}

	// Update hostname if provided. Unique among the owner's VMs, counting the
	// owner the VM is being handed to when this request also reassigns it.
	if req.Hostname != "" && req.Hostname != vm.Hostname {
		owner := vm.UserID
		if req.UserID != "" {
			owner = req.UserID
		}
		exists, err := s.vmRepo.HostnameExistsForUser(ctx, owner, req.Hostname, vm.ID)
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
			if err := s.quotaService.AdmitVMResizeTx(ctx, tx, vm.UserID, oldRes, *req.Resources); err != nil && !billedOverQuota(req.Billed, err) {
				return err
			}
			// Node capacity is checked in the same transaction as the user's
			// quota, so the headroom two concurrent resizes see is serialized.
			if err := AdmitNodeVCPU(ctx, tx, vm.NodeID, req.Resources.CPU-oldRes.CPU); err != nil {
				return err
			}
			if err := s.quotaService.AdmitNodeRAMTx(ctx, tx, vm.NodeID, req.Resources.RAM-oldRes.RAM); err != nil {
				return err
			}
			// Growing a disk draws on its pool as a new VM does. Only the
			// growth is new; the current size is already counted there.
			if err := AdmitPoolDisk(ctx, tx, VMPool(ctx, tx, vm.ID, vm.NodeID), req.Resources.Disk-oldRes.Disk); err != nil {
				return err
			}
			vm.Resources = *req.Resources
			if err := s.vmRepo.WithDB(tx).Update(ctx, vm); err != nil {
				return err
			}
			if err := clearForeignProject(ctx, tx, vm.ID, vm.UserID); err != nil {
				return err
			}
			return reownVMGroups(ctx, tx, vm.ID, vm.UserID)
		}); err != nil {
			return nil, err
		}

		// Enqueue the resize job only after the new resources are committed.
		paramsJSON, _ := json.Marshal(map[string]interface{}{"resources": *req.Resources, "start_after": req.StartAfter})
		job := queue.VMOperationJob{VMID: vm.ID, Operation: queue.VMOpResize, NodeID: vm.NodeID, Params: paramsJSON}
		if _, err := s.riverClient.Insert(ctx, job, nil); err != nil {
			return nil, fmt.Errorf("failed to enqueue VM resize job: %w", err)
		}
		return vm, nil
	}

	// Hostname-only (or no-op) update.
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.vmRepo.WithDB(tx).Update(ctx, vm); err != nil {
			return err
		}
		if err := clearForeignProject(ctx, tx, vm.ID, vm.UserID); err != nil {
			return err
		}
		return reownVMGroups(ctx, tx, vm.ID, vm.UserID)
	}); err != nil {
		return nil, fmt.Errorf("failed to update VM: %w", err)
	}
	return vm, nil
}

// ============================================================================
// Delete VM
// ============================================================================

// DeleteVM deletes a VM and enqueues a deletion job
func (s *VMService) DeleteVM(ctx context.Context, vmID string) error {
	return s.DeleteVMWithVolumes(ctx, vmID, false)
}

// DeleteVMWithVolumes is DeleteVM. removeImported also removes the VM's
// logical volumes that MaburVM did not create (an imported VM's), when they
// live in a registered LVM pool; without it they are kept and listed under
// their pool. Only an operator may ask for it.
func (s *VMService) DeleteVMWithVolumes(ctx context.Context, vmID string, removeImported bool) error {
	if VMMigrating(ctx, s.db, vmID) {
		return ErrVMMigrating
	}
	if diskMoveActive(ctx, s.db, vmID) {
		return ErrDiskMoveActive
	}
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrVMNotFound
		}
		return fmt.Errorf("failed to get VM: %w", err)
	}
	if err := deleteLockErr(ctx, vm); err != nil {
		return err
	}

	if s.riverClient != nil {
		// Enqueue delete job
		job := queue.VMOperationJob{
			VMID:      vm.ID,
			Operation: queue.VMOpDelete,
			NodeID:    vm.NodeID,
		}
		if removeImported {
			job.Params, _ = json.Marshal(map[string]bool{"remove_foreign_volumes": true})
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
		s.emitVMEvent(ctx, BillingEventVMDestroyed, vm.ID)
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
	if VMMigrating(ctx, s.db, req.VMID) {
		return nil, ErrVMMigrating
	}
	// A disk copied while the VM is off is whole only while it stays off. (A
	// live copy ends if the VM stops, and the disk stays where it was.)
	if req.Command == LifecycleStart && diskMoveActive(ctx, s.db, req.VMID) {
		return nil, ErrDiskMoveActive
	}
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

	switch req.Command {
	case LifecycleStart, LifecycleRestart:
		if vm.Suspended {
			return nil, fmt.Errorf("VM is suspended")
		}
	case LifecycleSuspend:
		if err := s.setSuspended(ctx, vm, true); err != nil {
			return nil, err
		}
	case LifecycleUnsuspend:
		if err := s.setSuspended(ctx, vm, false); err != nil {
			return nil, err
		}
	}

	if err := s.ensureVMNodeActive(ctx, vm.NodeID); err != nil {
		return nil, err
	}

	// For synchronous execution, call agent directly
	if !req.Async {
		resp, err := s.executeSyncLifecycle(ctx, vm, vmCommand, req)
		if err != nil && req.Command == LifecycleUnsuspend {
			// Suspended while powered off (billing suspend): there is nothing to
			// resume, so boot it.
			startReq := *req
			startReq.Command = LifecycleStart
			return s.executeSyncLifecycle(ctx, vm, pb.VMCommandType_VM_COMMAND_TYPE_START, &startReq)
		}
		return resp, err
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
	// ByCustomer applies the template's availability rules to a change of
	// system. Reinstalling the system the VM already runs is always allowed.
	ByCustomer bool `json:"-"`
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
// agent on the running VM. The stock cloud images do NOT ship qemu-guest-agent,
// so the agent's cloud-init installs it on first boot; VMs provisioned before
// that was added have no agent and will not respond to this.
func (s *VMService) ResetPassword(ctx context.Context, req *VMResetPasswordRequest) (*VMResetPasswordResponse, error) {
	if VMMigrating(ctx, s.db, req.VMID) {
		return nil, ErrVMMigrating
	}
	vm, err := s.vmRepo.GetByID(ctx, req.VMID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}
	if vm.Status != models.VMStatusRunning {
		return nil, fmt.Errorf("the VM must be running to reset its password")
	}
	if s.riverClient == nil {
		return nil, fmt.Errorf("job queue unavailable")
	}
	params := map[string]interface{}{"root_password": req.Password}
	sealJobSecrets(params)
	paramsJSON, _ := json.Marshal(params)
	result, err := s.riverClient.Insert(ctx, queue.VMOperationJob{
		VMID:      vm.ID,
		Operation: queue.VMOpResetPassword,
		NodeID:    vm.NodeID,
		Params:    paramsJSON,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to enqueue password reset job: %w", err)
	}
	// The password is being changed, so the one kept from creation is no longer the VM's.
	if err := ForgetInitialPassword(ctx, s.db, vm.ID); err != nil {
		s.logger.WarnContext(ctx, "could not erase the stored initial root password", "vm_id", vm.ID, "error", err)
	}
	return &VMResetPasswordResponse{VMID: vm.ID, JobID: result.Job.ID, Status: "pending"}, nil
}

// AttachISO enqueues attaching a bootable install/rescue ISO (by image URL or
// on-node path) to a stopped VM. operatorSupplied says the operator chose the
// image; anything else is a URL a customer typed, which the node fetches only
// from public addresses and within its size limit.
func (s *VMService) AttachISO(ctx context.Context, vmID, isoImage string, operatorSupplied bool) (int64, error) {
	return s.AttachISOSlot(ctx, vmID, ISOAttach{Image: isoImage, OperatorSupplied: operatorSupplied, Slot: models.ISOSlotInstall})
}

// ISOAttach is an ISO going into a VM's CD drive.
type ISOAttach struct {
	Image string
	// OperatorSupplied marks an ISO staff chose (fetched without the rules for
	// a customer's address).
	OperatorSupplied bool
	// Slot is the drive: models.ISOSlotInstall or models.ISOSlotDrivers.
	Slot int
	// SHA256 is what the node checks the ISO against, when known.
	SHA256 string
	// WhileRunning lets a running VM take it at once, where its node can.
	WhileRunning bool
}

// AttachISOSlot enqueues putting an ISO in one of a VM's CD drives.
func (s *VMService) AttachISOSlot(ctx context.Context, vmID string, a ISOAttach) (int64, error) {
	if VMMigrating(ctx, s.db, vmID) {
		return 0, ErrVMMigrating
	}
	if a.Image == "" {
		return 0, fmt.Errorf("iso image is required")
	}
	if a.Slot == 0 {
		a.Slot = models.ISOSlotInstall
	}
	if a.Slot != models.ISOSlotInstall && a.Slot != models.ISOSlotDrivers {
		return 0, fmt.Errorf("drive must be 1 (install) or 2 (drivers)")
	}
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrVMNotFound
		}
		return 0, fmt.Errorf("failed to get VM: %w", err)
	}
	if err := s.isoSlotsUsable(ctx, vm, a.Slot, a.WhileRunning); err != nil {
		return 0, err
	}
	if s.riverClient == nil {
		return 0, fmt.Errorf("job queue unavailable")
	}
	params := map[string]interface{}{"image_path": a.Image}
	if a.OperatorSupplied {
		params["iso_source"] = queue.ISOSourceOperator
	}
	meta := map[string]string{}
	if a.Slot != models.ISOSlotInstall {
		meta[models.ISOSlotKey] = strconv.Itoa(a.Slot)
	}
	if a.SHA256 != "" {
		meta[models.ISOSHA256Key] = a.SHA256
	}
	if len(meta) > 0 {
		params["metadata"] = meta
	}
	paramsJSON, _ := json.Marshal(params)
	res, err := s.riverClient.Insert(ctx, queue.VMOperationJob{
		VMID: vm.ID, Operation: queue.VMOpAttachISO, NodeID: vm.NodeID, Params: paramsJSON,
	}, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to enqueue attach-iso job: %w", err)
	}
	return res.Job.ID, nil
}

// isoSlotsUsable refuses what the VM's node cannot do: the second drive, or
// changing a disc while the VM runs, need an agent that has them.
func (s *VMService) isoSlotsUsable(ctx context.Context, vm *models.VM, slot int, whileRunning bool) error {
	running := vm.Status == models.VMStatusRunning
	if running && !whileRunning {
		return fmt.Errorf("stop the VM before attaching an ISO")
	}
	if !running && slot == models.ISOSlotInstall {
		return nil
	}
	if !s.nodeHasFeature(ctx, vm.NodeID, "iso_slots") {
		if slot != models.ISOSlotInstall {
			return fmt.Errorf("this VM's node needs an agent update before it has a second CD drive")
		}
		return fmt.Errorf("stop the VM before attaching an ISO; its node needs an agent update to change discs while a VM runs")
	}
	return nil
}

// nodeHasFeature asks a node's agent whether it can do something an older one
// cannot. A node that cannot be asked is taken not to.
func (s *VMService) nodeHasFeature(ctx context.Context, nodeID, feature string) bool {
	client, err := s.getAgentClient(ctx, nodeID)
	if err != nil {
		return false
	}
	actx, err := s.agentAuthContext(ctx, nodeID)
	if err != nil {
		return false
	}
	cctx, cancel := context.WithTimeout(actx, 15*time.Second)
	defer cancel()
	features, err := nodeFeatures(cctx, client)
	return err == nil && hasFeature(features, feature)
}

// DetachISO enqueues removing the install/rescue ISO from a VM.
func (s *VMService) DetachISO(ctx context.Context, vmID string) (int64, error) {
	return s.DetachISOSlot(ctx, vmID, models.ISOSlotInstall)
}

// DetachISOSlot enqueues emptying one of a VM's CD drives.
func (s *VMService) DetachISOSlot(ctx context.Context, vmID string, slot int) (int64, error) {
	if VMMigrating(ctx, s.db, vmID) {
		return 0, ErrVMMigrating
	}
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrVMNotFound
		}
		return 0, fmt.Errorf("failed to get VM: %w", err)
	}
	if slot == 0 {
		slot = models.ISOSlotInstall
	}
	if slot != models.ISOSlotInstall && slot != models.ISOSlotDrivers {
		return 0, fmt.Errorf("drive must be 1 (install) or 2 (drivers)")
	}
	if slot != models.ISOSlotInstall && !s.nodeHasFeature(ctx, vm.NodeID, "iso_slots") {
		return 0, fmt.Errorf("this VM's node needs an agent update before it has a second CD drive")
	}
	if s.riverClient == nil {
		return 0, fmt.Errorf("job queue unavailable")
	}
	var params []byte
	if slot != models.ISOSlotInstall {
		params, _ = json.Marshal(map[string]interface{}{"metadata": map[string]string{models.ISOSlotKey: strconv.Itoa(slot)}})
	}
	res, err := s.riverClient.Insert(ctx, queue.VMOperationJob{
		VMID: vm.ID, Operation: queue.VMOpDetachISO, NodeID: vm.NodeID, Params: params,
	}, nil)
	if err != nil {
		return 0, fmt.Errorf("failed to enqueue detach-iso job: %w", err)
	}
	return res.Job.ID, nil
}

// RescueAvailable reports whether rescue mode has an image to boot.
func (s *VMService) RescueAvailable(ctx context.Context) bool {
	iso, _ := RescueISO(ctx, s.db)
	return iso != ""
}

// RescueVM attaches a rescue ISO (which boots first) and marks the VM as in
// rescue mode. The VM must be stopped; starting it afterward boots into rescue.
// An empty isoURL means the operator's rescue image (System settings, else
// RESCUE_ISO_URL), whoever asks for the rescue.
func (s *VMService) RescueVM(ctx context.Context, vmID, isoURL string, operatorSupplied bool) (int64, error) {
	if VMMigrating(ctx, s.db, vmID) {
		return 0, ErrVMMigrating
	}
	if isoURL == "" {
		isoURL, _ = RescueISO(ctx, s.db)
		operatorSupplied = true
	}
	if isoURL == "" {
		return 0, ErrNoRescueImage
	}
	// AttachISO validates the VM exists, is stopped, and enqueues the boot-first ISO.
	jobID, err := s.AttachISO(ctx, vmID, isoURL, operatorSupplied)
	if err != nil {
		return 0, err
	}
	if err := s.db.WithContext(ctx).Model(&models.VM{}).Where("id = ?", vmID).Update("rescue_mode", true).Error; err != nil {
		return 0, fmt.Errorf("failed to mark rescue mode: %w", err)
	}
	s.emailOwner(ctx, vmID, EmailVMRescueOn)
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
	s.emailOwner(ctx, vmID, EmailVMRescueOff)
	return jobID, nil
}

// emailOwner tells a server's owner, in the panel and by email, in the
// background. The action it reports has already happened, so a failure is
// logged and nothing more.
func (s *VMService) emailOwner(ctx context.Context, vmID, key string) {
	go func(ctx context.Context) {
		if err := NotifyVMOwner(ctx, s.db, vmID, key); err != nil {
			s.logger.WarnContext(ctx, "could not email the server's owner", "vm_id", vmID, "email", key, "error", err)
		}
	}(context.WithoutCancel(ctx))
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
	if VMMigrating(ctx, s.db, req.VMID) {
		return nil, ErrVMMigrating
	}
	if diskMoveActive(ctx, s.db, req.VMID) {
		return nil, ErrDiskMoveActive
	}
	vm, err := s.vmRepo.GetByID(ctx, req.VMID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}
	if err := deleteLockErr(ctx, vm); err != nil {
		return nil, err
	}

	// VM must be stopped for rebuild
	if vm.Status == models.VMStatusRunning {
		return nil, fmt.Errorf("VM must be stopped before rebuilding")
	}

	// Validate template if specified
	kindChanged := "" // the new kind of system, when it differs from the installed one
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
		if template.IsISO() {
			return nil, fmt.Errorf("%w: %s is an ISO; attach it to the VM and install from the console instead", ErrRebuildNotPossible, template.Name)
		}
		if template.Firmware == models.FirmwareUEFI && !s.nodeHasFeature(ctx, vm.NodeID, models.FeatureUEFI) {
			return nil, ErrUEFINotSupported
		}
		if planID := ""; req.ByCustomer && req.TemplateID != vm.OSTemplateID {
			if vm.PlanID != nil {
				planID = *vm.PlanID
			}
			if !template.OfferedOn(planID) {
				return nil, ErrTemplateNotOffered
			}
		}
		// Any kind of system can replace any other: the node rewrites the VM's
		// virtual hardware for it. What cannot change is the VM's size.
		if osKind(template.OSType) == models.OSTypeWindows &&
			(vm.Resources.RAM < models.WindowsMinRAMMB || vm.Resources.Disk < models.WindowsMinDiskGB) {
			return nil, fmt.Errorf("%w: Windows Server needs at least %d GB of memory and a %d GB disk, and this VM has %d MB and %d GB",
				ErrRebuildNotPossible, models.WindowsMinRAMMB/1024, models.WindowsMinDiskGB, vm.Resources.RAM, vm.Resources.Disk)
		}
		if current, cerr := s.templateRepo.GetByID(ctx, vm.OSTemplateID); cerr == nil && osKind(current.OSType) != osKind(template.OSType) {
			kindChanged = osKind(template.OSType)
		}
		templateID = req.TemplateID
	}

	if kindChanged != "" {
		s.openRemoteAccess(ctx, vm.ID, kindChanged)
	}

	// Update VM template if changed
	if templateID != vm.OSTemplateID {
		vm.OSTemplateID = templateID
		if err := s.vmRepo.Update(ctx, vm); err != nil {
			return nil, fmt.Errorf("failed to update VM template: %w", err)
		}
	}

	// Generate new VNC password for security
	// The password only. generateVNCCredentials returns port 0 to mean "the
	// node picks one", and vms.vnc_port accepts NULL or 5900 and up: storing
	// that 0 made every reinstall fail on the constraint before it began. A
	// reinstall replaces the disk, not the console, so the port stays as it is.
	vncConfig := s.generateVNCCredentials()
	vm.VNCPassword = vncConfig.Password
	if err := s.vmRepo.UpdateVNCPassword(ctx, vm.ID, vncConfig.Password); err != nil {
		return nil, fmt.Errorf("failed to update VNC credentials: %w", err)
	}

	// Resolve the template image path so the agent can fetch/clone it.
	rebuildTmpl, err := s.templateRepo.GetByID(ctx, templateID)
	if err != nil {
		return nil, fmt.Errorf("failed to load template for rebuild: %w", err)
	}
	// A VM installed from an ISO is reinstalled from a disk template only.
	if rebuildTmpl.IsISO() {
		return nil, fmt.Errorf("%w: choose a system to reinstall the VM with", ErrRebuildNotPossible)
	}

	// Resolve the root password: explicit wins, else generate one when asked.
	rootPassword := req.Password
	generatedPassword := ""
	// A reinstalled router starts from an image with an empty admin password,
	// so it always gets a new one.
	if rootPassword == "" && (req.RegeneratePassword || osKind(rebuildTmpl.OSType) != models.OSTypeLinux) {
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
		"image_path":     models.GuestImageRef(rebuildTmpl.OSType, rebuildTmpl.ImagePath),
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
	if p, perr := vmIPv6Prefix(ctx, s.db, vm.ID); perr == nil && p != nil {
		params["ipv6_prefix"] = p.Prefix
		params["ipv6_address"] = p.Address()
		params["ipv6_gateway"] = models.IPv6LinkLocalGateway
	}
	rebuildMeta := map[string]string{}
	if rebuildTmpl.ChecksumSHA256 != "" {
		rebuildMeta[models.ImageSHA256Key] = rebuildTmpl.ChecksumSHA256
	}
	if rebuildTmpl.Firmware == models.FirmwareUEFI {
		rebuildMeta[models.FirmwareKey] = models.FirmwareUEFI
	}
	if len(rebuildMeta) > 0 {
		params["metadata"] = rebuildMeta
	}
	sealJobSecrets(params)
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

	// The old disk is gone, and with it the old password. Keep the new one for
	// the owner if the panel generated it; otherwise drop whatever was kept.
	if generatedPassword != "" {
		if err := StoreInitialPassword(ctx, s.db, vm.ID, generatedPassword); err != nil {
			s.logger.WarnContext(ctx, "could not keep the generated root password for its owner", "vm_id", vm.ID, "error", err)
		}
	} else if err := ForgetInitialPassword(ctx, s.db, vm.ID); err != nil {
		s.logger.WarnContext(ctx, "could not erase the previous stored root password", "vm_id", vm.ID, "error", err)
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

// osKind is a template's kind with the unset value read as Linux.
func osKind(osType string) string {
	if osType == "" {
		return models.OSTypeLinux
	}
	return osType
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

	// The console socket sits on the node's loopback whether the console is
	// enabled or not, so this check is what keeps a disabled one shut.
	if !vm.ConsoleEnabled {
		return nil, ErrConsoleDisabled
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
		if err := s.vmRepo.UpdateVNCPassword(ctx, vmID, vm.VNCPassword); err != nil {
			s.logger.Error("failed to save generated VNC password", "vm_id", vmID, "error", err)
		}
	}

	// Apply the password to the live domain so the browser (which authenticates
	// against QEMU with exactly this password) matches. Only meaningful while the
	// VM is running.
	//
	// This is best-effort, NOT fatal. A domain started without a console
	// password (anything started before consoles were given one at definition,
	// and many imported domains) runs with QEMU security type "None"; QMP
	// set_password then fails because auth can't be enabled at runtime — but the
	// console still works without a password (noVNC just skips the auth
	// handshake). Hard-failing here is what made GetVNCConfig return 500 "Failed
	// to get VNC config" for every imported VM. Domains started since are defined
	// with a passwd attribute, so the sync succeeds and the browser password
	// matches; a rare failure there degrades to a client-side "Authentication
	// failed" instead of blocking the whole console.
	node, err := s.nodeRepo.GetByID(ctx, vm.NodeID)
	if vm.Status == models.VMStatusRunning {
		if syncErr := s.syncVNCPassword(ctx, vm.NodeID, vmID, vm.VNCPassword); syncErr != nil {
			s.logger.WarnContext(ctx, "failed to apply VNC password to running VM; serving console without runtime password sync (expected until the VM is next powered off and on, if it was started without a console password)",
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
	// Only persist a real port. generateVNCCredentials returns 0 to mean "the
	// agent picks one via autoport", but the VM already HAS a live port here and
	// vms.vnc_port is constrained to NULL or >= 5900 — writing the 0 made every
	// rotation fail with a constraint violation, so no VNC password could ever be
	// rotated. A refresh changes the password, not the port.
	if vncConfig.Port > 0 {
		if err := s.vmRepo.UpdateVNCPort(ctx, vmID, vncConfig.Port); err != nil {
			return nil, fmt.Errorf("failed to update VNC port: %w", err)
		}
	} else if vm.VNCPort != nil {
		vncConfig.Port = *vm.VNCPort
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
		s.logger.WarnContext(ctx, "the running VM kept its console as it was started; the change is stored and applies when it is next powered off and on",
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
		// Adopt out-of-band resizes (virsh setvcpus, qemu-img resize...) so the
		// panel shows what the domain really has. A panel resize briefly reads
		// back the old spec until its job applies, then converges.
		if res, changed := configuredResources(vms[i].Resources, resp.GetConfiguredResources()); changed {
			if uerr := s.vmRepo.UpdateResources(ctx, vms[i].ID, res); uerr != nil {
				s.logger.WarnContext(ctx, "status reconcile: persist resources failed", "vm_id", vms[i].ID, "error", uerr)
			} else {
				s.logger.InfoContext(ctx, "status reconcile: adopted real VM spec", "vm_id", vms[i].ID,
					"cpu", res.CPU, "ram", res.RAM, "disk", res.Disk)
			}
		}
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

// configuredResources overlays the agent-reported spec onto the stored one.
// Zero fields mean the agent couldn't measure them (or is too old to send
// them) and keep the stored value.
func configuredResources(stored models.Resources, real *pb.VMResources) (models.Resources, bool) {
	out := stored
	if real == nil {
		return out, false
	}
	if c := int(real.GetVcpus()); c > 0 {
		out.CPU = c
	}
	if m := int(real.GetMemoryMb()); m > 0 {
		out.RAM = m
	}
	if d := int(real.GetDiskGb()); d > 0 {
		out.Disk = d
	}
	return out, out.CPU != stored.CPU || out.RAM != stored.RAM || out.Disk != stored.Disk
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
	// First make IPAM own what VMs already hold: an imported VM's address that
	// IPAM still lists as 'available' would otherwise be handed to a new VM (and
	// the ARP probe below can't be relied on to catch it — a probe that fails
	// open, e.g. no arping on the node, reports every IP as free).
	adopted, conflicts, err := s.ipamService.AdoptVMAddressesForNode(ctx, nodeID)
	if err != nil {
		s.logger.WarnContext(ctx, "IP reconcile: adopt VM addresses failed", "node_id", nodeID, "error", err)
	}
	if adopted > 0 {
		s.logger.InfoContext(ctx, "IP reconcile: adopted VM addresses into IPAM", "node_id", nodeID, "count", adopted)
	}
	for _, c := range conflicts {
		s.logger.WarnContext(ctx, "IP reconcile: address collision — two VMs claim one IP", "node_id", nodeID, "detail", c)
	}
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
