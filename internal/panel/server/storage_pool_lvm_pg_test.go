//go:build pg

package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
)

// 081 is forward and defensive: on top of itself, down and up again. The
// collector's measurement of an LVM pool round-trips through it.
func TestStoragePoolLVMMigration(t *testing.T) {
	db, raw, dsn := pgMigratedDB(t)
	require.NoError(t, runMigrations(dsn))
	up, down := migrationSQL(t, "081_storage_pool_lvm.up.sql"), migrationSQL(t, "081_storage_pool_lvm.down.sql")
	_, err := raw.Exec(up)
	require.NoError(t, err, "081 run twice")
	_, err = raw.Exec(down)
	require.NoError(t, err, "081 down")
	_, err = raw.Exec(up)
	require.NoError(t, err, "081 up after down")

	const node = "33333333-3333-3333-3333-333333333333"
	require.NoError(t, db.Exec(`INSERT INTO nodes (id, name, ip_address, token) VALUES (?, 'n1', '192.0.2.1', 't')`, node).Error)
	// A row from before 081 reads with an empty note and no metadata figure.
	require.NoError(t, db.Exec(`INSERT INTO storage_pools (id, name, type, path, node_id) VALUES ('44444444-4444-4444-4444-444444444444', 'old', 'dir', '/vm', ?)`, node).Error)
	var old models.StoragePool
	require.NoError(t, db.First(&old, "id = ?", "44444444-4444-4444-4444-444444444444").Error)
	assert.Equal(t, "", old.StatusNote)
	assert.Nil(t, old.MetadataPercent)

	repo := repository.NewStorageRepository(db)
	pool := &models.StoragePool{Name: "fast", Type: models.PoolTypeLVMThin, Path: "vg0/thin", NodeID: node, FileFormat: "raw", Status: "offline"}
	require.NoError(t, repo.CreatePool(pool))
	meta := 81.5
	pool.Status, pool.StatusNote, pool.MetadataPercent, pool.TotalSpace = "degraded", "vg0/thin is not a thin pool", &meta, 100
	require.NoError(t, repo.UpdatePoolMeasurement(pool))
	var got models.StoragePool
	require.NoError(t, db.First(&got, "id = ?", pool.ID).Error)
	assert.Equal(t, "degraded", got.Status)
	assert.Equal(t, "vg0/thin is not a thin pool", got.StatusNote)
	require.NotNil(t, got.MetadataPercent)
	assert.Equal(t, 81.5, *got.MetadataPercent)

	pool.Status, pool.StatusNote, pool.MetadataPercent = "online", "", nil
	require.NoError(t, repo.UpdatePoolMeasurement(pool))
	require.NoError(t, db.First(&got, "id = ?", pool.ID).Error)
	assert.Equal(t, "", got.StatusNote, "a cleared note is written, not skipped")
	assert.Nil(t, got.MetadataPercent)
}
