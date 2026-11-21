//go:build pg

package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 083 runs on a database built from every earlier migration, again on top of
// itself, down, and up again; a legacy node row offers no NAT ports until
// staff choose a range.
func TestFirewallGroupsNATMigrationIsRerunnable(t *testing.T) {
	db, raw, dsn := pgMigratedDB(t)
	require.NoError(t, runMigrations(dsn), "a second boot applies nothing and fails on nothing")
	up, down := migrationSQL(t, "083_firewall_groups_nat_vps.up.sql"), migrationSQL(t, "083_firewall_groups_nat_vps.down.sql")
	_, err := raw.Exec(up)
	require.NoError(t, err, "083 run twice")
	_, err = raw.Exec(down)
	require.NoError(t, err, "083 down")
	_, err = raw.Exec(`INSERT INTO nodes (id, name, ip_address, token) VALUES ('33333333-3333-3333-3333-333333333333', 'legacy', '192.0.2.1', 't')`)
	require.NoError(t, err)
	_, err = raw.Exec(up)
	require.NoError(t, err, "083 up after down, over existing rows")

	var rng struct{ NATPortStart, NATPortEnd int }
	require.NoError(t, db.Raw(`SELECT nat_port_start, nat_port_end FROM nodes WHERE name = 'legacy'`).Scan(&rng).Error)
	assert.Equal(t, 0, rng.NATPortStart)
	assert.Equal(t, 0, rng.NATPortEnd)
}
