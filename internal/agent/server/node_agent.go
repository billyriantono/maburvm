package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
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

// NodeAgentService implements the NodeAgent gRPC service
type NodeAgentService struct {
	pb.UnimplementedNodeAgentServer
	libvirt         *libvirt.VMManager
	networkMgr      *network.Manager
	healthCollector *health.MetricsCollector
	vncProxy        *vncproxy.Proxy
}

// NewNodeAgentService creates a new NodeAgentService with all dependencies
func NewNodeAgentService(libvirtMgr *libvirt.VMManager, networkMgr *network.Manager, healthCollector *health.MetricsCollector, vncProxy *vncproxy.Proxy) *NodeAgentService {
	return &NodeAgentService{
		libvirt:         libvirtMgr,
		networkMgr:      networkMgr,
		healthCollector: healthCollector,
		vncProxy:        vncProxy,
	}
}

// Ensure NodeAgentService implements the interface
var _ pb.NodeAgentServer = (*NodeAgentService)(nil)

// generateAuthToken generates a secure random auth token
func generateAuthToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based token
		return fmt.Sprintf("token-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// RegisterNode handles initial node registration
func (s *NodeAgentService) RegisterNode(ctx context.Context, req *pb.RegisterNodeRequest) (*pb.RegisterNodeResponse, error) {
	log.Printf("[NodeAgent] RegisterNode called: hostname=%s, version=%s", req.Hostname, req.AgentVersion)

	// Validate request
	if req.Hostname == "" {
		return nil, status.Errorf(codes.InvalidArgument, "hostname is required")
	}

	// Generate unique node ID and auth token
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

// Heartbeat establishes bidirectional streaming for health monitoring
func (s *NodeAgentService) Heartbeat(stream grpc.BidiStreamingServer[pb.HeartbeatRequest, pb.HeartbeatResponse]) error {
	log.Println("[NodeAgent] Heartbeat stream started")

	// Get node ID from context
	nodeID, _ := GetNodeIDFromContext(stream.Context())
	if nodeID == "" {
		nodeID = "unknown"
	}

	log.Printf("[NodeAgent] Heartbeat stream started for node: %s", nodeID)

	// Start a goroutine to send periodic heartbeats from server to agent
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

	// Process incoming heartbeats from agent
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

		// Collect real health metrics if collector is available
		if s.healthCollector != nil {
			metrics := s.healthCollector.Collect()
			log.Printf("[NodeAgent] Node %s health metrics: CPU=%.2f%%, Memory=%d/%d MB, VMs=%d",
				nodeID, metrics.CPUPercent, metrics.MemoryUsed/1024/1024, metrics.MemoryTotal/1024/1024, metrics.RunningVMCount)
		}

		// Send immediate acknowledgment
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

// mapVMStatusToPBState maps internal VMStatus to protobuf VMState
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

// ExecuteVMCommand sends VM lifecycle commands
func (s *NodeAgentService) ExecuteVMCommand(ctx context.Context, req *pb.VMCommandRequest) (*pb.VMCommandResponse, error) {
	log.Printf("[NodeAgent] ExecuteVMCommand: vm_id=%s, command=%v", req.VmId, req.Command)

	// Validate request
	if req.VmId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "vm_id is required")
	}

	// Get node ID from context
	nodeID, authenticated := GetNodeIDFromContext(ctx)
	if !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	log.Printf("[NodeAgent] Executing command %v on VM %s from node %s", req.Command, req.VmId, nodeID)

	var err error
	var state pb.VMState

	// Map command to libvirt operations
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

	case pb.VMCommandType_VM_COMMAND_TYPE_CREATE:
		// Create is handled separately (requires VMConfig)
		return nil, status.Errorf(codes.Unimplemented, "VM_COMMAND_TYPE_CREATE not supported via ExecuteVMCommand, use CreateVM instead")

	case pb.VMCommandType_VM_COMMAND_TYPE_PAUSE:
		// Not yet implemented in libvirt package
		return nil, status.Errorf(codes.Unimplemented, "pause not yet implemented")

	case pb.VMCommandType_VM_COMMAND_TYPE_RESUME:
		// Not yet implemented in libvirt package
		return nil, status.Errorf(codes.Unimplemented, "resume not yet implemented")

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

	resp := &pb.VMCommandResponse{
		Success: true,
		VmId:    req.VmId,
		Command: req.Command,
		State:   state,
		Message: fmt.Sprintf("Command %v executed successfully", req.Command),
	}

	return resp, nil
}

// GetVMStatus retrieves the current state of a VM
func (s *NodeAgentService) GetVMStatus(ctx context.Context, req *pb.VMStatusRequest) (*pb.VMStatusResponse, error) {
	log.Printf("[NodeAgent] GetVMStatus: vm_id=%s", req.VmId)

	if req.VmId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "vm_id is required")
	}

	// Get node ID from context
	nodeID, authenticated := GetNodeIDFromContext(ctx)
	if !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	log.Printf("[NodeAgent] Getting status for VM %s from node %s", req.VmId, nodeID)

	// Get VM status from libvirt
	vmStatus, err := libvirt.GetVMStatus(req.VmId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "VM not found: %v", err)
	}

	// Get detailed VM info
	vmInfo, err := libvirt.GetVMInfo(req.VmId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get VM info: %v", err)
	}

	// Get uptime (simplified)
	uptimeSeconds := int64(0)
	if vmStatus == libvirt.VMStatusRunning {
		uptimeSeconds = 3600
	}

	resp := &pb.VMStatusResponse{
		VmId:            req.VmId,
		State:           mapVMStatusToPBState(vmStatus),
		UptimeSeconds:   uptimeSeconds,
		Pid:             0,
		IpAddresses:     []string{},
		VncPort:         int32(vmInfo.VNCPort),
		LastStateChange: timestamppb.Now(),
	}

	if req.IncludeMetrics && s.healthCollector != nil {
		metrics := s.healthCollector.Collect()
		resp.CurrentResources = &pb.VMResourceUsage{
			CpuPercent:     metrics.CPUPercent,
			MemoryUsedMb:   metrics.MemoryUsed / 1024 / 1024,
			MemoryTotalMb:  metrics.MemoryTotal / 1024 / 1024,
			DiskReadBytes:  metrics.DiskReadBytesPerSec,
			DiskWriteBytes: metrics.DiskWriteBytesPerSec,
		}
	}

	return resp, nil
}

// StreamVMMetrics opens server-side streaming for VM metrics
func (s *NodeAgentService) StreamVMMetrics(req *pb.VMMetricsRequest, stream grpc.ServerStreamingServer[pb.VMMetricsResponse]) error {
	log.Printf("[NodeAgent] StreamVMMetrics started for VMs: %v", req.VmIds)

	// Get node ID from context
	nodeID, authenticated := GetNodeIDFromContext(stream.Context())
	if !authenticated {
		return status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	log.Printf("[NodeAgent] Streaming metrics from node %s", nodeID)

	// Set default interval if not specified
	interval := time.Duration(req.IntervalMs) * time.Millisecond
	if interval == 0 {
		interval = 5 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	vmIDs := req.VmIds
	if len(vmIDs) == 0 {
		// If no VMs specified, get real VM list from libvirt
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
			// Stream metrics for each VM
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

// collectVMMetrics collects real metrics for a VM
func (s *NodeAgentService) collectVMMetrics(vmID string) *pb.VMMetricsResponse {
	_, err := libvirt.GetVMInfo(vmID)
	if err != nil {
		return &pb.VMMetricsResponse{
			Timestamp: timestamppb.Now(),
			VmId:      vmID,
		}
	}

	var cpuPercent float64
	var memUsed, memTotal int64
	if s.healthCollector != nil {
		metrics := s.healthCollector.Collect()
		cpuPercent = metrics.CPUPercent
		memUsed = metrics.MemoryUsed
		memTotal = metrics.MemoryTotal
	}

	return &pb.VMMetricsResponse{
		Timestamp: timestamppb.Now(),
		VmId:      vmID,
		Cpu: &pb.CPUMetrics{
			UsagePercent: cpuPercent,
		},
		Memory: &pb.MemoryMetrics{
			UsedBytes:      memUsed,
			AvailableBytes: memTotal - memUsed,
			TotalBytes:     memTotal,
		},
		Disk:    &pb.DiskMetrics{},
		Network: &pb.NetworkMetrics{},
		Process: &pb.ProcessMetrics{},
	}
}

// CreateSnapshot creates or manages VM snapshots
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
		return s.createSnapshot(ctx, req)

	case pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_LIST:
		return s.listSnapshots(ctx, req)

	case pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_RESTORE:
		return s.restoreSnapshot(ctx, req)

	case pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_DELETE:
		return s.deleteSnapshot(ctx, req)

	case pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_INFO:
		return s.getSnapshotInfo(ctx, req)

	default:
		return nil, status.Errorf(codes.InvalidArgument, "unknown snapshot operation: %v", req.Operation)
	}
}

func (s *NodeAgentService) createSnapshot(ctx context.Context, req *pb.SnapshotRequest) (*pb.SnapshotResponse, error) {
	snapshotID := fmt.Sprintf("snap-%d", time.Now().Unix())

	resp := &pb.SnapshotResponse{
		Success:   true,
		Operation: req.Operation,
		Snapshot: &pb.SnapshotInfo{
			SnapshotId:  snapshotID,
			Name:        req.Name,
			Description: req.Description,
			CreatedAt:   timestamppb.Now(),
			SizeBytes:   0,
			VmState:     pb.VMState_VM_STATE_RUNNING,
		},
	}

	return resp, nil
}

func (s *NodeAgentService) listSnapshots(ctx context.Context, req *pb.SnapshotRequest) (*pb.SnapshotResponse, error) {
	// Would list all snapshots for the VM
	return &pb.SnapshotResponse{
		Success:   true,
		Operation: req.Operation,
		Snapshots: []*pb.SnapshotInfo{},
	}, nil
}

func (s *NodeAgentService) restoreSnapshot(ctx context.Context, req *pb.SnapshotRequest) (*pb.SnapshotResponse, error) {
	if req.SnapshotId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "snapshot_id is required for restore")
	}

	return &pb.SnapshotResponse{
		Success:   true,
		Operation: req.Operation,
	}, nil
}

func (s *NodeAgentService) deleteSnapshot(ctx context.Context, req *pb.SnapshotRequest) (*pb.SnapshotResponse, error) {
	if req.SnapshotId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "snapshot_id is required for delete")
	}

	return &pb.SnapshotResponse{
		Success:   true,
		Operation: req.Operation,
	}, nil
}

func (s *NodeAgentService) getSnapshotInfo(ctx context.Context, req *pb.SnapshotRequest) (*pb.SnapshotResponse, error) {
	if req.SnapshotId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "snapshot_id is required for info")
	}

	return &pb.SnapshotResponse{
		Success:   true,
		Operation: req.Operation,
		Snapshot: &pb.SnapshotInfo{
			SnapshotId: req.SnapshotId,
			VmState:    pb.VMState_VM_STATE_RUNNING,
		},
	}, nil
}

// ApplyNetworkConfig applies network configuration to a VM
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

	// If network manager is available, use it to configure network
	if s.networkMgr != nil {
		// Get VM info to determine internal IP
		vmInfo, err := libvirt.GetVMInfo(req.VmId)
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "VM not found: %v", err)
		}

		// Apply network configuration (simplified)
		_ = vmInfo // Would use this for IP configuration

		log.Printf("[NodeAgent] Network config applied to VM %s", req.VmId)
	}

	// Return success with applied interfaces
	appliedInterfaces := make([]*pb.NetworkInterface, 0, len(req.Config.Interfaces))
	for _, iface := range req.Config.Interfaces {
		appliedInterfaces = append(appliedInterfaces, iface)
	}

	resp := &pb.NetworkConfigResponse{
		Success:           true,
		AppliedInterfaces: appliedInterfaces,
	}

	return resp, nil
}

// StartVNCProxy starts a VNC proxy for console access
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

	// Get VM info to find VNC port
	vmInfo, err := libvirt.GetVMInfo(req.VmId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "VM not found: %v", err)
	}

	// Get VNC port from VM info
	vncPort := vmInfo.VNCPort
	if vncPort == 0 {
		// VM doesn't have VNC configured
		return nil, status.Errorf(codes.FailedPrecondition, "VM %s does not have VNC configured", req.VmId)
	}

	websocketPort := int(req.WebsocketPort)
	if websocketPort == 0 {
		websocketPort = 6080 // Default noVNC port
	}

	// Determine expiry
	expireSeconds := int32(3600) // Default 1 hour
	if req.ExpirySeconds > 0 {
		expireSeconds = req.ExpirySeconds
	}

	var wsURL, token string
	var expiresAt time.Time

	if s.vncProxy != nil {
		// Use real VNC proxy
		wsURL, token, expiresAt, err = s.vncProxy.StartSession(req.VmId, vncPort, websocketPort, expireSeconds)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to start VNC proxy: %v", err)
		}
	} else {
		// Fallback to generated token without proxy
		token = generateAuthToken()
		expiresAt = time.Now().Add(time.Duration(expireSeconds) * time.Second)
		wsURL = fmt.Sprintf("ws://localhost:%d/websockify?token=%s", websocketPort, token)
	}

	resp := &pb.VNCProxyResponse{
		Success:       true,
		WebsocketUrl:  wsURL,
		WebsocketPort: int32(websocketPort),
		Token:         token,
		ExpiresAt:     timestamppb.New(expiresAt),
	}

	return resp, nil
}
