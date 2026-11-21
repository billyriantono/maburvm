package service

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A node that stops answering: no reading is stored for it, it is marked
// offline after the configured number of missed checks (not before), operators
// are told once, and it is marked active and announced as back when it reports.
func TestCollectorMarksADeadNodeOfflineAndBack(t *testing.T) {
	db := newAlertDB(t)
	for _, q := range []string{
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, name TEXT NOT NULL, ip_address TEXT NOT NULL, status TEXT, token TEXT NOT NULL,
			creation_paused BOOLEAN NOT NULL DEFAULT 0, cert_fingerprint TEXT NOT NULL DEFAULT '', region_id TEXT, zone TEXT NOT NULL DEFAULT '',
			memory_total_bytes INTEGER NOT NULL DEFAULT 0, firewall_enforcement TEXT NOT NULL DEFAULT '',
			last_report_at DATETIME, failed_polls INTEGER NOT NULL DEFAULT 0, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE node_metrics (id INTEGER PRIMARY KEY AUTOINCREMENT, node_id TEXT NOT NULL, cpu_usage REAL NOT NULL DEFAULT 0,
			memory_usage REAL NOT NULL DEFAULT 0, disk_usage REAL NOT NULL DEFAULT 0, network_rx_bytes_per_sec REAL NOT NULL DEFAULT 0,
			network_tx_bytes_per_sec REAL NOT NULL DEFAULT 0, vm_count INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL DEFAULT '',
			conntrack_count INTEGER NOT NULL DEFAULT 0, conntrack_max INTEGER NOT NULL DEFAULT 0, recorded_at DATETIME NOT NULL)`,
		`INSERT INTO nodes (id, name, ip_address, status, token) VALUES ('n1', 'node1', '192.0.2.10', 'active', 't1'), ('n2', 'spare', '192.0.2.11', 'maintenance', 't2')`,
	} {
		require.NoError(t, db.Exec(q).Error)
	}
	h := newAlertHarness(t, db)
	h.settings(t, emailOn)
	nodeRepo := repository.NewNodeRepository(db)
	c := &MetricsCollector{nodeRepo: nodeRepo, repo: repository.NewMetricsRepository(db), db: db, logger: slog.Default(), alerts: h.AlertService}
	ctx := context.Background()

	live := &NodeMetrics{NodeID: "n1", Status: "online", Live: true, CPUUsage: 12, MemoryUsage: 40, MemoryTotal: 64 << 30, FirewallEnforcement: models.FirewallEnforced}
	dead := &NodeMetrics{NodeID: "n1", Status: "offline", FirewallEnforcement: models.FirewallUnknown}
	poll := func(id string, m *NodeMetrics) *models.Node {
		t.Helper()
		before, err := nodeRepo.GetByID(ctx, id)
		require.NoError(t, err)
		c.recordPoll(ctx, before, m, 3)
		after, err := nodeRepo.GetByID(ctx, id)
		require.NoError(t, err)
		return after
	}
	samples := func() (n int64) {
		require.NoError(t, db.Table("node_metrics").Count(&n).Error)
		return n
	}

	n := poll("n1", live)
	require.NotNil(t, n.LastReportAt)
	reported := *n.LastReportAt
	assert.EqualValues(t, 1, samples())
	assert.EqualValues(t, 64<<30, n.MemoryTotalBytes)

	// Two missed checks: still active, nothing said, and no zeros stored.
	for i := 1; i <= 2; i++ {
		n = poll("n1", dead)
		assert.Equal(t, models.NodeStatusActive, n.Status)
		assert.Equal(t, i, n.FailedPolls)
	}
	assert.Empty(t, h.flush())
	assert.EqualValues(t, 1, samples(), "a check that got no answer is not a reading")
	assert.True(t, n.LastReportAt.Equal(reported), "the last report does not advance while the node is silent")
	var fw string
	require.NoError(t, db.Raw(`SELECT firewall_enforcement FROM nodes WHERE id = 'n1'`).Scan(&fw).Error)
	assert.Equal(t, models.FirewallEnforced, fw, "no answer keeps the last known firewall state")

	// The third: offline, and operators are told once however long it lasts.
	n = poll("n1", dead)
	assert.Equal(t, models.NodeStatusOffline, n.Status)
	msgs := h.flush()
	require.Len(t, msgs, 1)
	assert.Equal(t, "[MaburVM] Node node1 is offline", msgs[0].Subject)
	assert.Contains(t, msgs[0].Text, "missed 3 checks")
	for i := 0; i < 5; i++ {
		poll("n1", dead)
		h.advance(time.Minute)
	}
	assert.Empty(t, h.flush())
	// Placement lists active nodes only, so it no longer sees this one.
	active, err := nodeRepo.ListActive(ctx)
	require.NoError(t, err)
	assert.Empty(t, active)

	// It answers again: active at once, the recovery after the settle time.
	n = poll("n1", live)
	assert.Equal(t, models.NodeStatusActive, n.Status)
	assert.Zero(t, n.FailedPolls)
	assert.True(t, n.LastReportAt.After(reported))
	assert.Empty(t, h.flush())
	h.advance(10 * time.Minute)
	poll("n1", live)
	msgs = h.flush()
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0].Text, "Resolved: Node node1 is offline")

	// The port answers but the agent does not report: the node stays active
	// (the panel can still reach it) and operators get a warning, not "offline".
	hung := &NodeMetrics{NodeID: "n1", Status: "online", FirewallEnforcement: models.FirewallUnknown}
	for i := 0; i < 3; i++ {
		n = poll("n1", hung)
	}
	assert.Equal(t, models.NodeStatusActive, n.Status)
	msgs = h.flush()
	require.Len(t, msgs, 1)
	assert.Equal(t, "[MaburVM] Node node1 is not reporting", msgs[0].Subject)

	// A node reporting that it no longer filters guest traffic is announced;
	// one too old to say is not.
	open := live
	unfiltered := *live
	unfiltered.FirewallEnforcement = models.FirewallOff
	poll("n1", &unfiltered)
	msgs = h.flush()
	require.Len(t, msgs, 1)
	assert.Contains(t, msgs[0].Text, "Critical: Node node1 is not filtering guest traffic")
	silentAgent := *open
	silentAgent.FirewallEnforcement = models.FirewallUnknown
	poll("n1", &silentAgent)
	var firing int64
	require.NoError(t, db.Model(&models.Alert{}).Where("alert_key = ? AND state = ? AND clear_since IS NULL", "firewall_off:n1", models.AlertStateFiring).Count(&firing).Error)
	assert.EqualValues(t, 1, firing, "an agent that says nothing neither raises nor clears it")

	// Put into maintenance while a check that read "active" was under way: the
	// check must not overwrite it, or the node would later be marked active.
	stale, err := nodeRepo.GetByID(ctx, "n1")
	require.NoError(t, err)
	require.Equal(t, models.NodeStatusActive, stale.Status)
	require.NoError(t, db.Exec(`UPDATE nodes SET failed_polls = 5 WHERE id = 'n1'`).Error)
	stale.FailedPolls = 5
	require.NoError(t, nodeRepo.UpdateStatus(ctx, "n1", models.NodeStatusMaintenance))
	c.recordPoll(ctx, stale, dead, 3)
	n, err = nodeRepo.GetByID(ctx, "n1")
	require.NoError(t, err)
	assert.Equal(t, models.NodeStatusMaintenance, n.Status)

	// A node in maintenance is never flipped or announced.
	down := &NodeMetrics{NodeID: "n2", Status: "offline", FirewallEnforcement: models.FirewallUnknown}
	for i := 0; i < 4; i++ {
		n = poll("n2", down)
	}
	assert.Equal(t, models.NodeStatusMaintenance, n.Status)
	h.advance(time.Hour)
	for _, m := range h.flush() {
		assert.NotContains(t, m.Text, "spare")
	}

	// A node added to the panel whose agent is not installed yet did not go
	// down: it is not announced as offline.
	require.NoError(t, db.Exec(`INSERT INTO nodes (id, name, ip_address, status, token) VALUES ('n3', 'fresh', '192.0.2.12', 'offline', 't3')`).Error)
	for i := 0; i < 4; i++ {
		poll("n3", &NodeMetrics{NodeID: "n3", Status: "offline", FirewallEnforcement: models.FirewallUnknown})
	}
	var fresh int64
	require.NoError(t, db.Model(&models.Alert{}).Where("resource_id = ?", "n3").Count(&fresh).Error)
	assert.Zero(t, fresh)
}

func TestPlacementPrefersNodesThatAnswered(t *testing.T) {
	a, b := models.Node{ID: "a"}, models.Node{ID: "b", FailedPolls: 2}
	assert.Equal(t, []models.Node{a}, nodesReporting([]models.Node{a, b}), "a node that stopped answering is passed over")
	assert.Equal(t, []models.Node{b}, nodesReporting([]models.Node{b}), "unless no node answered: the order is still tried")
}
