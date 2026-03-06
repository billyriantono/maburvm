package health

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/maburvm/panel/internal/shared/config"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Reporter sends node health status to the panel via gRPC heartbeat.
type Reporter struct {
	cfg          *config.AgentConfig
	panelAddr    string
	nodeID       string
	capabilities []string
	version      string

	mu        sync.RWMutex
	connected bool
	stopCh    chan struct{}

	metrics  *MetricsCollector
	interval time.Duration
}

// NewReporter creates a new health reporter.
func NewReporter(cfg *config.AgentConfig, panelAddr, nodeID, version string, capabilities []string) *Reporter {
	return &Reporter{
		cfg:          cfg,
		panelAddr:    panelAddr,
		nodeID:       nodeID,
		capabilities: capabilities,
		version:      version,
		stopCh:       make(chan struct{}),
		metrics:      NewMetricsCollector(),
		interval:     cfg.HeartbeatInterval,
	}
}

// Start begins the health reporting loop with automatic reconnection.
func (r *Reporter) Start(ctx context.Context) {
	if r.interval == 0 {
		r.interval = 30 * time.Second
	}

	log.Printf("[health] Starting health reporter to %s with interval %v", r.panelAddr, r.interval)

	go r.run(ctx)
}

// Stop stops the health reporter.
func (r *Reporter) Stop() {
	close(r.stopCh)
	log.Println("[health] Health reporter stopped")
}

// IsConnected returns the current connection status.
func (r *Reporter) IsConnected() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.connected
}

func (r *Reporter) run(ctx context.Context) {
	backoff := &exponentialBackoff{
		initial:    1 * time.Second,
		max:        60 * time.Second,
		multiplier: 2.0,
		current:    1 * time.Second,
	}

	for {
		select {
		case <-r.stopCh:
			return
		case <-ctx.Done():
			return
		default:
		}

		if err := r.runHeartbeat(ctx, backoff); err != nil {
			log.Printf("[health] Heartbeat connection lost: %v", err)
			r.setConnected(false)

			// Calculate backoff delay
			delay := backoff.next()
			log.Printf("[health] Reconnecting in %v...", delay)

			select {
			case <-r.stopCh:
				return
			case <-ctx.Done():
				return
			case <-time.After(delay):
				continue
			}
		}
	}
}

func (r *Reporter) runHeartbeat(ctx context.Context, backoff *exponentialBackoff) error {
	// Connect to panel
	conn, err := grpc.DialContext(ctx, r.panelAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return fmt.Errorf("failed to connect to panel: %w", err)
	}
	defer conn.Close()

	client := pb.NewNodeAgentClient(conn)

	// Establish bidirectional stream
	stream, err := client.Heartbeat(ctx)
	if err != nil {
		return fmt.Errorf("failed to create heartbeat stream: %w", err)
	}

	r.setConnected(true)
	backoff.reset()

	log.Printf("[health] Heartbeat stream established with panel")

	// Send initial heartbeat with registration info
	if err := r.sendHeartbeat(stream); err != nil {
		return fmt.Errorf("failed to send initial heartbeat: %w", err)
	}

	// Send periodic heartbeats
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopCh:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := r.sendHeartbeat(stream); err != nil {
				return fmt.Errorf("heartbeat send failed: %w", err)
			}
		}
	}
}

func (r *Reporter) sendHeartbeat(stream pb.NodeAgent_HeartbeatClient) error {
	// Collect current metrics
	metrics := r.metrics.Collect()

	// Get active VM IDs (placeholder - would integrate with VM manager)
	activeVMs := r.metrics.GetRunningVMIDs()

	// Build heartbeat request
	req := &pb.HeartbeatRequest{
		Timestamp:   timestamppb.Now(),
		NodeId:      r.nodeID,
		ActiveVmIds: activeVMs,
		AvailableResources: &pb.VMResources{
			// Available resources would be calculated from total - used
			Vcpus:    int32(metrics.AvailableCPUs),
			MemoryMb: metrics.AvailableMemoryMB,
			DiskGb:   metrics.AvailableDiskGB,
		},
		SystemLoad: &pb.SystemLoad{
			CpuPercent:             metrics.CPUPercent,
			MemoryUsedBytes:        metrics.MemoryUsed,
			MemoryTotalBytes:       metrics.MemoryTotal,
			DiskIoReadBytesPerSec:  metrics.DiskReadBytesPerSec,
			DiskIoWriteBytesPerSec: metrics.DiskWriteBytesPerSec,
			NetworkRxBytesPerSec:   metrics.NetworkRXBytesPerSec,
			NetworkTxBytesPerSec:   metrics.NetworkTXBytesPerSec,
		},
	}

	if err := stream.Send(req); err != nil {
		return fmt.Errorf("failed to send heartbeat: %w", err)
	}

	// Optionally receive response from panel
	resp, err := stream.Recv()
	if err != nil {
		// Connection might be closed, don't treat as error
		log.Printf("[health] Heartbeat response error: %v", err)
	} else if resp != nil {
		log.Printf("[health] Heartbeat acknowledged, pending commands: %v", resp.CommandsPending)
	}

	return nil
}

func (r *Reporter) setConnected(connected bool) {
	r.mu.Lock()
	r.connected = connected
	r.mu.Unlock()
}

// exponentialBackoff handles exponential backoff for reconnection.
type exponentialBackoff struct {
	initial    time.Duration
	max        time.Duration
	multiplier float64
	current    time.Duration
}

func (b *exponentialBackoff) next() time.Duration {
	delay := b.current
	b.current = time.Duration(float64(b.current) * b.multiplier)
	if b.current > b.max {
		b.current = b.max
	}
	return delay
}

func (b *exponentialBackoff) reset() {
	b.current = b.initial
}
