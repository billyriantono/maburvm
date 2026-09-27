package service

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrPoolNotDelegated          = errors.New("ip pool does not delegate prefixes per VM")
	ErrVMAlreadyHasIPv6Prefix    = errors.New("VM already has an IPv6 prefix")
	ErrNoAvailableIPv6Prefix     = errors.New("no free IPv6 prefix left in pool")
	ErrInvalidDelegatedPrefixLen = errors.New("delegated_prefix_len must be an IPv6 length longer than the pool CIDR and at most 64")
)

// validateDelegatedPrefixLen checks a pool's per-VM prefix length against its
// family and CIDR: IPv6 only, strictly inside the pool, and no longer than /64
// so every guest gets a SLAAC-sized network.
func validateDelegatedPrefixLen(family, cidr string, n int) error {
	if family != models.IPFamilyIPv6 || cidr == "" || n > 64 {
		return ErrInvalidDelegatedPrefixLen
	}
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("invalid CIDR")
	}
	if poolLen, _ := ipNet.Mask.Size(); poolLen >= n {
		return ErrInvalidDelegatedPrefixLen
	}
	return nil
}

// delegatedPrefix returns the idx-th /<n> inside poolCIDR, e.g. idx 5 of
// 2001:db8:20::/48 with n=64 is 2001:db8:20:5::/64. idx 0 is the pool's own
// first subnet and is reserved for the router<->node link, so allocation
// starts at 1; the caller enforces that, this only enforces the upper bound.
func delegatedPrefix(poolCIDR string, n, idx int) (string, error) {
	_, ipNet, err := net.ParseCIDR(poolCIDR)
	if err != nil || ipNet.IP.To4() != nil {
		return "", fmt.Errorf("invalid IPv6 pool CIDR %q", poolCIDR)
	}
	poolLen, _ := ipNet.Mask.Size()
	if n <= poolLen || n > 128 {
		return "", ErrInvalidDelegatedPrefixLen
	}
	if idx < 0 || idx >= delegatedPrefixCount(poolLen, n) {
		return "", fmt.Errorf("prefix index %d out of range for %s split into /%d", idx, poolCIDR, n)
	}
	// network + (idx << (128 - n)), as one 128-bit integer.
	addr := new(big.Int).SetBytes(ipNet.IP.To16())
	addr.Add(addr, new(big.Int).Lsh(big.NewInt(int64(idx)), uint(128-n)))
	out := make(net.IP, net.IPv6len)
	addr.FillBytes(out)
	return fmt.Sprintf("%s/%d", out, n), nil
}

// delegatedPrefixCount is how many /<n> fit in a /<poolLen> (capped so an
// absurd split cannot overflow int).
func delegatedPrefixCount(poolLen, n int) int {
	bits := n - poolLen
	if bits <= 0 {
		return 0
	}
	if bits > 30 {
		bits = 30
	}
	return 1 << bits
}

// lowestFreeIdx returns the smallest index >= 1 not in used (used sorted
// ascending), or -1 when max (exclusive) is reached. Gaps left by released
// prefixes are reused first, so the pool never "runs out" while it has holes.
func lowestFreeIdx(used []int, max int) int {
	next := 1
	for _, u := range used {
		if u < next {
			continue
		}
		if u > next {
			break
		}
		next++
	}
	if next >= max {
		return -1
	}
	return next
}

// AllocateIPv6PrefixInTx hands the lowest free prefix of a delegated pool to
// vmID. The pool row is locked FOR UPDATE for the duration, so two concurrent
// creates cannot pick the same index; the UNIQUE constraints are the backstop.
func (s *IPAMService) AllocateIPv6PrefixInTx(ctx context.Context, tx *gorm.DB, poolID, vmID, nodeID string) (*models.VMIPv6Prefix, error) {
	var pool models.IPPool
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&pool, "id = ?", poolID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrIPPoolNotFound
		}
		return nil, err
	}
	if !pool.IsDelegated() || pool.Family != models.IPFamilyIPv6 {
		return nil, ErrPoolNotDelegated
	}
	nodeIDs, err := s.repo.WithDB(tx).GetPoolNodeIDs(ctx, pool.ID)
	if err != nil {
		return nil, err
	}
	pool.NodeIDs = nodeIDs
	if nodeID != "" && !poolAllowsNode(&pool, nodeID) {
		return nil, ErrPoolNotAvailableOnNode
	}

	var existing int64
	if err := tx.WithContext(ctx).Model(&models.VMIPv6Prefix{}).Where("vm_id = ?", vmID).Count(&existing).Error; err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, ErrVMAlreadyHasIPv6Prefix
	}

	var used []int
	if err := tx.WithContext(ctx).Model(&models.VMIPv6Prefix{}).
		Where("pool_id = ?", pool.ID).Order("idx ASC").Pluck("idx", &used).Error; err != nil {
		return nil, err
	}
	_, ipNet, err := net.ParseCIDR(pool.CIDR)
	if err != nil {
		return nil, fmt.Errorf("pool %s has an invalid CIDR: %w", pool.Name, err)
	}
	poolLen, _ := ipNet.Mask.Size()
	idx := lowestFreeIdx(used, delegatedPrefixCount(poolLen, *pool.DelegatedPrefixLen))
	if idx < 0 {
		return nil, ErrNoAvailableIPv6Prefix
	}
	prefix, err := delegatedPrefix(pool.CIDR, *pool.DelegatedPrefixLen, idx)
	if err != nil {
		return nil, err
	}
	row := &models.VMIPv6Prefix{PoolID: pool.ID, VMID: vmID, Prefix: prefix, Idx: idx}
	if err := tx.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	return row, nil
}

// poolAllowsNode mirrors the node check of address allocation: a pool bound to
// nodes serves only those; an unbound pool serves any node.
func poolAllowsNode(pool *models.IPPool, nodeID string) bool {
	if len(pool.NodeIDs) == 0 {
		return pool.NodeID == nil || *pool.NodeID == "" || *pool.NodeID == nodeID
	}
	for _, nid := range pool.NodeIDs {
		if nid == nodeID {
			return true
		}
	}
	return false
}

// GetVMIPv6Prefix returns the VM's delegated prefix, or nil when it has none.
func (s *IPAMService) GetVMIPv6Prefix(ctx context.Context, vmID string) (*models.VMIPv6Prefix, error) {
	return vmIPv6Prefix(ctx, s.db, vmID)
}

// vmIPv6Prefix is GetVMIPv6Prefix for callers that hold a db handle but no
// IPAMService (the network service and the job workers).
func vmIPv6Prefix(ctx context.Context, db *gorm.DB, vmID string) (*models.VMIPv6Prefix, error) {
	var p models.VMIPv6Prefix
	err := db.WithContext(ctx).Where("vm_id = ?", vmID).First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ReleaseIPv6PrefixByVMInTx returns a VM's delegated prefix to its pool.
func (s *IPAMService) ReleaseIPv6PrefixByVMInTx(ctx context.Context, tx *gorm.DB, vmID string) error {
	return s.repo.WithDB(tx).ReleaseIPv6PrefixesByVMID(ctx, vmID)
}

// countDelegated fills DelegatedCount on a delegated pool.
func (s *IPAMService) countDelegated(ctx context.Context, pool *models.IPPool) {
	if pool == nil || !pool.IsDelegated() {
		return
	}
	_ = s.db.WithContext(ctx).Model(&models.VMIPv6Prefix{}).Where("pool_id = ?", pool.ID).Count(&pool.DelegatedCount).Error
}

// DelegatedPrefix is one /64 of a delegating pool with the VM that holds it.
type DelegatedPrefix struct {
	Prefix    string    `json:"prefix"`
	VMID      string    `json:"vm_id"`
	Hostname  string    `json:"hostname"`
	CreatedAt time.Time `json:"created_at"`
}

// ListDelegatedPrefixes lists a delegating pool's assigned prefixes, lowest first.
func (s *IPAMService) ListDelegatedPrefixes(ctx context.Context, poolID string) ([]DelegatedPrefix, error) {
	if _, err := s.GetPool(ctx, poolID); err != nil {
		return nil, err
	}
	out := []DelegatedPrefix{}
	err := s.db.WithContext(ctx).Table("vm_ipv6_prefixes p").
		Select("p.prefix::text AS prefix, p.vm_id, coalesce(v.hostname, '') AS hostname, p.created_at").
		Joins("LEFT JOIN vms v ON v.id = p.vm_id").
		Where("p.pool_id = ?", poolID).Order("p.idx").Scan(&out).Error
	return out, err
}
