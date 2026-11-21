//go:build pg

package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/shared/models"
)

// 095 is forward and defensive: on top of itself, down and up again. A shared
// pool from before it reads as it did, and a cluster key that reached the
// table in plaintext is sealed by the boot pass.
func TestCephClustersMigration(t *testing.T) {
	db, raw, dsn := pgMigratedDB(t)
	require.NoError(t, runMigrations(dsn))
	require.NoError(t, db.Exec(`INSERT INTO shared_storage_pools (id, name, kind, path) VALUES ('44444444-4444-4444-4444-444444444444', 'vmstore', 'mounted', '/mnt/vmstore')`).Error)
	up, down := migrationSQL(t, "095_ceph_clusters.up.sql"), migrationSQL(t, "095_ceph_clusters.down.sql")
	_, err := raw.Exec(up)
	require.NoError(t, err, "095 again")
	// An RBD pool and a node's part of it: code before 095 would take its
	// path for a directory, so down removes them once no VM is on them.
	require.NoError(t, db.Exec(`INSERT INTO nodes (id, name, ip_address, token, status, cpu_count, memory_total_bytes)
		VALUES ('55555555-5555-5555-5555-555555555555', 'n1', '192.0.2.1', 't', 'active', 4, 1073741824)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO shared_storage_pools (id, name, kind, path, rbd_pool) VALUES ('66666666-6666-6666-6666-666666666666', 'ceph-vms', 'rbd', 'vms', 'vms')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO storage_pools (id, name, type, path, node_id, shared_pool_id)
		VALUES ('77777777-7777-7777-7777-777777777777', 'ceph-vms', 'rbd', 'vms', '55555555-5555-5555-5555-555555555555', '66666666-6666-6666-6666-666666666666')`).Error)
	// A VM with its disk there: down is refused and removes nothing, since
	// code before 095 would take the image for a file.
	require.NoError(t, db.Exec(`INSERT INTO users (id, email, password_hash) VALUES ('11111111-1111-1111-1111-111111111111', 'alice@example.com', 'x')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO os_templates (id, name, version, image_path, checksum) VALUES ('88888888-8888-8888-8888-888888888888', 'debian', '12', '/x', 'c')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id, storage_pool_id) VALUES ('99999999-9999-9999-9999-999999999999',
		'11111111-1111-1111-1111-111111111111', '55555555-5555-5555-5555-555555555555', 'web-1', '88888888-8888-8888-8888-888888888888', '77777777-7777-7777-7777-777777777777')`).Error)
	_, err = raw.Exec(down)
	require.Error(t, err, "095 down with a VM on RBD")
	var kept int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM storage_pools WHERE type = 'rbd'`).Scan(&kept).Error)
	assert.EqualValues(t, 1, kept)
	require.NoError(t, db.Exec(`UPDATE vms SET deleted_at = NOW() WHERE id = '99999999-9999-9999-9999-999999999999'`).Error)
	_, err = raw.Exec(down)
	require.NoError(t, err, "095 down")
	var left int64
	require.NoError(t, db.Raw(`SELECT (SELECT COUNT(*) FROM storage_pools WHERE type = 'rbd') + (SELECT COUNT(*) FROM shared_storage_pools WHERE kind = 'rbd')`).Scan(&left).Error)
	assert.Zero(t, left)
	_, err = raw.Exec(up)
	require.NoError(t, err, "095 up after down")
	var old models.SharedStoragePool
	require.NoError(t, db.First(&old, "id = ?", "44444444-4444-4444-4444-444444444444").Error)
	assert.Equal(t, models.SharedPoolMounted, old.Kind)
	assert.Nil(t, old.CephClusterID)
	assert.Equal(t, "", old.RBDPool)

	models.SetSecretKeys("0123456789abcdef0123456789abcdef")
	t.Cleanup(func() { models.SetSecretKeys("") })
	const key = "bWFidXJ2bS10ZXN0LWtleS1ub3QtcmVhbA=="
	require.NoError(t, db.Exec(`INSERT INTO ceph_clusters (name, monitors, client_id, client_key) VALUES ('ceph-jkt', '10.0.0.1', 'maburvm', ?)`, key).Error)
	_, err = sealSecretsAtRest(context.Background(), db)
	require.NoError(t, err)
	var stored string
	require.NoError(t, db.Raw(`SELECT client_key FROM ceph_clusters WHERE name = 'ceph-jkt'`).Scan(&stored).Error)
	assert.True(t, models.IsSealed(stored), "sealed at rest")
	var c models.CephCluster
	require.NoError(t, db.First(&c, "name = ?", "ceph-jkt").Error)
	assert.Equal(t, key, c.ClientKey, "and still readable by the panel")
}
