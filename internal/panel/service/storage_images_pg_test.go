//go:build pg

package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
)

// The storage tier, image and disk-move schema (migration 088) as the
// services use it on PostgreSQL.
func TestStorageTiersImagesAndMovesPG(t *testing.T) {
	db := migrationDB(t)
	ctx := context.Background()
	exec := func(q string, args ...any) { require.NoError(t, db.Exec(q, args...).Error) }

	user, node := uuid.New().String(), uuid.New().String()
	exec(`INSERT INTO users (id, email, password_hash) VALUES (?, 'owner@example.com', 'x')`, user)
	exec(`INSERT INTO nodes (id, name, ip_address, token, status) VALUES (?, 'n1', '192.0.2.10', 'tok', 'active')`, node)
	fast, slow := uuid.New().String(), uuid.New().String()
	exec(`INSERT INTO storage_pools (id, name, type, status, path, node_id, total_space, available_space, is_primary) VALUES
		(?, 'fast', 'dir', 'online', '/nvme', ?, 1000, 900, false), (?, 'slow', 'dir', 'online', '/hdd', ?, 1000, 800, true)`, fast, node, slow, node)

	_, err := SetPoolTier(ctx, db, fast, "nvme")
	require.NoError(t, err)
	p, err := NodeDiskPool(ctx, db, node, "nvme")
	require.NoError(t, err)
	require.Equal(t, fast, p.ID)
	require.Equal(t, "nvme", p.Tier)
	p, err = NodeDiskPool(ctx, db, node, "")
	require.NoError(t, err)
	require.Equal(t, slow, p.ID, "untiered orders keep the primary pool")

	plan := uuid.New().String()
	exec(`INSERT INTO plans (id, name, cpu, ram, disk) VALUES (?, 'p', 1, 1024, 20)`, plan)
	plans := &PlanService{repo: repository.NewPlanRepository(db)}
	got, err := plans.GetPlan(ctx, plan)
	require.NoError(t, err)
	tier := "nvme"
	require.NoError(t, plans.saveStorageTier(ctx, got, &tier))
	require.Equal(t, "nvme", planStorageTier(ctx, db, plan))

	// Templates: the kind filter, and the drivers ISO setting.
	disk := &models.OSTemplate{Name: "debian", Version: "12", ImagePath: "https://x/debian.qcow2", Kind: models.TemplateKindDisk, Firmware: models.FirmwareBIOS, IsActive: true}
	iso := &models.OSTemplate{Name: "virtio", Version: "1", ImagePath: models.UploadImageRef("2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824", "virtio-win.iso"),
		Kind: models.TemplateKindISO, Firmware: models.FirmwareBIOS, IsActive: true, ChecksumSHA256: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"}
	require.NoError(t, db.Create(disk).Error)
	require.NoError(t, db.Create(iso).Error)
	templates := NewTemplateService(db, repository.NewTemplateRepository(db), repository.NewNodeRepository(db), nil)
	isos, err := templates.ListTemplatesOfKind(ctx, 0, 0, false, models.TemplateKindISO)
	require.NoError(t, err)
	require.Len(t, isos, 1)
	require.Equal(t, iso.ID, isos[0].ID)
	_, err = DriversISOTemplate(ctx, db)
	require.ErrorIs(t, err, ErrNoDriversISO)
	require.ErrorIs(t, SetDriversISO(ctx, db, disk.ID), ErrInvalidTemplate)
	require.NoError(t, SetDriversISO(ctx, db, iso.ID))
	drivers, err := DriversISOTemplate(ctx, db)
	require.NoError(t, err)
	require.Equal(t, iso.ID, drivers.ID)

	// An uploaded ISO is offered on a node only once it is there.
	vm := uuid.New().String()
	exec(`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id, resources, status) VALUES (?, ?, ?, 'web', ?, '{"cpu":1,"ram":1024,"disk":20}', 'running')`,
		vm, user, node, disk.ID)
	vms := &VMService{db: db, vmRepo: repository.NewVMRepository(db), templateRepo: repository.NewTemplateRepository(db)}
	_, err = vms.ISOTemplateFor(ctx, vm, iso.ID, false)
	require.ErrorContains(t, err, "still being copied")
	exec(`INSERT INTO template_node_status (template_id, node_id, status, checksum) VALUES (?, ?, 'available', ?)`, iso.ID, node, iso.ChecksumSHA256)
	ok, err := vms.ISOTemplateFor(ctx, vm, iso.ID, false)
	require.NoError(t, err)
	require.Equal(t, iso.ID, ok.ID)
	require.Nil(t, vms.AttachedISO2(ctx, vm))
	exec(`UPDATE vms SET attached_iso_2 = 'x.iso' WHERE id = ?`, vm)
	require.Equal(t, "x.iso", *vms.AttachedISO2(ctx, vm))

	// Growing the system disk writes only the VM's resources, as JSON.
	require.NoError(t, setVMResources(ctx, db, vm, models.Resources{CPU: 1, RAM: 1024, Disk: 40}))
	var stored models.VM
	require.NoError(t, db.Where("id = ?", vm).First(&stored).Error)
	require.Equal(t, 40, stored.Resources.Disk)
	require.Equal(t, "web", stored.Hostname)

	// One disk move of a VM at a time.
	move := &VMDiskMove{ID: uuid.New().String(), VMID: vm, NodeID: node, Target: "vda", DestPoolID: fast, Status: diskMoveRunning}
	require.NoError(t, db.Create(move).Error)
	require.True(t, diskMoveActive(ctx, db, vm))
	second := &VMDiskMove{ID: uuid.New().String(), VMID: vm, NodeID: node, Target: "vdb", DestPoolID: fast, Status: diskMoveQueued}
	require.Error(t, db.Create(second).Error)
	vms.finishDiskMove(ctx, move, diskMoveCompleted, "", "")
	require.False(t, diskMoveActive(ctx, db, vm))
	moves, err := vms.ListDiskMoves(ctx, vm)
	require.NoError(t, err)
	require.Len(t, moves, 1)
	require.Equal(t, diskMoveCompleted, moves[0].Status)
}
