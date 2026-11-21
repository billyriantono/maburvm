package service

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/shared/models"
)

func TestUsageWarningLevel(t *testing.T) {
	const gb = int64(1) << 30
	levels := []int{80, 100}
	for _, c := range []struct {
		name         string
		used, quota  int64
		warned, want int
	}{
		{"under every level", 79 * gb, 100 * gb, 0, 0},
		{"first level reached", 80 * gb, 100 * gb, 0, 80},
		{"already warned at that level", 90 * gb, 100 * gb, 80, 0},
		{"next level reached", 100 * gb, 100 * gb, 80, 100},
		{"both passed at once warns once, at the higher", 120 * gb, 100 * gb, 0, 100},
		{"unlimited", 500 * gb, 0, 0, 0},
	} {
		assert.Equal(t, c.want, UsageWarningLevel(c.used, c.quota, c.warned, levels), c.name)
	}
}

func TestBandwidthSettingsNormalize(t *testing.T) {
	s := BandwidthSettings{WarnPercents: []int{100, 50, 80}}
	require.NoError(t, s.Normalize())
	assert.Equal(t, []int{50, 80, 100}, s.WarnPercents)
	for _, bad := range [][]int{{0}, {101}, {80, 80}, {10, 20, 30, 40, 50, 60}} {
		s := BandwidthSettings{WarnPercents: bad}
		assert.Error(t, s.Normalize(), "%v", bad)
	}
	off := BandwidthSettings{WarnPercents: []int{}}
	require.NoError(t, off.Normalize(), "an empty list turns warnings off")
}

// The switch used to be BANDWIDTH_ENFORCE; that still decides until an
// operator saves the setting, which then wins.
func TestBandwidthEnforcementSource(t *testing.T) {
	db := newAlertDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS system_settings (section TEXT PRIMARY KEY, data TEXT NOT NULL, updated_at DATETIME)`).Error)
	ctx := context.Background()

	t.Setenv("BANDWIDTH_ENFORCE", "")
	on, src := BandwidthEnforcement(ctx, db)
	assert.False(t, on)
	assert.Equal(t, EnforceFromDefault, src)

	t.Setenv("BANDWIDTH_ENFORCE", "true")
	on, src = BandwidthEnforcement(ctx, db)
	assert.True(t, on)
	assert.Equal(t, EnforceFromEnvironment, src)

	require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('bandwidth', '{"enforce":false,"warnPercents":[90]}')`).Error)
	on, src = BandwidthEnforcement(ctx, db)
	assert.False(t, on)
	assert.Equal(t, EnforceFromSetting, src)
	assert.Equal(t, []int{90}, UsageWarnPercents(ctx, db))
}

func TestOverQuotaOutcome(t *testing.T) {
	assert.Contains(t, OverQuotaOutcome(models.OverQuotaThrottle, 0, true, false), "lowered to 10 Mbps")
	assert.Contains(t, OverQuotaOutcome(models.OverQuotaThrottle, 5, false, true), "keeps running at its usual speed",
		"with enforcement off the owner is not told about a throttle that will not happen")
	assert.Contains(t, OverQuotaOutcome(models.OverQuotaSuspend, 0, true, true), "It is used up, so the server is stopped")
	assert.Contains(t, OverQuotaOutcome(models.OverQuotaOverage, 0, false, false), "billed as extra usage")
}

// One repair is a reboot or a hand edit; three within the hour raise an alert.
func TestNoteRepairsAlertsOnlyWhenRepeated(t *testing.T) {
	db := newAlertDB(t)
	h := newAlertHarness(t, db)
	h.settings(t, emailOn)
	c := &MetricsCollector{db: db, logger: slog.Default(), alerts: h.AlertService}
	node := &models.Node{ID: "n1", Name: "node1"}
	ctx := context.Background()
	poll := func(n int64) {
		c.noteRepairs(ctx, node, &NodeMetrics{NetworkRepairs: n, NetworkRepairLast: "VM x: firewall has 0 entries, want 3"})
	}

	poll(4) // the first answer after a panel restart is only the starting point
	poll(5)
	poll(6)
	assert.Empty(t, h.rows(t))
	poll(0) // agent restarted: not a repair
	poll(1)
	rows := h.rows(t)
	require.Len(t, rows, 1)
	assert.Equal(t, models.AlertKindNetworkDrift, rows[0].Kind)
	assert.Contains(t, rows[0].Detail, "3 times in the last hour")
}

func TestPlanSMTPPolicy(t *testing.T) {
	assert.Error(t, (&PlanRequest{Name: "p", CPU: 1, RAM: 512, Disk: 10, SMTPPolicy: "sometimes"}).validate())
	assert.NoError(t, (&PlanRequest{Name: "p", CPU: 1, RAM: 512, Disk: 10, SMTPPolicy: models.SMTPBlocked}).validate())
}

// A plan that blocks outbound mail blocks it on the VMs ordered from it.
func TestCreateVMTakesThePlansMailPolicy(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	user, node, tmpl := seedVMIPAMDependencies(t, db)
	plan := seedPlan(t, db, true)
	require.NoError(t, db.Model(plan).Update("smtp_policy", models.SMTPBlocked).Error)
	svc := newPolicyVMService(db)

	resp, err := svc.CreateVM(ctx, &CreateVMRequest{UserID: user.ID.String(), Hostname: "mail.example.test", OSTemplateID: tmpl.ID,
		NodeID: node.ID, PlanID: plan.ID, PlanRequired: true})
	require.NoError(t, err)
	var blocked bool
	require.NoError(t, db.Raw(`SELECT smtp_blocked FROM vms WHERE id = ?`, resp.VM.ID).Scan(&blocked).Error)
	assert.True(t, blocked)
}

// A clone or restore of a VM whose mail was blocked stays blocked, whatever
// plan it is on: copying a VM must not be a way around the block.
func TestCopiesKeepTheMailBlock(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	user, node, tmpl := seedVMIPAMDependencies(t, db)
	src := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "src.example.test", OSTemplateID: tmpl.ID,
		Resources: models.Resources{CPU: 1, RAM: 512, Disk: 10}, Status: models.VMStatusStopped}
	require.NoError(t, db.Create(src).Error)
	require.NoError(t, db.Exec(`UPDATE vms SET smtp_blocked = TRUE WHERE id = ?`, src.ID).Error)
	svc := newPolicyVMService(db)

	req := &CreateVMRequest{UserID: user.ID.String(), Hostname: "copy.example.test", OSTemplateID: tmpl.ID, NodeID: node.ID,
		Resources: models.Resources{CPU: 1, RAM: 512, Disk: 10}}
	svc.inheritNetworkLimits(ctx, src.ID, req)
	require.True(t, req.BlockSMTP)
	resp, err := svc.CreateVM(ctx, req)
	require.NoError(t, err)
	var blocked bool
	require.NoError(t, db.Raw(`SELECT smtp_blocked FROM vms WHERE id = ?`, resp.VM.ID).Scan(&blocked).Error)
	assert.True(t, blocked)
}
