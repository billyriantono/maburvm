package service

import (
	"context"
	"errors"
	"testing"

	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
)

func TestParseChecksumList(t *testing.T) {
	const sum = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	const other = "0000000000000000000000000000000000000000000000000000000000000000"
	cases := []struct {
		name, body, file, want string
	}{
		{"sha256sums", other + " *jammy.img\n" + sum + " *noble-server-cloudimg-amd64.img\n", "noble-server-cloudimg-amd64.img", sum},
		{"two spaces", sum + "  debian.qcow2\n", "debian.qcow2", sum},
		{"bsd", "# comment\nSHA256 (Rocky-9.qcow2) = " + other + "\nSHA256 (Rocky-9-GenericCloud.latest.x86_64.qcow2) = " + sum + "\n", "Rocky-9-GenericCloud.latest.x86_64.qcow2", sum},
		{"single hash", sum + "\n", "anything.iso", sum},
		{"upper case", "2CF24DBA5FB0A30E26E83B2AC5B9E29E1B161E5C1FA7425E73043362938B9824  x.iso", "x.iso", sum},
	}
	for _, c := range cases {
		got, err := ParseChecksumList(c.body, c.file)
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v", c.name, got, err)
		}
	}
	if _, err := ParseChecksumList(other+" *a.img\n"+sum+" *b.img\n", "c.img"); !errors.Is(err, ErrInvalidTemplate) {
		t.Fatalf("a list without the file must refuse, got %v", err)
	}
}

func TestCheckImageAddress(t *testing.T) {
	ok := []struct {
		ref   string
		allow bool
	}{
		{"https://cloud.example/x.qcow2", false},
		{"http://mirror.example/x.qcow2", true},
		{"/var/lib/libvirt/templates/windows-2022.qcow2", false},
		{models.UploadImageRef("2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824", "virtio-win.iso"), false},
	}
	for _, c := range ok {
		require.NoError(t, checkImageAddress(c.ref, c.allow), c.ref)
	}
	for _, ref := range []string{"http://mirror.example/x.qcow2", "ftp://x/y.iso", "relative/path.qcow2"} {
		require.ErrorIs(t, checkImageAddress(ref, false), ErrInvalidTemplate, ref)
	}
}

func TestDiskOnlyGrowth(t *testing.T) {
	swap := 512
	old := models.Resources{CPU: 2, RAM: 2048, Disk: 40, Swap: &swap}
	grow := old
	grow.Disk = 60
	require.True(t, diskOnlyGrowth(old, grow))
	same := old
	require.False(t, diskOnlyGrowth(old, same))
	shrink := old
	shrink.Disk = 20
	require.False(t, diskOnlyGrowth(old, shrink))
	more := grow
	more.RAM = 4096
	require.False(t, diskOnlyGrowth(old, more), "a RAM change needs the VM stopped")
}

func TestPlanDiskDestination(t *testing.T) {
	dir := &models.StoragePool{Type: "dir"}
	thin := &models.StoragePool{Type: models.PoolTypeLVMThin}
	file := planDisk{FileName: "a.qcow2", Format: "qcow2"}
	lv := planDisk{FileName: "maburvm-a-vda", Format: "raw", Block: true}
	cases := []struct {
		d          planDisk
		pool       *models.StoragePool
		mode       string
		format, fn string
	}{
		{file, dir, models.MigrationLive, "qcow2", "a.qcow2"},
		{file, thin, models.MigrationOffline, "raw", "a.qcow2"},
		{lv, dir, models.MigrationLive, "qcow2", "maburvm-a-vda.qcow2"},
		{lv, dir, models.MigrationOffline, "raw", "maburvm-a-vda.img"},
		{lv, thin, models.MigrationLive, "raw", "maburvm-a-vda"},
	}
	for i, c := range cases {
		if f := c.d.destFormat(c.pool, c.mode); f != c.format {
			t.Errorf("%d: format %s, want %s", i, f, c.format)
		}
		if c.pool.Type == "dir" {
			if n := c.d.destFileName(c.pool, c.mode); n != c.fn {
				t.Errorf("%d: name %s, want %s", i, n, c.fn)
			}
		}
	}
}

func TestNodeDiskPoolAndMovePoolFollowTiers(t *testing.T) {
	ctx := context.Background()
	db, data, local := placementDB(t)
	require.NoError(t, db.Exec(`ALTER TABLE storage_pools ADD COLUMN tier TEXT NOT NULL DEFAULT ''`).Error)
	nvme := &models.StoragePool{ID: "pool-nvme", Name: "nvme", NodeID: "node-1", Path: "/nvme", Type: "dir", Status: "online", TotalSpace: 500 * gib, AvailableSpace: 400 * gib}
	thin := &models.StoragePool{ID: "pool-thin", Name: "thin", NodeID: "node-1", Path: "vg0/thin", Type: models.PoolTypeLVMThin, Status: "online", TotalSpace: 500 * gib, AvailableSpace: 450 * gib}
	require.NoError(t, db.Create(nvme).Error)
	require.NoError(t, db.Create(thin).Error)
	_, err := SetPoolTier(ctx, db, nvme.ID, " NVMe ")
	require.NoError(t, err)
	_, err = SetPoolTier(ctx, db, thin.ID, "nvme")
	require.NoError(t, err)
	_, err = SetPoolTier(ctx, db, local.ID, "bad tier!")
	require.ErrorIs(t, err, ErrInvalidStorageTier)

	// No tier: the node's primary pool, as before tiers.
	p, err := NodeDiskPool(ctx, db, "node-1", "")
	require.NoError(t, err)
	require.Equal(t, data.ID, p.ID)
	// A tier: the roomiest of its pools.
	p, err = NodeDiskPool(ctx, db, "node-1", "nvme")
	require.NoError(t, err)
	require.Equal(t, thin.ID, p.ID)
	_, err = NodeDiskPool(ctx, db, "node-1", "hdd")
	require.ErrorIs(t, err, ErrNoTierPool)

	tiers, err := StorageTiers(ctx, db)
	require.NoError(t, err)
	require.Equal(t, []string{"nvme"}, tiers)

	// A move lands in the VM's tier and kind: a file disk on nvme goes to the
	// nvme directory, a volume to the nvme thin pool, an untiered disk to an
	// untiered directory.
	nvme.Tier, thin.Tier = "nvme", "nvme"
	require.Equal(t, nvme.ID, movePool(ctx, db, "node-1", nvme).ID)
	require.Equal(t, thin.ID, movePool(ctx, db, "node-1", thin).ID)
	require.Equal(t, data.ID, movePool(ctx, db, "node-1", data).ID)
	require.Equal(t, data.ID, movePool(ctx, db, "node-1", nil).ID)
}

func TestDiskMoveRefusalCountsAsAMove(t *testing.T) {
	require.ErrorIs(t, ErrDiskMoveActive, ErrVMMigrating, "routes refusing changes during a move refuse them during a disk move")
	require.ErrorIs(t, ErrDiskMoveActive, ErrDiskMoveActive)
	require.False(t, errors.Is(ErrVMMigrating, ErrDiskMoveActive))
	require.False(t, diskMoveActive(context.Background(), nil, "vm"))
}
