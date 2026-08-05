package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/riverqueue/river"
	"gorm.io/gorm"
)

// defaultThrottleMbps is the speed a throttle-policy VM drops to when a plan
// leaves throttle_speed_mbps unset (0).
const defaultThrottleMbps = 10

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
	nodeRepo       *repository.NodeRepository
	vmRepo         *repository.VMRepository
	networkRepo    *repository.NetworkRepository
	storageRepo    repository.StorageRepository
	nodeService    *NodeService
	vmService      *VMService
	bwService      *BandwidthService
	networkService *NetworkService
	repo           *repository.MetricsRepository
	db             *gorm.DB
	interval       time.Duration
	retention      time.Duration
	enforce        bool   // apply restrictive over-quota actions (throttle/suspend)
	overageSecret  string // env fallback HMAC secret (BILLING_WEBHOOK_SECRET)
	httpClient     *http.Client
	logger         *slog.Logger
	tick           int // collectOnce counter, for slower-cadence work (IP reconcile)
}

// NewMetricsCollector wires a collector. interval is the sampling period;
// retention is how long samples are kept.
func NewMetricsCollector(db *gorm.DB, riverClient *river.Client[pgx.Tx], interval, retention time.Duration, logger *slog.Logger) *MetricsCollector {
	if logger == nil {
		logger = slog.Default()
	}
	nodeRepo := repository.NewNodeRepository(db)
	vmRepo := repository.NewVMRepository(db)
	networkRepo := repository.NewNetworkRepository(db)
	firewallRepo := repository.NewFirewallRepository(db)
	return &MetricsCollector{
		nodeRepo:    nodeRepo,
		vmRepo:      vmRepo,
		networkRepo: networkRepo,
		storageRepo: repository.NewStorageRepository(db),
		nodeService: NewNodeService(nodeRepo, db),
		// riverClient lets the collector enqueue throttle/restore network jobs.
		vmService:      NewVMService(db, vmRepo, nodeRepo, repository.NewTemplateRepository(db), riverClient, logger),
		bwService:      NewBandwidthService(repository.NewBandwidthUsageRepository(db), logger),
		networkService: NewNetworkService(db, networkRepo, firewallRepo, vmRepo, nodeRepo, riverClient),
		repo:           repository.NewMetricsRepository(db),
		db:             db,
		interval:       interval,
		retention:      retention,
		// Restrictive over-quota actions (throttle/suspend) are opt-in: customers'
		// VMs shouldn't be limited unless the operator enables it. Accounting, the
		// `exceeded` flag, and overage billing work regardless.
		enforce:       os.Getenv("BANDWIDTH_ENFORCE") == "true",
		overageSecret: os.Getenv("BILLING_WEBHOOK_SECRET"),
		httpClient:    &http.Client{Timeout: 10 * time.Second},
		logger:        logger,
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
			// Keep each node's default storage pool present and its capacity
			// live from the node's real disk, so /storage reflects the node.
			c.syncDefaultStoragePool(&nodes[i], m)
		}
	}

	// Reconcile DB VM status against reality on every online node, so a VM
	// started/stopped/crashed out-of-band is reflected in the panel (otherwise
	// status only changes on an explicit lifecycle command and drifts stale).
	for nodeID := range online {
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		c.vmService.ReconcileNodeVMStatuses(rctx, nodeID)
		c.vmService.ReapStuckVMs(rctx, nodeID)
		cancel()
	}

	// Less frequently (every ~5 ticks), ARP-reconcile each node's pool IPs so
	// addresses used by VMs the panel doesn't manage (pre-existing imported
	// guests) show as reserved and are never allocated. It's heavier (one ARP per
	// candidate IP) so it doesn't need to run every tick.
	c.tick++
	if c.tick%5 == 1 {
		for nodeID := range online {
			rctx, cancel := context.WithTimeout(ctx, 90*time.Second)
			c.vmService.ReconcileNodePoolIPs(rctx, nodeID)
			// Floating IPs are pure iptables/host-address state, so a node reboot
			// silently drops them. Re-applying the desired set (attach is
			// idempotent) is what makes them survive one.
			c.vmService.ReconcileFloatingIPs(rctx, nodeID)
			cancel()
		}
	}

	c.collectVMs(ctx, online)

	// Restore any throttled VMs whose quota has reset for the new period.
	c.restoreResetThrottles(ctx)

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
		c.enforceOverQuota(ctx, nodeID, exVM)
	}
}

// defaultPoolPath mirrors the agent's defaultImageDir: where VM disks and
// provisioned volumes live on each node.
const defaultPoolPath = "/var/lib/libvirt/images"

// syncDefaultStoragePool ensures each online node has a 'local' dir storage pool
// at the node's image dir and refreshes its capacity/status from the node's live
// disk metrics, so /storage shows real per-node storage instead of static values.
func (c *MetricsCollector) syncDefaultStoragePool(node *models.Node, m *NodeMetrics) {
	if m.DiskTotal <= 0 {
		return // no usable disk figure this tick
	}
	pools, err := c.storageRepo.GetPoolsByNodeID(node.ID)
	if err != nil {
		c.logger.Error("storage sync: list pools failed", "node_id", node.ID, "error", err)
		return
	}
	var pool *models.StoragePool
	for i := range pools {
		if pools[i].Path == defaultPoolPath {
			pool = &pools[i]
			break
		}
	}
	avail := max(m.DiskTotal-m.DiskUsed, 0)
	if pool == nil {
		if err := c.storageRepo.CreatePool(&models.StoragePool{
			Name:           "local",
			Type:           "dir",
			Status:         "online",
			Path:           defaultPoolPath,
			FileFormat:     "qcow2",
			IsPrimary:      true,
			NodeID:         node.ID,
			TotalSpace:     m.DiskTotal,
			UsedSpace:      m.DiskUsed,
			AvailableSpace: avail,
		}); err != nil {
			c.logger.Error("storage sync: create default pool failed", "node_id", node.ID, "error", err)
		}
		return
	}
	pool.TotalSpace = m.DiskTotal
	pool.UsedSpace = m.DiskUsed
	pool.AvailableSpace = avail
	pool.Status = "online"
	if err := c.storageRepo.UpdatePool(pool); err != nil {
		c.logger.Error("storage sync: update default pool failed", "node_id", node.ID, "error", err)
	}
}

// enforceOverQuota acts on a VM that just crossed its monthly data quota,
// following the over-quota policy snapshotted on its network interface:
//   - overage: emit a billing webhook (VM keeps full speed). Fires regardless of
//     BANDWIDTH_ENFORCE — it bills, it doesn't restrict.
//   - throttle: drop the live speed to ThrottleSpeedMbps (opt-in via enforce).
//   - suspend: stop the VM (opt-in via enforce).
func (c *MetricsCollector) enforceOverQuota(ctx context.Context, nodeID, vmID string) {
	net, err := c.networkRepo.GetByVMID(ctx, vmID)
	if err != nil {
		c.logger.Error("over-quota: no network for VM", "vm_id", vmID, "error", err)
		return
	}
	policy := net.OverQuotaPolicy
	if policy == "" {
		policy = models.OverQuotaThrottle
	}

	switch policy {
	case models.OverQuotaOverage:
		c.sendOverageWebhook(ctx, nodeID, vmID)

	case models.OverQuotaSuspend:
		if !c.enforce {
			c.logger.Warn("over-quota suspend skipped (BANDWIDTH_ENFORCE off)", "vm_id", vmID)
			return
		}
		if err := c.vmService.StopVMForEnforcement(ctx, nodeID, vmID); err != nil {
			c.logger.Error("over-quota: suspend failed", "vm_id", vmID, "error", err)
		} else {
			c.logger.Warn("over-quota: VM suspended (quota exceeded)", "vm_id", vmID)
		}

	default: // throttle
		if !c.enforce {
			c.logger.Warn("over-quota throttle skipped (BANDWIDTH_ENFORCE off)", "vm_id", vmID)
			return
		}
		speed := int64(net.ThrottleSpeedMbps)
		if speed <= 0 {
			speed = defaultThrottleMbps
		}
		if err := c.networkService.ApplyLiveBandwidth(ctx, vmID, speed); err != nil {
			c.logger.Error("over-quota: throttle failed", "vm_id", vmID, "error", err)
			return
		}
		if err := c.networkRepo.SetThrottled(ctx, net.ID, true); err != nil {
			c.logger.Error("over-quota: mark throttled failed", "vm_id", vmID, "error", err)
		}
		c.logger.Warn("over-quota: VM throttled (quota exceeded)", "vm_id", vmID, "speed_mbps", speed)
	}
}

// restoreResetThrottles un-throttles VMs whose quota has reset (new billing
// period) or was raised so usage is back under the limit, restoring each VM's
// normal provisioned speed. Called once per collection tick.
func (c *MetricsCollector) restoreResetThrottles(ctx context.Context) {
	nets, err := c.networkRepo.ListThrottled(ctx)
	if err != nil {
		c.logger.Error("restore throttles: list failed", "error", err)
		return
	}
	for i := range nets {
		net := &nets[i]
		vm, err := c.vmRepo.GetByID(ctx, net.VMID)
		if err != nil {
			continue
		}
		usage, err := c.bwService.GetVMUsage(ctx, net.VMID, vm.NodeID)
		if err != nil {
			continue
		}
		// Still over quota this period → keep throttled.
		if usage != nil && usage.QuotaBytes > 0 && usage.TotalBytes >= usage.QuotaBytes {
			continue
		}
		if err := c.networkService.ApplyLiveBandwidth(ctx, net.VMID, net.BandwidthLimit); err != nil {
			c.logger.Error("restore throttle: apply failed", "vm_id", net.VMID, "error", err)
			continue
		}
		if err := c.networkRepo.SetThrottled(ctx, net.ID, false); err != nil {
			c.logger.Error("restore throttle: clear flag failed", "vm_id", net.VMID, "error", err)
			continue
		}
		c.logger.Info("over-quota: VM speed restored (quota reset)", "vm_id", net.VMID, "speed_mbps", net.BandwidthLimit)
	}
}

// resolveOverageWebhook returns the overage webhook URL + HMAC secret from the
// admin-managed API settings (system_settings section 'api', editable at runtime
// via Settings → System → API, no restart needed). The secret falls back to the
// BILLING_WEBHOOK_SECRET env when unset in settings.
func (c *MetricsCollector) resolveOverageWebhook(ctx context.Context) (url, secret string) {
	secret = c.overageSecret
	if c.db == nil {
		return url, secret
	}
	var raw string
	if err := c.db.WithContext(ctx).
		Raw("SELECT data::text FROM system_settings WHERE section = 'api'").
		Scan(&raw).Error; err != nil || raw == "" {
		return url, secret
	}
	var cfg struct {
		WebhookURL string `json:"webhookUrl"`
		HMACSecret string `json:"hmacSecret"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return url, secret
	}
	if cfg.WebhookURL != "" {
		url = cfg.WebhookURL
	}
	if cfg.HMACSecret != "" {
		secret = cfg.HMACSecret
	}
	return url, secret
}

// sendOverageWebhook posts a signed bandwidth-overage event to the configured
// WHMCS endpoint so it can bill the overage. When no URL is set the overage is
// recorded in the logs only. Signature reuses the billing HMAC secret.
func (c *MetricsCollector) sendOverageWebhook(ctx context.Context, nodeID, vmID string) {
	usage, err := c.bwService.GetVMUsage(ctx, vmID, nodeID)
	if err != nil || usage == nil {
		c.logger.Error("overage: no usage for VM", "vm_id", vmID, "error", err)
		return
	}
	url, secret := c.resolveOverageWebhook(ctx)
	if url == "" {
		c.logger.Warn("bandwidth overage (no webhook URL configured — recorded only)",
			"vm_id", vmID, "used_gb", usage.UsedGB(), "quota_gb", usage.QuotaGB())
		return
	}
	body, _ := json.Marshal(map[string]any{
		"event":        "bandwidth.overage",
		"vm_id":        vmID,
		"node_id":      nodeID,
		"used_gb":      usage.UsedGB(),
		"quota_gb":     usage.QuotaGB(),
		"period_start": usage.PeriodStart,
		"period_end":   usage.PeriodEnd,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		c.logger.Error("overage webhook: build request failed", "vm_id", vmID, "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set("X-Webhook-Signature", hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Error("overage webhook: post failed", "vm_id", vmID, "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		c.logger.Error("overage webhook: non-2xx response", "vm_id", vmID, "status", resp.StatusCode)
		return
	}
	c.logger.Info("bandwidth overage webhook sent", "vm_id", vmID, "used_gb", usage.UsedGB())
}
