package service

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/maburvm/panel/internal/panel/repository"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// earlyStopAgent answers a stop as done at once, as an agent from before stops
// waited for the guest does; its status says where the VM really is.
type earlyStopAgent struct {
	pb.UnimplementedNodeAgentServer
	mu       sync.Mutex
	state    pb.VMState
	commands []pb.VMCommandType
}

func (a *earlyStopAgent) ExecuteVMCommand(_ context.Context, req *pb.VMCommandRequest) (*pb.VMCommandResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.commands = append(a.commands, req.Command)
	if req.Command == pb.VMCommandType_VM_COMMAND_TYPE_FORCE_STOP {
		a.state = pb.VMState_VM_STATE_STOPPED
	}
	return &pb.VMCommandResponse{Success: true, VmId: req.VmId, Command: req.Command, State: pb.VMState_VM_STATE_STOPPED}, nil
}

func (a *earlyStopAgent) GetVMStatus(_ context.Context, req *pb.VMStatusRequest) (*pb.VMStatusResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &pb.VMStatusResponse{VmId: req.VmId, State: a.state}, nil
}

func (a *earlyStopAgent) set(state pb.VMState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state, a.commands = state, nil
}

// A stop is recorded when the node's own status says the VM is off, whatever
// the answer to the command said.
func TestStopIsDoneWhenTheNodeSaysTheVMIsOff(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	user, node, tmpl := seedVMIPAMDependencies(t, db)
	require.NoError(t, db.Model(&models.Node{}).Where("id = ?", node.ID).Update("status", models.NodeStatusActive).Error)
	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "web01", OSTemplateID: tmpl.ID, Resources: models.Resources{CPU: 1, RAM: 1024, Disk: 20}, Status: models.VMStatusRunning}
	require.NoError(t, db.Create(vm).Error)

	agent := &earlyStopAgent{state: pb.VMState_VM_STATE_RUNNING}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterNodeAgentServer(srv, agent)
	go srv.Serve(lis)
	conn, err := grpc.NewClient("passthrough:///node",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close(); srv.Stop() })
	vmRepo := repository.NewVMRepository(db)
	svc := NewVMService(db, vmRepo, repository.NewNodeRepository(db), repository.NewTemplateRepository(db), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	svc.grpcConns[node.ID] = conn
	status := func() models.VMStatus {
		got, err := vmRepo.GetByID(context.Background(), vm.ID)
		require.NoError(t, err)
		return got.Status
	}

	// The caller gives up while the guest is still on.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	resp, err := svc.ExecuteLifecycleCommand(ctx, &LifecycleRequest{VMID: vm.ID, Command: LifecycleStop})
	require.ErrorIs(t, err, ErrVMLifecycleFailed)
	require.Equal(t, pb.VMState_VM_STATE_RUNNING.String(), resp.NewState)
	require.Equal(t, models.VMStatusRunning, status(), "a VM still on was recorded as stopped")

	agent.set(pb.VMState_VM_STATE_STOPPED)
	_, err = svc.ExecuteLifecycleCommand(context.Background(), &LifecycleRequest{VMID: vm.ID, Command: LifecycleStop})
	require.NoError(t, err)
	require.Equal(t, models.VMStatusStopped, status())

	// Quota enforcement does not leave a guest that stays on running: after a
	// short wait its power is cut.
	require.NoError(t, vmRepo.UpdateStatus(context.Background(), vm.ID, models.VMStatusRunning))
	agent.set(pb.VMState_VM_STATE_RUNNING)
	prev := enforcementStopWait
	enforcementStopWait = 200 * time.Millisecond
	t.Cleanup(func() { enforcementStopWait = prev })
	require.NoError(t, svc.StopVMForEnforcement(context.Background(), node.ID, vm.ID))
	require.Equal(t, []pb.VMCommandType{pb.VMCommandType_VM_COMMAND_TYPE_STOP, pb.VMCommandType_VM_COMMAND_TYPE_FORCE_STOP}, agent.commands)
	require.Equal(t, models.VMStatusStopped, status())
}
