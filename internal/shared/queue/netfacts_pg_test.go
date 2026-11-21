//go:build pg

package queue

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	sharedb "github.com/maburvm/panel/internal/shared/db"
)

// On a database built by every migration, the worker's reads of pools,
// prefixes and cards work against the real column types (uuid, inet, cidr),
// and migration 091 goes down and up again cleanly.
func TestVMNetworkFactsOnPostgres(t *testing.T) {
	base := os.Getenv("MABURVM_TEST_PG_DSN")
	if base == "" {
		t.Skip("MABURVM_TEST_PG_DSN is not set")
	}
	ctx := context.Background()
	name := fmt.Sprintf("maburvm_netfacts_%d", time.Now().UnixNano()%1_000_000_000)
	admin, err := sql.Open("pgx", base)
	require.NoError(t, err)
	_, err = admin.Exec(`CREATE DATABASE "` + name + `"`)
	admin.Close()
	require.NoError(t, err)
	u, err := url.Parse(base)
	require.NoError(t, err)
	u.Path = "/" + name
	raw, err := sql.Open("pgx", u.String())
	require.NoError(t, err)
	t.Cleanup(func() {
		raw.Close()
		if a, err := sql.Open("pgx", base); err == nil {
			_, _ = a.Exec(`DROP DATABASE IF EXISTS "` + name + `" WITH (FORCE)`)
			a.Close()
		}
	})
	require.NoError(t, sharedb.EnsureMigrationsTable(ctx, raw))
	_, err = sharedb.Up(ctx, raw, sharedb.MigrationsFS, "migrations", 5*time.Minute, nil)
	require.NoError(t, err, "every migration applies to an empty database")

	down, err := fs.ReadFile(sharedb.MigrationsFS, "migrations/091_vm_nics_dhcp_dns.down.sql")
	require.NoError(t, err)
	up, err := fs.ReadFile(sharedb.MigrationsFS, "migrations/091_vm_nics_dhcp_dns.up.sql")
	require.NoError(t, err)
	_, err = raw.Exec(string(down))
	require.NoError(t, err)
	_, err = raw.Exec(string(up))
	require.NoError(t, err)
	_, err = raw.Exec(string(up))
	require.NoError(t, err, "the up migration runs twice without harm")

	var nodeID, poolID, v6PoolID, vmID string
	require.NoError(t, raw.QueryRow(`INSERT INTO nodes (name, ip_address, token) VALUES ('n', '192.0.2.250', 'tok') RETURNING id`).Scan(&nodeID))
	require.NoError(t, raw.QueryRow(`INSERT INTO ip_pools (name, family, cidr, gateway, bridge, nameservers, dhcp) VALUES ('p', 'ipv4', '192.0.2.0/24', '192.0.2.1', 'br0', '9.9.9.9', true) RETURNING id`).Scan(&poolID))
	require.NoError(t, raw.QueryRow(`INSERT INTO ip_pools (name, family, cidr, gateway, delegated_prefix_len, nameservers) VALUES ('p6', 'ipv6', '2001:db8::/48', 'fe80::1', 64, '2620:fe::9') RETURNING id`).Scan(&v6PoolID))
	var userID, tmplID string
	require.NoError(t, raw.QueryRow(`INSERT INTO users (name, email, password_hash, role) VALUES ('u', 'u@example.com', 'x', 'client') RETURNING id`).Scan(&userID))
	require.NoError(t, raw.QueryRow(`INSERT INTO os_templates (name, version, image_path) VALUES ('debian', '12', '/i.qcow2') RETURNING id`).Scan(&tmplID))
	require.NoError(t, raw.QueryRow(`INSERT INTO vms (user_id, node_id, hostname, os_template_id, resources) VALUES ($1, $2, 'v', $3, '{"cpu":1,"ram":512,"disk":10}') RETURNING id`,
		userID, nodeID, tmplID).Scan(&vmID))
	_, err = raw.Exec(`INSERT INTO ip_addresses (pool_id, address, family, status, vm_id) VALUES ($1, '192.0.2.10', 'ipv4', 'assigned', $2)`, poolID, vmID)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO vm_ipv6_prefixes (pool_id, vm_id, prefix, idx, extra_addresses) VALUES ($1, $2, '2001:db8:0:5::/64', 5, '2001:db8:0:5::25')`, v6PoolID, vmID)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO vm_nics (vm_id, position, bridge, mac_address) VALUES ($1, 0, 'br0', '52:54:00:00:00:01')`, vmID)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO vm_nics (vm_id, position, bridge, mac_address) VALUES ($1, 1, 'br1', '52:54:00:00:00:01')`, "other")
	require.Error(t, err, "no MAC is given to two cards")
	_, err = raw.Exec(`UPDATE nodes SET vlan_bridges = 'br1:bond0' WHERE id = $1`, nodeID)
	require.NoError(t, err)

	gdb, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	facts := vmAddressFacts(ctx, gdb, vmID)
	require.Equal(t, addressFacts{DNS: []string{"9.9.9.9"}, Gateway: "192.0.2.1", DHCP: true, Bridge: "br0", Prefix: 24}, facts["192.0.2.10"])
	dns, extra := vmIPv6Facts(ctx, gdb, vmID)
	require.Equal(t, []string{"2620:fe::9"}, dns)
	require.Equal(t, []string{"2001:db8:0:5::25"}, extra)
	gw, prefix, ok := vmPrimaryPool(ctx, gdb, vmID, "192.0.2.10")
	require.True(t, ok)
	require.Equal(t, "192.0.2.1", gw)
	require.Equal(t, 24, prefix)
	require.EqualValues(t, 1, vmCardCount(ctx, gdb, vmID))
}
