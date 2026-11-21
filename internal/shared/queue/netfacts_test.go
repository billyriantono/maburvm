package queue

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// The worker reads each address's pool for the guest's resolvers and the
// node's DHCP, and the routed prefix for further IPv6 addresses.
func TestVMNetworkFacts(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:facts-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	for _, stmt := range []string{
		`CREATE TABLE ip_pools (id TEXT PRIMARY KEY, gateway TEXT, cidr TEXT, nameservers TEXT NOT NULL DEFAULT '', dhcp BOOLEAN NOT NULL DEFAULT 0, bridge TEXT, deleted_at DATETIME)`,
		`CREATE TABLE ip_addresses (id TEXT PRIMARY KEY, pool_id TEXT, address TEXT, vm_id TEXT, deleted_at DATETIME)`,
		`CREATE TABLE vm_ipv6_prefixes (id TEXT PRIMARY KEY, pool_id TEXT, vm_id TEXT, prefix TEXT, extra_addresses TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE vm_nics (id TEXT PRIMARY KEY, vm_id TEXT, position INTEGER, bridge TEXT, mac_address TEXT)`,
		`INSERT INTO ip_pools VALUES ('p4', '192.0.2.1', '192.0.2.0/24', '9.9.9.9,2620:fe::fe', 1, NULL, NULL), ('pv', '10.0.0.1', '10.0.0.0/24', '', 0, 'vpcbr', NULL), ('p6', 'fe80::1', '2001:db8::/48', '2620:fe::9', 0, NULL, NULL)`,
		`INSERT INTO ip_addresses VALUES ('a1', 'p4', '192.0.2.10', 'vm-1', NULL), ('a2', 'pv', '10.0.0.5', 'vm-1', NULL), ('a3', 'p4', '192.0.2.11', 'vm-2', NULL)`,
		`INSERT INTO vm_ipv6_prefixes VALUES ('x', 'p6', 'vm-1', '2001:db8:0:5::/64', '2001:db8:0:5::25')`,
		`INSERT INTO vm_nics VALUES ('n1', 'vm-1', 0, 'br0', '52:54:00:00:00:01'), ('n2', 'vm-1', 1, 'vpcbr', '52:54:00:00:00:02')`,
	} {
		require.NoError(t, db.Exec(stmt).Error, stmt)
	}
	ctx := context.Background()
	facts := vmAddressFacts(ctx, db, "vm-1")
	require.Equal(t, addressFacts{DNS: []string{"9.9.9.9", "2620:fe::fe"}, Gateway: "192.0.2.1", DHCP: true, Prefix: 24}, facts["192.0.2.10"])
	require.Equal(t, addressFacts{Gateway: "10.0.0.1", Bridge: "vpcbr", Prefix: 24}, facts["10.0.0.5"])
	require.Len(t, facts, 2, "another VM's address is not this one's")

	dns, extra := vmIPv6Facts(ctx, db, "vm-1")
	require.Equal(t, []string{"2620:fe::9"}, dns)
	require.Equal(t, []string{"2001:db8:0:5::25"}, extra)

	gw, prefix, ok := vmPrimaryPool(ctx, db, "vm-1", "192.0.2.10")
	require.True(t, ok)
	require.Equal(t, "192.0.2.1", gw)
	require.Equal(t, 24, prefix)
	require.EqualValues(t, 2, vmCardCount(ctx, db, "vm-1"))

	// A schema without the new columns has nothing to give, and no error.
	require.NoError(t, db.Exec(`DROP TABLE ip_pools`).Error)
	require.Empty(t, vmAddressFacts(ctx, db, "vm-1"))
}
