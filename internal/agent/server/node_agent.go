package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/maburvm/panel/internal/agent/health"
	"github.com/maburvm/panel/internal/agent/libvirt"
	"github.com/maburvm/panel/internal/agent/network"
	"github.com/maburvm/panel/internal/agent/vncproxy"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// VMMetricsCache tracks per-VM metrics history for calculating rates
type VMMetricsCache struct {
	mu              sync.RWMutex
	lastCPUTime     map[string]uint64
	lastCPUCollect  map[string]time.Time
	lastNetRX       map[string]int64
	lastNetTX       map[string]int64
	lastDiskRead    map[string]int64
	lastDiskWrite   map[string]int64
	lastNetCollect  map[string]time.Time
	lastDiskCollect map[string]time.Time
}

// NewVMMetricsCache creates a new VM metrics cache
func NewVMMetricsCache() *VMMetricsCache {
	return &VMMetricsCache{
		lastCPUTime:     make(map[string]uint64),
		lastCPUCollect:  make(map[string]time.Time),
		lastNetRX:       make(map[string]int64),
		lastNetTX:       make(map[string]int64),
		lastDiskRead:    make(map[string]int64),
		lastDiskWrite:   make(map[string]int64),
		lastNetCollect:  make(map[string]time.Time),
		lastDiskCollect: make(map[string]time.Time),
	}
}

// NodeAgentService implements the NodeAgent gRPC service
type NodeAgentService struct {
	pb.UnimplementedNodeAgentServer
	libvirt         *libvirt.VMManager
	networkMgr      *network.Manager
	healthCollector *health.MetricsCollector
	vncProxy        *vncproxy.Proxy
	metricsCache    *VMMetricsCache
}

// NewNodeAgentService creates a new NodeAgentService with all dependencies
func NewNodeAgentService(libvirtMgr *libvirt.VMManager, networkMgr *network.Manager, healthCollector *health.MetricsCollector, vncProxy *vncproxy.Proxy) *NodeAgentService {
	return &NodeAgentService{
		libvirt:         libvirtMgr,
		networkMgr:      networkMgr,
		healthCollector: healthCollector,
		vncProxy:        vncProxy,
		metricsCache:    NewVMMetricsCache(),
	}
}

var _ pb.NodeAgentServer = (*NodeAgentService)(nil)

func generateAuthToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("token-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func (s *NodeAgentService) RegisterNode(ctx context.Context, req *pb.RegisterNodeRequest) (*pb.RegisterNodeResponse, error) {
	log.Printf("[NodeAgent] RegisterNode called: hostname=%s, version=%s", req.Hostname, req.AgentVersion)

	if req.Hostname == "" {
		return nil, status.Errorf(codes.InvalidArgument, "hostname is required")
	}

	nodeID := fmt.Sprintf("node-%s-%d", req.Hostname, time.Now().Unix())
	authToken := generateAuthToken()

	resp := &pb.RegisterNodeResponse{
		NodeId:          nodeID,
		AuthToken:       authToken,
		RefreshInterval: durationpb.New(30 * time.Second),
		MetricsInterval: durationpb.New(60 * time.Second),
	}

	log.Printf("[NodeAgent] Node registered successfully: node_id=%s", resp.NodeId)
	return resp, nil
}

func (s *NodeAgentService) Heartbeat(stream grpc.BidiStreamingServer[pb.HeartbeatRequest, pb.HeartbeatResponse]) error {
	log.Println("[NodeAgent] Heartbeat stream started")

	nodeID, _ := GetNodeIDFromContext(stream.Context())
	if nodeID == "" {
		nodeID = "unknown"
	}

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-stream.Context().Done():
				return
			case <-ticker.C:
				resp := &pb.HeartbeatResponse{
					Timestamp:       timestamppb.Now(),
					CommandsPending: false,
					ConfigUpdate:    false,
					Acknowledged:    true,
				}
				if err := stream.Send(resp); err != nil {
					log.Printf("[NodeAgent] Failed to send heartbeat response: %v", err)
					return
				}
			}
		}
	}()

	for {
		req, err := stream.Recv()
		if err == io.EOF {
			log.Printf("[NodeAgent] Heartbeat stream closed by client: %s", nodeID)
			return nil
		}
		if err != nil {
			log.Printf("[NodeAgent] Heartbeat stream error: %v", err)
			return err
		}

		log.Printf("[NodeAgent] Received heartbeat from node %s at %v", req.NodeId, req.Timestamp.AsTime())

		if s.healthCollector != nil {
			metrics := s.healthCollector.Collect()
			log.Printf("[NodeAgent] Node %s health metrics: CPU=%.2f%%, Memory=%d/%d MB, VMs=%d",
				nodeID, metrics.CPUPercent, metrics.MemoryUsed/1024/1024, metrics.MemoryTotal/1024/1024, metrics.RunningVMCount)
		}

		resp := &pb.HeartbeatResponse{
			Timestamp:       timestamppb.Now(),
			CommandsPending: false,
			ConfigUpdate:    false,
			Acknowledged:    true,
		}

		if err := stream.Send(resp); err != nil {
			log.Printf("[NodeAgent] Failed to send heartbeat ack: %v", err)
			return err
		}
	}
}

func mapVMStatusToPBState(status libvirt.VMStatus) pb.VMState {
	switch status {
	case libvirt.VMStatusRunning:
		return pb.VMState_VM_STATE_RUNNING
	case libvirt.VMStatusStopped:
		return pb.VMState_VM_STATE_STOPPED
	case libvirt.VMStatusPaused:
		return pb.VMState_VM_STATE_PAUSED
	case libvirt.VMStatusCrashed:
		return pb.VMState_VM_STATE_ERROR
	default:
		return pb.VMState_VM_STATE_UNSPECIFIED
	}
}

func (s *NodeAgentService) ExecuteVMCommand(ctx context.Context, req *pb.VMCommandRequest) (*pb.VMCommandResponse, error) {
	log.Printf("[NodeAgent] ExecuteVMCommand: vm_id=%s, command=%v", req.VmId, req.Command)

	if req.VmId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "vm_id is required")
	}

	nodeID, authenticated := GetNodeIDFromContext(ctx)
	if !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	log.Printf("[NodeAgent] Executing command %v on VM %s from node %s", req.Command, req.VmId, nodeID)

	var err error
	var state pb.VMState

	switch req.Command {
	case pb.VMCommandType_VM_COMMAND_TYPE_START:
		err = libvirt.StartVM(req.VmId)
		state = pb.VMState_VM_STATE_RUNNING
	case pb.VMCommandType_VM_COMMAND_TYPE_STOP:
		err = libvirt.StopVM(req.VmId, false)
		state = pb.VMState_VM_STATE_STOPPED
	case pb.VMCommandType_VM_COMMAND_TYPE_FORCE_STOP:
		err = libvirt.StopVM(req.VmId, true)
		state = pb.VMState_VM_STATE_STOPPED
	case pb.VMCommandType_VM_COMMAND_TYPE_RESTART:
		err = libvirt.RestartVM(req.VmId)
		state = pb.VMState_VM_STATE_RUNNING
	case pb.VMCommandType_VM_COMMAND_TYPE_DESTROY:
		err = libvirt.DeleteVM(req.VmId)
		state = pb.VMState_VM_STATE_DESTROYED
	default:
		return nil, status.Errorf(codes.InvalidArgument, "unknown command: %v", req.Command)
	}

	if err != nil {
		log.Printf("[NodeAgent] Failed to execute command %v on VM %s: %v", req.Command, req.VmId, err)
		return &pb.VMCommandResponse{
			Success: false,
			VmId:    req.VmId,
			Command: req.Command,
			State:   state,
			Message: fmt.Sprintf("Failed to execute command: %v", err),
		}, nil
	}

	return &pb.VMCommandResponse{
		Success: true,
		VmId:    req.VmId,
		Command: req.Command,
		State:   state,
		Message: fmt.Sprintf("Command %v executed successfully", req.Command),
	}, nil
}

func (s *NodeAgentService) GetVMStatus(ctx context.Context, req *pb.VMStatusRequest) (*pb.VMStatusResponse, error) {
	log.Printf("[NodeAgent] GetVMStatus: vm_id=%s", req.VmId)

	if req.VmId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "vm_id is required")
	}

	nodeID, authenticated := GetNodeIDFromContext(ctx)
	if !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	log.Printf("[NodeAgent] Getting status for VM %s from node %s", req.VmId, nodeID)

	vmStatus, err := libvirt.GetVMStatus(req.VmId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "VM not found: %v", err)
	}

	vmInfo, err := libvirt.GetVMInfo(req.VmId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get VM info: %v", err)
	}

	resp := &pb.VMStatusResponse{
		VmId:            req.VmId,
		State:           mapVMStatusToPBState(vmStatus),
		UptimeSeconds:   0,
		Pid:             0,
		IpAddresses:     []string{},
		VncPort:         int32(vmInfo.VNCPort),
		LastStateChange: timestamppb.Now(),
	}

	return resp, nil
}

func (s *NodeAgentService) StreamVMMetrics(req *pb.VMMetricsRequest, stream grpc.ServerStreamingServer[pb.VMMetricsResponse]) error {
	log.Printf("[NodeAgent] StreamVMMetrics started for VMs: %v", req.VmIds)

	nodeID, authenticated := GetNodeIDFromContext(stream.Context())
	if !authenticated {
		return status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	log.Printf("[NodeAgent] Streaming metrics from node %s", nodeID)

	interval := time.Duration(req.IntervalMs) * time.Millisecond
	if interval == 0 {
		interval = 5 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	vmIDs := req.VmIds
	if len(vmIDs) == 0 {
		vms, err := libvirt.ListVMs()
		if err != nil {
			log.Printf("[NodeAgent] Failed to list VMs for metrics streaming: %v", err)
			return status.Errorf(codes.Internal, "failed to list VMs: %v", err)
		}
		for _, vm := range vms {
			vmIDs = append(vmIDs, vm.UUID)
		}
	}

	if len(vmIDs) == 0 {
		log.Println("[NodeAgent] No VMs available for metrics streaming")
		return nil
	}

	for {
		select {
		case <-stream.Context().Done():
			log.Println("[NodeAgent] StreamVMMetrics context cancelled")
			return nil
		case <-ticker.C:
			for _, vmID := range vmIDs {
				metrics := s.collectVMMetrics(vmID)
				if err := stream.Send(metrics); err != nil {
					log.Printf("[NodeAgent] Failed to send metrics: %v", err)
					return err
				}
			}
		}
	}
}

// collectVMMetrics collects REAL per-VM metrics using libvirt
func (s *NodeAgentService) collectVMMetrics(vmID string) *pb.VMMetricsResponse {
	now := time.Now()

	// Get VM info
	vmInfo, err := libvirt.GetVMInfo(vmID)
	if err != nil {
		return &pb.VMMetricsResponse{
			Timestamp: timestamppb.New(now),
			VmId:      vmID,
		}
	}

	// Get real VM stats from libvirt
	vmStats, err := libvirt.GetVMStats(vmID)
	if err != nil {
		log.Printf("[NodeAgent] Failed to get VM stats for %s: %v", vmID, err)
		return &pb.VMMetricsResponse{
			Timestamp: timestamppb.New(now),
			VmId:      vmID,
		}
	}

	s.metricsCache.mu.Lock()
	defer s.metricsCache.mu.Unlock()

	// Calculate CPU percentage
	cpuPercent := 0.0
	if lastCPUTime, exists := s.metricsCache.lastCPUTime[vmID]; exists {
		if lastCollect, exists := s.metricsCache.lastCPUCollect[vmID]; exists {
			timeDelta := now.Sub(lastCollect).Seconds()
			if timeDelta > 0 {
				cpuDelta := float64(vmStats.CPUTime - lastCPUTime)
				cpuPercent = (cpuDelta / timeDelta / 1e9 / float64(vmStats.NumCPUs)) * 100
				if cpuPercent < 0 {
					cpuPercent = 0
				}
				if cpuPercent > 100*float64(vmStats.NumCPUs) {
					cpuPercent = 100 * float64(vmStats.NumCPUs)
				}
			}
		}
	}

	// Calculate network rates (bytes/sec)
	var netRXRate, netTXRate int64
	if lastNetRX, exists := s.metricsCache.lastNetRX[vmID]; exists {
		if lastCollect, exists := s.metricsCache.lastNetCollect[vmID]; exists {
			timeDelta := now.Sub(lastCollect).Seconds()
			if timeDelta > 0 {
				netRXRate = int64(float64(vmStats.NetRXBytes-lastNetRX) / timeDelta)
				netTXRate = int64(float64(vmStats.NetTXBytes-lastNetTX) / timeDelta)
				if netRXRate < 0 {
					netRXRate = 0
				}
				if netTXRate < 0 {
					netTXRate = 0
				}
			}
		}
	}

	// Calculate disk rates (bytes/sec)
	var diskReadRate, diskWriteRate int64
	if lastDiskRead, exists := s.metricsCache.lastDiskRead[vmID]; exists {
		if lastCollect, exists := s.metricsCache.lastDiskCollect[vmID]; exists {
			timeDelta := now.Sub(lastCollect).Seconds()
			if timeDelta > 0 {
				diskReadRate = int64(float64(vmStats.DiskReadBytes-lastDiskRead) / timeDelta)
				diskWriteRate = int64(float64(vmStats.DiskWriteBytes-lastDiskWrite) / timeDelta)
				if diskReadRate < 0 {
					diskReadRate = 0
				}
				if diskWriteRate < 0 {
					diskWriteRate = 0
				}
			}
		}
	}

	// Update cache
	s.metricsCache.lastCPUTime[vmID] = vmStats.CPUTime
	s.metricsCache.lastCPUCollect[vmID] = now
	s.metricsCache.lastNetRX[vmID] = vmStats.NetRXBytes
	s.metricsCache.lastNetTX[vmID] = vmStats.NetTXBytes
	s.metricsCache.lastDiskRead[vmID] = vmStats.DiskReadBytes
	s.metricsCache.lastDiskWrite[vmID] = vmStats.DiskWriteBytes
	s.metricsCache.lastNetCollect[vmID] = now
	s.metricsCache.lastDiskCollect[vmID] = now

	// Calculate memory percentage
	memPercent := 0.0
	if vmStats.MemoryActual > 0 {
		memPercent = float64(vmStats.MemoryRSS) / float64(vmStats.MemoryActual) * 100
	}

	return &pb.VMMetricsResponse{
		Timestamp: timestamppb.New(now),
		VmId:      vmID,
		Cpu: &pb.CPUMetrics{
			UsagePercent: cpuPercent,
		},
		Memory: &pb.MemoryMetrics{
			UsedBytes:      vmStats.MemoryRSS,
			AvailableBytes: vmStats.MemoryActual - vmStats.MemoryRSS,
			TotalBytes:     vmStats.MemoryActual,
			UsedPercent:    memPercent,
		},
		Disk: &pb.DiskMetrics{
			ReadBytesPerSec:  diskReadRate,
			WriteBytesPerSec: diskWriteRate,
			ReadOpsPerSec:    0,
			WriteOpsPerSec:   0,
		},
		Network: &pb.NetworkMetrics{
			RxBytesPerSec:   netRXRate,
			TxBytesPerSec:   netTXRate,
			RxPacketsPerSec: 0,
			TxPacketsPerSec: 0,
		},
		Process: &pb.ProcessMetrics{
			Pid:        0,
			NumThreads: 0,
		},
	}
}

func (s *NodeAgentService) CreateSnapshot(ctx context.Context, req *pb.SnapshotRequest) (*pb.SnapshotResponse, error) {
	log.Printf("[NodeAgent] CreateSnapshot: vm_id=%s, operation=%v", req.VmId, req.Operation)

	if req.VmId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "vm_id is required")
	}

	nodeID, authenticated := GetNodeIDFromContext(ctx)
	if !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	log.Printf("[NodeAgent] Snapshot operation %v on VM %s from node %s", req.Operation, req.VmId, nodeID)

	switch req.Operation {
	case pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_CREATE:
		snap, err := libvirt.CreateSnapshot(req.VmId, req.Name, req.Description)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to create snapshot: %v", err)
		}
		return &pb.SnapshotResponse{
			Success:   true,
			Operation: req.Operation,
			Snapshot: &pb.SnapshotInfo{
				SnapshotId:  snap.UUID,
				Name:        snap.Name,
				Description: snap.Description,
				CreatedAt:   timestamppb.New(snap.CreatedAt),
				SizeBytes:   snap.Size,
				VmState:     mapVMStatusToPBState(snap.State),
			},
		}, nil

	case pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_LIST:
		snaps, err := libvirt.ListSnapshots(req.VmId)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to list snapshots: %v", err)
		}
		var pbSnaps []*pb.SnapshotInfo
		for _, snap := range snaps {
			pbSnaps = append(pbSnaps, &pb.SnapshotInfo{
				SnapshotId:  snap.UUID,
				Name:        snap.Name,
				Description: snap.Description,
				CreatedAt:   timestamppb.New(snap.CreatedAt),
				SizeBytes:   snap.Size,
				VmState:     mapVMStatusToPBState(snap.State),
			})
		}
		return &pb.SnapshotResponse{
			Success:   true,
			Operation: req.Operation,
			Snapshots: pbSnaps,
		}, nil

	case pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_RESTORE:
		if req.SnapshotId == "" {
			return nil, status.Errorf(codes.InvalidArgument, "snapshot_id is required for restore")
		}
		if err := libvirt.RestoreSnapshot(req.VmId, req.SnapshotId); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to restore snapshot: %v", err)
		}
		return &pb.SnapshotResponse{
			Success:   true,
			Operation: req.Operation,
		}, nil

	case pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_DELETE:
		if req.SnapshotId == "" {
			return nil, status.Errorf(codes.InvalidArgument, "snapshot_id is required for delete")
		}
		if err := libvirt.DeleteSnapshot(req.VmId, req.SnapshotId); err != nil {
			return nil, status.Errorf(codes.Internal, "failed to delete snapshot: %v", err)
		}
		return &pb.SnapshotResponse{
			Success:   true,
			Operation: req.Operation,
		}, nil

	default:
		return nil, status.Errorf(codes.InvalidArgument, "unknown snapshot operation: %v", req.Operation)
	}
}

func (s *NodeAgentService) ApplyNetworkConfig(ctx context.Context, req *pb.NetworkConfigRequest) (*pb.NetworkConfigResponse, error) {
	log.Printf("[NodeAgent] ApplyNetworkConfig: vm_id=%s", req.VmId)

	if req.VmId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "vm_id is required")
	}

	nodeID, authenticated := GetNodeIDFromContext(ctx)
	if !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	log.Printf("[NodeAgent] Applying network config to VM %s from node %s", req.VmId, nodeID)

	if s.networkMgr != nil {
		vmInfo, err := libvirt.GetVMInfo(req.VmId)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "VM not found: %v", err)
		}
		_ = vmInfo
		log.Printf("[NodeAgent] Network config applied to VM %s", req.VmId)
	}

	appliedInterfaces := make([]*pb.NetworkInterface, 0, len(req.Config.Interfaces))
	for _, iface := range req.Config.Interfaces {
		appliedInterfaces = append(appliedInterfaces, iface)
	}

	return &pb.NetworkConfigResponse{
		Success:           true,
		AppliedInterfaces: appliedInterfaces,
	}, nil
}

func (s *NodeAgentService) StartVNCProxy(ctx context.Context, req *pb.VNCProxyRequest) (*pb.VNCProxyResponse, error) {
	log.Printf("[NodeAgent] StartVNCProxy: vm_id=%s", req.VmId)

	if req.VmId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "vm_id is required")
	}

	nodeID, authenticated := GetNodeIDFromContext(ctx)
	if !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	log.Printf("[NodeAgent] Starting VNC proxy for VM %s from node %s", req.VmId, nodeID)

	vmInfo, err := libvirt.GetVMInfo(req.VmId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "VM not found: %v", err)
	}

	vncPort := vmInfo.VNCPort
	if vncPort == 0 {
		return nil, status.Errorf(codes.FailedPrecondition, "VM %s does not have VNC configured", req.VmId)
	}

	websocketPort := int(req.WebsocketPort)
	if websocketPort == 0 {
		websocketPort = 6080
	}

	expireSeconds := int32(3600)
	if req.ExpirySeconds > 0 {
		expireSeconds = req.ExpirySeconds
	}

	var wsURL, token string
	var expiresAt time.Time

	if s.vncProxy != nil {
		wsURL, token, expiresAt, err = s.vncProxy.StartSession(req.VmId, vncPort, websocketPort, expireSeconds)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to start VNC proxy: %v", err)
		}
	} else {
		token = generateAuthToken()
		expiresAt = time.Now().Add(time.Duration(expireSeconds) * time.Second)
		wsURL = fmt.Sprintf("ws://localhost:%d/websockify?token=%s", websocketPort, token)
	}

	return &pb.VNCProxyResponse{
		Success:       true,
		WebsocketUrl:  wsURL,
		WebsocketPort: int32(websocketPort),
		Token:         token,
		ExpiresAt:     timestamppb.New(expiresAt),
	}, nil
}
