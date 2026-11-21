package service

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
)

// Storage admission through the service, on the paths that never went through
// placement: a create that names its node, and a disk grow.
func TestVMServiceAdmitsStorageOnCreateAndGrow(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	for _, stmt := range []string{
		`ALTER TABLE vms ADD COLUMN storage_pool_id TEXT`,
		`CREATE TABLE storage_pools (id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT, status TEXT, total_space INTEGER DEFAULT 0, used_space INTEGER DEFAULT 0, available_space INTEGER DEFAULT 0, path TEXT NOT NULL, file_format TEXT, alert_threshold INTEGER DEFAULT 90, overcommit INTEGER DEFAULT 0, is_primary BOOLEAN DEFAULT 0, node_id TEXT NOT NULL, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE storage_volumes (id TEXT PRIMARY KEY, name TEXT NOT NULL, pool_id TEXT NOT NULL, vm_id TEXT, size INTEGER NOT NULL DEFAULT 0, format TEXT, path TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE system_settings (section TEXT PRIMARY KEY, data TEXT)`,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	user, node, tmpl := seedVMIPAMDependencies(t, db)
	pool := &models.StoragePool{ID: "pool-data", Name: "data", NodeID: node.ID, Path: "/data", Type: "dir", Status: "online", IsPrimary: true, TotalSpace: 500 * gib, AvailableSpace: 25 * gib}
	require.NoError(t, db.Create(pool).Error)
	free := func(gb int64) {
		require.NoError(t, db.Model(&models.StoragePool{}).Where("id = ?", pool.ID).Update("available_space", gb*gib).Error)
	}
	vmCount := func() (n int64) {
		require.NoError(t, db.Model(&models.VM{}).Count(&n).Error)
		return n
	}

	svc := NewVMService(db, repository.NewVMRepository(db), repository.NewNodeRepository(db), repository.NewTemplateRepository(db), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	create := func(host string, diskGB int) (*CreateVMResponse, error) {
		return svc.CreateVM(ctx, &CreateVMRequest{
			UserID: user.ID.String(), Hostname: host, OSTemplateID: tmpl.ID, NodeID: node.ID,
			Resources: models.Resources{CPU: 1, RAM: 512, Disk: diskGB},
		})
	}

	// 25 GB free cannot take a 10 GB disk and keep 20 GB in reserve.
	_, err := create("full.example.test", 10)
	require.ErrorIs(t, err, ErrNodeCapacityExceeded)
	require.Zero(t, vmCount(), "a refused order leaves no VM row")

	free(200)
	resp, err := create("fits.example.test", 10)
	require.NoError(t, err)
	var recorded *string
	require.NoError(t, db.Raw("SELECT storage_pool_id FROM vms WHERE id = ?", resp.VM.ID).Scan(&recorded).Error)
	require.NotNil(t, recorded)
	require.Equal(t, pool.ID, *recorded, "the VM remembers the pool its disk was created in")

	// An allocation ratio caps what is promised, however much is really free:
	// 0.1 x 500 GB = 50 GB, of which 10 are taken.
	require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('quotas', '{"diskAllocationRatio": 0.1}')`).Error)
	_, err = create("over-ratio.example.test", 41)
	require.ErrorIs(t, err, ErrNodeCapacityExceeded)
	require.EqualValues(t, 1, vmCount())

	// Growing a disk draws on the pool as a new one does.
	require.NoError(t, db.Model(&models.VM{}).Where("id = ?", resp.VM.ID).Update("status", models.VMStatusStopped).Error)
	_, err = svc.UpdateVM(ctx, &UpdateVMRequest{VMID: resp.VM.ID, Resources: &models.Resources{CPU: 1, RAM: 512, Disk: 51}})
	require.ErrorIs(t, err, ErrNodeCapacityExceeded, "10 + 41 is over the 50 GB cap")
	require.NoError(t, db.Exec(`DELETE FROM system_settings`).Error)
	free(25)
	_, err = svc.UpdateVM(ctx, &UpdateVMRequest{VMID: resp.VM.ID, Resources: &models.Resources{CPU: 1, RAM: 512, Disk: 20}})
	require.ErrorIs(t, err, ErrNodeCapacityExceeded, "no ratio, but the growth does not fit in real free space")
	var stored models.VM
	require.NoError(t, db.First(&stored, "id = ?", resp.VM.ID).Error)
	require.Equal(t, 10, stored.Resources.Disk, "a refused grow changes nothing")
}
