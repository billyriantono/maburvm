package service

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/maburvm/panel/internal/panel/client"
	"github.com/maburvm/panel/internal/panel/repository"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
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
	db   *gorm.DB
}

// NewMetricsService creates a new MetricsService.
func NewMetricsService(db *gorm.DB) *MetricsService {
	return &MetricsService{repo: repository.NewMetricsRepository(db), db: db}
}

// DB is the service's database, for the settings the handler reads.
func (s *MetricsService) DB() *gorm.DB { return s.db }

// NodeHistory returns a node's readings within the trailing window, oldest
// first, averaged down to at most limit points.
func (s *MetricsService) NodeHistory(ctx context.Context, nodeID string, window time.Duration, limit int) ([]models.NodeMetricSample, error) {
	since := time.Now().Add(-window)
	rows, err := s.repo.ListNodeSamples(ctx, nodeID, since, 0)
	if err != nil {
		return nil, err
	}
	return downsampleNode(rows, since, window, limit), nil
}

// downsampleNode drops the rows stored while a node could not be reached (they
// are zeros, not readings) and averages the rest into at most n equal slices
// of the window. A slice with no readings is left out, so an outage stays a
// gap. Connections tracked keeps its peak, because the peak is what fills the
// table; each point carries the time of the last reading it covers.
func downsampleNode(rows []models.NodeMetricSample, since time.Time, window time.Duration, n int) []models.NodeMetricSample {
	kept := make([]models.NodeMetricSample, 0, len(rows))
	for _, r := range rows {
		if r.Status != "offline" {
			kept = append(kept, r)
		}
	}
	if n <= 0 || len(kept) <= n || window <= 0 {
		return kept
	}
	width := window / time.Duration(n)
	if width <= 0 {
		return kept
	}
	out := make([]models.NodeMetricSample, 0, n)
	var (
		acc                    models.NodeMetricSample
		cpu, mem, disk, rx, tx float64
		peak                   int64
		count                  float64
		slice                  int64 = -1
	)
	flush := func() {
		if count == 0 {
			return
		}
		acc.CPUUsage, acc.MemoryUsage, acc.DiskUsage = cpu/count, mem/count, disk/count
		acc.NetworkRxBytesPerSec, acc.NetworkTxBytesPerSec = rx/count, tx/count
		acc.ConntrackCount = peak
		out = append(out, acc)
	}
	for _, r := range kept {
		at := int64(r.RecordedAt.Sub(since) / width)
		if at != slice {
			flush()
			slice, count, peak = at, 0, 0
			cpu, mem, disk, rx, tx = 0, 0, 0, 0, 0
		}
		acc = r
		cpu += r.CPUUsage
		mem += r.MemoryUsage
		disk += r.DiskUsage
		rx += r.NetworkRxBytesPerSec
		tx += r.NetworkTxBytesPerSec
		if r.ConntrackCount > peak {
			peak = r.ConntrackCount
		}
		count++
	}
	flush()
	return out
}

// VMHistory returns a VM's samples within the trailing window, oldest first,
// averaged down to at most limit points so a week reads as fast as an hour.
// Beyond the week of raw samples it reads the hourly averages, and beyond the
// hourly history (90 days unless changed) the daily ones.
func (s *MetricsService) VMHistory(ctx context.Context, vmID string, window time.Duration, limit int) ([]models.VMMetricSample, error) {
	since := time.Now().Add(-window)
	if window > RawMetricsWindow {
		list := s.repo.ListVMHours
		if window > time.Duration(LoadPortalSettings(ctx, s.db).GraphDays())*24*time.Hour {
			list = s.repo.ListVMDays
		}
		rows, err := list(ctx, vmID, since)
		if err != nil {
			return nil, err
		}
		return downsampleVM(rows, since, window, limit), nil
	}
	rows, err := s.repo.ListVMSamples(ctx, vmID, since, 0)
	if err != nil {
		return nil, err
	}
	return downsampleVM(rows, since, window, limit), nil
}

// downsampleVM averages samples into at most n equal slices of the window.
// A slice with no samples is left out, so time the VM spent powered off stays a
// gap instead of being drawn as a line. Each point carries the time of the last
// sample it covers.
func downsampleVM(rows []models.VMMetricSample, since time.Time, window time.Duration, n int) []models.VMMetricSample {
	if n <= 0 || len(rows) <= n || window <= 0 {
		return rows
	}
	width := window / time.Duration(n)
	if width <= 0 {
		return rows
	}
	out := make([]models.VMMetricSample, 0, n)
	var (
		acc                  models.VMMetricSample
		cpu, mem             float64
		used, rd, wr, rx, tx int64
		count                int64
		slice                int64 = -1
	)
	flush := func() {
		if count == 0 {
			return
		}
		acc.CPUUsage = cpu / float64(count)
		acc.MemoryUsage = mem / float64(count)
		acc.MemoryUsedBytes = used / count
		acc.DiskReadBytesPerSec = rd / count
		acc.DiskWriteBytesPerSec = wr / count
		acc.NetworkRxBytesPerSec = rx / count
		acc.NetworkTxBytesPerSec = tx / count
		out = append(out, acc)
	}
	for _, r := range rows {
		at := int64(r.RecordedAt.Sub(since) / width)
		if at != slice {
			flush()
			slice, count = at, 0
			cpu, mem, used, rd, wr, rx, tx = 0, 0, 0, 0, 0, 0, 0
		}
		acc = models.VMMetricSample{ID: r.ID, VMID: r.VMID, RecordedAt: r.RecordedAt}
		cpu += r.CPUUsage
		mem += r.MemoryUsage
		used += r.MemoryUsedBytes
		rd += r.DiskReadBytesPerSec
		wr += r.DiskWriteBytesPerSec
		rx += r.NetworkRxBytesPerSec
		tx += r.NetworkTxBytesPerSec
		count++
	}
	flush()
	return out
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
	managedNets    *ManagedNetworkService
	repo           *repository.MetricsRepository
	db             *gorm.DB
	interval       time.Duration
	retention      time.Duration
	riverClient    *river.Client[pgx.Tx] // queues outbound billing events
	abuse          *AbuseService
	reputation     *ReputationService
	logger         *slog.Logger
	tick           int       // collectOnce counter, for slower-cadence work (IP reconcile)
	rolledHour     time.Time // the last hour averaged into vm_metrics_hourly
	rolledDay      time.Time // the last day averaged into vm_metrics_daily
	alerts         *AlertService

	onlineMu sync.Mutex
	online   map[string]bool // nodes whose agent port answered the last check

	trackMu sync.Mutex
	// repairs follows each node's count of host-rule repairs, to tell
	// operators when a node keeps losing VM rules.
	repairs map[string]*repairTrack
	// pendingAbsent holds VMs marked as waiting for an address change that
	// their node did not list in the last check; a second miss clears them.
	pendingAbsent map[string]bool

	// cephPolls holds the nodes whose Ceph check is still running.
	cephPolls sync.Map
}

// repairTrack is what the collector remembers of one node's repair count.
type repairTrack struct {
	last  int64
	times []time.Time
}

// repairAlertCount repairs within repairAlertWindow make an alert: once is a
// reboot or a hand edit, again and again is something on the node rewriting
// the rules.
const (
	repairAlertCount  = 3
	repairAlertWindow = time.Hour
)

// SetAlerts gives the collector the service that tells operators about a node
// going offline, a pool filling up or a node that stopped filtering traffic.
// Without one the collector still marks nodes offline; nobody is told.
func (c *MetricsCollector) SetAlerts(a *AlertService) {
	c.alerts = a
	if c.vmService != nil {
		c.vmService.SetAlerts(a)
	}
	if c.abuse != nil {
		c.abuse.SetAlerts(a)
	}
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
	nodeService := NewNodeService(nodeRepo, db)
	c := &MetricsCollector{
		nodeRepo:    nodeRepo,
		vmRepo:      vmRepo,
		networkRepo: networkRepo,
		storageRepo: repository.NewStorageRepository(db),
		nodeService: nodeService,
		// Shares the node service's connection pool: the abuse sample runs on the
		// same tick and against the same nodes, so a second pool would re-dial
		// every node for no gain.
		abuse: NewAbuseService(db, nodeService, logger),
		// Address reputation is checked on the same loop but far more slowly:
		// blocklists and AbuseIPDB both meter queries daily, and a fleet swept
		// every minute would exhaust the quota and then learn nothing.
		reputation: NewReputationService(db, logger),
		// riverClient lets the collector enqueue throttle/restore network jobs.
		vmService:      NewVMService(db, vmRepo, nodeRepo, repository.NewTemplateRepository(db), riverClient, logger),
		bwService:      NewBandwidthService(repository.NewBandwidthUsageRepository(db), logger),
		networkService: NewNetworkService(db, networkRepo, firewallRepo, vmRepo, nodeRepo, riverClient),
		managedNets:    NewManagedNetworkService(db),
		repo:           repository.NewMetricsRepository(db),
		db:             db,
		interval:       interval,
		retention:      retention,
		riverClient:    riverClient,
		logger:         logger,
		repairs:        map[string]*repairTrack{},
		pendingAbsent:  map[string]bool{},
	}
	c.abuse.SetNetworkService(c.networkService)
	return c
}

// Run samples immediately, then on each tick until ctx is cancelled.
func (c *MetricsCollector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	c.logger.Info("metrics collector started", "interval", c.interval.String(), "retention", c.retention.String())
	c.pollNodes(ctx)
	// Node checks keep their own clock. The work in collectOnce talks to every
	// VM on every node and can take minutes when one of them hangs; a node
	// going down must be noticed on time all the same.
	go c.watchNodes(ctx)
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

func (c *MetricsCollector) watchNodes(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.pollNodes(ctx)
		}
	}
}

// NodeReportedHook runs each time a node's agent answers a check. The panel
// sets it (MigrationService.NodeReported); it must not block.
var NodeReportedHook func(ctx context.Context, nodeID string)

// nodeCheck is what one check of a node means for it.
type nodeCheck struct {
	// Failed is how many checks in a row have gone without a report.
	Failed int
	// Offline: enough checks missed and the agent port does not answer.
	Offline bool
	// Silent: enough checks missed although the port answers, which is an
	// agent that is hung, refusing the panel, or too old to report.
	Silent bool
}

// nodeHealthAfterPoll counts a check. A report resets the count; a node is
// only called offline or silent once it has missed `after` checks in a row, so
// one lost packet or a restarting agent does not mark it down.
func nodeHealthAfterPoll(failed int, reported, reachable bool, after int) nodeCheck {
	if reported {
		return nodeCheck{}
	}
	if after < 1 {
		after = 1
	}
	h := nodeCheck{Failed: failed + 1}
	if h.Failed >= after {
		h.Offline, h.Silent = !reachable, reachable
	}
	return h
}

// poolAlertSeverity is how loudly a pool's fill level should be announced:
// nothing below the pool's own alert level, a warning from there, critical
// from the panel-wide critical level. A pool with its alert level set to 0 has
// alerts switched off.
func poolAlertSeverity(used, total int64, warnPercent, criticalPercent int) string {
	if total <= 0 || warnPercent <= 0 {
		return ""
	}
	pct := float64(used) * 100 / float64(total)
	switch {
	case criticalPercent > 0 && pct >= float64(criticalPercent):
		return models.AlertSeverityCritical
	case pct >= float64(warnPercent):
		return models.AlertSeverityWarning
	}
	return ""
}

// pollNodes checks every node at once, each under its own deadline, so a node
// that hangs delays nothing but its own answer. It stores the readings, keeps
// each node's status true and raises or clears the node alerts.
func (c *MetricsCollector) pollNodes(ctx context.Context) {
	nodes, err := c.nodeRepo.List(ctx, 0, 0)
	if err != nil {
		c.logger.Error("metrics collector: list nodes failed", "error", err)
		return
	}
	polls := make([]*NodeMetrics, len(nodes))
	var wg sync.WaitGroup
	for i := range nodes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			polls[i] = c.nodeService.PollNodeMetrics(mctx, &nodes[i])
		}(i)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return // shutting down: an interrupted check says nothing about a node
	}

	after := c.alerts.NodeOfflineAfter(ctx)
	online := make(map[string]bool, len(nodes))
	ids := make([]string, len(nodes))
	for i := range nodes {
		ids[i] = nodes[i].ID
		if c.recordPoll(ctx, &nodes[i], polls[i], after) {
			online[nodes[i].ID] = true
		}
	}
	// A node deleted while its alert was open would otherwise fire for ever.
	c.alerts.DropGone(ctx, "node", ids, "Closed: the node was removed")
	c.onlineMu.Lock()
	c.online = online
	c.onlineMu.Unlock()

	// Sending is slow and may hang on a mail server; it must not hold the next check.
	go c.alerts.Flush(context.WithoutCancel(ctx))
}

// recordPoll stores one node's check and reports whether its agent port answered.
func (c *MetricsCollector) recordPoll(ctx context.Context, node *models.Node, m *NodeMetrics, after int) bool {
	nodeID := node.ID
	reachable := m.Status == "online"
	health := nodeHealthAfterPoll(node.FailedPolls, m.Live, reachable, after)
	now := time.Now()

	// Only a report is a reading. A node that did not answer used to get a row
	// of zeros stamped with the current time, which drew a flat line in the
	// charts and told Prometheus the node had just reported.
	if m.Live {
		sample := &models.NodeMetricSample{
			NodeID:               nodeID,
			CPUUsage:             m.CPUUsage,
			MemoryUsage:          m.MemoryUsage,
			DiskUsage:            m.DiskUsage,
			NetworkRxBytesPerSec: m.NetworkRxBytesPerSec,
			NetworkTxBytesPerSec: m.NetworkTxBytesPerSec,
			VMCount:              m.VMCount,
			Status:               m.Status,
			ConntrackCount:       m.ConntrackCount,
			ConntrackMax:         m.ConntrackMax,
			RecordedAt:           now,
		}
		if err := c.repo.InsertNodeSample(ctx, sample); err != nil {
			c.logger.Error("metrics collector: insert node sample failed", "node_id", nodeID, "error", err)
		}
		// Persist physical RAM for overcommit admission. The samples table keeps
		// percentages only, and this total is otherwise read live and thrown
		// away — but admission runs inside a DB transaction and cannot call the
		// agent. Only written when the node actually reported a total, so a
		// failed read never overwrites a good value with 0 ("unmeasured").
		if m.MemoryTotal > 0 && m.MemoryTotal != node.MemoryTotalBytes {
			if err := c.db.WithContext(ctx).Model(&models.Node{}).
				Where("id = ?", nodeID).
				Update("memory_total_bytes", m.MemoryTotal).Error; err != nil {
				c.logger.Warn("metrics collector: persist node memory total failed", "node_id", nodeID, "error", err)
			}
		}
		// Logical CPUs, for the vCPU ratio, kept the same way. By table name:
		// the model field is read-only.
		if m.AvailableCPUs > 0 && m.AvailableCPUs != node.CPUCount {
			if err := c.db.WithContext(ctx).Table("nodes").Where("id = ?", nodeID).
				Update("cpu_count", m.AvailableCPUs).Error; err != nil {
				c.logger.Warn("metrics collector: persist node CPU count failed", "node_id", nodeID, "error", err)
			}
		}
		// Persist what the node says about its firewall, so the security centre
		// and the VM firewall page can read it without calling the agent. Only
		// from a live answer: a node that did not answer keeps its last state.
		stored := m.FirewallEnforcement
		if stored == models.FirewallUnknown {
			stored = ""
		}
		if err := c.db.WithContext(ctx).Table("nodes").
			Where("id = ? AND firewall_enforcement <> ?", nodeID, stored).
			Update("firewall_enforcement", stored).Error; err != nil {
			c.logger.Warn("metrics collector: persist node firewall state failed", "node_id", nodeID, "error", err)
		}
		// The address NAT VMs' ports answer on, shown to their owners and used
		// to verify their hostnames.
		if err := c.db.WithContext(ctx).Table("nodes").
			Where("id = ? AND nat_address <> ?", nodeID, m.NATAddress).
			Update("nat_address", m.NATAddress).Error; err != nil {
			c.logger.Warn("metrics collector: persist node NAT address failed", "node_id", nodeID, "error", err)
		}
		c.syncGuestPending(ctx, nodeID, m.GuestNetworkPending)
		c.noteRepairs(ctx, node, m)
		// A node that answers is told, until it has heard, which of its VMs
		// were recovered onto other nodes while it was down.
		if NodeReportedHook != nil {
			NodeReportedHook(ctx, nodeID)
		}
	}
	// By table name: the model fields are read-only (see models.Node).
	seen := map[string]interface{}{"failed_polls": health.Failed}
	if m.Live {
		seen["last_report_at"] = now
	}
	if err := c.db.WithContext(ctx).Table("nodes").Where("id = ?", nodeID).Updates(seen).Error; err != nil {
		c.logger.Warn("metrics collector: persist node check failed", "node_id", nodeID, "error", err)
	}

	switch {
	case health.Offline && node.Status == models.NodeStatusActive:
		// Placement and every job read this status, so a dead node stops
		// receiving orders here and not when someone opens the Nodes page.
		if err := c.moveNodeStatus(ctx, nodeID, models.NodeStatusActive, models.NodeStatusOffline); err != nil {
			c.logger.Warn("metrics collector: mark node offline failed", "node_id", nodeID, "error", err)
		} else {
			c.logger.Warn("metrics collector: node marked offline", "node_id", nodeID, "missed_checks", health.Failed)
		}
	case reachable && node.Status == models.NodeStatusOffline:
		// A node marked offline stays offline, and every job for it fails with
		// "node is not active", until something marks it active again.
		if err := c.moveNodeStatus(ctx, nodeID, models.NodeStatusOffline, models.NodeStatusActive); err != nil {
			c.logger.Warn("metrics collector: mark node active failed", "node_id", nodeID, "error", err)
		} else {
			// Changes made while it was away never reached it: their jobs gave
			// up on a dead node. Send every VM's full network config again.
			n := 0
			if c.networkService != nil {
				n = c.networkService.ResyncNodeVMs(ctx, nodeID)
			}
			c.logger.Info("metrics collector: node back online, marked active", "node_id", nodeID, "network_resyncs", n)
		}
	}

	c.alertNode(ctx, node, m, health)
	return reachable
}

// moveNodeStatus changes a node's status only while it is still what the check
// read: a node put into maintenance during the check stays in maintenance.
func (c *MetricsCollector) moveNodeStatus(ctx context.Context, nodeID string, from, to models.NodeStatus) error {
	return c.db.WithContext(ctx).Model(&models.Node{}).Where("id = ? AND status = ?", nodeID, from).Update("status", to).Error
}

// alertNode raises and clears the alerts about one node from its last check.
func (c *MetricsCollector) alertNode(ctx context.Context, node *models.Node, m *NodeMetrics, health nodeCheck) {
	if c.alerts == nil {
		return
	}
	offline, silent, firewall := "node_offline:"+node.ID, "node_silent:"+node.ID, "firewall_off:"+node.ID
	if node.Status == models.NodeStatusMaintenance {
		// The operator took it out of service and knows why it is quiet.
		for _, key := range []string{offline, silent} {
			c.alerts.Drop(ctx, key, "Closed: the node was put into maintenance")
		}
		return
	}
	lastReport := "It has never reported."
	if node.LastReportAt != nil {
		lastReport = "Last report " + node.LastReportAt.UTC().Format("2006-01-02 15:04 UTC") + "."
	}
	in := AlertInput{ResourceType: "node", ResourceID: node.ID, ResourceName: node.Name}

	// A node that has never reported was added and not set up yet; it did not go down.
	if health.Offline && node.LastReportAt != nil {
		in.Key, in.Kind, in.Severity = offline, models.AlertKindNodeOffline, models.AlertSeverityCritical
		in.Title = "Node " + node.Name + " is offline"
		in.Detail = fmt.Sprintf("It has missed %d checks in a row. %s No new VMs are placed on it, and the state of the VMs on it is unknown.", health.Failed, lastReport)
		c.alerts.Raise(ctx, in)
	} else {
		c.alerts.Clear(ctx, offline)
	}
	if health.Silent {
		in.Key, in.Kind, in.Severity = silent, models.AlertKindNodeSilent, models.AlertSeverityWarning
		in.Title = "Node " + node.Name + " is not reporting"
		in.Detail = fmt.Sprintf("Its agent port answers, but the agent has not reported for %d checks. %s The agent may be hung, may refuse the panel, or may need an update.", health.Failed, lastReport)
		c.alerts.Raise(ctx, in)
	} else {
		c.alerts.Clear(ctx, silent)
	}
	// Only a node that said so: no answer, or an agent too old to say, changes nothing.
	if m.Live {
		switch m.FirewallEnforcement {
		case models.FirewallOff:
			in.Key, in.Kind, in.Severity = firewall, models.AlertKindFirewallOff, models.AlertSeverityCritical
			in.Title = "Node " + node.Name + " is not filtering guest traffic"
			in.Detail = "Bridge netfilter is off on this node, so VM firewall rules and address-spoofing protection are not enforced there."
			c.alerts.Raise(ctx, in)
		case models.FirewallEnforced:
			c.alerts.Clear(ctx, firewall)
		}
	}
}

// alertPool raises or clears the fill-level alert of a pool just measured.
func (c *MetricsCollector) alertPool(ctx context.Context, node *models.Node, p *models.StoragePool) {
	if c.alerts == nil {
		return
	}
	key := "pool_usage:" + p.ID
	severity := poolAlertSeverity(p.UsedSpace, p.TotalSpace, p.AlertThreshold, c.alerts.StorageCriticalPercent(ctx))
	if severity == "" {
		c.alerts.Clear(ctx, key)
		return
	}
	title, detail := poolAlertText(p, node.Name, severity)
	c.alerts.Raise(ctx, AlertInput{
		Key: key, Kind: models.AlertKindStoragePool, Severity: severity, Title: title, Detail: detail,
		ResourceType: "storage_pool", ResourceID: p.ID, ResourceName: p.Name,
	})
}

// poolAlertText words a pool's fill-level alert: nearly full at the warning
// level, full at the critical one, with what stops working and when.
func poolAlertText(p *models.StoragePool, nodeName, severity string) (string, string) {
	const gb = 1 << 30
	word := "nearly full"
	if severity == models.AlertSeverityCritical {
		word = "full"
	}
	title := fmt.Sprintf("Storage pool %s on %s is %s (%d%% used)", p.Name, nodeName, word, p.UsedSpace*100/p.TotalSpace)
	stop := fmt.Sprintf("New VMs and disks are refused on it once one would leave less than %d GB free.", diskHeadroomBytes/gb)
	if p.AvailableSpace < diskHeadroomBytes {
		stop = fmt.Sprintf("It has less than %d GB free, so new VMs and disks are already refused on it.", diskHeadroomBytes/gb)
	}
	detail := fmt.Sprintf("%.1f GB free of %.1f GB. %s Thin disks keep growing as guests write: when the pool runs out, every VM writing to it is paused until space is freed. Free space or move VMs off it now.",
		float64(p.AvailableSpace)/gb, float64(p.TotalSpace)/gb, stop)
	return title, detail
}

// collectOnce does the per-node and per-VM work on every node that answered
// the last check, then prunes stale samples. Per-target failures are logged
// and skipped so one unreachable node/VM never blocks the rest.
func (c *MetricsCollector) collectOnce(ctx context.Context) {
	nodes, err := c.nodeRepo.List(ctx, 0, 0)
	if err != nil {
		c.logger.Error("metrics collector: list nodes failed", "error", err)
		return
	}
	c.onlineMu.Lock()
	online := make(map[string]bool, len(c.online))
	for id := range c.online {
		online[id] = true
	}
	c.onlineMu.Unlock()

	// Each node's work runs beside the others', so a node that answers slowly
	// holds up only itself. Within a node it stays in order: VPCs before the
	// floating IPs that live in them.
	c.tick++
	ipTick := c.tick%5 == 1
	byID := make(map[string]*models.Node, len(nodes))
	var onlineIDs []string
	for i := range nodes {
		if online[nodes[i].ID] {
			byID[nodes[i].ID] = &nodes[i]
			onlineIDs = append(onlineIDs, nodes[i].ID)
		}
	}
	c.eachNode(ctx, onlineIDs, func(ctx context.Context, nodeID string) {
		// Guest connection rates: the only view that catches a compromised
		// guest, and the only one that sees guests the panel does not manage.
		actx, acancel := context.WithTimeout(ctx, 20*time.Second)
		if err := c.abuse.SampleNode(actx, nodeID); err != nil {
			c.logger.Warn("metrics collector: guest connection sample failed", "node_id", nodeID, "error", err)
		}
		acancel()
		// Keep each node's default storage pool present and its capacity
		// live from the node's real disk, so /storage reflects the node.
		c.syncStoragePools(ctx, byID[nodeID])
		c.pollCeph(ctx, byID[nodeID])

		// Reconcile DB VM status against reality, so a VM started, stopped or
		// crashed out of band is reflected in the panel (otherwise status only
		// changes on an explicit lifecycle command and drifts stale).
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		c.vmService.ReconcileNodeVMStatuses(rctx, nodeID)
		c.vmService.ReapStuckVMs(rctx, nodeID)
		cancel()

		// Less often (every ~5 ticks), ARP-reconcile the node's pool IPs so
		// addresses used by VMs the panel doesn't manage (pre-existing imported
		// guests) show as reserved and are never allocated. It's heavier (one
		// ARP per candidate IP) so it doesn't need to run every tick.
		if !ipTick {
			return
		}
		rctx, cancel = context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		c.vmService.ReconcileNodePoolIPs(rctx, nodeID)
		// VPCs before floating IPs: a floating IP in a VPC lives in its
		// router namespace, which a reboot removes.
		if err := c.managedNets.ReconcileNodeVPCs(rctx, nodeID); err != nil {
			c.logger.Warn("metrics collector: VPC reconcile failed", "node_id", nodeID, "error", err)
		}
		// Floating IPs are pure iptables/host-address state, so a node reboot
		// silently drops them. Re-applying the desired set (attach is
		// idempotent) is what makes them survive one.
		c.vmService.ReconcileFloatingIPs(rctx, nodeID)
	})
	// After every node's pools are synced: one made this tick is not "gone".
	var poolIDs []string
	if err := c.db.WithContext(ctx).Model(&models.StoragePool{}).Pluck("id", &poolIDs).Error; err == nil {
		c.alerts.DropGone(ctx, "storage_pool", poolIDs, "Closed: the pool was removed")
	}

	c.collectVMs(ctx, online)

	// Restore any throttled VMs whose quota has reset for the new period.
	c.restoreResetThrottles(ctx)

	// Address reputation, on a much slower cadence and in small batches. The
	// external services meter by the day, so sweeping the whole fleet at once
	// would burn the quota and leave the rest unchecked while reporting nothing
	// wrong. Assigned addresses are checked first inside the service: a listing
	// on an address nobody uses costs nothing today, one on a customer's address
	// is costing them right now.
	if c.tick%20 == 1 {
		rctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		if n := c.reputation.CheckDueAddresses(rctx, 24*time.Hour, 25); n > 0 {
			c.logger.Info("reputation: addresses checked", "count", n)
		}
		cancel()
	}

	c.rollupHours(ctx)

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

// collectorNodeParallelism is how many nodes the collector works on at once.
// ponytail: fixed; make it a setting if a fleet outgrows it (the per-node
// passes then queue, they are never skipped).
const collectorNodeParallelism = 8

// vmPassBudget is the longest one node's VM sampling may take in a tick. A
// node whose agent answers each VM slowly would otherwise stretch the tick by
// 30 seconds a VM; what is left is sampled on a later tick.
const vmPassBudget = 3 * time.Minute

// eachNode runs fn for every node, up to collectorNodeParallelism at once,
// and returns when all are done. A panic in one node's work is logged and
// costs only that node's pass.
func (c *MetricsCollector) eachNode(ctx context.Context, nodeIDs []string, fn func(ctx context.Context, nodeID string)) {
	sem := make(chan struct{}, collectorNodeParallelism)
	var wg sync.WaitGroup
	for _, id := range nodeIDs {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer func() {
				if p := recover(); p != nil {
					c.logger.Error("metrics collector: a node's pass panicked", "node_id", id, "panic", p)
				}
				<-sem
				wg.Done()
			}()
			fn(ctx, id)
		}(id)
	}
	wg.Wait()
}

// vmsByNode groups the VMs on online nodes, each node's list starting at a
// different place every tick, so a pass cut short by vmPassBudget does not
// always leave out the same VMs.
func vmsByNode(vms []models.VM, online map[string]bool, tick int) (order []string, byNode map[string][]models.VM) {
	byNode = map[string][]models.VM{}
	for _, vm := range vms {
		if !online[vm.NodeID] {
			continue
		}
		if _, seen := byNode[vm.NodeID]; !seen {
			order = append(order, vm.NodeID)
		}
		byNode[vm.NodeID] = append(byNode[vm.NodeID], vm)
	}
	for id, list := range byNode {
		k := tick % len(list)
		byNode[id] = slices.Concat(list[k:], list[:k])
	}
	return order, byNode
}

// collectVMs samples each running VM that lives on an online node: the nodes
// side by side, each node's VMs one after another. Sampling a VM on an
// offline node is skipped to avoid wasting the tick on calls that will time out.
func (c *MetricsCollector) collectVMs(ctx context.Context, online map[string]bool) {
	vms, err := c.vmRepo.ListByStatus(ctx, models.VMStatusRunning, 0, 0)
	if err != nil {
		c.logger.Error("metrics collector: list running vms failed", "error", err)
		return
	}
	order, byNode := vmsByNode(vms, online, c.tick)
	c.eachNode(ctx, order, func(ctx context.Context, nodeID string) {
		pass, cancel := context.WithTimeout(ctx, vmPassBudget)
		defer cancel()
		list := byNode[nodeID]
		for i, vm := range list {
			if pass.Err() != nil {
				if ctx.Err() == nil {
					c.logger.Warn("metrics collector: node too slow, VMs left for the next tick", "node_id", nodeID, "skipped", len(list)-i)
				}
				return
			}
			c.sampleVM(pass, vm)
		}
	})
}

// sampleVM stores one VM's reading and counts its traffic.
func (c *MetricsCollector) sampleVM(ctx context.Context, vm models.VM) {
	mctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	m, err := c.vmService.GetVMMetrics(mctx, vm.NodeID, vm.ID)
	cancel()
	if err != nil {
		c.logger.Warn("metrics collector: get vm metrics failed", "vm_id", vm.ID, "error", err)
		return
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

// accountBandwidth pulls the VM's cumulative traffic counters and accumulates
// the delta into the current billing period, enforces the monthly quota when
// enforcement is on (System settings, or BANDWIDTH_ENFORCE before that), and
// warns the owner as usage passes each warning level.
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
		// A pool goes over together, and its VMs may live on other nodes.
		node := nodeID
		if exVM != vmID {
			if vm, err := c.vmRepo.GetByID(ctx, exVM); err == nil {
				node = vm.NodeID
			}
		}
		c.enforceOverQuota(ctx, node, exVM)
	}
	c.warnUsage(ctx, vmID, nodeID)
}

// syncGuestPending clears the "waiting for an address change" mark of VMs on
// a node that its agent no longer lists: the guest took the change. A VM must
// be missing from two checks in a row, so a mark written between a check and
// its processing is not cleared by that check.
func (c *MetricsCollector) syncGuestPending(ctx context.Context, nodeID string, pending []string) {
	var marked []string
	if err := c.db.WithContext(ctx).Table("vms").Where("node_id = ? AND guest_network_pending = ?", nodeID, true).
		Pluck("id", &marked).Error; err != nil || len(marked) == 0 {
		return
	}
	listed := make(map[string]bool, len(pending))
	for _, id := range pending {
		listed[id] = true
	}
	var clear []string
	c.trackMu.Lock()
	if c.pendingAbsent == nil {
		c.pendingAbsent = map[string]bool{}
	}
	for _, id := range marked {
		switch {
		case listed[id]:
			delete(c.pendingAbsent, id)
		case c.pendingAbsent[id]:
			delete(c.pendingAbsent, id)
			clear = append(clear, id)
		default:
			c.pendingAbsent[id] = true
		}
	}
	c.trackMu.Unlock()
	if len(clear) == 0 {
		return
	}
	if err := c.db.WithContext(ctx).Table("vms").Where("id IN ? AND guest_network_pending = ?", clear, true).
		Updates(map[string]interface{}{"guest_network_note": nil, "guest_network_pending": false}).Error; err != nil {
		c.logger.Warn("metrics collector: clearing taken address changes failed", "node_id", nodeID, "error", err)
		return
	}
	c.logger.Info("metrics collector: guests took their pending address change", "node_id", nodeID, "vms", clear)
}

// noteRepairs logs host-rule repairs a node reports and raises an alert when
// they keep happening.
func (c *MetricsCollector) noteRepairs(ctx context.Context, node *models.Node, m *NodeMetrics) {
	c.trackMu.Lock()
	if c.repairs == nil {
		c.repairs = map[string]*repairTrack{}
	}
	t, seen := c.repairs[node.ID]
	if !seen {
		// The first answer after a panel restart is the starting point.
		c.repairs[node.ID] = &repairTrack{last: m.NetworkRepairs}
		c.trackMu.Unlock()
		return
	}
	// A count lower than before is an agent restart, not a repair.
	added := m.NetworkRepairs - t.last
	t.last = m.NetworkRepairs
	if added <= 0 {
		c.trackMu.Unlock()
		return
	}
	now := time.Now()
	for i := int64(0); i < added && i < repairAlertCount; i++ {
		t.times = append(t.times, now)
	}
	kept := t.times[:0]
	for _, at := range t.times {
		if now.Sub(at) <= repairAlertWindow {
			kept = append(kept, at)
		}
	}
	t.times = kept
	recent := len(kept)
	c.trackMu.Unlock()

	c.logger.Warn("node re-applied VM host rules that were changed or missing", "node_id", node.ID, "node", node.Name,
		"repairs", added, "last", m.NetworkRepairLast)
	if recent < repairAlertCount {
		return
	}
	c.alerts.Event(ctx, AlertInput{
		Key: "network_drift:" + node.ID, Kind: models.AlertKindNetworkDrift, Severity: models.AlertSeverityWarning,
		Title: "Node " + node.Name + " keeps losing VM network rules",
		Detail: fmt.Sprintf("Its agent found VM firewall, anti-spoofing or NAT rules changed or missing and put them back %d times in the last hour, so something on the node is rewriting them. Until that stops, a VM can be unprotected for up to five minutes at a time. Last: %s",
			recent, m.NetworkRepairLast),
		ResourceType: "node", ResourceID: node.ID, ResourceName: node.Name,
	})
}

// warnUsage emails a VM's owner the first time each month its transfer reaches
// a warning level. The level is recorded before sending, so a failed send is
// not retried into a burst of mail.
func (c *MetricsCollector) warnUsage(ctx context.Context, vmID, nodeID string) {
	levels := UsageWarnPercents(ctx, c.db)
	if len(levels) == 0 {
		return
	}
	usage, err := c.bwService.GetVMUsage(ctx, vmID, nodeID)
	if err != nil || usage == nil {
		return
	}
	used, quota, pooled := c.bwService.EffectiveUsage(ctx, usage)
	if quota <= 0 {
		return
	}
	level := UsageWarningLevel(used, quota, usage.WarnedPercent, levels)
	if level == 0 {
		return
	}
	// A pool is warned once, not once per VM in it: every row is marked
	// together and only the update that marked them sends.
	ids := []string{usage.ID}
	if pooled {
		ids = c.bwService.Pool(ctx, vmID).UsageIDs
	}
	res := c.db.WithContext(ctx).Table("bandwidth_usages").Where("id IN ? AND warned_percent < ?", ids, level).Update("warned_percent", level)
	if res.Error != nil || res.RowsAffected == 0 {
		return
	}
	extra := c.transferWarningFields(ctx, vmID, usage, used, quota, pooled)
	go func() {
		if err := NotifyVMOwnerWith(context.WithoutCancel(ctx), c.db, vmID, EmailTransferWarn, extra); err != nil {
			c.logger.Warn("transfer warning email failed", "vm_id", vmID, "level", level, "error", err)
		}
	}()
	c.logger.Info("transfer warning due", "vm_id", vmID, "level", level, "used_bytes", used, "quota_bytes", quota, "pooled", pooled)
}

// transferWarningFields fills the transfer placeholders of a warning email
// from what the allowance is measured against: the VM's own, or its pool's.
func (c *MetricsCollector) transferWarningFields(ctx context.Context, vmID string, u *models.BandwidthUsage, used, quota int64, pooled bool) map[string]string {
	const gb = 1024 * 1024 * 1024
	pct := float64(used) * 100 / float64(quota)
	after := "Nothing changes once it is used up."
	if net, err := c.networkRepo.GetByVMID(ctx, vmID); err == nil {
		enforced, _ := BandwidthEnforcement(ctx, c.db)
		after = OverQuotaOutcome(net.OverQuotaPolicy, net.ThrottleSpeedMbps, enforced, pct >= 100)
	}
	quotaText := fmt.Sprintf("%.0f GB", float64(quota)/gb)
	if pooled {
		quotaText += " (shared by your account's servers)"
	}
	return map[string]string{
		"used":    fmt.Sprintf("%.1f GB", float64(used)/gb),
		"quota":   quotaText,
		"percent": fmt.Sprintf("%.0f%%", pct),
		// The first day of the next period, not the last of this one.
		"resets": u.PeriodStart.AddDate(0, 1, 0).Format("2 January"),
		"after":  after,
	}
}

// OverQuotaOutcome says in one sentence what happens to a VM over its transfer
// quota as it is set up: its policy, and whether restrictions are enforced.
func OverQuotaOutcome(policy string, throttleMbps int, enforced, reached bool) string {
	when := "Once it is used up, "
	if reached {
		when = "It is used up, so "
	}
	switch policy {
	case models.OverQuotaOverage:
		return when + "further transfer is billed as extra usage."
	case models.OverQuotaSuspend:
		if enforced {
			return when + "the server is stopped until the allowance starts again."
		}
	default:
		if enforced {
			if throttleMbps <= 0 {
				throttleMbps = defaultThrottleMbps
			}
			return fmt.Sprintf("%sthe server's speed is lowered to %d Mbps until the allowance starts again.", when, throttleMbps)
		}
	}
	return when + "the server keeps running at its usual speed."
}

// defaultPoolPath mirrors the agent's defaultImageDir: where VM disks and
// provisioned volumes live on each node.
const defaultPoolPath = "/var/lib/libvirt/images"

// syncDefaultStoragePool ensures each online node has a 'local' dir storage pool
// at the node's image dir and refreshes its capacity/status from the node's live
// disk metrics, so /storage shows real per-node storage instead of static values.
// syncStoragePools refreshes every pool on a node from the filesystem that
// actually backs it, and records storage in use that no pool covers.
//
// It replaces a version that took the node's ROOT filesystem usage and wrote it
// onto whichever pool pointed at the default image directory. That was wrong in
// both directions at once on a live node: the pool directory was empty and
// reported 104 GB in use, while the separate volume holding all 24 customers'
// disks sat at 76% full and was not represented at all. An operator reading the
// panel saw 89% free on storage that had 214 GB left.
func (c *MetricsCollector) syncStoragePools(ctx context.Context, node *models.Node) {
	pools, err := c.storageRepo.GetPoolsByNodeID(node.ID)
	if err != nil {
		c.logger.Error("storage sync: list pools failed", "node_id", node.ID, "error", err)
		return
	}

	paths := make([]string, 0, len(pools)+1)
	seen := map[string]bool{}
	var lvmPools []*pb.LVMPool
	for i := range pools {
		if pools[i].Type == models.PoolTypeRBD {
			continue // no directory on the node: measured with its cluster (pollCeph)
		}
		if models.IsLVMPoolType(pools[i].Type) {
			if vg, thin, err := models.SplitLVMPoolPath(pools[i].Type, pools[i].Path); err == nil {
				lvmPools = append(lvmPools, &pb.LVMPool{Vg: vg, ThinPool: thin})
			}
			continue
		}
		if p := pools[i].Path; p != "" && !seen[p] {
			paths = append(paths, p)
			seen[p] = true
		}
	}
	// Always measure the default directory, so a node with no pools yet still
	// gets one created with real numbers.
	if !seen[defaultPoolPath] {
		paths = append(paths, defaultPoolPath)
	}

	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	report, err := c.nodeService.AgentClient().GetStorageReport(rctx, node.ID, paths, lvmPools)
	cancel()
	if err != nil {
		c.logger.Warn("storage sync: report failed", "node_id", node.ID, "error", err)
		return
	}

	byPath := make(map[string]client.PoolCapacity, len(report.Pools))
	for _, p := range report.Pools {
		byPath[p.Path] = p
	}
	byLVM := make(map[string]*pb.LVMPoolCapacity, len(report.LVMPools))
	for _, p := range report.LVMPools {
		byLVM[p.GetVg()+"/"+p.GetThinPool()] = p
	}

	for i := range pools {
		if pools[i].Type == models.PoolTypeRBD {
			continue
		}
		if models.IsLVMPoolType(pools[i].Type) {
			measureLVMPool(&pools[i], byLVM)
			if err := c.storageRepo.UpdatePoolMeasurement(&pools[i]); err != nil {
				c.logger.Error("storage sync: update pool failed", "pool_id", pools[i].ID, "error", err)
			}
			if pools[i].Status == "online" {
				c.alertPool(ctx, node, &pools[i])
				c.alertThinMetadata(ctx, node, &pools[i])
			}
			continue
		}
		cap, ok := byPath[pools[i].Path]
		if !ok {
			continue
		}
		// A pool whose path is missing on the node is degraded, not empty.
		// Leaving it "online" with zero usage would read as a healthy empty pool
		// and invite an operator to place VMs on storage that is not there.
		if !cap.Exists {
			pools[i].Status = "degraded"
			pools[i].StatusNote = "The directory does not exist on the node."
			if err := c.storageRepo.UpdatePoolMeasurement(&pools[i]); err != nil {
				c.logger.Error("storage sync: update pool failed", "pool_id", pools[i].ID, "error", err)
			}
			continue
		}
		// A shared pool that is not mounted on this node: the node says so, or
		// the pool's marker cannot be read there. It takes no disks here until
		// it is back.
		problem := cap.Problem
		if problem == "" && pools[i].SharedPoolID != nil && c.vmService != nil {
			problem = NewSharedPoolService(c.db, c.vmService).MemberProblem(ctx, *pools[i].SharedPoolID, node)
		}
		if problem != "" {
			pools[i].Status = "degraded"
			pools[i].StatusNote = problem
			if err := c.storageRepo.UpdatePoolMeasurement(&pools[i]); err != nil {
				c.logger.Error("storage sync: update pool failed", "pool_id", pools[i].ID, "error", err)
			}
			c.alertSharedMount(ctx, node, &pools[i], problem)
			continue
		}
		c.alertSharedMount(ctx, node, &pools[i], "")
		pools[i].TotalSpace = cap.Total
		pools[i].UsedSpace = cap.Used
		pools[i].AvailableSpace = cap.Available
		pools[i].Status = "online"
		pools[i].StatusNote = ""
		if err := c.storageRepo.UpdatePoolMeasurement(&pools[i]); err != nil {
			c.logger.Error("storage sync: update pool failed", "pool_id", pools[i].ID, "error", err)
		}
		c.alertPool(ctx, node, &pools[i])
	}

	if !seen[defaultPoolPath] {
		if cap, ok := byPath[defaultPoolPath]; ok && cap.Exists {
			// Primary only on a node with no pools: recreating a deleted 'local'
			// as primary would send new VMs back to the root filesystem.
			if err := c.storageRepo.CreatePool(&models.StoragePool{
				Name:           "local",
				Type:           "dir",
				Status:         "online",
				Path:           defaultPoolPath,
				FileFormat:     "qcow2",
				IsPrimary:      len(pools) == 0,
				NodeID:         node.ID,
				TotalSpace:     cap.Total,
				UsedSpace:      cap.Used,
				AvailableSpace: cap.Available,
			}); err != nil {
				c.logger.Error("storage sync: create default pool failed", "node_id", node.ID, "error", err)
			}
		}
	}

	c.warnUnregisteredStorage(node, pools, report.DiskLocations)
}

// alertSharedMount raises, or with no problem clears, the alert of a shared
// pool that is not mounted on one of its member nodes.
func (c *MetricsCollector) alertSharedMount(ctx context.Context, node *models.Node, p *models.StoragePool, problem string) {
	alertSharedMember(ctx, c.alerts, node, p, problem)
}

// alertSharedMember is alertSharedMount for any caller: p is the node's row
// for the shared pool.
func alertSharedMember(ctx context.Context, alerts *AlertService, node *models.Node, p *models.StoragePool, problem string) {
	if alerts == nil || p.SharedPoolID == nil {
		return
	}
	key := "pool_shared:" + p.ID
	if problem == "" {
		alerts.Clear(ctx, key)
		return
	}
	alerts.Raise(ctx, AlertInput{
		Key: key, Kind: models.AlertKindStoragePool, Severity: models.AlertSeverityCritical,
		Title:        fmt.Sprintf("Shared pool %s is not usable on %s", p.Name, node.Name),
		Detail:       problem + " VMs on this node with disks in the pool cannot start, and no new disk is placed there, until it is usable again.",
		ResourceType: "storage_pool", ResourceID: p.ID, ResourceName: p.Name,
	})
}

// pollCeph checks the Ceph clusters enabled on a node, beside the node's
// other work: a cluster that answers slowly holds up nothing else, and a
// check still running when the next tick comes is not started twice. A fleet
// without Ceph pays one small query per node.
func (c *MetricsCollector) pollCeph(ctx context.Context, node *models.Node) {
	if c.vmService == nil {
		return
	}
	svc := NewCephService(c.db, c.vmService, c.alerts)
	if !svc.EnabledOn(ctx, node.ID) {
		return
	}
	if _, running := c.cephPolls.LoadOrStore(node.ID, true); running {
		return
	}
	go func() {
		defer c.cephPolls.Delete(node.ID)
		defer func() {
			if p := recover(); p != nil {
				c.logger.Error("metrics collector: a Ceph check panicked", "node_id", node.ID, "panic", p)
			}
		}()
		pctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		svc.Poll(pctx, node)
	}()
}

// measureLVMPool applies what the node reported for an LVM pool. A pool the
// node did not report, or reported unusable, is degraded with the reason: it
// takes no new disks until the node says it can.
func measureLVMPool(p *models.StoragePool, byLVM map[string]*pb.LVMPoolCapacity) {
	p.MetadataPercent = nil
	vg, thin, err := models.SplitLVMPoolPath(p.Type, p.Path)
	if err != nil {
		p.Status, p.StatusNote = "degraded", err.Error()
		return
	}
	c, ok := byLVM[vg+"/"+thin]
	switch {
	case !ok:
		p.Status, p.StatusNote = "degraded", "This node's agent cannot use LVM pools yet; update it under Settings, System."
		return
	case !c.GetExists():
		p.Status, p.StatusNote = "degraded", c.GetError()
		return
	}
	p.TotalSpace, p.UsedSpace, p.AvailableSpace = c.GetTotalBytes(), c.GetUsedBytes(), c.GetAvailableBytes()
	if thin != "" {
		meta := c.GetMetadataPercent()
		p.MetadataPercent = &meta
	}
	if c.GetError() != "" {
		p.Status, p.StatusNote = "degraded", c.GetError()
		return
	}
	p.Status, p.StatusNote = "online", ""
}

// Thin pool metadata levels. Metadata fills on its own schedule, and when it
// is full every thin volume in the pool stops taking writes.
const (
	thinMetadataWarnPercent     = 75
	thinMetadataCriticalPercent = 90
)

// alertThinMetadata raises or clears a thin pool's metadata alert.
func (c *MetricsCollector) alertThinMetadata(ctx context.Context, node *models.Node, p *models.StoragePool) {
	if c.alerts == nil || p.MetadataPercent == nil {
		return
	}
	key := "pool_metadata:" + p.ID
	severity := poolAlertSeverity(int64(*p.MetadataPercent*100), 10000, thinMetadataWarnPercent, thinMetadataCriticalPercent)
	if severity == "" {
		c.alerts.Clear(ctx, key)
		return
	}
	c.alerts.Raise(ctx, AlertInput{
		Key: key, Kind: models.AlertKindStoragePool, Severity: severity,
		Title: fmt.Sprintf("Thin pool %s on %s has used %.0f%% of its metadata", p.Name, node.Name, *p.MetadataPercent),
		Detail: "When a thin pool's metadata is full, every VM disk in it stops taking writes, however much data space is free. " +
			"Grow it on the node (lvextend --poolmetadatasize) before it fills.",
		ResourceType: "storage_pool", ResourceID: p.ID, ResourceName: p.Name,
	})
}

// warnUnregisteredStorage logs directories holding domain disks that no pool
// covers.
//
// This is how the original fault would have been caught: both nodes kept nearly
// every customer disk on a mount the panel had no pool for, so nothing watched
// the filesystem that mattered. Logged per tick rather than stored, because the
// remedy is for an operator to register the pool — at which point the message
// stops on its own.
func (c *MetricsCollector) warnUnregisteredStorage(node *models.Node, pools []models.StoragePool, locations []client.DiskLocation) {
	if len(locations) == 0 {
		return
	}
	covered := make(map[string]bool, len(pools))
	for i := range pools {
		if vg, _, err := models.SplitLVMPoolPath(pools[i].Type, pools[i].Path); err == nil {
			covered["/dev/"+vg] = true
			continue
		}
		covered[strings.TrimSuffix(pools[i].Path, "/")] = true
	}
	for _, loc := range locations {
		if covered[strings.TrimSuffix(loc.Path, "/")] {
			continue
		}
		c.logger.Warn("storage sync: domain disks live outside any registered pool",
			"node_id", node.ID, "path", loc.Path, "disks", loc.DiskCount)
	}
}

// enforceOverQuota acts on a VM that just crossed its monthly data quota,
// following the over-quota policy snapshotted on its network interface:
//   - overage: emit a billing webhook (VM keeps full speed). Fires whether or
//     not enforcement is on — it bills, it doesn't restrict.
//   - throttle: drop the live speed to ThrottleSpeedMbps (when enforced).
//   - suspend: stop the VM (when enforced).
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
		if on, _ := BandwidthEnforcement(ctx, c.db); !on {
			c.logger.Warn("over-quota suspend skipped (enforcement is off)", "vm_id", vmID)
			return
		}
		if err := c.vmService.StopVMForEnforcement(ctx, nodeID, vmID); err != nil {
			c.logger.Error("over-quota: suspend failed", "vm_id", vmID, "error", err)
		} else {
			c.logger.Warn("over-quota: VM suspended (quota exceeded)", "vm_id", vmID)
		}

	default: // throttle
		if on, _ := BandwidthEnforcement(ctx, c.db); !on {
			c.logger.Warn("over-quota throttle skipped (enforcement is off)", "vm_id", vmID)
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
		// Still over quota this period (or its pool is) → keep throttled.
		if used, quota, _ := c.bwService.EffectiveUsage(ctx, usage); quota > 0 && used >= quota {
			continue
		}
		// An abuse rate limit holds the speed down for its own reason.
		if c.abuse.RateLimited(ctx, net.VMID) {
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

// sendOverageWebhook tells the billing system a VM crossed its transfer
// quota, through the same stored-and-retried delivery as every other billing
// event. With no webhook URL set the overage is recorded in the logs only.
func (c *MetricsCollector) sendOverageWebhook(ctx context.Context, nodeID, vmID string) {
	usage, err := c.bwService.GetVMUsage(ctx, vmID, nodeID)
	if err != nil || usage == nil {
		c.logger.Error("overage: no usage for VM", "vm_id", vmID, "error", err)
		return
	}
	c.logger.Warn("bandwidth overage", "vm_id", vmID, "used_gb", usage.UsedGB(), "quota_gb", usage.QuotaGB())
	EmitVMBillingEvent(ctx, c.db, c.riverClient, c.logger, BillingEventOverage, vmID, map[string]any{
		"node_id":      nodeID,
		"used_gb":      usage.UsedGB(),
		"quota_gb":     usage.QuotaGB(),
		"period_start": usage.PeriodStart,
		"period_end":   usage.PeriodEnd,
	})
}
