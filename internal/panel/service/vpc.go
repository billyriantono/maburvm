package service

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"strings"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

var (
	// ErrVPCNotFound is returned for a missing VPC, or one the caller does not own.
	ErrVPCNotFound = errors.New("VPC not found")
	// ErrVPCSubnetOverlap is returned when a tenant's new subnet overlaps one of
	// their OWN existing VPCs. Overlap with another tenant is fine and expected —
	// each VPC has its own router namespace, so 10.0.0.0/24 can belong to any
	// number of customers at once.
	ErrVPCSubnetOverlap = errors.New("subnet overlaps one of your existing VPCs")
	// ErrVPCQuotaExceeded caps how many VPCs one tenant may hold.
	ErrVPCQuotaExceeded = errors.New("VPC limit reached for this account")
	// ErrVPCInUse blocks deleting a VPC that still has VMs in it.
	ErrVPCInUse = errors.New("VPC still has VMs in it")
	// ErrVPCWrongRegion is returned when a VM's chosen region and its chosen
	// private network are in different places. A VPC lives on one node, so it
	// belongs to exactly one region and cannot be joined from another.
	ErrVPCWrongRegion = errors.New("that private network is in a different region; pick a network in the region you selected")
)

// CreateVPCRequest is a tenant's own description of a private network.
type CreateVPCRequest struct {
	Name    string `json:"name"`
	Subnet  string `json:"subnet"`
	Gateway string `json:"gateway,omitempty"` // defaults to the first usable address
	NodeID  string `json:"node_id,omitempty"` // admin override; otherwise auto-selected
	// Region the network lives in. A VPC does not span regions, so a customer
	// must say which location it belongs to — otherwise they discover the answer
	// only when a VM refuses to join it.
	Region string `json:"region,omitempty"`
}

// VPCService owns tenant VPCs: the managed network, its node-side provisioning
// (delegated to ManagedNetworkService) and the private IP pool VMs draw from.
type VPCService struct {
	db     *gorm.DB
	mn     *ManagedNetworkService
	ipam   *IPAMService
	vmRepo interface {
		CountByNetworkBridge(ctx context.Context, bridge string) (int64, error)
	}
}

func NewVPCService(db *gorm.DB, ipam *IPAMService) *VPCService {
	return &VPCService{db: db, mn: NewManagedNetworkService(db), ipam: ipam}
}

// subnetsOverlap reports whether two CIDRs cover any address in common. Testing
// containment in BOTH directions is what catches the asymmetric case: a /23 does
// not sit inside a /24, but it swallows it.
func subnetsOverlap(a, b *net.IPNet) bool {
	return a.Contains(b.IP) || b.Contains(a.IP)
}

// firstUsableAddress returns the address after the network address, the
// conventional gateway for a subnet.
func firstUsableAddress(ipnet *net.IPNet) string {
	ip := ipnet.IP.To4()
	if ip == nil {
		return ""
	}
	next := net.IPv4(ip[0], ip[1], ip[2], ip[3]).To4()
	next[3]++
	return next.String()
}

// Create provisions a tenant VPC: validates the subnet against the tenant's own
// networks, materialises the router namespace on a node, and creates the private
// IP pool its VMs draw addresses from.
func (s *VPCService) Create(ctx context.Context, userID string, req *CreateVPCRequest) (*models.ManagedNetwork, error) {
	if strings.TrimSpace(req.Name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	ip, ipnet, err := net.ParseCIDR(strings.TrimSpace(req.Subnet))
	if err != nil {
		return nil, fmt.Errorf("invalid subnet %q: it must look like 10.0.0.0/24", req.Subnet)
	}
	if ip.To4() == nil || !ip.IsPrivate() {
		return nil, fmt.Errorf("subnet must be a private range: 10.0.0.0/8, 172.16.0.0/12 or 192.168.0.0/16")
	}
	if ones, _ := ipnet.Mask.Size(); ones > 29 {
		return nil, fmt.Errorf("subnet %s is too small to host VMs; use /29 or larger", req.Subnet)
	}
	gateway := strings.TrimSpace(req.Gateway)
	if gateway == "" {
		gateway = firstUsableAddress(ipnet)
	}
	if gw := net.ParseIP(gateway); gw == nil || !ipnet.Contains(gw) {
		return nil, fmt.Errorf("gateway %s is not inside %s", gateway, req.Subnet)
	}

	nodeID := strings.TrimSpace(req.NodeID)
	if nodeID == "" {
		nodeID, err = s.pickNodeInRegion(ctx, strings.TrimSpace(req.Region))
		if err != nil {
			return nil, err
		}
	}

	var created *models.ManagedNetwork
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialise this tenant's VPC creates. Two concurrent requests could
		// otherwise both pass the overlap check and both be accepted.
		if err := lockUserVPCs(ctx, tx, userID); err != nil {
			return err
		}

		var existing []models.ManagedNetwork
		if err := tx.Where("user_id = ? AND type = ?", userID, NetworkTypeVPC).Find(&existing).Error; err != nil {
			return err
		}
		if len(existing) >= VPCsPerUser(ctx, s.db) {
			return ErrVPCQuotaExceeded
		}
		for i := range existing {
			_, other, perr := net.ParseCIDR(existing[i].Subnet)
			if perr != nil {
				continue
			}
			if subnetsOverlap(ipnet, other) {
				return fmt.Errorf("%w: %s overlaps %s (%s)",
					ErrVPCSubnetOverlap, req.Subnet, existing[i].Subnet, existing[i].Name)
			}
		}

		net := &models.ManagedNetwork{
			Name:    strings.TrimSpace(req.Name),
			Type:    NetworkTypeVPC,
			Subnet:  ipnet.String(),
			Gateway: gateway,
			NodeID:  &nodeID,
			UserID:  &userID,
		}
		// Create provisions the router namespace on the node and records its
		// bridge; done inside the transaction so a node failure rolls the row back
		// rather than leaving a VPC that exists only in the database.
		if err := s.mn.CreateTx(ctx, tx, net); err != nil {
			return err
		}
		created = net
		return nil
	})
	if err != nil {
		return nil, err
	}

	// The pool is what lets VMs actually get an address in the VPC. It is bound
	// to the VPC's own bridge, which is unique per VPC, so two tenants' identical
	// subnets never draw from the same pool.
	if _, err := s.ipam.CreatePool(ctx, &CreateIPPoolRequest{
		Name:        "vpc-" + created.ID[:8],
		Family:      models.IPFamilyIPv4,
		CIDR:        created.Subnet,
		Gateway:     created.Gateway,
		Bridge:      created.Bridge,
		NodeIDs:     []string{nodeID},
		Description: "private addresses for VPC " + created.Name,
	}); err != nil {
		// Roll the VPC back rather than leave one whose VMs could never get an address.
		_ = s.Delete(ctx, userID, created.ID)
		return nil, fmt.Errorf("failed to create the VPC's address pool: %w", err)
	}
	return created, nil
}

// lockUserVPCs takes a transaction-scoped advisory lock keyed on the tenant, so
// concurrent creates for the same account serialise through the overlap check.
func lockUserVPCs(ctx context.Context, tx *gorm.DB, userID string) error {
	h := fnv.New64a()
	_, _ = h.Write([]byte("maburvm-vpc:" + userID))
	return tx.WithContext(ctx).Exec("SELECT pg_advisory_xact_lock(?)", int64(h.Sum64())).Error
}

// List returns a tenant's VPCs (all of them when userID is empty, for admins).
func (s *VPCService) List(ctx context.Context, userID string) ([]models.ManagedNetwork, error) {
	var out []models.ManagedNetwork
	q := s.db.WithContext(ctx).Where("type = ?", NetworkTypeVPC)
	if userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	if err := q.Order("created_at DESC").Find(&out).Error; err != nil {
		return nil, err
	}
	s.fillRegions(ctx, out)
	return out, nil
}

// fillRegions annotates networks with the region of the node they sit on, so a
// customer can see that a VPC belongs to one location rather than all of them.
func (s *VPCService) fillRegions(ctx context.Context, nets []models.ManagedNetwork) {
	var rows []struct {
		NodeID  string
		ID      string
		Name    string
		Country string
	}
	if err := s.db.WithContext(ctx).Table("nodes").
		Select("nodes.id AS node_id, regions.id, regions.name, regions.country").
		Joins("JOIN regions ON regions.id = nodes.region_id").
		Scan(&rows).Error; err != nil {
		return
	}
	byNode := make(map[string]struct{ ID, Name, Country string }, len(rows))
	for _, r := range rows {
		byNode[r.NodeID] = struct{ ID, Name, Country string }{r.ID, r.Name, r.Country}
	}
	for i := range nets {
		if nets[i].NodeID == nil {
			continue
		}
		if reg, ok := byNode[*nets[i].NodeID]; ok {
			nets[i].RegionID, nets[i].RegionName, nets[i].RegionCountry = reg.ID, reg.Name, reg.Country
		}
	}
}

// Get loads one VPC, enforcing ownership for non-admin callers.
func (s *VPCService) Get(ctx context.Context, userID, id string) (*models.ManagedNetwork, error) {
	var vpc models.ManagedNetwork
	q := s.db.WithContext(ctx).Where("id = ? AND type = ?", id, NetworkTypeVPC)
	if userID != "" {
		q = q.Where("user_id = ?", userID)
	}
	if err := q.First(&vpc).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVPCNotFound
		}
		return nil, err
	}
	s.fillRegions(ctx, []models.ManagedNetwork{vpc})
	one := []models.ManagedNetwork{vpc}
	s.fillRegions(ctx, one)
	return &one[0], nil
}

// Delete removes a VPC, its address pool and its node-side namespace. It refuses
// while VMs are still in it, so a customer cannot strand their own machines.
func (s *VPCService) Delete(ctx context.Context, userID, id string) error {
	vpc, err := s.Get(ctx, userID, id)
	if err != nil {
		return err
	}
	if vpc.Bridge != "" {
		var inUse int64
		if err := s.db.WithContext(ctx).Model(&models.IPAddress{}).
			Joins("JOIN ip_pools p ON p.id = ip_addresses.pool_id").
			Where("p.bridge = ? AND ip_addresses.vm_id IS NOT NULL", vpc.Bridge).
			Count(&inUse).Error; err == nil && inUse > 0 {
			return fmt.Errorf("%w (%d)", ErrVPCInUse, inUse)
		}
		// Drop the pool first; leaving it would let a later VM be handed an address
		// on a bridge that no longer exists.
		var pools []models.IPPool
		if err := s.db.WithContext(ctx).Where("bridge = ?", vpc.Bridge).Find(&pools).Error; err == nil {
			for i := range pools {
				_ = s.ipam.DeletePool(ctx, pools[i].ID)
			}
		}
	}
	return s.mn.Delete(ctx, id)
}

// pickNode chooses a node for a VPC. Any active node will do: a VPC is
// self-contained on one host, and its subnet cannot clash with another tenant's.
func (s *VPCService) pickNodeInRegion(ctx context.Context, regionIDOrSlug string) (string, error) {
	q := s.db.WithContext(ctx).Where("status = ?", "active")
	if regionIDOrSlug != "" {
		region, err := NewRegionService(s.db).Get(ctx, regionIDOrSlug)
		if err != nil {
			return "", err
		}
		q = q.Where("region_id = ?", region.ID)
	}
	var node models.Node
	if err := q.Order("created_at ASC").First(&node).Error; err != nil {
		if regionIDOrSlug != "" {
			return "", ErrRegionNoCapacity
		}
		return "", fmt.Errorf("no active node available to host a private network")
	}
	return node.ID, nil
}
