package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
)

// BandwidthService handles bandwidth quota tracking and enforcement
type BandwidthService struct {
	repo   *repository.BandwidthUsageRepository
	logger *slog.Logger

	// lastCounters tracks the last reported cumulative counters per VM
	// to calculate deltas between heartbeats.
	// Key: vmID, Value: {rxBytes, txBytes}
	mu           sync.RWMutex
	lastCounters map[string]*trafficCounter
}

type trafficCounter struct {
	RxBytes int64
	TxBytes int64
}

// NewBandwidthService creates a new BandwidthService
func NewBandwidthService(repo *repository.BandwidthUsageRepository, logger *slog.Logger) *BandwidthService {
	return &BandwidthService{
		repo:         repo,
		logger:       logger,
		lastCounters: make(map[string]*trafficCounter),
	}
}

// VMBandwidthReport mirrors the proto message for decoupling
type VMBandwidthReport struct {
	VMID          string
	RxBytes       int64
	TxBytes       int64
	InterfaceName string
}

// ProcessHeartbeatBandwidth processes bandwidth reports from a node heartbeat.
// It calculates deltas from the last known counters and accumulates them.
// Returns list of VMs that exceeded their quota (for enforcement).
func (s *BandwidthService) ProcessHeartbeatBandwidth(ctx context.Context, nodeID string, reports []VMBandwidthReport) ([]string, error) {
	var exceededVMs []string

	for _, report := range reports {
		rxDelta, txDelta := s.calculateDelta(report.VMID, report.RxBytes, report.TxBytes)

		// Skip if no new traffic (or first report — delta is 0)
		if rxDelta == 0 && txDelta == 0 {
			continue
		}

		// Accumulate usage
		usage, err := s.repo.AccumulateUsage(ctx, report.VMID, nodeID, rxDelta, txDelta)
		if err != nil {
			s.logger.ErrorContext(ctx, "failed to accumulate bandwidth usage",
				"vm_id", report.VMID,
				"error", err,
			)
			continue
		}

		// Check if quota exceeded
		if usage.QuotaBytes > 0 && usage.TotalBytes >= usage.QuotaBytes && !usage.Exceeded {
			if err := s.repo.MarkExceeded(ctx, report.VMID); err != nil {
				s.logger.ErrorContext(ctx, "failed to mark bandwidth exceeded",
					"vm_id", report.VMID,
					"error", err,
				)
			} else {
				exceededVMs = append(exceededVMs, report.VMID)
				s.logger.WarnContext(ctx, "VM bandwidth quota exceeded",
					"vm_id", report.VMID,
					"total_bytes", usage.TotalBytes,
					"quota_bytes", usage.QuotaBytes,
					"used_gb", usage.UsedGB(),
					"quota_gb", usage.QuotaGB(),
				)
			}
		}
	}

	return exceededVMs, nil
}

// calculateDelta computes the traffic delta since last report.
// Handles counter resets (VM reboot) by treating decrease as a fresh start.
func (s *BandwidthService) calculateDelta(vmID string, currentRx, currentTx int64) (rxDelta, txDelta int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	last, exists := s.lastCounters[vmID]
	if !exists {
		// First report for this VM — store counters, return 0 delta
		s.lastCounters[vmID] = &trafficCounter{RxBytes: currentRx, TxBytes: currentTx}
		return 0, 0
	}

	// Calculate deltas
	rxDelta = currentRx - last.RxBytes
	txDelta = currentTx - last.TxBytes

	// Handle counter reset (VM rebooted — counters went to 0)
	if rxDelta < 0 {
		rxDelta = currentRx
	}
	if txDelta < 0 {
		txDelta = currentTx
	}

	// Update stored counters
	last.RxBytes = currentRx
	last.TxBytes = currentTx

	return rxDelta, txDelta
}

// GetVMUsage returns the current period bandwidth usage for a VM
func (s *BandwidthService) GetVMUsage(ctx context.Context, vmID, nodeID string) (*models.BandwidthUsage, error) {
	return s.repo.GetCurrentPeriod(ctx, vmID, nodeID)
}

// GetVMUsageHistory returns all bandwidth usage records for a VM
func (s *BandwidthService) GetVMUsageHistory(ctx context.Context, vmID string) ([]models.BandwidthUsage, error) {
	return s.repo.GetByVMID(ctx, vmID)
}

// GetNodeUsage returns current period usage for all VMs on a node
func (s *BandwidthService) GetNodeUsage(ctx context.Context, nodeID string) ([]models.BandwidthUsage, error) {
	return s.repo.GetByNodeID(ctx, nodeID)
}

// SetVMQuota sets a VM's monthly bandwidth quota in GB (0 = unlimited) and
// applies it to the current period, clearing any overage flag once under quota.
func (s *BandwidthService) SetVMQuota(ctx context.Context, vmID string, quotaGB int64) error {
	if quotaGB < 0 {
		return fmt.Errorf("quota must be >= 0")
	}
	return s.repo.SetQuota(ctx, vmID, quotaGB)
}

// ClearVMCounters removes cached counters for a VM (call on VM delete/stop)
func (s *BandwidthService) ClearVMCounters(vmID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.lastCounters, vmID)
}
