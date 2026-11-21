//go:build pg

package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/shared/models"
)

// 080 applies through the runner, again on itself, down and up; a VM has one
// active move at a time and a node one running drain, while finished records
// pile up as history.
func TestVMMigrationMigration(t *testing.T) {
	db, raw, dsn := pgMigratedDB(t)
	require.NoError(t, runMigrations(dsn))
	up, down := migrationSQL(t, "080_vm_migration.up.sql"), migrationSQL(t, "080_vm_migration.down.sql")
	for _, step := range []struct{ name, stmt string }{{"080 again", up}, {"080 down", down}, {"080 up after down", up}} {
		_, err := raw.Exec(step.stmt)
		require.NoError(t, err, step.name)
	}

	const vm, src, dst = "55555555-5555-5555-5555-555555555555", "33333333-3333-3333-3333-333333333333", "44444444-4444-4444-4444-444444444444"
	_, err := raw.Exec(`INSERT INTO nodes (id, name, ip_address, token) VALUES ($1, 'n1', '192.0.2.1', 't')`, src)
	require.NoError(t, err)
	var key string
	require.NoError(t, raw.QueryRow(`SELECT migration_public_key FROM nodes WHERE id = $1`, src).Scan(&key))
	assert.Equal(t, "", key, "an existing node has no migration key until its agent reports one")

	dest := dst
	first := &models.VMMigration{VMID: vm, SourceNodeID: src, DestNodeID: &dest, Mode: models.MigrationLive, Status: models.MigrationRunning}
	require.NoError(t, db.Create(first).Error)
	second := &models.VMMigration{VMID: vm, SourceNodeID: src, DestNodeID: &dest, Mode: models.MigrationLive, Status: models.MigrationQueued}
	assert.Error(t, db.Create(second).Error, "a second active move of one VM is refused")
	require.NoError(t, db.Model(first).Update("status", models.MigrationFailed).Error)
	require.NoError(t, db.Create(second).Error, "once the first ended, a new move is allowed")

	d := &models.NodeDrain{NodeID: src, Status: models.DrainRunning, Concurrency: 1, Destinations: map[string]string{vm: dst}}
	require.NoError(t, db.Create(d).Error)
	assert.Error(t, db.Create(&models.NodeDrain{NodeID: src, Status: models.DrainRunning, Concurrency: 1, Destinations: map[string]string{}}).Error,
		"one running drain per node")
	var got models.NodeDrain
	require.NoError(t, db.First(&got, "id = ?", d.ID).Error)
	assert.Equal(t, map[string]string{vm: dst}, got.Destinations)
}
