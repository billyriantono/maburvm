package service

import (
	"testing"

	"github.com/stretchr/testify/assert"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

func TestSameCPUNeedsEveryFeature(t *testing.T) {
	a := &pb.GetMigrationInfoResponse{CpuVendor: "Intel", CpuModel: "Skylake-Server-IBRS", CpuFeatures: []string{"avx2", "ssse3"}}
	b := &pb.GetMigrationInfoResponse{CpuVendor: "Intel", CpuModel: "Skylake-Server-IBRS", CpuFeatures: []string{"avx2", "ssse3"}}
	assert.True(t, sameCPU(a, b))
	b.CpuFeatures = []string{"avx2"}
	assert.False(t, sameCPU(a, b), "a missing feature is a different CPU to a guest that sees the host's")
	assert.False(t, sameCPU(&pb.GetMigrationInfoResponse{}, &pb.GetMigrationInfoResponse{}), "unknown CPUs are never the same")
}

func TestMibPerSecond(t *testing.T) {
	assert.Equal(t, int64(0), mibPerSecond(0))
	assert.Equal(t, int64(1), mibPerSecond(1), "a set cap is never 0, which libvirt reads as none")
	assert.Equal(t, int64(119), mibPerSecond(1000))
}

func TestDiskMapsCopyDisksAndPlaceTheSeed(t *testing.T) {
	plan := &migrationPlan{
		disks: []planDisk{{Target: "vda", FileSize: 10}, {Target: "vdb", FileSize: 20}},
		seed:  &planDisk{Target: "sda"},
	}
	prep := &pb.PrepareMigrationTargetResponse{Paths: map[string]string{"vda": "/data/a.qcow2", "vdb": "/data/a-vdb.qcow2"}, SeedPath: "/data/a-seed.iso"}
	got := diskMaps(plan, prep)
	assert.Len(t, got, 3)
	assert.Equal(t, &pb.MigrationDiskMap{Target: "vda", DestPath: "/data/a.qcow2", Copy: true, ExpectedBytes: 10}, got[0])
	assert.Equal(t, &pb.MigrationDiskMap{Target: "sda", DestPath: "/data/a-seed.iso"}, got[2], "the seed is placed, not copied")
}

func TestCheckEnvCountsWhatAPlanSendsToANode(t *testing.T) {
	env := newCheckEnv()
	vm := &models.VM{Resources: models.Resources{CPU: 2, RAM: 4096}}
	env.add("n1", "p1", vm, 40)
	env.add("n1", "p1", vm, 10)
	loads := withExtra([]nodeLoad{{Node: models.Node{ID: "n1"}, AllocatedVCPU: 1, AllocatedRAMMB: 1024}, {Node: models.Node{ID: "n2"}}}, env)
	assert.Equal(t, 5, loads[0].AllocatedVCPU)
	assert.Equal(t, int64(1024+8192), loads[0].AllocatedRAMMB)
	assert.Equal(t, 0, loads[1].AllocatedVCPU)
	assert.Equal(t, 50, env.extra["n1"].poolGB["p1"])
}

// A VM with its system disk on shared storage and a data disk on local
// storage: the shared drives stay at their paths, are never prepared on the
// destination (so never rolled back there), and are named for the source to
// keep; only the local disk is copied and counted against the pool.
func TestMixedMoveCopiesOnlyTheLocalDisk(t *testing.T) {
	plan := &migrationPlan{
		disks: []planDisk{
			{Target: "vda", Path: "/mnt/s/a.qcow2", FileName: "a.qcow2", Format: "qcow2", Virtual: 20 << 30, FileSize: 10},
			{Target: "vdb", Path: "/vm/a-vdb.qcow2", FileName: "a-vdb.qcow2", Format: "qcow2", Virtual: 10 << 30, FileSize: 20},
		},
		seed: &planDisk{Target: "sda", Path: "/mnt/s/a-seed.iso", FileName: "a-seed.iso"},
		stay: map[string]bool{"vda": true, "sda": true},
	}
	got := diskMaps(plan, &pb.PrepareMigrationTargetResponse{Paths: map[string]string{"vdb": "/data/a-vdb.qcow2"}})
	assert.Equal(t, []*pb.MigrationDiskMap{
		{Target: "vda", DestPath: "/mnt/s/a.qcow2"},
		{Target: "vdb", DestPath: "/data/a-vdb.qcow2", Copy: true, ExpectedBytes: 20},
		{Target: "sda", DestPath: "/mnt/s/a-seed.iso"},
	}, got)
	assert.Equal(t, []string{"/mnt/s/a.qcow2", "/mnt/s/a-seed.iso"}, plan.keptPaths())

	f := &srcFacts{vm: &models.VM{ID: "v"}, mode: models.MigrationOffline, diskGB: 30, disks: plan.disks, seed: plan.seed}
	req := (&MigrationService{}).targetRequest(f, &models.StoragePool{Path: "/data"}, "m", false, plan.stay)
	assert.Len(t, req.Disks, 1)
	assert.Equal(t, "vdb", req.Disks[0].Target)
	assert.Empty(t, req.SeedFileName, "a seed that stays is not written again")
	assert.EqualValues(t, 20, req.FreeBytesNeeded)

	member := &models.StoragePool{ID: "q"}
	assert.Equal(t, 10, f.copyGB(map[string]*models.StoragePool{"vda": member}))
	assert.Equal(t, 0, f.copyGB(map[string]*models.StoragePool{"vda": member, "vdb": member}))
	assert.Equal(t, 30, f.copyGB(nil))
}
