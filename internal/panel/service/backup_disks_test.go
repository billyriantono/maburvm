package service

import (
	"context"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// featureAgent can test a destination and says what it can do.
type featureAgent struct {
	storeAgent
	features []string
}

func (a *featureAgent) GetNodeInfo(context.Context, *pb.GetNodeInfoRequest) (*pb.GetNodeInfoResponse, error) {
	return &pb.GetNodeInfoResponse{Success: true, Features: a.features}, nil
}

// useAgentFor makes the VM service reach nodeID's agent at agent.
func useAgentFor(t *testing.T, f *restoreFixture, nodeID string, agent pb.NodeAgentServer) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterNodeAgentServer(srv, agent)
	go srv.Serve(lis)
	conn, err := grpc.NewClient("passthrough:///node",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close(); srv.Stop() })
	f.svc.vmService.grpcConns[nodeID] = conn
}

func withDataDisk(t *testing.T, f *restoreFixture) {
	t.Helper()
	f.backup.Disks = []models.BackupDisk{
		{Target: "vda", Key: f.backup.StoragePath, Checksum: "c0ffee"},
		{Target: "vdb", Key: "prod/backups/shop-vdb.qcow2", Checksum: "beef", VirtualSize: 50 << 30},
	}
	require.NoError(t, f.db.Model(f.backup).Select("disks").Updates(f.backup).Error)
}

// An agent that would leave a backup's data disks out is refused before
// anything is created on its node.
func TestRestoreAsNewWithDataDisksNeedsAnAgentThatRestoresThemAll(t *testing.T) {
	ctx := context.Background()

	t.Run("agent without the feature", func(t *testing.T) {
		agent := &featureAgent{}
		f := newRestoreFixture(t, agent)
		withDataDisk(t, f)
		useAgentFor(t, f, f.target.ID, agent)
		before := f.vmCount(t)
		_, err := f.svc.RestoreAsNewVM(ctx, &RestoreAsNewRequest{BackupID: f.backup.ID, NodeID: f.target.ID})
		require.ErrorContains(t, err, "too old to restore them all")
		require.Equal(t, before, f.vmCount(t))
	})

	t.Run("agent that restores every disk", func(t *testing.T) {
		agent := &featureAgent{features: []string{featureRestoreDisks}}
		f := newRestoreFixture(t, agent)
		withDataDisk(t, f)
		useAgentFor(t, f, f.target.ID, agent)
		_, err := f.svc.RestoreAsNewVM(ctx, &RestoreAsNewRequest{BackupID: f.backup.ID, NodeID: f.target.ID})
		require.NoError(t, err)
	})

	t.Run("one-disk backup needs no new agent", func(t *testing.T) {
		f := newRestoreFixture(t, &storeAgent{})
		_, err := f.svc.RestoreAsNewVM(ctx, &RestoreAsNewRequest{BackupID: f.backup.ID, NodeID: f.target.ID})
		require.NoError(t, err)
	})
}

func TestRestoreCreateRequestCarriesTheDataDisks(t *testing.T) {
	backup := &models.Backup{StoragePath: "b/x.qcow2", Checksum: "c", Disks: []models.BackupDisk{
		{Target: "vda", Key: "b/x.qcow2"}, {Target: "vdb", Key: "b/x-vdb.qcow2", Checksum: "d"}}}
	req := restoreCreateRequest(backup, &models.VM{Hostname: "h"}, &RestoreAsNewRequest{NodeID: "n"}, true)
	params := map[string]interface{}{}
	req.Restore.apply(params)
	require.Equal(t, []models.BackupDisk{{Target: "vdb", Key: "b/x-vdb.qcow2", Checksum: "d"}}, params["restore_disks"])

	backup.Disks = nil
	req = restoreCreateRequest(backup, &models.VM{Hostname: "h"}, &RestoreAsNewRequest{NodeID: "n"}, true)
	params = map[string]interface{}{}
	req.Restore.apply(params)
	require.NotContains(t, params, "restore_disks")
}

func TestDeleteBackupRemovesEveryDisk(t *testing.T) {
	f := newRestoreFixture(t, &storeAgent{})
	withDataDisk(t, f)
	store := &fakeObjects{present: map[string]bool{f.backup.StoragePath: true, "prod/backups/shop-vdb.qcow2": true}}
	f.svc.objectStore = store
	left, err := f.svc.DeleteBackup(context.Background(), f.backup.ID)
	require.NoError(t, err)
	require.False(t, left)
	require.ElementsMatch(t, []string{f.backup.StoragePath, "prod/backups/shop-vdb.qcow2"}, store.deleted)
}
