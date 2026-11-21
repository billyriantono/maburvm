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
