package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/maburvm/panel/internal/panel/repository"
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
	if err := s.enqueueNetworkConfigJob(ctx, vm, network, nil); err != nil {
		// Log error but don't fail the request
		fmt.Printf("failed to enqueue network config job: %v\n", err)
	}

	return &AddNetworkResponse{Network: network}, nil
}

// SetBandwidthRequest contains data for setting bandwidth limit
type SetBandwidthRequest struct {
	BandwidthLimit int64 `json:"bandwidth_limit" validate:"required,min=0"`
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
	if err := s.enqueueNetworkConfigJob(ctx, vm, network, nil); err != nil {
		fmt.Printf("failed to enqueue bandwidth config job: %v\n", err)
	}

	return nil
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

	// Enqueue NAT config job to agent
	if err := s.enqueueNATConfigJob(ctx, vm, portForward); err != nil {
		fmt.Printf("failed to enqueue NAT config job: %v\n", err)
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

	// Enqueue NAT removal job to agent
	if err := s.enqueueNATRemovalJob(ctx, vm, &portForward); err != nil {
		fmt.Printf("failed to enqueue NAT removal job: %v\n", err)
	}

	return nil
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

	// Enqueue firewall config job to agent (sync all rules)
	if err := s.enqueueFirewallConfigJob(ctx, vm); err != nil {
		fmt.Printf("failed to enqueue firewall config job: %v\n", err)
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

	// Enqueue firewall config job to agent (sync all remaining rules)
	if err := s.enqueueFirewallConfigJob(ctx, vm); err != nil {
		fmt.Printf("failed to enqueue firewall config job: %v\n", err)
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
	if err := s.enqueueNetworkConfigJob(ctx, vm, network, nil); err != nil {
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

	out := make([]NetworkInterfaceDetail, len(nets))
	for i, n := range nets {
		d := NetworkInterfaceDetail{Network: n}
		if a, ok := addrByIP[hostOnlyIP(n.IPAddress)]; ok {
			d.RDNS = a.RDNS
			d.PoolID = a.PoolID
			if p, ok := pools[a.PoolID]; ok {
				d.Gateway = p.Gateway
				d.Bridge = p.Bridge
				d.Netmask = netmaskFromCIDR(p.CIDR)
			}
		}
		out[i] = d
	}
	return out, nil
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

	// Get all firewall rules
	rules, err := s.firewallRepo.ListByVMID(ctx, vmID, 0, 0)
	if err != nil {
		return fmt.Errorf("failed to get firewall rules: %w", err)
	}

	// Enqueue full sync job for each network
	for _, network := range networks {
		var portForwards []models.PortForward
		if err := s.db.WithContext(ctx).Where("network_id = ?", network.ID).Find(&portForwards).Error; err != nil {
			continue
		}
		_ = s.enqueueNetworkConfigJob(ctx, vm, &network, rules)
	}

	return nil
}

// enqueueNetworkConfigJob enqueues a job to configure network on the agent
func (s *NetworkService) enqueueNetworkConfigJob(ctx context.Context, vm *models.VM, network *models.Network, firewallRules []models.FirewallRule) error {
	if s.riverClient == nil {
		return fmt.Errorf("river client not initialized")
	}

	params := NetworkConfigParams{
		IPAddress:      network.IPAddress,
		BandwidthLimit: network.BandwidthLimit,
		VLANID:         network.VLANID,
	}

	if firewallRules != nil {
		params.FirewallRules = firewallRules
	}

	paramsJSON, _ := json.Marshal(params)

	job := queue.VMOperationJob{
		VMID:      vm.ID,
		NodeID:    vm.NodeID,
		Operation: queue.VMOpConfigureNetwork,
		Params:    paramsJSON,
	}

	_, err := s.riverClient.Insert(ctx, job, nil)
	return err
}

// enqueueNATConfigJob enqueues a job to configure NAT on the agent
func (s *NetworkService) enqueueNATConfigJob(ctx context.Context, vm *models.VM, portForward *models.PortForward) error {
	if s.riverClient == nil {
		return fmt.Errorf("river client not initialized")
	}

	params := NATConfigParams{
		ExternalPort: portForward.ExternalPort,
		InternalPort: portForward.InternalPort,
		Protocol:     portForward.Protocol,
	}

	paramsJSON, _ := json.Marshal(params)

	job := queue.VMOperationJob{
		VMID:      vm.ID,
		NodeID:    vm.NodeID,
		Operation: queue.VMOpAddPortForward,
		Params:    paramsJSON,
	}

	_, err := s.riverClient.Insert(ctx, job, nil)
	return err
}

// enqueueNATRemovalJob enqueues a job to remove NAT on the agent
func (s *NetworkService) enqueueNATRemovalJob(ctx context.Context, vm *models.VM, portForward *models.PortForward) error {
	if s.riverClient == nil {
		return fmt.Errorf("river client not initialized")
	}

	params := NATConfigParams{
		ExternalPort: portForward.ExternalPort,
		InternalPort: portForward.InternalPort,
		Protocol:     portForward.Protocol,
	}

	paramsJSON, _ := json.Marshal(params)

	job := queue.VMOperationJob{
		VMID:      vm.ID,
		NodeID:    vm.NodeID,
		Operation: queue.VMOpRemovePortForward,
		Params:    paramsJSON,
	}

	_, err := s.riverClient.Insert(ctx, job, nil)
	return err
}

// enqueueFirewallConfigJob enqueues a job to configure firewall on the agent
func (s *NetworkService) enqueueFirewallConfigJob(ctx context.Context, vm *models.VM) error {
	if s.riverClient == nil {
		return fmt.Errorf("river client not initialized")
	}

	// Get all firewall rules for this VM
	rules, err := s.firewallRepo.ListByVMID(ctx, vm.ID, 0, 0)
	if err != nil {
		return fmt.Errorf("failed to get firewall rules: %w", err)
	}

	params := FirewallConfigParams{
		Rules: rules,
	}

	paramsJSON, _ := json.Marshal(params)

	job := queue.VMOperationJob{
		VMID:      vm.ID,
		NodeID:    vm.NodeID,
		Operation: queue.VMOpConfigureFirewall,
		Params:    paramsJSON,
	}

	_, err = s.riverClient.Insert(ctx, job, nil)
	return err
}

// NetworkConfigParams contains network configuration parameters
type NetworkConfigParams struct {
	IPAddress      string                `json:"ip_address"`
	BandwidthLimit int64                 `json:"bandwidth_limit"`
	VLANID         *int                  `json:"vlan_id,omitempty"`
	FirewallRules  []models.FirewallRule `json:"firewall_rules,omitempty"`
}

// NATConfigParams contains NAT/port forwarding configuration parameters
type NATConfigParams struct {
	ExternalPort int    `json:"external_port"`
	InternalPort int    `json:"internal_port"`
	Protocol     string `json:"protocol"`
}

// FirewallConfigParams contains firewall configuration parameters
type FirewallConfigParams struct {
	Rules []models.FirewallRule `json:"rules"`
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
	if err := s.enqueueNetworkConfigJob(ctx, vm, network, nil); err != nil {
		fmt.Printf("failed to enqueue anti-spoofing config job: %v\n", err)
	}

	return nil
}
