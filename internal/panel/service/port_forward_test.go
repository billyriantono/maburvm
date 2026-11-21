package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newPortForwardTestService(t *testing.T) (*NetworkService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:pf-%s?mode=memory&cache=shared", t.Name())),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE networks (
		id TEXT PRIMARY KEY, vm_id TEXT, ip_address TEXT, bandwidth_limit INTEGER,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, deleted_at DATETIME
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE port_forwards (
		id TEXT PRIMARY KEY, vm_id TEXT, network_id TEXT, node_id TEXT,
		external_port INTEGER, internal_port INTEGER, protocol TEXT, source_ip TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, deleted_at DATETIME
	)`).Error)
	createGroupAndNATTables(t, db)

	svc := NewNetworkService(
		db,
		repository.NewNetworkRepository(db),
		repository.NewFirewallRepository(db),
		repository.NewVMRepository(db),
		repository.NewNodeRepository(db),
		nil, // riverClient — not exercised by the query-only methods under test
	)
	return svc, db
}

func TestPrimaryNetworkID(t *testing.T) {
	svc, db := newPortForwardTestService(t)
	ctx := context.Background()

	// No interface yet → ErrNetworkNotFound.
	_, err := svc.primaryNetworkID(ctx, "vm-1")
	require.ErrorIs(t, err, ErrNetworkNotFound)

	require.NoError(t, db.Exec(`INSERT INTO networks (id, vm_id, ip_address) VALUES ('net-1','vm-1','10.0.0.5')`).Error)
	id, err := svc.primaryNetworkID(ctx, "vm-1")
	require.NoError(t, err)
	require.Equal(t, "net-1", id)
}

func TestGetPortForwardsForVM(t *testing.T) {
	svc, db := newPortForwardTestService(t)
	ctx := context.Background()

	require.NoError(t, db.Exec(`INSERT INTO port_forwards (id, vm_id, network_id, external_port, internal_port, protocol, source_ip)
		VALUES ('pf-1','vm-1','net-1',8080,80,'tcp','0.0.0.0/0'),
		       ('pf-2','vm-1','net-1',2222,22,'tcp','0.0.0.0/0'),
		       ('pf-3','vm-2','net-9',9090,90,'tcp','0.0.0.0/0')`).Error)

	forwards, err := svc.GetPortForwardsForVM(ctx, "vm-1")
	require.NoError(t, err)
	require.Len(t, forwards, 2, "should return only this VM's forwards")
	for _, f := range forwards {
		require.Equal(t, "vm-1", f.VMID)
	}

	// A VM with no forwards returns an empty slice, not an error.
	none, err := svc.GetPortForwardsForVM(ctx, "vm-3")
	require.NoError(t, err)
	require.Empty(t, none)
}

// Three VMs: A and B share node 1, C is on node 2.
const (
	pfVMA, pfNetA = "11111111-1111-4111-8111-111111111111", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	pfVMB, pfNetB = "22222222-2222-4222-8222-222222222222", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	pfVMC, pfNetC = "33333333-3333-4333-8333-333333333333", "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	pfNode1       = "99999999-9999-4999-8999-999999999991"
	pfNode2       = "99999999-9999-4999-8999-999999999992"
)

func seedPortForwardVMs(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`CREATE TABLE vms (
		id TEXT PRIMARY KEY, user_id TEXT, node_id TEXT, hostname TEXT, resources TEXT, status TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, deleted_at DATETIME
	)`).Error)
	for _, r := range [][3]string{{pfVMA, pfNode1, pfNetA}, {pfVMB, pfNode1, pfNetB}, {pfVMC, pfNode2, pfNetC}} {
		require.NoError(t, db.Exec(`INSERT INTO vms (id, user_id, node_id, hostname, resources, status) VALUES (?, 'u', ?, 'h', '{}', 'running')`, r[0], r[1]).Error)
		require.NoError(t, db.Exec(`INSERT INTO networks (id, vm_id, ip_address) VALUES (?, ?, '10.0.0.5')`, r[2], r[0]).Error)
	}
}

// A port on a node belongs to one VM. The source restriction is not part of
// the key: two forwards on one port shadow each other on the node whatever
// their sources are. Another node's ports are its own.
func TestAddPortForwardIsUniquePerNode(t *testing.T) {
	svc, db := newPortForwardTestService(t)
	seedPortForwardVMs(t, db)
	ctx := context.Background()

	first, err := svc.AddPortForward(ctx, pfVMA, pfNetA, &AddPortForwardRequest{ExternalPort: 8080, InternalPort: 80})
	require.NoError(t, err)
	require.Equal(t, pfNode1, first.PortForward.NodeID, "the forward records the node it takes a port on")

	_, err = svc.AddPortForward(ctx, pfVMB, pfNetB, &AddPortForwardRequest{ExternalPort: 8080, InternalPort: 80, SourceIP: "198.51.100.0/24"})
	require.ErrorIs(t, err, ErrPortAlreadyInUse, "another VM on the same node, different source")

	_, err = svc.AddPortForward(ctx, pfVMA, pfNetA, &AddPortForwardRequest{ExternalPort: 8080, InternalPort: 81, SourceIP: "198.51.100.0/24"})
	require.ErrorIs(t, err, ErrPortAlreadyInUse, "the same VM, different source")

	_, err = svc.AddPortForward(ctx, pfVMB, pfNetB, &AddPortForwardRequest{ExternalPort: 8080, InternalPort: 80, Protocol: "udp"})
	require.NoError(t, err, "the other protocol is a different port")

	_, err = svc.AddPortForward(ctx, pfVMC, pfNetC, &AddPortForwardRequest{ExternalPort: 8080, InternalPort: 80})
	require.NoError(t, err, "one node's forward must not block the port on another node")

	// Removing the forward frees the port on that node.
	require.NoError(t, svc.RemovePortForward(ctx, pfVMA, pfNetA, first.PortForward.ID))
	_, err = svc.AddPortForward(ctx, pfVMB, pfNetB, &AddPortForwardRequest{ExternalPort: 8080, InternalPort: 80})
	require.NoError(t, err)
}

func TestAddPortForwardIsCappedPerVM(t *testing.T) {
	svc, db := newPortForwardTestService(t)
	seedPortForwardVMs(t, db)
	ctx := context.Background()

	for i := 0; i < DefaultPortForwardsPerVM; i++ {
		_, err := svc.AddPortForward(ctx, pfVMA, pfNetA, &AddPortForwardRequest{ExternalPort: 20000 + i, InternalPort: 80})
		require.NoError(t, err)
	}
	_, err := svc.AddPortForward(ctx, pfVMA, pfNetA, &AddPortForwardRequest{ExternalPort: 30000, InternalPort: 80})
	require.ErrorIs(t, err, ErrPortForwardLimit)

	// The limit is per VM: its neighbour on the same node is unaffected.
	_, err = svc.AddPortForward(ctx, pfVMB, pfNetB, &AddPortForwardRequest{ExternalPort: 30000, InternalPort: 80})
	require.NoError(t, err)
}

// What a node last said about its firewall decides what a VM's firewall page
// claims. A node that has said nothing is unknown, never enforced.
func TestFirewallEnforcementOfAVMsNode(t *testing.T) {
	svc, db := newPortForwardTestService(t)
	seedPortForwardVMs(t, db)
	ctx := context.Background()

	require.Equal(t, "unknown", svc.FirewallEnforcement(ctx, pfVMA), "no nodes table at all must not read as enforced")

	require.NoError(t, db.Exec(`CREATE TABLE nodes (id TEXT PRIMARY KEY, firewall_enforcement TEXT NOT NULL DEFAULT '')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO nodes (id, firewall_enforcement) VALUES (?, 'off'), (?, '')`, pfNode1, pfNode2).Error)
	require.Equal(t, "off", svc.FirewallEnforcement(ctx, pfVMA))
	require.Equal(t, "unknown", svc.FirewallEnforcement(ctx, pfVMC), "an agent that does not report is unknown")

	require.NoError(t, db.Exec(`UPDATE nodes SET firewall_enforcement = 'enforced' WHERE id = ?`, pfNode1).Error)
	require.Equal(t, "enforced", svc.FirewallEnforcement(ctx, pfVMB))
	require.Equal(t, "unknown", svc.FirewallEnforcement(ctx, "no-such-vm"))
}

// Every VM's rules share one chain on its node, so a VM may hold only so many.
func TestAddFirewallRuleIsCappedPerVM(t *testing.T) {
	svc, db := newPortForwardTestService(t)
	seedPortForwardVMs(t, db)
	require.NoError(t, db.Exec(`CREATE TABLE firewall_rules (
		id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, protocol TEXT NOT NULL, port_range TEXT, action TEXT NOT NULL,
		direction TEXT NOT NULL, source_ip TEXT DEFAULT '0.0.0.0/0', priority INTEGER DEFAULT 100,
		icmp_type TEXT NOT NULL DEFAULT '', log BOOLEAN NOT NULL DEFAULT 0, family TEXT NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, deleted_at DATETIME
	)`).Error)
	setQuotaSettings(t, db, `{"firewallRuleMaxPerVm": 3}`)
	ctx := context.Background()
	rule := func(vm string) error {
		_, err := svc.AddFirewallRule(ctx, vm, &AddFirewallRuleRequest{Protocol: "tcp", PortRange: "22", Action: "allow", Direction: "inbound", Priority: 100})
		return err
	}
	for i := 0; i < 3; i++ {
		require.NoError(t, rule(pfVMA))
	}
	require.ErrorIs(t, rule(pfVMA), ErrFirewallRuleLimit)
	require.NoError(t, rule(pfVMB), "the limit is per VM")

	// Unset, the built-in default applies.
	setQuotaSettings(t, db, `{}`)
	require.Equal(t, DefaultFirewallRulesPerVM, FirewallRulesPerVM(ctx, db))
}

// A VM's IPv6 rides on its IPv4 interface, so its last IPv4 may not go first.
func TestReleasingTheLastIPv4KeepsIPv6Routed(t *testing.T) {
	svc, db := newPortForwardTestService(t)
	ctx := context.Background()
	require.NoError(t, db.Exec(`CREATE TABLE vm_ipv6_prefixes (id TEXT PRIMARY KEY, pool_id TEXT NOT NULL, vm_id TEXT NOT NULL, prefix TEXT NOT NULL, idx INTEGER NOT NULL, extra_addresses TEXT NOT NULL DEFAULT '', created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE ip_pools (id TEXT PRIMARY KEY, cidr TEXT, gateway TEXT, delegated_prefix_len INTEGER, deleted_at DATETIME)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE ip_addresses (id TEXT PRIMARY KEY, pool_id TEXT, address TEXT, family TEXT, vm_id TEXT, deleted_at DATETIME)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO networks (id, vm_id, ip_address) VALUES ('net-1','vm-1','203.0.113.5'), ('net-2','vm-1','203.0.113.6')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO vm_ipv6_prefixes (id, pool_id, vm_id, prefix, idx) VALUES ('p-1','pool-6','vm-1','2001:db8:5::/64',1)`).Error)

	require.False(t, svc.strandsIPv6(ctx, "vm-1", "net-1"), "another IPv4 is left to carry it")
	require.NoError(t, db.Exec(`DELETE FROM networks WHERE id = 'net-2'`).Error)
	require.True(t, svc.strandsIPv6(ctx, "vm-1", "net-1"))
	require.NoError(t, db.Exec(`DELETE FROM vm_ipv6_prefixes`).Error)
	require.False(t, svc.strandsIPv6(ctx, "vm-1", "net-1"), "no IPv6: the last IPv4 may go")
}
