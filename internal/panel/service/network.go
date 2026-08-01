package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	panelclient "github.com/maburvm/panel/internal/panel/client"
	"github.com/maburvm/panel/internal/panel/repository"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
	"github.com/riverqueue/river"
	"gorm.io/gorm"
)

// Common errors for network operations
var (
	ErrNetworkNotFound      = fmt.Errorf("network not found")
	ErrNetworkAlreadyExists = fmt.Errorf("network already exists for this VM")
	ErrIPAlreadyInUse       = fmt.Errorf("IP address already in use")
	ErrFirewallRuleNotFound = fmt.Errorf("firewall rule not found")
	ErrPortForwardNotFound  = fmt.Errorf("port forward not found")
	ErrPortAlreadyInUse     = fmt.Errorf("external port already in use")
	ErrInvalidVLANID        = fmt.Errorf("invalid VLAN ID (must be 1-4094)")
	ErrInvalidBandwidth     = fmt.Errorf("invalid bandwidth limit")
	ErrInvalidPortRange     = fmt.Errorf("invalid port range")
)

// NetworkService handles network-related operations
type NetworkService struct {
	db           *gorm.DB
	networkRepo  *repository.NetworkRepository
	firewallRepo *repository.FirewallRepository
	vmRepo       *repository.VMRepository
	nodeRepo     *repository.NodeRepository
	riverClient  *river.Client[pgx.Tx]
}

// NewNetworkService creates a new NetworkService instance
func NewNetworkService(
	db *gorm.DB,
	networkRepo *repository.NetworkRepository,
	firewallRepo *repository.FirewallRepository,
	vmRepo *repository.VMRepository,
	nodeRepo *repository.NodeRepository,
	riverClient *river.Client[pgx.Tx],
) *NetworkService {
	return &NetworkService{
		db:           db,
		networkRepo:  networkRepo,
		firewallRepo: firewallRepo,
		vmRepo:       vmRepo,
		nodeRepo:     nodeRepo,
		riverClient:  riverClient,
	}
}

// AddNetworkRequest contains data for adding a network interface to a VM
type AddNetworkRequest struct {
	IPAddress      string `json:"ip_address" validate:"required,ip"`
	BandwidthLimit int64  `json:"bandwidth_limit,omitempty" validate:"omitempty,min=0"`
	VLANID         *int   `json:"vlan_id,omitempty" validate:"omitempty,min=1,max=4094"`
}

// AddNetworkResponse contains the created network data
type AddNetworkResponse struct {
	Network *models.Network `json:"network"`
}

// AddNetworkInterface adds a network interface to a VM
func (s *NetworkService) AddNetworkInterface(ctx context.Context, vmID string, req *AddNetworkRequest) (*AddNetworkResponse, error) {
	// Verify VM exists
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("VM not found")
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}

	// Validate IP address format
	if net.ParseIP(req.IPAddress) == nil {
		return nil, ErrInvalidIPAddress
	}

	// Check if IP is already in use
	exists, err := s.networkRepo.IPAddressExists(ctx, req.IPAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to check IP existence: %w", err)
	}
	if exists {
		return nil, ErrIPAlreadyInUse
	}

	// Create network
	network := &models.Network{
		VMID:           vmID,
		IPAddress:      req.IPAddress,
		BandwidthLimit: req.BandwidthLimit,
		VLANID:         req.VLANID,
	}

	// Validate network struct
	if errs := network.Validate(); len(errs) > 0 {
		return nil, fmt.Errorf("validation failed: %v", errs)
	}

	// Save to database
	if err := s.networkRepo.Create(ctx, network); err != nil {
		return nil, fmt.Errorf("failed to create network: %w", err)
	}

	// Enqueue network config job to agent
	if err := s.enqueueNetworkConfigJob(ctx, vm, network); err != nil {
		// Log error but don't fail the request
		fmt.Printf("failed to enqueue network config job: %v\n", err)
	}

	return &AddNetworkResponse{Network: network}, nil
}

// SetBandwidthRequest contains data for setting bandwidth limit.
// BandwidthLimit is in Mbps; 0 means unlimited and 10000 (10 Gbps) is the
// ceiling, matching the VM-creation bandwidth bound.
type SetBandwidthRequest struct {
	BandwidthLimit int64 `json:"bandwidth_limit" validate:"min=0,max=10000"`
}

// SetBandwidthLimit sets the bandwidth limit for a network interface
func (s *NetworkService) SetBandwidthLimit(ctx context.Context, vmID string, networkID string, req *SetBandwidthRequest) error {
	// Verify VM exists
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("VM not found")
		}
		return fmt.Errorf("failed to get VM: %w", err)
	}

	// Get network
	network, err := s.networkRepo.GetByID(ctx, networkID)
	if err != nil {
		return ErrNetworkNotFound
	}

	// Verify network belongs to VM
	if network.VMID != vmID {
		return ErrNetworkNotFound
	}

	// Update bandwidth limit
	if err := s.networkRepo.UpdateBandwidthLimit(ctx, networkID, req.BandwidthLimit); err != nil {
		return fmt.Errorf("failed to update bandwidth limit: %w", err)
	}

	// Enqueue bandwidth config job to agent
	network.BandwidthLimit = req.BandwidthLimit
	if err := s.enqueueNetworkConfigJob(ctx, vm, network); err != nil {
		fmt.Printf("failed to enqueue bandwidth config job: %v\n", err)
	}

	return nil
}

// ApplyLiveBandwidth pushes a live interface speed (Mbps) to a VM's primary
// network WITHOUT changing the stored BandwidthLimit. Bandwidth quota
// enforcement uses this to throttle a VM (and later restore it) while keeping
// the VM's normal provisioned speed on record. 0 = unlimited. Firewall rules
// and VLAN are preserved (the same full network-config job the admin path uses).
func (s *NetworkService) ApplyLiveBandwidth(ctx context.Context, vmID string, mbps int64) error {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		return fmt.Errorf("failed to get VM: %w", err)
	}
	network, err := s.networkRepo.GetByVMID(ctx, vmID)
	if err != nil {
		return ErrNetworkNotFound
	}
	netCopy := *network
	netCopy.BandwidthLimit = mbps
	// enqueueNetworkConfigJob loads the full firewall set at the chokepoint, so a
	// live bandwidth change no longer risks wiping the firewall.
	return s.enqueueNetworkConfigJob(ctx, vm, &netCopy)
}

// AddPortForwardRequest contains data for adding a port forward rule
type AddPortForwardRequest struct {
	ExternalPort int    `json:"external_port" validate:"required,min=1,max=65535"`
	InternalPort int    `json:"internal_port" validate:"required,min=1,max=65535"`
	Protocol     string `json:"protocol,omitempty" validate:"omitempty,oneof=tcp udp"`
	SourceIP     string `json:"source_ip,omitempty" validate:"omitempty,ip_or_cidr"`
}

// AddPortForwardResponse contains the created port forward data
type AddPortForwardResponse struct {
	PortForward *models.PortForward `json:"port_forward"`
}

// AddPortForward adds a port forwarding (NAT) rule to a VM
func (s *NetworkService) AddPortForward(ctx context.Context, vmID string, networkID string, req *AddPortForwardRequest) (*AddPortForwardResponse, error) {
	// Verify VM exists
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("VM not found")
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}

	// Get network
	network, err := s.networkRepo.GetByID(ctx, networkID)
	if err != nil {
		return nil, ErrNetworkNotFound
	}

	// Verify network belongs to VM
	if network.VMID != vmID {
		return nil, ErrNetworkNotFound
	}

	// Set defaults
	if req.Protocol == "" {
		req.Protocol = "tcp"
	}
	if req.SourceIP == "" {
		req.SourceIP = "0.0.0.0/0"
	}

	// Create port forward record
	portForward := &models.PortForward{
		VMID:         vmID,
		NetworkID:    networkID,
		ExternalPort: req.ExternalPort,
		InternalPort: req.InternalPort,
		Protocol:     req.Protocol,
		SourceIP:     req.SourceIP,
	}

	// Validate
	if errs := portForward.Validate(); len(errs) > 0 {
		return nil, fmt.Errorf("validation failed: %v", errs)
	}

	// Save to database using GORM directly since we don't have a repository
	if err := s.db.WithContext(ctx).Create(portForward).Error; err != nil {
		return nil, fmt.Errorf("failed to create port forward: %w", err)
	}

	// Enqueue full network resync (ReplaceAll) so the new DNAT is applied on the host.
	if err := s.enqueueNetworkResync(ctx, vm); err != nil {
		fmt.Printf("failed to enqueue network resync: %v\n", err)
	}

	return &AddPortForwardResponse{PortForward: portForward}, nil
}

// RemovePortForward removes a port forwarding rule
func (s *NetworkService) RemovePortForward(ctx context.Context, vmID string, networkID string, forwardID string) error {
	// Verify VM exists
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("VM not found")
		}
		return fmt.Errorf("failed to get VM: %w", err)
	}

	// Get port forward
	var portForward models.PortForward
	if err := s.db.WithContext(ctx).Where("id = ? AND vm_id = ? AND network_id = ?", forwardID, vmID, networkID).First(&portForward).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return ErrPortForwardNotFound
		}
		return fmt.Errorf("failed to get port forward: %w", err)
	}

	// Delete from database
	if err := s.db.WithContext(ctx).Delete(&portForward).Error; err != nil {
		return fmt.Errorf("failed to delete port forward: %w", err)
	}

	// Enqueue full network resync (ReplaceAll) so the removed DNAT is dropped on the host.
	if err := s.enqueueNetworkResync(ctx, vm); err != nil {
		fmt.Printf("failed to enqueue network resync: %v\n", err)
	}

	return nil
}

// primaryNetworkID returns the VM's primary (first) network interface ID, used
// as the default target for VM-level port forwarding.
func (s *NetworkService) primaryNetworkID(ctx context.Context, vmID string) (string, error) {
	netIface, err := s.networkRepo.GetByVMID(ctx, vmID)
	if err != nil {
		return "", ErrNetworkNotFound
	}
	return netIface.ID, nil
}

// AddPortForwardForVM adds a port forward to the VM's primary network interface.
// It backs the VM-level /vms/:id/port-forwards endpoint, which addresses port
// forwards by VM rather than by a specific interface (Virtualizor-style).
func (s *NetworkService) AddPortForwardForVM(ctx context.Context, vmID string, req *AddPortForwardRequest) (*AddPortForwardResponse, error) {
	networkID, err := s.primaryNetworkID(ctx, vmID)
	if err != nil {
		return nil, err
	}
	return s.AddPortForward(ctx, vmID, networkID, req)
}

// GetPortForwardsForVM returns all of a VM's port forwards across its interfaces.
func (s *NetworkService) GetPortForwardsForVM(ctx context.Context, vmID string) ([]models.PortForward, error) {
	var forwards []models.PortForward
	if err := s.db.WithContext(ctx).Where("vm_id = ?", vmID).Order("created_at DESC").Find(&forwards).Error; err != nil {
		return nil, fmt.Errorf("failed to list port forwards: %w", err)
	}
	return forwards, nil
}

// RemovePortForwardForVM removes a VM's port forward by ID, resolving the owning
// network interface from the record so callers needn't supply it.
func (s *NetworkService) RemovePortForwardForVM(ctx context.Context, vmID, forwardID string) error {
	var forward models.PortForward
	if err := s.db.WithContext(ctx).Where("id = ? AND vm_id = ?", forwardID, vmID).First(&forward).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return ErrPortForwardNotFound
		}
		return fmt.Errorf("failed to get port forward: %w", err)
	}
	return s.RemovePortForward(ctx, vmID, forward.NetworkID, forwardID)
}

// AddFirewallRuleRequest contains data for adding a firewall rule
type AddFirewallRuleRequest struct {
	Protocol  string `json:"protocol" validate:"required,oneof=tcp udp icmp all"`
	PortRange string `json:"port_range,omitempty" validate:"omitempty,port_range"`
	Action    string `json:"action" validate:"required,oneof=allow deny"`
	Direction string `json:"direction" validate:"required,oneof=inbound outbound"`
	SourceIP  string `json:"source_ip,omitempty" validate:"omitempty,ip_or_cidr"`
	Priority  int    `json:"priority" validate:"required,min=1,max=1000"`
}

// AddFirewallRuleResponse contains the created firewall rule data
type AddFirewallRuleResponse struct {
	FirewallRule *models.FirewallRule `json:"firewall_rule"`
}

// AddFirewallRule adds a firewall rule to a VM
func (s *NetworkService) AddFirewallRule(ctx context.Context, vmID string, req *AddFirewallRuleRequest) (*AddFirewallRuleResponse, error) {
	// Verify VM exists
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("VM not found")
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}

	// Set defaults
	if req.SourceIP == "" {
		req.SourceIP = "0.0.0.0/0"
	}

	// Create firewall rule
	rule := &models.FirewallRule{
		VMID:      vmID,
		Protocol:  req.Protocol,
		PortRange: req.PortRange,
		Action:    req.Action,
		Direction: req.Direction,
		SourceIP:  req.SourceIP,
		Priority:  req.Priority,
	}

	// Validate
	if errs := rule.Validate(); len(errs) > 0 {
		return nil, fmt.Errorf("validation failed: %v", errs)
	}

	// Save to database
	if err := s.firewallRepo.Create(ctx, rule); err != nil {
		return nil, fmt.Errorf("failed to create firewall rule: %w", err)
	}

	// Enqueue full network resync (ReplaceAll) so the firewall rule is enforced on the host.
	if err := s.enqueueNetworkResync(ctx, vm); err != nil {
		fmt.Printf("failed to enqueue network resync: %v\n", err)
	}

	return &AddFirewallRuleResponse{FirewallRule: rule}, nil
}

// RemoveFirewallRule removes a firewall rule from a VM
func (s *NetworkService) RemoveFirewallRule(ctx context.Context, vmID string, ruleID string) error {
	// Verify VM exists
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("VM not found")
		}
		return fmt.Errorf("failed to get VM: %w", err)
	}

	// Get firewall rule
	rule, err := s.firewallRepo.GetByID(ctx, ruleID)
	if err != nil {
		return ErrFirewallRuleNotFound
	}

	// Verify rule belongs to VM
	if rule.VMID != vmID {
		return ErrFirewallRuleNotFound
	}

	// Delete from database
	if err := s.firewallRepo.Delete(ctx, ruleID); err != nil {
		return fmt.Errorf("failed to delete firewall rule: %w", err)
	}

	// Enqueue full network resync (ReplaceAll) so the remaining rules are re-applied on the host.
	if err := s.enqueueNetworkResync(ctx, vm); err != nil {
		fmt.Printf("failed to enqueue network resync: %v\n", err)
	}

	return nil
}

// SetVLANRequest contains data for setting VLAN ID
type SetVLANRequest struct {
	VLANID int `json:"vlan_id" validate:"required,min=1,max=4094"`
}

// SetVLAN sets the VLAN ID for a network interface
func (s *NetworkService) SetVLAN(ctx context.Context, vmID string, networkID string, req *SetVLANRequest) error {
	// Verify VM exists
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("VM not found")
		}
		return fmt.Errorf("failed to get VM: %w", err)
	}

	// Get network
	network, err := s.networkRepo.GetByID(ctx, networkID)
	if err != nil {
		return ErrNetworkNotFound
	}

	// Verify network belongs to VM
	if network.VMID != vmID {
		return ErrNetworkNotFound
	}

	// Update VLAN ID
	vlanID := req.VLANID
	if err := s.networkRepo.UpdateVLANID(ctx, networkID, &vlanID); err != nil {
		return fmt.Errorf("failed to update VLAN ID: %w", err)
	}

	// Enqueue VLAN config job to agent
	network.VLANID = &vlanID
	if err := s.enqueueNetworkConfigJob(ctx, vm, network); err != nil {
		fmt.Printf("failed to enqueue VLAN config job: %v\n", err)
	}

	return nil
}

// GetNetworkInterfaces returns all network interfaces for a VM
func (s *NetworkService) GetNetworkInterfaces(ctx context.Context, vmID string) ([]models.Network, error) {
	// Verify VM exists
	if _, err := s.vmRepo.GetByID(ctx, vmID); err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("VM not found")
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}

	return s.networkRepo.ListByVMID(ctx, vmID)
}

// NetworkInterfaceDetail enriches a VM interface with the gateway, netmask,
// bridge and rDNS resolved from the owning IPAM pool/address, so the VM detail
// page can show full per-IP networking like Virtualizor.
type NetworkInterfaceDetail struct {
	models.Network
	Gateway string `json:"gateway,omitempty"`
	Netmask string `json:"netmask,omitempty"`
	Bridge  string `json:"bridge,omitempty"`
	RDNS    string `json:"rdns,omitempty"`
	PoolID  string `json:"pool_id,omitempty"`
}

// GetNetworkInterfaceDetails returns a VM's interfaces enriched with gateway,
// netmask, bridge and rDNS looked up from the IPAM tables by matching each
// interface IP to a managed address and its pool. Enrichment is best-effort:
// interfaces whose IP isn't tracked in IPAM still return with the base fields.
func (s *NetworkService) GetNetworkInterfaceDetails(ctx context.Context, vmID string) ([]NetworkInterfaceDetail, error) {
	nets, err := s.GetNetworkInterfaces(ctx, vmID)
	if err != nil {
		return nil, err
	}

	// Fall back to the agent's live view when no interface is recorded in the DB
	// (e.g. DHCP VMs created without a pool, imports, or pre-existing VMs) so the
	// UI reflects the addresses the VM actually has rather than showing nothing.
	if len(nets) == 0 {
		nets = s.liveVMInterfaces(ctx, vmID)
	}

	// Load IPAM addresses assigned to this VM, indexed by host address.
	var addrs []models.IPAddress
	if err := s.db.WithContext(ctx).Where("vm_id = ?", vmID).Find(&addrs).Error; err != nil {
		return nil, fmt.Errorf("failed to load IPAM addresses: %w", err)
	}
	addrByIP := make(map[string]models.IPAddress, len(addrs))
	poolIDs := make([]string, 0, len(addrs))
	seenPool := make(map[string]bool)
	for _, a := range addrs {
		addrByIP[hostOnlyIP(a.Address)] = a
		if a.PoolID != "" && !seenPool[a.PoolID] {
			seenPool[a.PoolID] = true
			poolIDs = append(poolIDs, a.PoolID)
		}
	}

	pools := make(map[string]models.IPPool)
	if len(poolIDs) > 0 {
		var ps []models.IPPool
		if err := s.db.WithContext(ctx).Where("id IN ?", poolIDs).Find(&ps).Error; err != nil {
			return nil, fmt.Errorf("failed to load IP pools: %w", err)
		}
		for _, p := range ps {
			pools[p.ID] = p
		}
	}

	// Virtualizor-style: load all CIDR-matching pools so interfaces whose IP
	// isn't explicitly assigned (e.g. pre-existing networks or imports) still
	// get gateway, netmask and bridge from the owning pool.
	var allPools []models.IPPool
	// NOTE: `cidr` is a Postgres cidr-typed column. Comparing it to '' (e.g.
	// `cidr != ''`) makes Postgres cast '' to cidr and fail with "invalid input
	// syntax for type inet", which 500'd this whole endpoint (SQLite in tests
	// tolerates it, Postgres in prod does not). An unset cidr is NULL, so
	// IS NOT NULL alone is the correct and sufficient filter.
	if err := s.db.WithContext(ctx).Where("cidr IS NOT NULL AND deleted_at IS NULL").Find(&allPools).Error; err != nil {
		return nil, fmt.Errorf("failed to load all IP pools: %w", err)
	}

	out := make([]NetworkInterfaceDetail, len(nets))
	for i, n := range nets {
		d := NetworkInterfaceDetail{Network: n}
		ip := hostOnlyIP(n.IPAddress)
		if a, ok := addrByIP[ip]; ok {
			d.RDNS = a.RDNS
			d.PoolID = a.PoolID
			if p, ok := pools[a.PoolID]; ok {
				d.Gateway = p.Gateway
				d.Bridge = p.Bridge
				d.Netmask = netmaskFromCIDR(p.CIDR)
			}
		} else {
			// Lazy CIDR match: find the first pool whose CIDR contains this IP.
			parsedIP := net.ParseIP(ip)
			if parsedIP != nil {
				for _, p := range allPools {
					_, ipnet, err := net.ParseCIDR(p.CIDR)
					if err != nil {
						continue
					}
					if ipnet.Contains(parsedIP) {
						d.Gateway = p.Gateway
						d.Bridge = p.Bridge
						d.Netmask = netmaskFromCIDR(p.CIDR)
						d.PoolID = p.ID
						break
					}
				}
			}
		}
		// Last-resort Virtualizor-style display fallback: when an imported/pre-existing
		// interface has no IPAM pool yet, infer common IPv4 /24 details so the VM page
		// still shows usable network information instead of blank columns. Operators
		// can later attach the IP to a real IP pool to override these values.
		if d.Gateway == "" && d.Netmask == "" {
			d.Gateway, d.Netmask = inferIPv4NetworkDefaults(ip)
		}

		out[i] = d
	}
	return out, nil
}

// liveVMInterfaces queries the agent for the VM's current addresses and returns
// synthesized interface records. Best-effort: returns nil when the VM isn't
// running, the node is unreachable, or no usable address is reported.
func (s *NetworkService) liveVMInterfaces(ctx context.Context, vmID string) []models.Network {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil || vm.Status != models.VMStatusRunning {
		return nil
	}
	node, err := s.nodeRepo.GetByID(ctx, vm.NodeID)
	if err != nil {
		return nil
	}
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(dialCtx, fmt.Sprintf("%s:50051", node.IPAddress),
		grpc.WithTransportCredentials(panelclient.NodeTLSCredentials(node.ID, node.IPAddress)),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil
	}
	defer conn.Close()
	authCtx := metadata.NewOutgoingContext(ctx, metadata.New(map[string]string{"authorization": "Bearer " + node.Token}))
	resp, err := pb.NewNodeAgentClient(conn).GetVMStatus(authCtx, &pb.VMStatusRequest{VmId: vmID})
	if err != nil || resp == nil {
		return nil
	}
	var out []models.Network
	for _, ip := range resp.IpAddresses {
		ip = strings.TrimSpace(ip)
		if ip == "" || strings.HasPrefix(ip, "127.") || ip == "::1" {
			continue
		}
		out = append(out, models.Network{VMID: vmID, IPAddress: ip})
	}
	return out
}

// hostOnlyIP strips any /prefix from an inet string
// ("203.0.113.10/24" -> "203.0.113.10").
func hostOnlyIP(ip string) string {
	if i := strings.IndexByte(ip, '/'); i >= 0 {
		return ip[:i]
	}
	return ip
}

// prefixFromCIDR returns the CIDR prefix length (e.g. 24) for a pool CIDR, or 0
// when it can't be parsed.
func prefixFromCIDR(cidr string) int {
	if cidr == "" {
		return 0
	}
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return 0
	}
	ones, _ := ipnet.Mask.Size()
	return ones
}

// netmaskFromCIDR returns the dotted-decimal netmask for an IPv4 CIDR, or the
// "/prefix" length for IPv6. Empty string when cidr can't be parsed.
func netmaskFromCIDR(cidr string) string {
	if cidr == "" {
		return ""
	}
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return ""
	}
	if len(ipnet.Mask) == net.IPv4len {
		return fmt.Sprintf("%d.%d.%d.%d", ipnet.Mask[0], ipnet.Mask[1], ipnet.Mask[2], ipnet.Mask[3])
	}
	ones, _ := ipnet.Mask.Size()
	return fmt.Sprintf("/%d", ones)
}

func inferIPv4NetworkDefaults(ip string) (gateway string, netmask string) {
	parsed := net.ParseIP(ip).To4()
	if parsed == nil {
		return "", ""
	}
	return fmt.Sprintf("%d.%d.%d.1", parsed[0], parsed[1], parsed[2]), "255.255.255.0"
}

// GetPortForwards returns all port forwards for a VM's network
func (s *NetworkService) GetPortForwards(ctx context.Context, vmID string, networkID string) ([]models.PortForward, error) {
	var portForwards []models.PortForward
	if err := s.db.WithContext(ctx).Where("vm_id = ? AND network_id = ?", vmID, networkID).Find(&portForwards).Error; err != nil {
		return nil, fmt.Errorf("failed to get port forwards: %w", err)
	}
	return portForwards, nil
}

// GetFirewallRules returns all firewall rules for a VM
func (s *NetworkService) GetFirewallRules(ctx context.Context, vmID string) ([]models.FirewallRule, error) {
	return s.firewallRepo.ListByVMID(ctx, vmID, 0, 0)
}

// SyncNetworkConfig syncs all network configuration for a VM (used on agent reconnection)
func (s *NetworkService) SyncNetworkConfig(ctx context.Context, vmID string) error {
	// Get VM with relations
	vm, err := s.vmRepo.GetByIDWithNode(ctx, vmID)
	if err != nil {
		return fmt.Errorf("failed to get VM: %w", err)
	}

	// Get all networks
	networks, err := s.networkRepo.ListByVMID(ctx, vmID)
	if err != nil {
		return fmt.Errorf("failed to get networks: %w", err)
	}

	// Enqueue a full-config sync job per interface. enqueueNetworkConfigJob loads
	// the complete firewall set itself, so the whole config is re-applied.
	for i := range networks {
		_ = s.enqueueNetworkConfigJob(ctx, vm, &networks[i])
	}

	return nil
}

// enqueueNetworkConfigJob enqueues a job to configure network on the agent.
//
// The agent applies this with ReplaceAll semantics: it flushes the VM's entire
// network config (firewall, anti-spoof, VLAN, bandwidth, NAT) and rebuilds it
// from exactly what this job carries. So this MUST always send the VM's FULL
// current firewall rule set — an attribute-only change (bandwidth/VLAN/anti-
// spoof) that omitted the rules would silently wipe the firewall and reopen
// blocked ports. Rules are loaded here, at the single chokepoint, so no caller
// can reintroduce that footgun by forgetting to pass them.
func (s *NetworkService) enqueueNetworkConfigJob(ctx context.Context, vm *models.VM, network *models.Network) error {
	if s.riverClient == nil {
		return fmt.Errorf("river client not initialized")
	}

	// Always send the complete rule set; ReplaceAll drops anything not present.
	// On load failure do NOT enqueue: a partial config would wipe the firewall.
	// The DB change is already persisted and SyncNetworkConfig re-applies full
	// state on the next agent reconnect.
	rules, err := s.firewallRepo.ListByVMID(ctx, vm.ID, 0, 0)
	if err != nil {
		return fmt.Errorf("failed to load firewall rules for network config: %w", err)
	}

	params := NetworkConfigParams{
		IPAddress:      network.IPAddress,
		BandwidthLimit: network.BandwidthLimit,
		VLANID:         network.VLANID,
		AntiSpoofing:   network.AntiSpoofing,
		FirewallRules:  rules,
	}

	// Ship the VM's port forwards as part of the desired state so a full
	// ReplaceAll re-apply (which wipes MABURVM-NAT) re-creates them instead of
	// dropping them.
	var pfs []models.PortForward
	if err := s.db.WithContext(ctx).Where("vm_id = ?", vm.ID).Find(&pfs).Error; err == nil {
		for _, pf := range pfs {
			params.PortForwards = append(params.PortForwards, PortForwardParam{
				ExternalPort: pf.ExternalPort,
				InternalPort: pf.InternalPort,
				Protocol:     pf.Protocol,
				SourceIP:     pf.SourceIP,
			})
		}
	}

	paramsJSON, _ := json.Marshal(params)

	job := queue.VMOperationJob{
		VMID:      vm.ID,
		NodeID:    vm.NodeID,
		Operation: queue.VMOpConfigureNetwork,
		Params:    paramsJSON,
	}

	_, err = s.riverClient.Insert(ctx, job, nil)
	return err
}

// enqueueNetworkResync pushes the VM's FULL desired network state (IP, bandwidth,
// anti-spoofing, firewall rules, port forwards) to the agent via ApplyNetworkConfig
// (ReplaceAll). enqueueNetworkConfigJob loads the complete firewall + port-forward
// set at its chokepoint, so every firewall / port-forward mutation converges host
// iptables idempotently. No interface yet → nothing enforceable, so the rules
// persist in the DB and are applied on IP assignment (SyncNetworkConfig).
func (s *NetworkService) enqueueNetworkResync(ctx context.Context, vm *models.VM) error {
	network, err := s.networkRepo.GetByVMID(ctx, vm.ID)
	if err != nil {
		return nil
	}
	return s.enqueueNetworkConfigJob(ctx, vm, network)
}

// NetworkConfigParams contains network configuration parameters
type NetworkConfigParams struct {
	IPAddress      string                `json:"ip_address"`
	BandwidthLimit int64                 `json:"bandwidth_limit"`
	VLANID         *int                  `json:"vlan_id,omitempty"`
	AntiSpoofing   bool                  `json:"anti_spoofing"`
	FirewallRules  []models.FirewallRule `json:"firewall_rules,omitempty"`
	PortForwards   []PortForwardParam    `json:"port_forwards,omitempty"`
}

// PortForwardParam is a DNAT rule carried in a network-config job.
type PortForwardParam struct {
	ExternalPort int    `json:"external_port"`
	InternalPort int    `json:"internal_port"`
	Protocol     string `json:"protocol"`
	SourceIP     string `json:"source_ip"`
}

// SetAntiSpoofingRequest contains data for toggling anti-spoofing
type SetAntiSpoofingRequest struct {
	Enabled bool `json:"enabled"`
}

// SetAntiSpoofing enables or disables anti-spoofing for a network interface
func (s *NetworkService) SetAntiSpoofing(ctx context.Context, vmID string, networkID string, req *SetAntiSpoofingRequest) error {
	// Verify VM exists
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return fmt.Errorf("VM not found")
		}
		return fmt.Errorf("failed to get VM: %w", err)
	}

	// Get network
	network, err := s.networkRepo.GetByID(ctx, networkID)
	if err != nil {
		return ErrNetworkNotFound
	}

	// Verify network belongs to VM
	if network.VMID != vmID {
		return ErrNetworkNotFound
	}

	// Update anti_spoofing flag
	if err := s.networkRepo.UpdateAntiSpoofing(ctx, networkID, req.Enabled); err != nil {
		return fmt.Errorf("failed to update anti-spoofing: %w", err)
	}

	// Enqueue network config job to agent (anti-spoofing is part of network setup)
	network.AntiSpoofing = req.Enabled
	if err := s.enqueueNetworkConfigJob(ctx, vm, network); err != nil {
		fmt.Printf("failed to enqueue anti-spoofing config job: %v\n", err)
	}

	return nil
}
