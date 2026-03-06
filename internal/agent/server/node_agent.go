package server

import (
	"context"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// NodeAgentService implements the NodeAgent gRPC service
type NodeAgentService struct {
	proto.UnimplementedNodeAgentServer
}

// Ensure NodeAgentService implements the interface
var _ proto.NodeAgentServer = (*NodeAgentService)(nil)

// RegisterNode handles initial node registration
func (s *NodeAgentService) RegisterNode(ctx context.Context, req *proto.RegisterNodeRequest) (*proto.RegisterNodeResponse, error) {
	log.Printf("[NodeAgent] RegisterNode called: hostname=%s, version=%s", req.Hostname, req.AgentVersion)

	// Validate request
	if req.Hostname == "" {
		return nil, status.Errorf(codes.InvalidArgument, "hostname is required")
	}

	// TODO: Implement actual node registration logic
	// - Check if node already exists
	// - Generate or retrieve node ID
	// - Generate auth token
	// - Store node information in database

	// For now, return a mock response
	resp := &proto.RegisterNodeResponse{
		NodeId:          fmt.Sprintf("node-%s", req.Hostname),
		AuthToken:       "mock-auth-token-placeholder",
		RefreshInterval: durationpb.New(30 * time.Second),
		MetricsInterval: durationpb.New(60 * time.Second),
	}

	log.Printf("[NodeAgent] Node registered successfully: node_id=%s", resp.NodeId)
	return resp, nil
}

// Heartbeat establishes bidirectional streaming for health monitoring
func (s *NodeAgentService) Heartbeat(stream grpc.BidiStreamingServer[proto.HeartbeatRequest, proto.HeartbeatResponse]) error {
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
				resp := &proto.HeartbeatResponse{
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

		// TODO: Process heartbeat data
		// - Update node status in database
		// - Check resource availability
		// - Update active VMs list

		// Send immediate acknowledgment
		resp := &proto.HeartbeatResponse{
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

// ExecuteVMCommand sends VM lifecycle commands
func (s *NodeAgentService) ExecuteVMCommand(ctx context.Context, req *proto.VMCommandRequest) (*proto.VMCommandResponse, error) {
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

	// TODO: Implement actual VM command execution
	// - Validate VM exists on this node
	// - Execute the appropriate libvirt/QEMU command
	// - Wait for completion (unless async)
	// - Return result

	resp := &proto.VMCommandResponse{
		Success: true,
		VmId:    req.VmId,
		Command: req.Command,
		State:   proto.VMState_VM_STATE_RUNNING,
		Message: fmt.Sprintf("Command %v executed successfully", req.Command),
	}

	return resp, nil
}

// GetVMStatus retrieves the current state of a VM
func (s *NodeAgentService) GetVMStatus(ctx context.Context, req *proto.VMStatusRequest) (*proto.VMStatusResponse, error) {
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

	// TODO: Implement actual VM status retrieval
	// - Query libvirt for VM status
	// - Get resource usage if requested
	// - Return comprehensive status

	resp := &proto.VMStatusResponse{
		VmId:            req.VmId,
		State:           proto.VMState_VM_STATE_RUNNING,
		UptimeSeconds:   3600,
		Pid:             12345,
		IpAddresses:     []string{"192.168.1.100"},
		VncPort:         5900,
		LastStateChange: timestamppb.Now(),
	}

	if req.IncludeMetrics {
		resp.CurrentResources = &proto.VMResourceUsage{
			CpuPercent:     25.5,
			MemoryUsedMb:   2048,
			MemoryTotalMb:  4096,
			DiskReadBytes:  1024000,
			DiskWriteBytes: 512000,
		}
	}

	return resp, nil
}

// StreamVMMetrics opens server-side streaming for VM metrics
func (s *NodeAgentService) StreamVMMetrics(req *proto.VMMetricsRequest, stream grpc.ServerStreamingServer[proto.VMMetricsResponse]) error {
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
		// If no VMs specified, stream metrics for all VMs
		vmIDs = []string{"vm-1", "vm-2"} // TODO: Get from actual VM manager
	}

	for {
		select {
		case <-stream.Context().Done():
			log.Println("[NodeAgent] StreamVMMetrics context cancelled")
			return nil
		case <-ticker.C:
			// Stream metrics for each VM
			for _, vmID := range vmIDs {
				metrics := s.generateMockMetrics(vmID)
				if err := stream.Send(metrics); err != nil {
					log.Printf("[NodeAgent] Failed to send metrics: %v", err)
					return err
				}
			}
		}
	}
}

// generateMockMetrics creates mock metrics for testing
func (s *NodeAgentService) generateMockMetrics(vmID string) *proto.VMMetricsResponse {
	return &proto.VMMetricsResponse{
		Timestamp: timestamppb.Now(),
		VmId:      vmID,
		Cpu: &proto.CPUMetrics{
			UsagePercent:     25.0,
			PerVcpuPercent:   []double{20.0, 30.0},
			StealTimePercent: 0.5,
			IowaitPercent:    1.0,
		},
		Memory: &proto.MemoryMetrics{
			UsedBytes:      2147483648,
			AvailableBytes: 2147483648,
			TotalBytes:     4294967296,
			SwapUsedBytes:  0,
			SwapTotalBytes: 1073741824,
			CacheBytes:     536870912,
		},
		Disk: &proto.DiskMetrics{
			ReadBytesPerSec:  1048576,
			WriteBytesPerSec: 524288,
			ReadIops:         100,
			WriteIops:        50,
			ReadLatencyMs:    2.5,
			WriteLatencyMs:   3.0,
			DiskUsagePercent: 45.0,
		},
		Network: &proto.NetworkMetrics{
			RxBytesPerSec:   1024000,
			TxBytesPerSec:   512000,
			RxPacketsPerSec: 1000,
			TxPacketsPerSec: 500,
			RxErrorsPerSec:  0,
			TxErrorsPerSec:  0,
			RxDroppedPerSec: 0,
			TxDroppedPerSec: 0,
		},
		Process: &proto.ProcessMetrics{
			ProcessCount:   150,
			ThreadCount:    300,
			LoadAverage1m:  0.5,
			LoadAverage5m:  0.4,
			LoadAverage15m: 0.3,
		},
	}
}

// CreateSnapshot creates or manages VM snapshots
func (s *NodeAgentService) CreateSnapshot(ctx context.Context, req *proto.SnapshotRequest) (*proto.SnapshotResponse, error) {
	log.Printf("[NodeAgent] CreateSnapshot: vm_id=%s, operation=%v", req.VmId, req.Operation)

	if req.VmId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "vm_id is required")
	}

	nodeID, authenticated := GetNodeIDFromContext(ctx)
	if !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	log.Printf("[NodeAgent] Snapshot operation %v on VM %s from node %s", req.Operation, req.VmId, nodeID)

	// TODO: Implement snapshot operations
	// - Create snapshot using libvirt
	// - List existing snapshots
	// - Restore from snapshot
	// - Delete snapshot

	resp := &proto.SnapshotResponse{
		Success:   true,
		Operation: req.Operation,
		Snapshot: &proto.SnapshotInfo{
			SnapshotId:  fmt.Sprintf("snap-%d", time.Now().Unix()),
			Name:        req.Name,
			Description: req.Description,
			CreatedAt:   timestamppb.Now(),
			SizeBytes:   1073741824,
			VmState:     proto.VMState_VM_STATE_RUNNING,
		},
	}

	return resp, nil
}

// ApplyNetworkConfig applies network configuration to a VM
func (s *NodeAgentService) ApplyNetworkConfig(ctx context.Context, req *proto.NetworkConfigRequest) (*proto.NetworkConfigResponse, error) {
	log.Printf("[NodeAgent] ApplyNetworkConfig: vm_id=%s", req.VmId)

	if req.VmId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "vm_id is required")
	}

	nodeID, authenticated := GetNodeIDFromContext(ctx)
	if !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	log.Printf("[NodeAgent] Applying network config to VM %s from node %s", req.VmId, nodeID)

	// TODO: Implement network configuration
	// - Apply TC rules for bandwidth limiting
	// - Apply iptables/nftables for firewall rules
	// - Configure network interfaces

	resp := &proto.NetworkConfigResponse{
		Success:           true,
		AppliedInterfaces: req.Config.Interfaces,
	}

	return resp, nil
}

// StartVNCProxy starts a VNC proxy for console access
func (s *NodeAgentService) StartVNCProxy(ctx context.Context, req *proto.VNCProxyRequest) (*proto.VNCProxyResponse, error) {
	log.Printf("[NodeAgent] StartVNCProxy: vm_id=%s", req.VmId)

	if req.VmId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "vm_id is required")
	}

	nodeID, authenticated := GetNodeIDFromContext(ctx)
	if !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}

	log.Printf("[NodeAgent] Starting VNC proxy for VM %s from node %s", req.VmId, nodeID)

	// TODO: Implement VNC proxy
	// - Start websockify proxy
	// - Configure VNC port
	// - Generate access token
	// - Set expiry if specified

	websocketPort := req.WebsocketPort
	if websocketPort == 0 {
		websocketPort = 6080 // Default noVNC port
	}

	resp := &proto.VNCProxyResponse{
		Success:       true,
		WebsocketUrl:  fmt.Sprintf("ws://localhost:%d/websockify", websocketPort),
		WebsocketPort: websocketPort,
		Token:         "vnc-token-placeholder",
		ExpiresAt:     timestamppb.New(time.Now().Add(time.Duration(req.ExpirySeconds) * time.Second)),
	}

	return resp, nil
}

type double = float64
