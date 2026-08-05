package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

var (
	ErrIPPoolNotFound         = errors.New("ip pool not found")
	ErrIPAddressNotFound      = errors.New("ip address not found")
	ErrNoAvailableIPAddress   = errors.New("no available IP address")
	ErrPoolNotAvailableOnNode = errors.New("ip pool is not assigned to the selected node")
	ErrInvalidIPFamily        = errors.New("invalid IP family")
	ErrInvalidIPAddress       = errors.New("invalid IP address")
)

type IPAMService struct {
	db          *gorm.DB
	repo        *repository.IPAMRepository
	dnsProvider DNSProvider // optional; when set+configured, rDNS pushes PTRs live
}

func NewIPAMService(db *gorm.DB, repo *repository.IPAMRepository) *IPAMService {
	return &IPAMService{db: db, repo: repo}
}

// SetDNSProvider wires a live nameserver provider so SetRDNS pushes PTR records
// (e.g. to PowerDNS) in addition to persisting them.
func (s *IPAMService) SetDNSProvider(p DNSProvider) { s.dnsProvider = p }

type CreateIPPoolRequest struct {
	Name        string   `json:"name"`
	NodeIDs     []string `json:"node_ids,omitempty"`
	Family      string   `json:"family"`
	CIDR        string   `json:"cidr,omitempty"`
	Gateway     string   `json:"gateway,omitempty"`
	Bridge      string   `json:"bridge,omitempty"`
	RangeStart  string   `json:"range_start,omitempty"`
	RangeEnd    string   `json:"range_end,omitempty"`
	Description string   `json:"description,omitempty"`
	Orderable   bool     `json:"orderable,omitempty"`
}

// UpdateIPPoolRequest carries editable metadata for an existing pool. Every
// field is a pointer so an omitted field is left unchanged (PATCH semantics) and
// a present field (including an empty string) overwrites the stored value.
// CIDR, family and the address range are intentionally NOT editable here —
// changing them would desync the pool's already-generated addresses.
type UpdateIPPoolRequest struct {
	Name        *string   `json:"name,omitempty"`
	Gateway     *string   `json:"gateway,omitempty"`
	Bridge      *string   `json:"bridge,omitempty"`
	Description *string   `json:"description,omitempty"`
	NodeIDs     *[]string `json:"node_ids,omitempty"`
	Orderable   *bool     `json:"orderable,omitempty"`
}

type CreateIPAddressRequest struct {
	NodeID  *string `json:"node_id,omitempty"`
	Address string  `json:"address"`
	Family  string  `json:"family,omitempty"`
	Status  string  `json:"status,omitempty"`
	Note    string  `json:"note,omitempty"`
}

type AllocateIPAddressRequest struct {
	PoolID      string  `json:"pool_id"`
	NodeID      *string `json:"node_id,omitempty"`
	VMID        *string `json:"vm_id,omitempty"`
	RequestedIP string  `json:"requested_ip,omitempty"`
}

func (s *IPAMService) CreatePool(ctx context.Context, req *CreateIPPoolRequest) (*models.IPPool, error) {
	family := defaultFamily(req.Family)
	if err := validateFamily(family); err != nil {
		return nil, err
	}
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if err := validateOptionalIPAMFields(family, req.CIDR, req.Gateway, req.RangeStart, req.RangeEnd); err != nil {
		return nil, err
	}
	pool := &models.IPPool{
		Name:        req.Name,
		NodeIDs:     req.NodeIDs,
		Family:      family,
		CIDR:        req.CIDR,
		Gateway:     req.Gateway,
		Bridge:      req.Bridge,
		RangeStart:  req.RangeStart,
		RangeEnd:    req.RangeEnd,
		Description: req.Description,
		Orderable:   req.Orderable,
	}
	if err := s.repo.CreatePool(ctx, pool); err != nil {
		return nil, err
	}

	// Auto-generate IP addresses from range or CIDR
	addresses := generatePoolAddresses(pool)
	for i := range addresses {
		addresses[i].PoolID = pool.ID
		if err := s.repo.CreateAddress(ctx, &addresses[i]); err != nil {
			// Log but don't fail pool creation
			break
		}
	}

	return pool, nil
}

// UpdatePool applies metadata edits to an existing pool. The most consequential
// editable field is Bridge: a VM re-reads its pool's bridge from here on every
// start, so correcting a wrong/stale bridge lets a stuck VM (e.g. one defined
// against a now-removed virbr0) boot on its next start.
func (s *IPAMService) UpdatePool(ctx context.Context, id string, req *UpdateIPPoolRequest) (*models.IPPool, error) {
	pool, err := s.GetPool(ctx, id)
	if err != nil {
		return nil, err
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return nil, fmt.Errorf("name cannot be empty")
		}
		pool.Name = name
	}
	if req.Gateway != nil {
		gw := strings.TrimSpace(*req.Gateway)
		if gw != "" && net.ParseIP(gw) == nil {
			return nil, fmt.Errorf("invalid gateway address %q", gw)
		}
		pool.Gateway = gw
	}
	if req.Bridge != nil {
		br := strings.TrimSpace(*req.Bridge)
		if err := validateBridgeName(br); err != nil {
			return nil, err
		}
		pool.Bridge = br
	}
	if req.Description != nil {
		pool.Description = *req.Description
	}
	if req.Orderable != nil {
		pool.Orderable = *req.Orderable
	}
	if err := s.repo.UpdatePool(ctx, pool); err != nil {
		return nil, err
	}
	// Node reassignment lives in the junction table, so it's applied separately.
	if req.NodeIDs != nil {
		if err := s.repo.UpdatePoolNodes(ctx, pool.ID, *req.NodeIDs); err != nil {
			return nil, err
		}
		pool.NodeIDs = *req.NodeIDs
	}
	return pool, nil
}

// validateBridgeName rejects names that can't be a Linux bridge interface (the
// kernel IFNAMSIZ limit is 15 chars; no whitespace or path separators). An empty
// name is allowed and means "fall back to the node/agent default bridge".
func validateBridgeName(name string) error {
	if name == "" {
		return nil
	}
	if len(name) > 15 {
		return fmt.Errorf("bridge name %q is too long (max 15 characters)", name)
	}
	if strings.ContainsAny(name, " \t/\\") {
		return fmt.Errorf("bridge name %q contains invalid characters", name)
	}
	return nil
}

func (s *IPAMService) ListPools(ctx context.Context, limit, offset int) ([]models.IPPool, error) {
	return s.repo.ListPools(ctx, limit, offset)
}

// ListPoolsForNode returns the pools usable on a given node: those bound to it
// (junction table or legacy node_id) plus global pools. Used to auto-assign a
// public IP when the caller didn't pick a pool (e.g. client self-service orders).
func (s *IPAMService) ListPoolsForNode(ctx context.Context, nodeID string) ([]models.IPPool, error) {
	return s.repo.ListPoolsForNode(ctx, nodeID)
}

func (s *IPAMService) GetPool(ctx context.Context, id string) (*models.IPPool, error) {
	pool, err := s.repo.GetPool(ctx, id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrIPPoolNotFound
	}
	return pool, err
}

func (s *IPAMService) DeletePool(ctx context.Context, id string) error {
	if err := s.repo.DeletePool(ctx, id); err != nil {
		return err
	}
	return nil
}

func (s *IPAMService) GenerateAddresses(ctx context.Context, poolID string) (int, error) {
	pool, err := s.repo.GetPool(ctx, poolID)
	if err != nil {
		return 0, fmt.Errorf("pool not found: %w", err)
	}

	addresses := generatePoolAddresses(pool)
	if len(addresses) == 0 {
		return 0, fmt.Errorf("cannot generate addresses: pool has no CIDR or range defined")
	}

	// Get existing addresses to avoid duplicates
	existing, err := s.repo.ListAddresses(ctx, poolID, 0, 0)
	if err != nil {
		return 0, err
	}
	existingSet := make(map[string]*models.IPAddress, len(existing))
	for i := range existing {
		existingSet[existing[i].Address] = &existing[i]
	}

	// Get assigned IPs from networks table to cross-reference
	assignedIPs, err := s.repo.GetAssignedIPMap(ctx)
	if err != nil {
		return 0, err
	}

	count := 0
	for i := range addresses {
		vmID, isAssigned := assignedIPs[addresses[i].Address]

		// If address already exists, sync its status
		if existingAddr, exists := existingSet[addresses[i].Address]; exists {
			if isAssigned && existingAddr.Status != models.IPAddressStatusAssigned {
				existingAddr.Status = models.IPAddressStatusAssigned
				existingAddr.VMID = &vmID
				_ = s.repo.UpdateAddress(ctx, existingAddr)
				count++
			}
			continue
		}

		// New address — create it
		addresses[i].PoolID = pool.ID
		if isAssigned {
			addresses[i].Status = models.IPAddressStatusAssigned
			addresses[i].VMID = &vmID
		}
		if err := s.repo.CreateAddress(ctx, &addresses[i]); err != nil {
			break
		}
		count++
	}

	return count, nil
}

func (s *IPAMService) UpdatePoolNodes(ctx context.Context, id string, nodeIDs []string) error {
	_, err := s.GetPool(ctx, id)
	if err != nil {
		return err
	}
	return s.repo.UpdatePoolNodes(ctx, id, nodeIDs)
}

func (s *IPAMService) AddAddress(ctx context.Context, poolID string, req *CreateIPAddressRequest) (*models.IPAddress, error) {
	pool, err := s.GetPool(ctx, poolID)
	if err != nil {
		return nil, err
	}
	family := defaultFamily(req.Family)
	if req.Family == "" {
		family = pool.Family
	}
	if err := validateFamily(family); err != nil {
		return nil, err
	}
	if family != pool.Family {
		return nil, ErrInvalidIPFamily
	}
	if net.ParseIP(req.Address) == nil {
		return nil, ErrInvalidIPAddress
	}
	status := req.Status
	if status == "" {
		status = models.IPAddressStatusAvailable
	}
	if err := validateAddressStatus(status); err != nil {
		return nil, err
	}
	address := &models.IPAddress{
		PoolID:  poolID,
		NodeID:  req.NodeID,
		Address: req.Address,
		Family:  family,
		Status:  status,
		Note:    req.Note,
	}
	if err := s.repo.CreateAddress(ctx, address); err != nil {
		return nil, err
	}
	return address, nil
}

func (s *IPAMService) ListAddresses(ctx context.Context, poolID string, limit, offset int) ([]models.IPAddress, error) {
	if _, err := s.GetPool(ctx, poolID); err != nil {
		return nil, err
	}
	return s.repo.ListAddresses(ctx, poolID, limit, offset)
}

// AllocateAddress atomically assigns an available or explicitly requested IP address with a row lock.
func (s *IPAMService) AllocateAddress(ctx context.Context, req *AllocateIPAddressRequest) (*models.IPAddress, error) {
	var allocated *models.IPAddress
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		address, err := s.allocateAddressInTx(ctx, tx, req)
		if err != nil {
			return err
		}
		allocated = address
		return nil
	})
	if err != nil {
		return nil, err
	}
	return allocated, nil
}

func (s *IPAMService) allocateAddressInTx(ctx context.Context, tx *gorm.DB, req *AllocateIPAddressRequest) (*models.IPAddress, error) {
	txRepo := s.repo.WithDB(tx)
	pool, err := txRepo.GetPool(ctx, req.PoolID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrIPPoolNotFound
		}
		return nil, err
	}

	// Verify node is allowed for this pool
	if req.NodeID != nil && *req.NodeID != "" && len(pool.NodeIDs) > 0 {
		allowed := false
		for _, nid := range pool.NodeIDs {
			if nid == *req.NodeID {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, ErrPoolNotAvailableOnNode
		}
	}

	var address *models.IPAddress
	if req.RequestedIP != "" {
		parsedIP := net.ParseIP(req.RequestedIP)
		if parsedIP == nil || ipFamily(parsedIP) != pool.Family {
			return nil, ErrInvalidIPAddress
		}
		address, err = txRepo.FindAddressForUpdate(ctx, req.PoolID, req.RequestedIP)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrIPAddressNotFound
			}
			return nil, err
		}
		if address.Status != models.IPAddressStatusAvailable && address.Status != models.IPAddressStatusReserved {
			return nil, ErrNoAvailableIPAddress
		}
		// A reserved IP auto-flagged as live-on-the-network (externalIPNote) is used
		// by a host we don't manage — never hand it out, even when explicitly
		// requested. Admin-reserved IPs (different/empty note) stay requestable.
		if address.Status == models.IPAddressStatusReserved && address.Note == externalIPNote {
			return nil, ErrNoAvailableIPAddress
		}
		// A floating IP is held reserved for its tenant between attachments;
		// handing it out here as a VM's direct address would hijack it.
		if address.DeliveryMode == models.IPDeliveryFloating {
			return nil, ErrNoAvailableIPAddress
		}
	} else {
		address, err = txRepo.FindAvailableAddressForUpdate(ctx, req.PoolID, req.NodeID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrNoAvailableIPAddress
			}
			return nil, err
		}
	}

	if req.NodeID != nil && *req.NodeID != "" && address.NodeID != nil && *address.NodeID != "" && *address.NodeID != *req.NodeID {
		return nil, ErrNoAvailableIPAddress
	}
	address.Status = models.IPAddressStatusAssigned
	address.VMID = req.VMID
	if req.NodeID != nil {
		address.NodeID = req.NodeID
	}
	if err := txRepo.UpdateAddress(ctx, address); err != nil {
		return nil, err
	}
	return address, nil
}

func (s *IPAMService) AllocateAddressInTx(ctx context.Context, tx *gorm.DB, req *AllocateIPAddressRequest) (*models.IPAddress, error) {
	return s.allocateAddressInTx(ctx, tx, req)
}

func (s *IPAMService) ReleaseAddress(ctx context.Context, addressID string) error {
	address, err := s.repo.GetAddress(ctx, addressID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrIPAddressNotFound
		}
		return err
	}
	address.Status = models.IPAddressStatusAvailable
	address.VMID = nil
	return s.repo.UpdateAddress(ctx, address)
}

func (s *IPAMService) ReleaseAddressesByVMIDInTx(ctx context.Context, tx *gorm.DB, vmID string) error {
	return s.repo.WithDB(tx).ReleaseAddressesByVMID(ctx, vmID)
}

func (s *IPAMService) AssignImportedAddressInTx(ctx context.Context, tx *gorm.DB, vmID, nodeID, ip string) error {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return nil
	}
	family := ipFamily(parsed)
	txRepo := s.repo.WithDB(tx)
	pools, err := txRepo.ListPoolsForNode(ctx, nodeID)
	if err != nil {
		return err
	}
	for _, pool := range pools {
		if pool.Family != family || !ipInPool(parsed, &pool) {
			continue
		}
		address, err := txRepo.FindAddressForUpdate(ctx, pool.ID, ip)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			nid := nodeID
			address = &models.IPAddress{
				PoolID:  pool.ID,
				NodeID:  &nid,
				Address: ip,
				Family:  family,
				Status:  models.IPAddressStatusAssigned,
				VMID:    &vmID,
				Note:    "imported VM address",
			}
			return txRepo.CreateAddress(ctx, address)
		}
		if err != nil {
			return err
		}
		if address.Status != models.IPAddressStatusAvailable && address.Status != models.IPAddressStatusReserved {
			return nil
		}
		address.Status = models.IPAddressStatusAssigned
		address.VMID = &vmID
		if nodeID != "" && address.NodeID == nil {
			address.NodeID = &nodeID
		}
		return txRepo.UpdateAddress(ctx, address)
	}
	return nil
}

func ipInPool(ip net.IP, pool *models.IPPool) bool {
	if pool.CIDR != "" {
		_, cidr, err := net.ParseCIDR(pool.CIDR)
		if err == nil && cidr.Contains(ip) {
			return true
		}
	}
	if pool.RangeStart != "" && pool.RangeEnd != "" {
		start := net.ParseIP(pool.RangeStart)
		end := net.ParseIP(pool.RangeEnd)
		return ipCompare(ip, start) >= 0 && ipCompare(ip, end) <= 0
	}
	return false
}

func ipCompare(a, b net.IP) int {
	if a == nil || b == nil {
		return -1
	}
	if a4, b4 := a.To4(), b.To4(); a4 != nil && b4 != nil {
		a, b = a4, b4
	} else {
		a, b = a.To16(), b.To16()
	}
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

func defaultFamily(family string) string {
	if family == "" {
		return models.IPFamilyIPv4
	}
	return family
}

func validateFamily(family string) error {
	if family != models.IPFamilyIPv4 && family != models.IPFamilyIPv6 {
		return ErrInvalidIPFamily
	}
	return nil
}

func validateAddressStatus(status string) error {
	switch status {
	case models.IPAddressStatusAvailable, models.IPAddressStatusReserved, models.IPAddressStatusAssigned, models.IPAddressStatusDisabled:
		return nil
	default:
		return fmt.Errorf("invalid IP address status")
	}
}

func validateOptionalIPAMFields(family, cidr, gateway, start, end string) error {
	if cidr != "" {
		ip, _, err := net.ParseCIDR(cidr)
		if err != nil || ipFamily(ip) != family {
			return fmt.Errorf("invalid CIDR")
		}
	}
	for _, value := range []string{gateway, start, end} {
		if value == "" {
			continue
		}
		ip := net.ParseIP(value)
		if ip == nil || ipFamily(ip) != family {
			return ErrInvalidIPAddress
		}
	}
	return nil
}

func ipFamily(ip net.IP) string {
	if ip.To4() != nil {
		return models.IPFamilyIPv4
	}
	return models.IPFamilyIPv6
}

// generatePoolAddresses creates IP address entries based on pool's range or CIDR.
// Gateway and network/broadcast addresses are excluded.
// Max 1024 addresses per pool to prevent accidental huge allocations.
const maxAutoGenerateAddresses = 1024

func generatePoolAddresses(pool *models.IPPool) []models.IPAddress {
	var startIP, endIP net.IP

	if pool.RangeStart != "" && pool.RangeEnd != "" {
		startIP = net.ParseIP(pool.RangeStart)
		endIP = net.ParseIP(pool.RangeEnd)
	} else if pool.CIDR != "" {
		ip, ipNet, err := net.ParseCIDR(pool.CIDR)
		if err != nil {
			return nil
		}
		startIP = nextIP(ip.Mask(ipNet.Mask)) // skip network address
		endIP = lastIP(ipNet)
		endIP = prevIP(endIP) // skip broadcast address
	}

	if startIP == nil || endIP == nil {
		return nil
	}

	gateway := net.ParseIP(pool.Gateway)
	var addresses []models.IPAddress
	current := cloneIP(startIP)

	for ipCompare(current, endIP) <= 0 && len(addresses) < maxAutoGenerateAddresses {
		// Skip gateway
		if gateway != nil && current.Equal(gateway) {
			current = nextIP(current)
			continue
		}
		addresses = append(addresses, models.IPAddress{
			Address: current.String(),
			Family:  pool.Family,
			Status:  models.IPAddressStatusAvailable,
		})
		current = nextIP(current)
	}

	return addresses
}

func nextIP(ip net.IP) net.IP {
	result := cloneIP(ip)
	for i := len(result) - 1; i >= 0; i-- {
		result[i]++
		if result[i] != 0 {
			break
		}
	}
	return result
}

func prevIP(ip net.IP) net.IP {
	result := cloneIP(ip)
	for i := len(result) - 1; i >= 0; i-- {
		result[i]--
		if result[i] != 255 {
			break
		}
	}
	return result
}

func lastIP(ipNet *net.IPNet) net.IP {
	ip := cloneIP(ipNet.IP)
	for i := range ip {
		ip[i] |= ^ipNet.Mask[i]
	}
	return ip
}

func cloneIP(ip net.IP) net.IP {
	dup := make(net.IP, len(ip))
	copy(dup, ip)
	return dup
}
