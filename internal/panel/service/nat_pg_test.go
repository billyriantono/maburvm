//go:build pg

package service

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
)

// On Postgres: group rules with cidr sources merge into a VM's rules in order,
// and concurrent NAT orders on one node never get overlapping blocks.
func TestFirewallGroupsAndNATBlocksOnPostgres(t *testing.T) {
	db := migrationDB(t)
	ctx := context.Background()
	exec := func(q string, args ...interface{}) {
		t.Helper()
		require.NoError(t, db.Exec(q, args...).Error, q)
	}
	const (
		user, node, tmpl = "11111111-1111-1111-1111-111111111111", "33333333-3333-3333-3333-333333333333", "44444444-4444-4444-4444-444444444444"
	)
	exec(`INSERT INTO users (id, email, password_hash) VALUES (?, 'u@example.com', 'x')`, user)
	exec(`INSERT INTO nodes (id, name, ip_address, token, nat_port_start, nat_port_end) VALUES (?, 'n1', '192.0.2.1', 't', 30000, 30199)`, node)
	exec(`INSERT INTO os_templates (id, name, version, image_path) VALUES (?, 'debian', '12', '/i.qcow2')`, tmpl)
	vms := make([]string, 8)
	for i := range vms {
		var id string
		require.NoError(t, db.Raw(`INSERT INTO vms (user_id, node_id, hostname, os_template_id, resources) VALUES (?, ?, ?, ?, '{}') RETURNING id`,
			user, node, "vm"+string(rune('a'+i)), tmpl).Scan(&id).Error)
		vms[i] = id
	}

	groups := NewFirewallGroupService(db, nil)
	staff := GroupViewer{UserID: user, Staff: true, Manage: true}
	g, err := groups.Create(ctx, staff, "Office", "")
	require.NoError(t, err)
	_, err = groups.AddRule(ctx, staff, g.ID, &AddFirewallRuleRequest{Protocol: "tcp", PortRange: "22", Action: "allow", Direction: "inbound", SourceIP: "198.51.100.0/24", Priority: 5})
	require.NoError(t, err)
	exec(`INSERT INTO firewall_rules (vm_id, protocol, port_range, action, direction, priority) VALUES (?, 'tcp', '22', 'deny', 'inbound', 1)`, vms[0])
	require.NoError(t, groups.Attach(ctx, staff, vms[0], g.ID, models.FirewallGroupBefore))
	rules, err := repository.NewFirewallRepository(db).ListEffectiveByVMID(ctx, vms[0])
	require.NoError(t, err)
	require.Len(t, rules, 2)
	assert.Equal(t, "198.51.100.0/24", rules[0].SourceIP, "the group's office allow comes before the VM's own deny")
	assert.Equal(t, "deny", rules[1].Action)

	var wg sync.WaitGroup
	blocks := make([]*models.NATPortBlock, len(vms))
	errs := make([]error, len(vms))
	for i := range vms {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = db.Transaction(func(tx *gorm.DB) error {
				b, err := allocateNATPortBlockTx(ctx, tx, node, vms[i], 20)
				blocks[i] = b
				return err
			})
		}(i)
	}
	wg.Wait()
	seen := map[int]bool{}
	for i, b := range blocks {
		require.NoError(t, errs[i])
		for p := b.StartPort; p <= b.EndPort(); p++ {
			require.False(t, seen[p], "port %d handed out twice", p)
			seen[p] = true
		}
		assert.True(t, b.StartPort >= 30000 && b.EndPort() <= 30199)
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		_, err := allocateNATPortBlockTx(ctx, tx, node, "55555555-5555-5555-5555-555555555555", 100)
		return err
	})
	assert.ErrorIs(t, err, ErrNATPortsExhausted)

	// Deleting a VM's network rows releases its block, hostnames and groups.
	exec(`INSERT INTO vm_hostnames (vm_id, hostname, verify_token) VALUES (?, 'a.example.com', 't')`, vms[0])
	require.NoError(t, repository.NewNetworkRepository(db).DeleteByVMID(ctx, vms[0]))
	var left int64
	require.NoError(t, db.Raw(`SELECT (SELECT COUNT(*) FROM nat_port_blocks WHERE vm_id = ?) + (SELECT COUNT(*) FROM vm_hostnames WHERE vm_id = ?) + (SELECT COUNT(*) FROM vm_firewall_groups WHERE vm_id = ?)`,
		vms[0], vms[0], vms[0]).Scan(&left).Error)
	assert.Zero(t, left)

	// An account deleted with its VMs given away hands over its groups too.
	const heir = "22222222-2222-2222-2222-222222222222"
	exec(`INSERT INTO users (id, email, password_hash) VALUES (?, 'heir@example.com', 'x')`, heir)
	own, err := groups.Create(ctx, GroupViewer{UserID: user}, "Mine", "")
	require.NoError(t, err)

	// A VM handed to another account gets that account's copy of the group.
	_, err = groups.AddRule(ctx, GroupViewer{UserID: user}, own.ID, &AddFirewallRuleRequest{Protocol: "tcp", PortRange: "443", Action: "allow", Direction: "inbound", SourceIP: "203.0.113.0/24", Priority: 3})
	require.NoError(t, err)
	require.NoError(t, groups.Attach(ctx, GroupViewer{UserID: user}, vms[1], own.ID, ""))
	require.NoError(t, reownVMGroups(ctx, db, vms[1], heir))
	var copied struct {
		UserID   string
		SourceIP string
	}
	require.NoError(t, db.Raw(`SELECT g.user_id, r.source_ip FROM vm_firewall_groups a JOIN firewall_groups g ON g.id = a.group_id
		JOIN firewall_group_rules r ON r.group_id = g.id WHERE a.vm_id = ?`, vms[1]).Scan(&copied).Error)
	assert.Equal(t, heir, copied.UserID)
	assert.Equal(t, "203.0.113.0/24", copied.SourceIP)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, err := ReassignUserVMs(ctx, tx, user, heir)
		return err
	}))
	var owner string
	require.NoError(t, db.Raw(`SELECT user_id FROM firewall_groups WHERE id = ?`, own.ID).Scan(&owner).Error)
	assert.Equal(t, heir, owner)
}
