package service

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// MetricsService serves persisted node metric history to the API.
type MetricsService struct {
	repo *repository.MetricsRepository
}

// NewMetricsService creates a new MetricsService.
func NewMetricsService(db *gorm.DB) *MetricsService {
	return &MetricsService{repo: repository.NewMetricsRepository(db)}
}

// NodeHistory returns a node's samples within the trailing window, oldest first.
func (s *MetricsService) NodeHistory(ctx context.Context, nodeID string, window time.Duration, limit int) ([]models.NodeMetricSample, error) {
	since := time.Now().Add(-window)
	return s.repo.ListNodeSamples(ctx, nodeID, since, limit)
}

// VMHistory returns a VM's samples within the trailing window, oldest first.
func (s *MetricsService) VMHistory(ctx context.Context, vmID string, window time.Duration, limit int) ([]models.VMMetricSample, error) {
	since := time.Now().Add(-window)
	return s.repo.ListVMSamples(ctx, vmID, since, limit)
}

// MetricsCollector periodically samples every node's (and running VM's) live
// metrics and persists them, pruning samples older than the retention window.
type MetricsCollector struct {
	nodeRepo    *repository.NodeRepository
	vmRepo      *repository.VMRepository
	nodeService *NodeService
	vmService   *VMService
	bwService   *BandwidthService
	repo        *repository.MetricsRepository
	interval    time.Duration
	retention   time.Duration
	enforce     bool // stop VMs that exceed their monthly bandwidth quota
	logger      *slog.Logger
}

// NewMetricsCollector wires a collector. interval is the sampling period;
// retention is how long samples are kept.
func NewMetricsCollector(db *gorm.DB, interval, retention time.Duration, logger *slog.Logger) *MetricsCollector {
	if logger == nil {
		logger = slog.Default()
	}
	nodeRepo := repository.NewNodeRepository(db)
	vmRepo := repository.NewVMRepository(db)
	return &MetricsCollector{
		nodeRepo:    nodeRepo,
		vmRepo:      vmRepo,
		nodeService: NewNodeService(nodeRepo, db),
		// riverClient is nil: the collector only reads metrics, never enqueues jobs.
		vmService: NewVMService(db, vmRepo, nodeRepo, repository.NewTemplateRepository(db), nil, logger),
		bwService: NewBandwidthService(repository.NewBandwidthUsageRepository(db), logger),
		repo:      repository.NewMetricsRepository(db),
		interval:  interval,
		retention: retention,
		// Enforcement (auto-stop on overage) is opt-in: customers' VMs shouldn't be
		// stopped unless the operator deliberately enables it. Accounting + the
		// `exceeded` flag + the UI work regardless.
		enforce: os.Getenv("BANDWIDTH_ENFORCE") == "true",
		logger:  logger,
	}
}

// Run samples immediately, then on each tick until ctx is cancelled.
func (c *MetricsCollector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	c.logger.Info("metrics collector started", "interval", c.interval.String(), "retention", c.retention.String())
	c.collectOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			c.logger.Info("metrics collector stopped")
			return
		case <-ticker.C:
			c.collectOnce(ctx)
		}
	}
}

// collectOnce samples all nodes (and running VMs on online nodes) once, then
// prunes stale samples. Per-target failures are logged and skipped so one
// unreachable node/VM never blocks the rest.
func (c *MetricsCollector) collectOnce(ctx context.Context) {
	nodes, err := c.nodeRepo.List(ctx, 0, 0)
	if err != nil {
		c.logger.Error("metrics collector: list nodes failed", "error", err)
		return
	}

	online := make(map[string]bool, len(nodes))
	for i := range nodes {
		nodeID := nodes[i].ID
		mctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		m, err := c.nodeService.GetNodeMetrics(mctx, nodeID)
		cancel()
		if err != nil {
			c.logger.Warn("metrics collector: get node metrics failed", "node_id", nodeID, "error", err)
			continue
		}
		sample := &models.NodeMetricSample{
			NodeID:               nodeID,
			CPUUsage:             m.CPUUsage,
			MemoryUsage:          m.MemoryUsage,
			DiskUsage:            m.DiskUsage,
			NetworkRxBytesPerSec: m.NetworkRxBytesPerSec,
			NetworkTxBytesPerSec: m.NetworkTxBytesPerSec,
			VMCount:              m.VMCount,
			Status:               m.Status,
			RecordedAt:           time.Now(),
		}
		if err := c.repo.InsertNodeSample(ctx, sample); err != nil {
			c.logger.Error("metrics collector: insert node sample failed", "node_id", nodeID, "error", err)
		}
		if m.Status == "online" {
			online[nodeID] = true
		}
	}

	// Reconcile DB VM status against reality on every online node, so a VM
	// started/stopped/crashed out-of-band is reflected in the panel (otherwise
	// status only changes on an explicit lifecycle command and drifts stale).
	for nodeID := range online {
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		c.vmService.ReconcileNodeVMStatuses(rctx, nodeID)
		cancel()
	}

	c.collectVMs(ctx, online)

	if c.retention > 0 {
		cutoff := time.Now().Add(-c.retention)
		if removed, err := c.repo.PruneNodeSamplesBefore(ctx, cutoff); err != nil {
			c.logger.Error("metrics collector: prune node samples failed", "error", err)
		} else if removed > 0 {
			c.logger.Debug("metrics collector: pruned stale node samples", "removed", removed)
		}
		if removed, err := c.repo.PruneVMSamplesBefore(ctx, cutoff); err != nil {
			c.logger.Error("metrics collector: prune vm samples failed", "error", err)
		} else if removed > 0 {
			c.logger.Debug("metrics collector: pruned stale vm samples", "removed", removed)
		}
	}
}

// collectVMs samples each running VM that lives on an online node. Sampling a VM
// on an offline node is skipped to avoid wasting the tick on calls that will time out.
func (c *MetricsCollector) collectVMs(ctx context.Context, online map[string]bool) {
	vms, err := c.vmRepo.ListByStatus(ctx, models.VMStatusRunning, 0, 0)
	if err != nil {
		c.logger.Error("metrics collector: list running vms failed", "error", err)
		return
	}
	for i := range vms {
		vm := vms[i]
		if !online[vm.NodeID] {
			continue
		}
		mctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		m, err := c.vmService.GetVMMetrics(mctx, vm.NodeID, vm.ID)
		cancel()
		if err != nil {
			c.logger.Warn("metrics collector: get vm metrics failed", "vm_id", vm.ID, "error", err)
			continue
		}
		sample := &models.VMMetricSample{
			VMID:                 vm.ID,
			CPUUsage:             m.CpuPercent,
			MemoryUsage:          m.MemoryUsedPercent,
			MemoryUsedBytes:      m.MemoryUsed,
			DiskReadBytesPerSec:  m.DiskReadBytesPerSec,
			DiskWriteBytesPerSec: m.DiskWriteBytesPerSec,
			NetworkRxBytesPerSec: m.NetworkRxBytesPerSec,
			NetworkTxBytesPerSec: m.NetworkTxBytesPerSec,
			RecordedAt:           time.Now(),
		}
		if err := c.repo.InsertVMSample(ctx, sample); err != nil {
			c.logger.Error("metrics collector: insert vm sample failed", "vm_id", vm.ID, "error", err)
		}

		c.accountBandwidth(ctx, vm.ID, vm.NodeID)
	}
}

// accountBandwidth pulls the VM's cumulative traffic counters and accumulates
// the delta into the current billing period, enforcing the monthly quota when
// enabled (BANDWIDTH_ENFORCE=true → stop the VM on overage).
func (c *MetricsCollector) accountBandwidth(ctx context.Context, vmID, nodeID string) {
	bctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	rx, tx, err := c.vmService.GetVMTrafficCounters(bctx, nodeID, vmID)
	cancel()
	if err != nil {
		c.logger.Warn("metrics collector: get vm traffic counters failed", "vm_id", vmID, "error", err)
		return
	}

	exceeded, err := c.bwService.ProcessHeartbeatBandwidth(ctx, nodeID, []VMBandwidthReport{
		{VMID: vmID, RxBytes: rx, TxBytes: tx},
	})
	if err != nil {
		c.logger.Error("metrics collector: bandwidth accounting failed", "vm_id", vmID, "error", err)
		return
	}

	for _, exVM := range exceeded {
		if !c.enforce {
			c.logger.Warn("bandwidth quota exceeded (enforcement disabled)", "vm_id", exVM)
			continue
		}
		if err := c.vmService.StopVMForEnforcement(ctx, nodeID, exVM); err != nil {
			c.logger.Error("bandwidth enforcement: failed to stop VM", "vm_id", exVM, "error", err)
		} else {
			c.logger.Warn("bandwidth enforcement: VM stopped (quota exceeded)", "vm_id", exVM)
		}
	}
}
