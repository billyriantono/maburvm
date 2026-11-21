// Package fakeagent is a stand-in for a node agent. It answers the NodeAgent
// gRPC service from memory, records every call, and lets a test script any
// answer. It never touches a hypervisor.
//
// The queue worker tests run it in process; cmd/fake-agent serves it over TLS
// for browser tests. Nothing in the panel or the agent imports it.
package fakeagent

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
)

// Features is what a current agent reports in GetNodeInfo.
var Features = []string{"restore_disks", "lvm_disks", "backup_targets", "incremental_backup", "hostname_proxy"}

// Hook answers one RPC in place of the default. Returning (nil, nil) hands the
// call on to the default, so a hook can fail the first tries and then let the
// node behave.
type Hook func(req proto.Message) (proto.Message, error)

// Call is one RPC as it reached the agent.
type Call struct {
	Method string
	Req    proto.Message
	// Authorization is the bearer header it carried.
	Authorization string
}

// VM is a domain as the fake node holds it.
type VM struct {
	ID        string
	State     pb.VMState
	Config    *pb.VMConfig
	ISO       string
	Snapshots []string
	// SnapshotStates is the state each snapshot was taken in, which a revert
	// returns the VM to; one not listed reverts to stopped.
	SnapshotStates map[string]pb.VMState
	Disks          []string // data disk targets, vdb onwards
}

// Agent is one fake node.
type Agent struct {
	pb.UnimplementedNodeAgentServer

	// Token, when set, is the bearer token every call must carry, as a real
	// agent requires. A call without it is refused as Unauthenticated.
	Token string
	// Features is what GetNodeInfo reports; nil means the package default.
	Features []string

	mu    sync.Mutex
	vms   map[string]*VM
	calls []Call
	hooks map[string]Hook
}

// New returns an empty node.
func New() *Agent {
	return &Agent{vms: map[string]*VM{}, hooks: map[string]Hook{}}
}

// On replaces the answer to one RPC, named as on the service ("BackupDisk").
// A nil hook restores the default.
func (a *Agent) On(method string, h Hook) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if h == nil {
		delete(a.hooks, method)
		return
	}
	a.hooks[method] = h
}

// Calls returns the requests one RPC received, oldest first.
func (a *Agent) Calls(method string) []Call {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []Call
	for _, c := range a.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// Commands returns the VM commands received, in order, as "COMMAND vm_id".
func (a *Agent) Commands() []string {
	var out []string
	for _, c := range a.Calls("ExecuteVMCommand") {
		r := c.Req.(*pb.VMCommandRequest)
		out = append(out, strings.TrimPrefix(r.GetCommand().String(), "VM_COMMAND_TYPE_")+" "+r.GetVmId())
	}
	return out
}

// VM returns a copy of a domain, and whether the node has it.
func (a *Agent) VM(id string) (VM, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	vm, ok := a.vms[id]
	if !ok {
		return VM{}, false
	}
	return *vm, true
}

// PutVM places a domain on the node, as if it had been created earlier.
func (a *Agent) PutVM(vm VM) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.vms[vm.ID] = &vm
}

// VMs returns the ids of the domains on the node, sorted.
func (a *Agent) VMs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	ids := make([]string, 0, len(a.vms))
	for id := range a.vms {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ServerOptions holds the interceptors that check the token.
func (a *Agent) ServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
			if err := a.authorize(ctx); err != nil {
				return nil, err
			}
			return next(ctx, req)
		}),
		grpc.StreamInterceptor(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
			if err := a.authorize(ss.Context()); err != nil {
				return err
			}
			return next(srv, ss)
		}),
	}
}

func (a *Agent) authorize(ctx context.Context) error {
	if a.Token == "" {
		return nil
	}
	if bearer(ctx) != "Bearer "+a.Token {
		return status.Error(codes.Unauthenticated, "missing or wrong authorization token")
	}
	return nil
}

func bearer(ctx context.Context) string {
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("authorization"); len(v) > 0 {
		return v[0]
	}
	return ""
}

// Dial serves the agent in process and returns a client for it, with a
// function that stops both.
func (a *Agent) Dial() (pb.NodeAgentClient, func(), error) {
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer(a.ServerOptions()...)
	pb.RegisterNodeAgentServer(srv, a)
	go func() { _ = srv.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough:///fake-node",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		srv.Stop()
		return nil, nil, err
	}
	return pb.NewNodeAgentClient(conn), func() { conn.Close(); srv.Stop() }, nil
}

// answer records a call, runs its hook if there is one, and otherwise the
// default, which runs holding the lock.
func answer[Req, Resp proto.Message](ctx context.Context, a *Agent, method string, req Req, def func() (Resp, error)) (Resp, error) {
	a.mu.Lock()
	a.calls = append(a.calls, Call{Method: method, Req: proto.Clone(req), Authorization: bearer(ctx)})
	h := a.hooks[method]
	a.mu.Unlock()
	if h != nil {
		resp, err := h(req)
		if err != nil {
			var zero Resp
			return zero, err
		}
		if resp != nil {
			return resp.(Resp), nil
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return def()
}

func notFound(what string) *pb.ErrorResponse {
	return &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_NOT_FOUND, Message: what}
}

// --- VM lifecycle -----------------------------------------------------------

func (a *Agent) ExecuteVMCommand(ctx context.Context, req *pb.VMCommandRequest) (*pb.VMCommandResponse, error) {
	return answer(ctx, a, "ExecuteVMCommand", req, func() (*pb.VMCommandResponse, error) {
		resp := &pb.VMCommandResponse{Success: true, VmId: req.VmId, Command: req.Command}
		vm := a.vms[req.VmId]
		if vm == nil && req.Command != pb.VMCommandType_VM_COMMAND_TYPE_CREATE {
			resp.Success = false
			// The words libvirt uses, which the panel recognises.
			resp.Error = notFound("domain not found: no domain with matching uuid '" + req.VmId + "'")
			return resp, nil
		}
		switch req.Command {
		case pb.VMCommandType_VM_COMMAND_TYPE_CREATE:
			vm = &VM{ID: req.VmId, State: pb.VMState_VM_STATE_RUNNING, Config: req.Config}
			for _, d := range req.Config.GetRestoreDisks() {
				vm.Disks = append(vm.Disks, d.GetTarget())
				resp.RestoredDisks = append(resp.RestoredDisks, &pb.BackupDiskFile{Target: d.GetTarget(), Path: "/vm/" + req.VmId + "-" + d.GetTarget() + ".qcow2", SizeBytes: d.GetSizeBytes()})
			}
			a.vms[req.VmId] = vm
		case pb.VMCommandType_VM_COMMAND_TYPE_START, pb.VMCommandType_VM_COMMAND_TYPE_RESTART, pb.VMCommandType_VM_COMMAND_TYPE_RESUME:
			vm.State = pb.VMState_VM_STATE_RUNNING
		case pb.VMCommandType_VM_COMMAND_TYPE_STOP, pb.VMCommandType_VM_COMMAND_TYPE_FORCE_STOP:
			vm.State = pb.VMState_VM_STATE_STOPPED
		case pb.VMCommandType_VM_COMMAND_TYPE_PAUSE:
			vm.State = pb.VMState_VM_STATE_PAUSED
		case pb.VMCommandType_VM_COMMAND_TYPE_DESTROY:
			delete(a.vms, req.VmId)
			resp.State = pb.VMState_VM_STATE_DESTROYED
			resp.StorageDisposition = pb.DiskStorageDisposition_DISK_STORAGE_DISPOSITION_ABSENT
			return resp, nil
		case pb.VMCommandType_VM_COMMAND_TYPE_REBUILD:
			// A reinstall leaves the VM off on its new disk; the panel starts it.
			vm.State, vm.Snapshots = pb.VMState_VM_STATE_STOPPED, nil
			if req.Config != nil {
				vm.Config = req.Config
			}
		case pb.VMCommandType_VM_COMMAND_TYPE_RESIZE:
			if r := req.Config.GetResources(); r != nil {
				if vm.Config == nil {
					vm.Config = &pb.VMConfig{}
				}
				vm.Config.Resources = r
			}
		case pb.VMCommandType_VM_COMMAND_TYPE_ATTACH_ISO:
			vm.ISO = req.Config.GetImageId()
		case pb.VMCommandType_VM_COMMAND_TYPE_DETACH_ISO:
			vm.ISO = ""
		}
		resp.State = vm.State
		return resp, nil
	})
}

func (a *Agent) GetVMStatus(ctx context.Context, req *pb.VMStatusRequest) (*pb.VMStatusResponse, error) {
	return answer(ctx, a, "GetVMStatus", req, func() (*pb.VMStatusResponse, error) {
		vm := a.vms[req.VmId]
		if vm == nil {
			return nil, status.Errorf(codes.NotFound, "VM not found: domain not found")
		}
		resp := &pb.VMStatusResponse{VmId: vm.ID, State: vm.State, VncPort: 5901, LastStateChange: timestamppb.Now()}
		if r := vm.Config.GetResources(); r != nil {
			resp.ConfiguredResources = &pb.VMResources{Vcpus: r.Vcpus, MemoryMb: r.MemoryMb, DiskGb: r.DiskGb}
		}
		for _, i := range vm.Config.GetNetworkConfig().GetInterfaces() {
			if i.GetIpAddress() != "" && vm.State == pb.VMState_VM_STATE_RUNNING {
				resp.IpAddresses = append(resp.IpAddresses, i.GetIpAddress())
			}
		}
		return resp, nil
	})
}

func (a *Agent) StreamVMMetrics(req *pb.VMMetricsRequest, stream grpc.ServerStreamingServer[pb.VMMetricsResponse]) error {
	ids, err := answer(stream.Context(), a, "StreamVMMetrics", req, func() (*pb.VMMetricsRequest, error) {
		out := &pb.VMMetricsRequest{VmIds: req.VmIds}
		if len(out.VmIds) == 0 {
			for id := range a.vms {
				out.VmIds = append(out.VmIds, id)
			}
		}
		return out, nil
	})
	if err != nil {
		return err
	}
	for _, id := range ids.VmIds {
		if err := stream.Send(&pb.VMMetricsResponse{Timestamp: timestamppb.Now(), VmId: id,
			Cpu: &pb.CPUMetrics{UsagePercent: 3}, Memory: &pb.MemoryMetrics{UsedBytes: 256 << 20, TotalBytes: 1 << 30}}); err != nil {
			return err
		}
	}
	<-stream.Context().Done()
	return nil
}

func (a *Agent) SetVMLimits(ctx context.Context, req *pb.SetVMLimitsRequest) (*pb.SetVMLimitsResponse, error) {
	return answer(ctx, a, "SetVMLimits", req, func() (*pb.SetVMLimitsResponse, error) {
		return &pb.SetVMLimitsResponse{Success: true}, nil
	})
}

func (a *Agent) SetConsoleAccess(ctx context.Context, req *pb.SetConsoleAccessRequest) (*pb.SetConsoleAccessResponse, error) {
	return answer(ctx, a, "SetConsoleAccess", req, func() (*pb.SetConsoleAccessResponse, error) {
		return &pb.SetConsoleAccessResponse{Success: true, VncPort: 5901}, nil
	})
}

// --- Snapshots, backups, disks ----------------------------------------------

func (a *Agent) CreateSnapshot(ctx context.Context, req *pb.SnapshotRequest) (*pb.SnapshotResponse, error) {
	return answer(ctx, a, "CreateSnapshot", req, func() (*pb.SnapshotResponse, error) {
		vm := a.vms[req.VmId]
		resp := &pb.SnapshotResponse{Success: true, Operation: req.Operation}
		if vm == nil {
			resp.Success, resp.Error = false, notFound("domain not found")
			return resp, nil
		}
		find := func(name string) int {
			for i, s := range vm.Snapshots {
				if s == name {
					return i
				}
			}
			return -1
		}
		switch req.Operation {
		case pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_CREATE:
			name := req.Name
			if name == "" {
				name = fmt.Sprintf("snap-%d", len(vm.Snapshots)+1)
			}
			vm.Snapshots = append(vm.Snapshots, name)
			if vm.SnapshotStates == nil {
				vm.SnapshotStates = map[string]pb.VMState{}
			}
			vm.SnapshotStates[name] = vm.State
			resp.Snapshot = &pb.SnapshotInfo{SnapshotId: name, Name: name, CreatedAt: timestamppb.Now(), VmState: vm.State}
		case pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_RESTORE:
			if find(req.SnapshotId) < 0 {
				resp.Success, resp.Error = false, notFound("snapshot not found: "+req.SnapshotId)
				return resp, nil
			}
			vm.State = pb.VMState_VM_STATE_STOPPED
			if st, ok := vm.SnapshotStates[req.SnapshotId]; ok {
				vm.State = st
			}
			resp.Snapshot = &pb.SnapshotInfo{SnapshotId: req.SnapshotId, VmState: vm.State}
		case pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_DELETE:
			i := find(req.SnapshotId)
			if i < 0 {
				resp.Success, resp.Error = false, notFound("snapshot not found: "+req.SnapshotId)
				return resp, nil
			}
			vm.Snapshots = append(vm.Snapshots[:i], vm.Snapshots[i+1:]...)
		case pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_LIST:
			for _, s := range vm.Snapshots {
				resp.Snapshots = append(resp.Snapshots, &pb.SnapshotInfo{SnapshotId: s, Name: s})
			}
		}
		return resp, nil
	})
}

// BackupDisk writes one file per disk under the key it was given, and makes
// an incremental backup whenever it is asked for one.
func (a *Agent) BackupDisk(ctx context.Context, req *pb.BackupDiskRequest) (*pb.BackupDiskResponse, error) {
	return answer(ctx, a, "BackupDisk", req, func() (*pb.BackupDiskResponse, error) {
		vm := a.vms[req.VmId]
		if vm == nil {
			return &pb.BackupDiskResponse{Error: notFound("primary disk not found: domain not found")}, nil
		}
		resp := &pb.BackupDiskResponse{Success: true, UsedRequestStore: req.ObjectStore != nil, Compression: req.Compression,
			Consistency: "live", Mode: "full", Checkpoint: req.Checkpoint, ObjectKey: req.DestinationKey}
		if req.IncrementalFrom != "" {
			resp.Mode = "incremental"
		}
		targets := []string{"vda"}
		if req.AllDisks {
			targets = append(targets, vm.Disks...)
		}
		for i, t := range targets {
			key := req.DestinationKey
			if i > 0 {
				key = strings.TrimSuffix(key, ".qcow2") + "-" + t + ".qcow2"
			}
			resp.Disks = append(resp.Disks, &pb.BackupDiskFile{Target: t, Key: key, SizeBytes: 1 << 20, Checksum: "sha256-" + t, VirtualSizeBytes: 10 << 30})
			resp.SizeBytes += 1 << 20
		}
		resp.Checksum = resp.Disks[0].Checksum
		return resp, nil
	})
}

func (a *Agent) RestoreDisk(ctx context.Context, req *pb.RestoreDiskRequest) (*pb.RestoreDiskResponse, error) {
	return answer(ctx, a, "RestoreDisk", req, func() (*pb.RestoreDiskResponse, error) {
		vm := a.vms[req.VmId]
		if vm == nil {
			return &pb.RestoreDiskResponse{Error: notFound("domain not found")}, nil
		}
		// As a real node: never under a running disk.
		if vm.State != pb.VMState_VM_STATE_STOPPED {
			return &pb.RestoreDiskResponse{Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL,
				Message: "VM must be stopped to restore (state: " + vm.State.String() + ")"}}, nil
		}
		vm.Snapshots = nil
		return &pb.RestoreDiskResponse{Success: true, BytesRestored: 1 << 20}, nil
	})
}

func (a *Agent) TestObjectStore(ctx context.Context, req *pb.TestObjectStoreRequest) (*pb.TestObjectStoreResponse, error) {
	return answer(ctx, a, "TestObjectStore", req, func() (*pb.TestObjectStoreResponse, error) {
		return &pb.TestObjectStoreResponse{Success: true}, nil
	})
}

func (a *Agent) DeleteBackupFiles(ctx context.Context, req *pb.DeleteBackupFilesRequest) (*pb.DeleteBackupFilesResponse, error) {
	return answer(ctx, a, "DeleteBackupFiles", req, func() (*pb.DeleteBackupFilesResponse, error) {
		return &pb.DeleteBackupFilesResponse{Deleted: req.Keys}, nil
	})
}

func (a *Agent) AttachDisk(ctx context.Context, req *pb.AttachDiskRequest) (*pb.AttachDiskResponse, error) {
	return answer(ctx, a, "AttachDisk", req, func() (*pb.AttachDiskResponse, error) {
		vm := a.vms[req.VmId]
		if vm == nil {
			return &pb.AttachDiskResponse{Error: notFound("domain not found"), OperationId: req.OperationId}, nil
		}
		dev := fmt.Sprintf("vd%c", 'b'+len(vm.Disks))
		vm.Disks = append(vm.Disks, dev)
		return &pb.AttachDiskResponse{Success: true, Device: dev, Path: "/vm/" + req.VmId + "-" + dev + ".qcow2", OperationId: req.OperationId,
			StorageDisposition: pb.DiskStorageDisposition_DISK_STORAGE_DISPOSITION_ATTACHED}, nil
	})
}

func (a *Agent) DetachDisk(ctx context.Context, req *pb.DetachDiskRequest) (*pb.DetachDiskResponse, error) {
	return answer(ctx, a, "DetachDisk", req, func() (*pb.DetachDiskResponse, error) {
		if vm := a.vms[req.VmId]; vm != nil {
			for i, d := range vm.Disks {
				if d == req.Device {
					vm.Disks = append(vm.Disks[:i], vm.Disks[i+1:]...)
					break
				}
			}
		}
		return &pb.DetachDiskResponse{Success: true, OperationId: req.OperationId,
			StorageDisposition: pb.DiskStorageDisposition_DISK_STORAGE_DISPOSITION_ABSENT}, nil
	})
}

func (a *Agent) CreateStorageVolume(ctx context.Context, req *pb.CreateStorageVolumeRequest) (*pb.CreateStorageVolumeResponse, error) {
	return answer(ctx, a, "CreateStorageVolume", req, func() (*pb.CreateStorageVolumeResponse, error) {
		return &pb.CreateStorageVolumeResponse{Success: true, Path: strings.TrimSuffix(req.PoolPath, "/") + "/" + req.Name, ActualSizeBytes: req.SizeGb << 30}, nil
	})
}

func (a *Agent) DeleteStorageVolume(ctx context.Context, req *pb.DeleteStorageVolumeRequest) (*pb.DeleteStorageVolumeResponse, error) {
	return answer(ctx, a, "DeleteStorageVolume", req, func() (*pb.DeleteStorageVolumeResponse, error) {
		return &pb.DeleteStorageVolumeResponse{Success: true}, nil
	})
}

func (a *Agent) SyncTemplate(ctx context.Context, req *pb.SyncTemplateRequest) (*pb.SyncTemplateResponse, error) {
	return answer(ctx, a, "SyncTemplate", req, func() (*pb.SyncTemplateResponse, error) {
		name := req.ImageUrl[strings.LastIndex(req.ImageUrl, "/")+1:]
		return &pb.SyncTemplateResponse{Success: true, LocalPath: "/var/lib/maburvm/templates/" + name, SizeBytes: 1 << 30, Checksum: req.ExpectedChecksum}, nil
	})
}

func (a *Agent) ImportDisk(ctx context.Context, req *pb.DiskImportRequest) (*pb.DiskImportResponse, error) {
	return answer(ctx, a, "ImportDisk", req, func() (*pb.DiskImportResponse, error) {
		return &pb.DiskImportResponse{Success: true}, nil
	})
}

// --- Network ------------------------------------------------------------------

func (a *Agent) ApplyNetworkConfig(ctx context.Context, req *pb.NetworkConfigRequest) (*pb.NetworkConfigResponse, error) {
	return answer(ctx, a, "ApplyNetworkConfig", req, func() (*pb.NetworkConfigResponse, error) {
		return &pb.NetworkConfigResponse{Success: true, AppliedInterfaces: req.GetConfig().GetInterfaces()}, nil
	})
}

func (a *Agent) DefineNetwork(ctx context.Context, req *pb.DefineNetworkRequest) (*pb.DefineNetworkResponse, error) {
	return answer(ctx, a, "DefineNetwork", req, func() (*pb.DefineNetworkResponse, error) {
		bridge := req.Bridge
		if bridge == "" {
			bridge = "mbr-" + req.Name
		}
		return &pb.DefineNetworkResponse{Success: true, Bridge: bridge}, nil
	})
}

func (a *Agent) UndefineNetwork(ctx context.Context, req *pb.UndefineNetworkRequest) (*pb.UndefineNetworkResponse, error) {
	return answer(ctx, a, "UndefineNetwork", req, func() (*pb.UndefineNetworkResponse, error) {
		return &pb.UndefineNetworkResponse{Success: true}, nil
	})
}

// ProbeIPs finds every address free.
func (a *Agent) ProbeIPs(ctx context.Context, req *pb.ProbeIPsRequest) (*pb.ProbeIPsResponse, error) {
	return answer(ctx, a, "ProbeIPs", req, func() (*pb.ProbeIPsResponse, error) {
		return &pb.ProbeIPsResponse{Checked: req.IpAddresses}, nil
	})
}

func (a *Agent) ConfigureFloatingIP(ctx context.Context, req *pb.FloatingIPRequest) (*pb.FloatingIPResponse, error) {
	return answer(ctx, a, "ConfigureFloatingIP", req, func() (*pb.FloatingIPResponse, error) {
		return &pb.FloatingIPResponse{Success: true}, nil
	})
}

func (a *Agent) ConfigureVPC(ctx context.Context, req *pb.VPCRequest) (*pb.VPCResponse, error) {
	return answer(ctx, a, "ConfigureVPC", req, func() (*pb.VPCResponse, error) {
		return &pb.VPCResponse{Success: true, Bridge: "vpc-" + req.VpcId, RouterNetns: "vpcr-" + req.VpcId}, nil
	})
}

func (a *Agent) SetQuarantine(ctx context.Context, req *pb.SetQuarantineRequest) (*pb.SetQuarantineResponse, error) {
	return answer(ctx, a, "SetQuarantine", req, func() (*pb.SetQuarantineResponse, error) {
		resp := &pb.SetQuarantineResponse{Success: true}
		if req.Quarantined {
			resp.QuarantinedMacs = []string{req.Mac}
		}
		return resp, nil
	})
}

func (a *Agent) GetGuestConnections(ctx context.Context, req *pb.GetGuestConnectionsRequest) (*pb.GetGuestConnectionsResponse, error) {
	return answer(ctx, a, "GetGuestConnections", req, func() (*pb.GetGuestConnectionsResponse, error) {
		return &pb.GetGuestConnectionsResponse{Success: true}, nil
	})
}

// --- Node ---------------------------------------------------------------------

func (a *Agent) RegisterNode(ctx context.Context, req *pb.RegisterNodeRequest) (*pb.RegisterNodeResponse, error) {
	return answer(ctx, a, "RegisterNode", req, func() (*pb.RegisterNodeResponse, error) {
		return &pb.RegisterNodeResponse{AuthToken: a.Token}, nil
	})
}

func (a *Agent) Heartbeat(stream grpc.BidiStreamingServer[pb.HeartbeatRequest, pb.HeartbeatResponse]) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			return nil
		}
		if _, err := answer(stream.Context(), a, "Heartbeat", req, func() (*pb.HeartbeatRequest, error) { return req, nil }); err != nil {
			return err
		}
		if err := stream.Send(&pb.HeartbeatResponse{Timestamp: timestamppb.Now(), Acknowledged: true}); err != nil {
			return err
		}
	}
}

const (
	memTotal  = 64 << 30
	diskTotal = 2 << 40
)

func (a *Agent) running() int32 {
	var n int32
	for _, vm := range a.vms {
		if vm.State == pb.VMState_VM_STATE_RUNNING {
			n++
		}
	}
	return n
}

func (a *Agent) GetNodeInfo(ctx context.Context, req *pb.GetNodeInfoRequest) (*pb.GetNodeInfoResponse, error) {
	return answer(ctx, a, "GetNodeInfo", req, func() (*pb.GetNodeInfoResponse, error) {
		features := a.Features
		if features == nil {
			features = Features
		}
		return &pb.GetNodeInfoResponse{
			Success:          true,
			OsInfo:           &pb.OSInfo{OsName: "Ubuntu", OsVersion: "24.04", KernelVersion: "6.8.0", Architecture: "x86_64"},
			CpuInfo:          &pb.CPUInfo{Model: "Fake CPU", Cores: 16, Threads: 32, Architecture: "x86_64"},
			MemoryTotalBytes: memTotal,
			DiskTotalBytes:   diskTotal,
			LibvirtVersion:   "10.0.0",
			AgentVersion:     "fake",
			AgentCommit:      "fake",
			Features:         features,
			CpuPercent:       4,
			MemoryUsedBytes:  8 << 30,
			RunningVmCount:   a.running(),
			AvailableCpus:    32,
		}, nil
	})
}

func (a *Agent) GetLiveMetrics(ctx context.Context, req *pb.GetLiveMetricsRequest) (*pb.GetLiveMetricsResponse, error) {
	return answer(ctx, a, "GetLiveMetrics", req, func() (*pb.GetLiveMetricsResponse, error) {
		return &pb.GetLiveMetricsResponse{
			Success: true, CpuPercent: 4, MemoryUsedBytes: 8 << 30, MemoryTotalBytes: memTotal, MemoryUsedPercent: 12.5,
			DiskUsedBytes: 100 << 30, DiskTotalBytes: diskTotal, DiskUsedPercent: 5, LoadAvg_1: 0.4, LoadAvg_5: 0.3, LoadAvg_15: 0.2,
			RunningVmCount: a.running(), AvailableCpus: 32, AvailableMemoryMb: 56 << 10, AvailableDiskGb: 1900,
			ConntrackCount: 120, ConntrackMax: 262144, FirewallEnforcement: "enforced",
		}, nil
	})
}

func (a *Agent) GetStorageReport(ctx context.Context, req *pb.GetStorageReportRequest) (*pb.GetStorageReportResponse, error) {
	return answer(ctx, a, "GetStorageReport", req, func() (*pb.GetStorageReportResponse, error) {
		resp := &pb.GetStorageReportResponse{Success: true}
		for _, p := range req.Paths {
			resp.Pools = append(resp.Pools, &pb.PoolCapacity{Path: p, Exists: true, TotalBytes: diskTotal, UsedBytes: 100 << 30, AvailableBytes: diskTotal - 100<<30, Filesystem: "ext4"})
		}
		for _, p := range req.LvmPools {
			resp.LvmPools = append(resp.LvmPools, &pb.LVMPoolCapacity{Vg: p.Vg, ThinPool: p.ThinPool, Exists: true, TotalBytes: 1 << 40, AvailableBytes: 1 << 39})
		}
		return resp, nil
	})
}

func (a *Agent) GetPreflight(ctx context.Context, req *pb.PreflightRequest) (*pb.PreflightResponse, error) {
	return answer(ctx, a, "GetPreflight", req, func() (*pb.PreflightResponse, error) {
		return &pb.PreflightResponse{Success: true, Checks: []*pb.PreflightCheck{
			{Id: "kvm", Name: "Hardware virtualisation", Status: "pass"},
			{Id: "libvirt", Name: "libvirt", Status: "pass"},
		}}, nil
	})
}

func (a *Agent) GetExportProgress(ctx context.Context, req *pb.GetExportProgressRequest) (*pb.GetExportProgressResponse, error) {
	return answer(ctx, a, "GetExportProgress", req, func() (*pb.GetExportProgressResponse, error) {
		return &pb.GetExportProgressResponse{Success: true}, nil
	})
}

func (a *Agent) ScanVMs(ctx context.Context, req *pb.ScanVMsRequest) (*pb.ScanVMsResponse, error) {
	return answer(ctx, a, "ScanVMs", req, func() (*pb.ScanVMsResponse, error) {
		resp := &pb.ScanVMsResponse{Success: true}
		for _, id := range sortedKeys(a.vms) {
			vm := a.vms[id]
			state := "shutoff"
			if vm.State == pb.VMState_VM_STATE_RUNNING {
				state = "running"
			}
			r := vm.Config.GetResources()
			resp.Vms = append(resp.Vms, &pb.ScannedVM{Uuid: id, Name: id, Cpu: r.GetVcpus(), MemoryMb: r.GetMemoryMb(), Status: state})
		}
		resp.TotalFound = int32(len(resp.Vms))
		return resp, nil
	})
}

func sortedKeys(m map[string]*VM) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// --- Moving VMs between nodes ---------------------------------------------------
// Enough for a move to run to the end; the panel's migration tests drive the
// protocol step by step with their own node.

func (a *Agent) GetMigrationInfo(ctx context.Context, req *pb.GetMigrationInfoRequest) (*pb.GetMigrationInfoResponse, error) {
	return answer(ctx, a, "GetMigrationInfo", req, func() (*pb.GetMigrationInfoResponse, error) {
		resp := &pb.GetMigrationInfoResponse{Success: true, MigrationPublicKey: "ssh-ed25519 AAAAfake", CpuVendor: "Fake", CpuModel: "Fake CPU",
			LibvirtVersion: "10.0.0", LinkOk: true}
		if vm := a.vms[req.VmId]; vm != nil {
			resp.VmFound, resp.VmState, resp.SnapshotCount = true, strings.ToLower(strings.TrimPrefix(vm.State.String(), "VM_STATE_")), int32(len(vm.Snapshots))
			resp.Disks = []*pb.MigrationDiskInfo{{Target: "vda", Device: "disk", Path: "/vm/" + vm.ID + ".qcow2", Format: "qcow2", SourceType: "file",
				VirtualSizeBytes: vm.Config.GetResources().GetDiskGb() << 30, FileSizeBytes: 1 << 30}}
		}
		return resp, nil
	})
}

func (a *Agent) SetMigrationPeers(ctx context.Context, req *pb.SetMigrationPeersRequest) (*pb.SetMigrationPeersResponse, error) {
	return answer(ctx, a, "SetMigrationPeers", req, func() (*pb.SetMigrationPeersResponse, error) {
		return &pb.SetMigrationPeersResponse{Success: true}, nil
	})
}

func (a *Agent) PrepareMigrationTarget(ctx context.Context, req *pb.PrepareMigrationTargetRequest) (*pb.PrepareMigrationTargetResponse, error) {
	return answer(ctx, a, "PrepareMigrationTarget", req, func() (*pb.PrepareMigrationTargetResponse, error) {
		paths := map[string]string{}
		for _, d := range req.Disks {
			paths[d.Target] = strings.TrimSuffix(req.PoolPath, "/") + "/" + d.FileName
		}
		return &pb.PrepareMigrationTargetResponse{Success: true, Paths: paths}, nil
	})
}

func (a *Agent) MigrateVM(ctx context.Context, req *pb.MigrateVMRequest) (*pb.MigrateVMResponse, error) {
	return answer(ctx, a, "MigrateVM", req, func() (*pb.MigrateVMResponse, error) {
		return &pb.MigrateVMResponse{Success: true}, nil
	})
}

func (a *Agent) PullMigratedVM(ctx context.Context, req *pb.PullMigratedVMRequest) (*pb.PullMigratedVMResponse, error) {
	return answer(ctx, a, "PullMigratedVM", req, func() (*pb.PullMigratedVMResponse, error) {
		a.vms[req.VmId] = &VM{ID: req.VmId, State: pb.VMState_VM_STATE_STOPPED}
		return &pb.PullMigratedVMResponse{Success: true}, nil
	})
}

func (a *Agent) GetMigrationProgress(ctx context.Context, req *pb.GetMigrationProgressRequest) (*pb.GetMigrationProgressResponse, error) {
	return answer(ctx, a, "GetMigrationProgress", req, func() (*pb.GetMigrationProgressResponse, error) {
		return &pb.GetMigrationProgressResponse{State: "completed", Phase: "done", TotalBytes: 1 << 30, ProcessedBytes: 1 << 30}, nil
	})
}

func (a *Agent) AbortMigration(ctx context.Context, req *pb.AbortMigrationRequest) (*pb.AbortMigrationResponse, error) {
	return answer(ctx, a, "AbortMigration", req, func() (*pb.AbortMigrationResponse, error) {
		return &pb.AbortMigrationResponse{Success: true}, nil
	})
}

func (a *Agent) FinishMigrationTarget(ctx context.Context, req *pb.FinishMigrationTargetRequest) (*pb.FinishMigrationTargetResponse, error) {
	return answer(ctx, a, "FinishMigrationTarget", req, func() (*pb.FinishMigrationTargetResponse, error) {
		resp := &pb.FinishMigrationTargetResponse{Success: true}
		if vm := a.vms[req.VmId]; vm != nil {
			if !req.Commit {
				delete(a.vms, req.VmId)
			} else {
				resp.VmState = strings.ToLower(strings.TrimPrefix(vm.State.String(), "VM_STATE_"))
			}
		}
		return resp, nil
	})
}

func (a *Agent) FinishMigrationSource(ctx context.Context, req *pb.FinishMigrationSourceRequest) (*pb.FinishMigrationSourceResponse, error) {
	return answer(ctx, a, "FinishMigrationSource", req, func() (*pb.FinishMigrationSourceResponse, error) {
		delete(a.vms, req.VmId)
		return &pb.FinishMigrationSourceResponse{Success: true}, nil
	})
}
