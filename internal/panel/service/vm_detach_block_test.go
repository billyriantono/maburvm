package service

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/maburvm/panel/internal/panel/repository"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// detachAgent records the detach requests it is sent.
type detachAgent struct {
	pb.UnimplementedNodeAgentServer
	got []*pb.DetachDiskRequest
}

func (a *detachAgent) DetachDisk(_ context.Context, req *pb.DetachDiskRequest) (*pb.DetachDiskResponse, error) {
	a.got = append(a.got, req)
	return &pb.DetachDiskResponse{Success: true}, nil
}

// A data disk on a logical volume is deleted inside the pool holding it; an
// imported one is detached and kept, listed under its pool; one outside every
// registered pool is not touched.
func TestDetachDiskOnLVM(t *testing.T) {
	ctx := context.Background()
	db := setupVMIPAMTestDB(t)
	for _, stmt := range []string{
		`CREATE TABLE storage_pools (id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT, status TEXT, total_space INTEGER DEFAULT 0, used_space INTEGER DEFAULT 0, available_space INTEGER DEFAULT 0, path TEXT NOT NULL, file_format TEXT, alert_threshold INTEGER DEFAULT 90, overcommit INTEGER DEFAULT 0, is_primary BOOLEAN DEFAULT 0, status_note TEXT NOT NULL DEFAULT '', metadata_percent REAL, node_id TEXT NOT NULL, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE storage_volumes (id TEXT PRIMARY KEY, name TEXT NOT NULL, pool_id TEXT NOT NULL, vm_id TEXT, size INTEGER NOT NULL DEFAULT 0, format TEXT, path TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	user, node, tmpl := seedVMIPAMDependencies(t, db)
	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "db01", OSTemplateID: tmpl.ID, Resources: models.Resources{CPU: 1, RAM: 1024, Disk: 20}, Status: models.VMStatusRunning}
	require.NoError(t, db.Create(vm).Error)
	pool := &models.StoragePool{ID: "pool-vg0", Name: "vg0", NodeID: node.ID, Type: models.PoolTypeLVM, Path: "vg0", Status: "online", IsPrimary: true}
	require.NoError(t, db.Create(pool).Error)
	own := models.LVMDiskName(vm.ID, "d1")
	for _, d := range []models.VMDisk{
		{VMID: vm.ID, Device: "vdb", SizeGB: 10, Path: "/dev/vg0/" + own},
		{VMID: vm.ID, Device: "vdc", SizeGB: 5, Path: "/dev/vg0/legacy101-1"},
		{VMID: vm.ID, Device: "vdd", SizeGB: 3, Path: "/dev/other/x"},
	} {
		require.NoError(t, db.Create(&d).Error)
	}

	agent := &detachAgent{}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterNodeAgentServer(srv, agent)
	go srv.Serve(lis)
	conn, err := grpc.NewClient("passthrough:///node",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close(); srv.Stop() })
	vmRepo, nodeRepo := repository.NewVMRepository(db), repository.NewNodeRepository(db)
	svc := NewVMService(db, vmRepo, nodeRepo, repository.NewTemplateRepository(db), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.grpcConns[node.ID] = conn

	require.NoError(t, svc.DetachDisk(ctx, vm.ID, "vdb", true))
	require.NoError(t, svc.DetachDisk(ctx, vm.ID, "vdc", true))
	err = svc.DetachDisk(ctx, vm.ID, "vdd", true)
	require.ErrorContains(t, err, "not in a storage pool registered")

	require.Len(t, agent.got, 2, "the device outside every pool never reaches the node")
	assert.True(t, agent.got[0].DeleteVolume)
	assert.Equal(t, models.PoolTypeLVM, agent.got[0].PoolType)
	assert.Equal(t, "vg0", agent.got[0].PoolPath)
	assert.False(t, agent.got[1].DeleteVolume, "an imported volume is kept")

	var kept []models.StorageVolume
	require.NoError(t, db.Find(&kept).Error)
	require.Len(t, kept, 1)
	assert.Equal(t, "/dev/vg0/legacy101-1", kept[0].Path)
	assert.Equal(t, pool.ID, kept[0].PoolID)
	assert.Equal(t, "db01 vdc (kept)", kept[0].Name)
	var left []models.VMDisk
	require.NoError(t, db.Where("vm_id = ?", vm.ID).Find(&left).Error)
	require.Len(t, left, 1)
	assert.Equal(t, "vdd", left[0].Device)
}
