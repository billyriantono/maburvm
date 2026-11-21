//go:build pg

package server

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
)

// pgMigratedDB is a throwaway database built by the panel's own migration
// runner, dropped when the test ends.
func pgMigratedDB(t *testing.T) (*gorm.DB, *sql.DB, string) {
	t.Helper()
	baseDSN := os.Getenv("MABURVM_TEST_PG_DSN")
	if baseDSN == "" {
		t.Skip("MABURVM_TEST_PG_DSN is not set")
	}
	name := fmt.Sprintf("maburvm_netenf_%d", time.Now().UnixNano()%1_000_000_000)
	admin, err := sql.Open("pgx", baseDSN)
	require.NoError(t, err)
	defer admin.Close()
	_, err = admin.Exec("CREATE DATABASE " + quoteIdent(name))
	require.NoError(t, err)
	t.Cleanup(func() {
		a, err := sql.Open("pgx", baseDSN)
		if err != nil {
			return
		}
		defer a.Close()
		_, _ = a.Exec("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = " + quoteLit(name))
		_, _ = a.Exec("DROP DATABASE IF EXISTS " + quoteIdent(name))
	})
	dsn := replaceDBName(baseDSN, name)
	require.NoError(t, runMigrations(dsn), "every migration applies to an empty database")
	raw, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { raw.Close() })
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	return gdb, raw, dsn
}

func migrationSQL(t *testing.T, file string) string {
	t.Helper()
	fsys, root := migrationsSource()
	b, err := fs.ReadFile(fsys, path.Join(root, file))
	require.NoError(t, err)
	return string(b)
}

// 073 runs on a database built from every earlier migration, again on top of
// itself (it is forward and defensive), down, and up again.
func TestNetworkEnforcementMigrationIsRerunnable(t *testing.T) {
	db, raw, dsn := pgMigratedDB(t)
	require.NoError(t, runMigrations(dsn), "a second boot applies nothing and fails on nothing")
	up, down := migrationSQL(t, "073_network_enforcement.up.sql"), migrationSQL(t, "073_network_enforcement.down.sql")
	_, err := raw.Exec(up)
	require.NoError(t, err, "073 run twice")
	_, err = raw.Exec(down)
	require.NoError(t, err, "073 down")
	_, err = raw.Exec(up)
	require.NoError(t, err, "073 up after down")

	var cols []string
	require.NoError(t, db.Raw(`SELECT column_name FROM information_schema.columns
		WHERE (table_name = 'vms' AND column_name IN ('firewall_default','smtp_blocked','guest_network_note','guest_network_pending'))
		   OR (table_name = 'plans' AND column_name = 'smtp_policy')
		   OR (table_name = 'firewall_rules' AND column_name IN ('icmp_type','log','family'))
		   OR (table_name = 'bandwidth_usages' AND column_name = 'warned_percent')`).Scan(&cols).Error)
	assert.Len(t, cols, 9)
}

// Who held an address when: every way an address is assigned and released
// leaves a row, and the lookup answers by address and moment.
func TestIPAssignmentHistory(t *testing.T) {
	db, _, _ := pgMigratedDB(t)
	exec := func(q string, args ...interface{}) {
		t.Helper()
		require.NoError(t, db.Exec(q, args...).Error, q)
	}
	const (
		alice, bob = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
		node, tmpl = "33333333-3333-3333-3333-333333333333", "44444444-4444-4444-4444-444444444444"
		vm1, vm2   = "55555555-5555-5555-5555-555555555555", "66666666-6666-6666-6666-666666666666"
		pool       = "77777777-7777-7777-7777-777777777777"
	)
	exec(`INSERT INTO users (id, email, password_hash) VALUES (?, 'alice@example.com', 'x'), (?, 'bob@example.com', 'x')`, alice, bob)
	exec(`INSERT INTO nodes (id, name, ip_address, token) VALUES (?, 'n1', '192.0.2.1', 't')`, node)
	exec(`INSERT INTO os_templates (id, name, version, image_path, checksum) VALUES (?, 'debian', '12', '/x', 'c')`, tmpl)
	exec(`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id) VALUES (?, ?, ?, 'web-1', ?), (?, ?, ?, 'web-2', ?)`,
		vm1, alice, node, tmpl, vm2, bob, node, tmpl)
	exec(`INSERT INTO ip_pools (id, name, node_id, cidr, gateway) VALUES (?, 'p', ?, '203.0.113.0/24', '203.0.113.1')`, pool, node)

	// Created with an address: IPAM row and interface both name it; one holding.
	exec(`INSERT INTO ip_addresses (pool_id, node_id, address, status, vm_id) VALUES (?, ?, '203.0.113.10', 'assigned', ?)`, pool, node, vm1)
	exec(`INSERT INTO networks (vm_id, ip_address) VALUES (?, '203.0.113.10')`, vm1)
	held := time.Now()
	time.Sleep(20 * time.Millisecond)

	// Released: the interface goes and the address returns to the pool.
	exec(`UPDATE networks SET deleted_at = NOW() WHERE vm_id = ?`, vm1)
	exec(`UPDATE ip_addresses SET status = 'available', vm_id = NULL WHERE address = '203.0.113.10'`)
	time.Sleep(20 * time.Millisecond)

	// Reused by another customer, as a floating address.
	exec(`UPDATE ip_addresses SET status = 'assigned', vm_id = ?, delivery_mode = 'floating' WHERE address = '203.0.113.10'`, vm2)
	reused := time.Now()

	svc := service.NewNetworkService(db, repository.NewNetworkRepository(db), repository.NewFirewallRepository(db),
		repository.NewVMRepository(db), repository.NewNodeRepository(db), nil)
	ctx := context.Background()

	all, err := svc.IPHistory(ctx, "203.0.113.10", nil)
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, "bob@example.com", all[0].UserEmail, "newest first")
	assert.Equal(t, "floating", all[0].Kind)
	assert.Nil(t, all[0].ReleasedAt)
	assert.Equal(t, "alice@example.com", all[1].UserEmail)
	assert.Equal(t, "web-1", all[1].VMHostname)
	require.NotNil(t, all[1].ReleasedAt)
	assert.False(t, all[1].Estimated)

	then, err := svc.IPHistory(ctx, "203.0.113.10", &held)
	require.NoError(t, err)
	require.Len(t, then, 1)
	assert.Equal(t, "alice@example.com", then[0].UserEmail, "the abuse report's moment names who held it then")

	now, err := svc.IPHistory(ctx, "203.0.113.10", &reused)
	require.NoError(t, err)
	require.Len(t, now, 1)
	assert.Equal(t, "bob@example.com", now[0].UserEmail)

	// A VM handed to another owner: the holding moves with it from that moment.
	exec(`UPDATE vms SET user_id = ? WHERE id = ?`, alice, vm2)
	rows, err := svc.IPHistory(ctx, "203.0.113.10", nil)
	require.NoError(t, err)
	require.Len(t, rows, 3)
	assert.Equal(t, "alice@example.com", rows[0].UserEmail)
	assert.Nil(t, rows[0].ReleasedAt)
	require.NotNil(t, rows[1].ReleasedAt, "bob's holding ends at the hand-over")

	// A deleted VM holds nothing.
	exec(`UPDATE vms SET deleted_at = NOW() WHERE id = ?`, vm2)
	rows, err = svc.IPHistory(ctx, "203.0.113.10", nil)
	require.NoError(t, err)
	for _, r := range rows {
		assert.NotNil(t, r.ReleasedAt)
	}

	// An address inside a delegated IPv6 prefix finds the prefix's holder.
	exec(`INSERT INTO vm_ipv6_prefixes (vm_id, pool_id, prefix, idx) VALUES (?, ?, '2001:db8:20:5::/64', 1)`, vm1, pool)
	v6, err := svc.IPHistory(ctx, "2001:db8:20:5::1234", nil)
	require.NoError(t, err)
	require.Len(t, v6, 1)
	assert.Equal(t, "ipv6_prefix", v6[0].Kind)
	assert.Equal(t, "alice@example.com", v6[0].UserEmail)

	_, err = svc.IPHistory(ctx, "not-an-address", nil)
	assert.ErrorIs(t, err, service.ErrInvalidIPAddress)
}

// Addresses held when the history began are recorded, marked as estimated.
func TestIPAssignmentHistoryBackfill(t *testing.T) {
	db, raw, _ := pgMigratedDB(t)
	_, err := raw.Exec(migrationSQL(t, "073_network_enforcement.down.sql"))
	require.NoError(t, err)
	for _, q := range []string{
		`INSERT INTO users (id, email, password_hash) VALUES ('11111111-1111-1111-1111-111111111111', 'alice@example.com', 'x')`,
		`INSERT INTO nodes (id, name, ip_address, token) VALUES ('33333333-3333-3333-3333-333333333333', 'n1', '192.0.2.1', 't')`,
		`INSERT INTO os_templates (id, name, version, image_path, checksum) VALUES ('44444444-4444-4444-4444-444444444444', 'debian', '12', '/x', 'c')`,
		`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id) VALUES ('55555555-5555-5555-5555-555555555555',
			'11111111-1111-1111-1111-111111111111', '33333333-3333-3333-3333-333333333333', 'web-1', '44444444-4444-4444-4444-444444444444')`,
		`INSERT INTO networks (vm_id, ip_address) VALUES ('55555555-5555-5555-5555-555555555555', '198.51.100.7')`,
		`INSERT INTO firewall_rules (vm_id, protocol, action, direction) VALUES ('55555555-5555-5555-5555-555555555555', 'tcp', 'allow', 'inbound')`,
		`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id) VALUES ('66666666-6666-6666-6666-666666666666',
			'11111111-1111-1111-1111-111111111111', '33333333-3333-3333-3333-333333333333', 'legacy', '44444444-4444-4444-4444-444444444444')`,
	} {
		_, err := raw.Exec(q)
		require.NoError(t, err, q)
	}
	_, err = raw.Exec(migrationSQL(t, "073_network_enforcement.up.sql"))
	require.NoError(t, err)

	var est []struct {
		Address   string
		Estimated bool
	}
	require.NoError(t, db.Raw(`SELECT host(address) AS address, estimated FROM ip_assignment_history`).Scan(&est).Error)
	require.Len(t, est, 1)
	assert.Equal(t, "198.51.100.7", est[0].Address)
	assert.True(t, est[0].Estimated)

	// The VM with rules is already default-deny; the one without keeps accepting.
	var defaults []string
	require.NoError(t, db.Raw(`SELECT firewall_default FROM vms ORDER BY hostname`).Scan(&defaults).Error)
	assert.Equal(t, []string{"accept", "drop"}, defaults)

	// Reject is a stored action now.
	require.NoError(t, db.Exec(`INSERT INTO firewall_rules (vm_id, protocol, action, direction) VALUES ('55555555-5555-5555-5555-555555555555', 'tcp', 'reject', 'inbound')`).Error)
	assert.Error(t, db.Exec(`INSERT INTO firewall_rules (vm_id, protocol, action, direction) VALUES ('55555555-5555-5555-5555-555555555555', 'tcp', 'bounce', 'inbound')`).Error)
}
