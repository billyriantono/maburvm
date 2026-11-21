package queue

import (
	"context"
	"net"
	"strings"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/shared/models"
)

// workerDB is the panel's database, nil in a worker built without one.
func workerDB() *gorm.DB {
	if globalWorkerContext == nil {
		return nil
	}
	return globalWorkerContext.DB
}

// hostIP drops a prefix length from an address as the database prints it.
func hostIP(a string) string { return strings.SplitN(a, "/", 2)[0] }

// addressFacts are what the pool an address came from says about it: the
// resolvers its guest gets, its gateway, and whether the node serves it by
// DHCP.
type addressFacts struct {
	DNS     []string
	Gateway string
	DHCP    bool
	// Bridge and Prefix are the pool's bridge and prefix length.
	Bridge string
	Prefix int
}

// vmAddressFacts reads addressFacts for each of a VM's IPAM addresses. A
// database without the columns (an older test schema) has none to give.
func vmAddressFacts(ctx context.Context, db *gorm.DB, vmID string) map[string]addressFacts {
	var rows []struct {
		Address     string
		Gateway     string
		Nameservers string
		DHCP        bool `gorm:"column:dhcp"`
		Bridge      string
		CIDR        string `gorm:"column:cidr"`
	}
	out := map[string]addressFacts{}
	err := db.WithContext(ctx).Session(&gorm.Session{Logger: gormlogger.Discard}).Table("ip_addresses a").
		Select("a.address, COALESCE(CAST(p.gateway AS TEXT), '') AS gateway, p.nameservers, p.dhcp, COALESCE(p.bridge, '') AS bridge, COALESCE(CAST(p.cidr AS TEXT), '') AS cidr").
		Joins("JOIN ip_pools p ON CAST(p.id AS TEXT) = CAST(a.pool_id AS TEXT) AND p.deleted_at IS NULL").
		Where("CAST(a.vm_id AS TEXT) = ? AND a.deleted_at IS NULL", vmID).Scan(&rows).Error
	if err != nil {
		return out
	}
	for _, r := range rows {
		f := addressFacts{DNS: models.SplitList(r.Nameservers), Gateway: hostIP(r.Gateway), Bridge: r.Bridge}
		if _, n, err := net.ParseCIDR(r.CIDR); err == nil {
			f.Prefix, _ = n.Mask.Size()
		}
		// DHCP hands out IPv4 only.
		if ip := net.ParseIP(hostIP(r.Address)); ip != nil && ip.To4() != nil {
			f.DHCP = r.DHCP
		}
		out[hostIP(r.Address)] = f
	}
	return out
}

// vmIPv6Facts reads the resolvers of the pool a VM's routed IPv6 prefix came
// from and the further addresses its guest holds in it.
func vmIPv6Facts(ctx context.Context, db *gorm.DB, vmID string) (dns, extra []string) {
	var row struct {
		Nameservers    string
		ExtraAddresses string
	}
	if err := db.WithContext(ctx).Session(&gorm.Session{Logger: gormlogger.Discard}).Table("vm_ipv6_prefixes v").
		Select("p.nameservers, v.extra_addresses").
		Joins("JOIN ip_pools p ON CAST(p.id AS TEXT) = CAST(v.pool_id AS TEXT)").
		Where("CAST(v.vm_id AS TEXT) = ?", vmID).Limit(1).Scan(&row).Error; err != nil {
		return nil, nil
	}
	return models.SplitList(row.Nameservers), models.SplitList(row.ExtraAddresses)
}

// vmPrimaryPool is the gateway and prefix length of the pool the VM's
// address came from; false when it is not an IPAM address.
func vmPrimaryPool(ctx context.Context, db *gorm.DB, vmID, address string) (string, int, bool) {
	var row struct {
		Gateway string
		CIDR    string `gorm:"column:cidr"`
	}
	err := db.WithContext(ctx).Session(&gorm.Session{Logger: gormlogger.Discard}).Table("ip_addresses a").
		Select("COALESCE(CAST(p.gateway AS TEXT), '') AS gateway, CAST(p.cidr AS TEXT) AS cidr").
		Joins("JOIN ip_pools p ON CAST(p.id AS TEXT) = CAST(a.pool_id AS TEXT)").
		Where("CAST(a.vm_id AS TEXT) = ? AND a.address = ? AND a.deleted_at IS NULL", vmID, hostIP(address)).
		Limit(1).Scan(&row).Error
	if err != nil || row.CIDR == "" {
		return "", 0, false
	}
	_, n, err := net.ParseCIDR(row.CIDR)
	if err != nil {
		return "", 0, false
	}
	ones, _ := n.Mask.Size()
	return hostIP(row.Gateway), ones, true
}

// vmCardCount is how many network cards the panel has on record for a VM.
func vmCardCount(ctx context.Context, db *gorm.DB, vmID string) int64 {
	var n int64
	_ = db.WithContext(ctx).Session(&gorm.Session{Logger: gormlogger.Discard}).Table("vm_nics").Where("vm_id = ?", vmID).Count(&n).Error
	return n
}
