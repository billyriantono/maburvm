package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const alertTestKey = "0123456789abcdef0123456789abcdef"

// alertHarness is an AlertService over an in-memory database, with a clock the
// test moves and a record of every message that would have gone out.
type alertHarness struct {
	*AlertService
	db    *gorm.DB
	clock time.Time
	sent  []alertMessage
	fail  bool
}

func newAlertDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	for _, q := range []string{
		`CREATE TABLE system_settings (section TEXT PRIMARY KEY, data TEXT, updated_at DATETIME)`,
		`CREATE TABLE vms (id TEXT PRIMARY KEY, hostname TEXT)`,
		`CREATE TABLE alerts (id TEXT PRIMARY KEY, alert_key TEXT NOT NULL, kind TEXT NOT NULL, severity TEXT NOT NULL,
			state TEXT NOT NULL, title TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '', resource_type TEXT NOT NULL DEFAULT '',
			resource_id TEXT NOT NULL DEFAULT '', resource_name TEXT NOT NULL DEFAULT '', fired_at DATETIME NOT NULL,
			clear_since DATETIME, resolved_at DATETIME, notified_at DATETIME, recovery_notified_at DATETIME,
			delivered BOOLEAN NOT NULL DEFAULT 0, attempts INTEGER NOT NULL DEFAULT 0, delivery TEXT NOT NULL DEFAULT '')`,
		`CREATE UNIQUE INDEX idx_alerts_open_key ON alerts (alert_key) WHERE state = 'firing'`,
	} {
		require.NoError(t, db.Exec(q).Error)
	}
	return db
}

// newAlertHarness starts with email delivery switched on and a ten-minute
// settle time.
func newAlertHarness(t *testing.T, db *gorm.DB) *alertHarness {
	t.Helper()
	h := &alertHarness{db: db, clock: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)}
	h.AlertService = NewAlertService(db, nil, alertTestKey)
	h.now = func() time.Time { return h.clock }
	h.deliver = func(_ context.Context, _ alertConfig, msg alertMessage) []AlertChannelResult {
		if h.fail {
			return []AlertChannelResult{{Channel: "email", Message: "connection refused"}}
		}
		h.sent = append(h.sent, msg)
		return []AlertChannelResult{{Channel: "email", OK: true, Message: "sent to 1 of 1"}}
	}
	return h
}

func (h *alertHarness) settings(t *testing.T, st storedAlertSettings) {
	t.Helper()
	data, _ := json.Marshal(st)
	require.NoError(t, h.db.Exec(`INSERT INTO system_settings (section, data) VALUES (?, ?)
		ON CONFLICT (section) DO UPDATE SET data = excluded.data`, AlertSettingsSection, string(data)).Error)
	h.cached = nil
}

func (h *alertHarness) advance(d time.Duration) { h.clock = h.clock.Add(d) }

// flush runs one delivery pass and returns the messages it sent.
func (h *alertHarness) flush() []alertMessage {
	h.sent = nil
	h.Flush(context.Background())
	return h.sent
}

func (h *alertHarness) rows(t *testing.T) []models.Alert {
	t.Helper()
	var rows []models.Alert
	require.NoError(t, h.db.Order("fired_at, id").Find(&rows).Error)
	return rows
}

var emailOn = storedAlertSettings{EmailEnabled: true, EmailRecipients: []string{"ops@example.com"}, CooldownMinutes: 10}

func nodeDown(severity string) AlertInput {
	return AlertInput{
		Key: "node_offline:n1", Kind: models.AlertKindNodeOffline, Severity: severity,
		Title: "Node n1 is offline", ResourceType: "node", ResourceID: "n1", ResourceName: "n1",
	}
}

func TestAlertFlappingConditionIsOneAlertAndOneRecovery(t *testing.T) {
	h := newAlertHarness(t, newAlertDB(t))
	h.settings(t, emailOn)
	ctx := context.Background()

	h.Raise(ctx, nodeDown(models.AlertSeverityCritical))
	h.Raise(ctx, nodeDown(models.AlertSeverityCritical)) // every check raises again
	msgs := h.flush()
	require.Len(t, msgs, 1)
	assert.Equal(t, "[MaburVM] Node n1 is offline", msgs[0].Subject)
	assert.Equal(t, models.AlertStateFiring, msgs[0].Payload.Alerts[0].Status)

	// The node flaps for eight minutes: up, down, up, down.
	for i := 0; i < 4; i++ {
		h.advance(time.Minute)
		h.Clear(ctx, "node_offline:n1")
		assert.Empty(t, h.flush(), "a flap must not send anything")
		h.advance(time.Minute)
		h.Raise(ctx, nodeDown(models.AlertSeverityCritical))
		assert.Empty(t, h.flush(), "a flap must not send anything")
	}
	require.Len(t, h.rows(t), 1, "flapping is one alert in the history")

	// It comes back and stays back. Nothing is said until the settle time passes.
	h.Clear(ctx, "node_offline:n1")
	h.advance(9 * time.Minute)
	h.Clear(ctx, "node_offline:n1") // later checks must not restart the wait
	assert.Empty(t, h.flush())
	h.advance(time.Minute)
	msgs = h.flush()
	require.Len(t, msgs, 1)
	assert.Equal(t, models.AlertStateResolved, msgs[0].Payload.Alerts[0].Status)
	assert.Contains(t, msgs[0].Text, "Resolved: Node n1 is offline\nClear again after 18 min.")
	require.NotNil(t, msgs[0].Payload.Alerts[0].ResolvedAt)
	assert.Equal(t, time.UTC, msgs[0].Payload.Alerts[0].ResolvedAt.Location())
	assert.Empty(t, h.flush(), "a recovery is announced once")

	rows := h.rows(t)
	require.Len(t, rows, 1)
	assert.Equal(t, models.AlertStateResolved, rows[0].State)
	assert.NotNil(t, rows[0].RecoveryNotifiedAt)

	// Down again after it resolved: a new alert, announced again.
	h.Raise(ctx, nodeDown(models.AlertSeverityCritical))
	assert.Len(t, h.flush(), 1)
	assert.Len(t, h.rows(t), 2)
}

func TestAlertStateSurvivesARestart(t *testing.T) {
	db := newAlertDB(t)
	before := newAlertHarness(t, db)
	before.settings(t, emailOn)
	ctx := context.Background()
	before.Raise(ctx, nodeDown(models.AlertSeverityCritical))
	require.Len(t, before.flush(), 1)

	// A new process over the same database: the node is still down.
	after := newAlertHarness(t, db)
	after.clock = before.clock.Add(time.Minute)
	after.Raise(ctx, nodeDown(models.AlertSeverityCritical))
	assert.Empty(t, after.flush(), "an alert already announced is not announced again after a restart")
	require.Len(t, after.rows(t), 1)

	// And the recovery still goes out, from the process that did not see it open.
	after.Clear(ctx, "node_offline:n1")
	after.advance(10 * time.Minute)
	msgs := after.flush()
	require.Len(t, msgs, 1)
	assert.Equal(t, models.AlertStateResolved, msgs[0].Payload.Alerts[0].Status)
}

func TestAlertThatGetsWorseIsAnnouncedAgain(t *testing.T) {
	h := newAlertHarness(t, newAlertDB(t))
	h.settings(t, emailOn)
	ctx := context.Background()
	pool := func(severity, title string) AlertInput {
		return AlertInput{Key: "pool_usage:p1", Kind: models.AlertKindStoragePool, Severity: severity, Title: title}
	}

	h.Raise(ctx, pool(models.AlertSeverityWarning, "Storage pool vm is 91% full"))
	require.Len(t, h.flush(), 1)
	h.Raise(ctx, pool(models.AlertSeverityCritical, "Storage pool vm is 96% full"))
	msgs := h.flush()
	require.Len(t, msgs, 1, "warning to critical is said again")
	assert.Contains(t, msgs[0].Text, "Critical: Storage pool vm is 96% full")

	h.Raise(ctx, pool(models.AlertSeverityWarning, "Storage pool vm is 93% full"))
	assert.Empty(t, h.flush(), "easing back to a warning is not news")
	rows := h.rows(t)
	require.Len(t, rows, 1)
	assert.Equal(t, models.AlertSeverityWarning, rows[0].Severity)
}

func TestAlertEventIsNotRepeatedInsideTheSettleTime(t *testing.T) {
	h := newAlertHarness(t, newAlertDB(t))
	h.settings(t, emailOn)
	ctx := context.Background()
	failed := func(vm string) AlertInput {
		return AlertInput{Key: "backup_failed:" + vm, Kind: models.AlertKindBackupFailed, Severity: models.AlertSeverityWarning, Title: "Backup failed for " + vm}
	}

	h.Event(ctx, failed("web1"))
	require.Len(t, h.flush(), 1)

	// The same VM fails every three minutes; another VM fails once.
	for i := 0; i < 3; i++ {
		h.advance(3 * time.Minute)
		h.Event(ctx, failed("web1"))
	}
	h.Event(ctx, failed("db1"))
	msgs := h.flush()
	require.Len(t, msgs, 1)
	require.Len(t, msgs[0].Payload.Alerts, 1, "only the other VM is news")
	assert.Equal(t, "backup_failed:db1", msgs[0].Payload.Alerts[0].Key)

	rows := h.rows(t)
	require.Len(t, rows, 5, "every failure is in the history")
	suppressed := 0
	for _, r := range rows {
		if strings.HasPrefix(r.Delivery, "Not sent: the same alert") {
			suppressed++
		}
	}
	assert.Equal(t, 3, suppressed)

	// Past the settle time, counted from the one that was sent, it is said again.
	h.advance(2 * time.Minute)
	h.Event(ctx, failed("web1"))
	assert.Len(t, h.flush(), 1)
}

func TestAlertBurstIsOneMessage(t *testing.T) {
	h := newAlertHarness(t, newAlertDB(t))
	h.settings(t, emailOn)
	ctx := context.Background()
	for _, vm := range []string{"a", "b", "c"} {
		h.Event(ctx, AlertInput{Key: "backup_failed:" + vm, Kind: models.AlertKindBackupFailed, Severity: models.AlertSeverityWarning, Title: "Backup failed for " + vm})
	}
	msgs := h.flush()
	require.Len(t, msgs, 1)
	assert.Len(t, msgs[0].Payload.Alerts, 3)
	assert.Equal(t, "[MaburVM] 3 alerts: Backup failed for a and 2 more", msgs[0].Subject)
}

func TestAlertDeliveryIsRetriedThenGivenUp(t *testing.T) {
	h := newAlertHarness(t, newAlertDB(t))
	h.settings(t, emailOn)
	ctx := context.Background()

	h.fail = true
	h.Raise(ctx, nodeDown(models.AlertSeverityCritical))
	h.flush()
	row := h.rows(t)[0]
	assert.Nil(t, row.NotifiedAt, "a failed send stays due")
	assert.Equal(t, "Will be tried again. email: connection refused", row.Delivery)

	// The mail server comes back: the alert goes out on the next pass.
	h.fail = false
	require.Len(t, h.flush(), 1)
	row = h.rows(t)[0]
	assert.True(t, row.Delivered)
	assert.NotNil(t, row.NotifiedAt)

	// One that never gets through is given up on, and its recovery is not sent
	// either: operators were never told it was down.
	h.fail = true
	h.Event(ctx, AlertInput{Key: "job_failed:x", Kind: models.AlertKindJobFailed, Severity: models.AlertSeverityWarning, Title: "Job x failed"})
	for i := 0; i < maxAlertAttempts; i++ {
		h.flush()
	}
	var ev models.Alert
	require.NoError(t, h.db.Where("alert_key = ?", "job_failed:x").First(&ev).Error)
	assert.NotNil(t, ev.NotifiedAt)
	assert.False(t, ev.Delivered)
	assert.Contains(t, ev.Delivery, "Not sent after 5 tries")
	h.fail = false
	assert.Empty(t, h.flush())
}

// A node that is still down must reach operators once delivery works again,
// however long the mail server or the chat service was away.
func TestOpenAlertIsRetriedUntilDelivered(t *testing.T) {
	h := newAlertHarness(t, newAlertDB(t))
	h.settings(t, emailOn)
	ctx := context.Background()
	tries := 0
	h.deliver = func(context.Context, alertConfig, alertMessage) []AlertChannelResult {
		tries++
		return []AlertChannelResult{{Channel: "email", Message: "connection refused"}}
	}

	h.Raise(ctx, nodeDown(models.AlertSeverityCritical))
	for i := 0; i < 60; i++ { // an hour of passes with delivery down
		h.Flush(ctx)
		h.advance(time.Minute)
	}
	row := h.rows(t)[0]
	assert.Nil(t, row.NotifiedAt, "an open alert nobody was told about stays due")
	assert.Contains(t, row.Delivery, "Will be tried again")
	assert.Equal(t, maxAlertAttempts+3, tries, "one try a minute at first, then about every 15 minutes")

	h.deliver = func(_ context.Context, _ alertConfig, msg alertMessage) []AlertChannelResult {
		h.sent = append(h.sent, msg)
		return []AlertChannelResult{{Channel: "email", OK: true, Message: "sent to 1 of 1"}}
	}
	sent := 0
	for i := 0; i < alertSlowRetryEvery; i++ {
		sent += len(h.flush())
	}
	assert.Equal(t, 1, sent, "delivered once, at the next slow try")
	assert.True(t, h.rows(t)[0].Delivered)
}

func TestAlertOpenedAndClosedUnannouncedSendsNoRecovery(t *testing.T) {
	h := newAlertHarness(t, newAlertDB(t))
	h.settings(t, emailOn)
	ctx := context.Background()
	h.fail = true
	h.Raise(ctx, nodeDown(models.AlertSeverityCritical))
	h.flush()
	h.Clear(ctx, "node_offline:n1")
	h.advance(10 * time.Minute)
	h.fail = false
	assert.Empty(t, h.flush(), "nobody heard it open, so nobody is told it closed")
	row := h.rows(t)[0]
	assert.Equal(t, models.AlertStateResolved, row.State)
	assert.Equal(t, "Not sent: it cleared before it was announced", row.Delivery)
}

func TestAlertWithNoDeliveryIsStillInTheHistory(t *testing.T) {
	h := newAlertHarness(t, newAlertDB(t))
	ctx := context.Background()
	h.Raise(ctx, nodeDown(models.AlertSeverityCritical))
	assert.Empty(t, h.flush())
	rows, total, err := h.List(ctx, true, 10, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	assert.Equal(t, "Not sent: no delivery is set up", rows[0].Delivery)
	assert.False(t, rows[0].Delivered)
}

func TestAlertKindSwitchedOff(t *testing.T) {
	h := newAlertHarness(t, newAlertDB(t))
	h.settings(t, emailOn)
	ctx := context.Background()
	h.Raise(ctx, nodeDown(models.AlertSeverityCritical))
	require.Len(t, h.flush(), 1)

	off := emailOn
	off.Kinds = map[string]bool{models.AlertKindNodeOffline: false}
	h.settings(t, off)
	h.Raise(ctx, nodeDown(models.AlertSeverityCritical))
	silent := nodeDown(models.AlertSeverityWarning)
	silent.Key, silent.Kind = "node_silent:n1", models.AlertKindNodeSilent
	h.Raise(ctx, silent) // goes with the node switch
	h.advance(time.Hour)
	assert.Empty(t, h.flush(), "a kind that is switched off sends nothing, not even a recovery")
	rows := h.rows(t)
	require.Len(t, rows, 1)
	assert.Equal(t, models.AlertStateResolved, rows[0].State)

	// The other kinds are untouched.
	h.Event(ctx, AlertInput{Key: "backup_failed:v", Kind: models.AlertKindBackupFailed, Severity: models.AlertSeverityWarning, Title: "Backup failed"})
	assert.Len(t, h.flush(), 1)
}

func TestAlertClosedByAnOperator(t *testing.T) {
	h := newAlertHarness(t, newAlertDB(t))
	h.settings(t, emailOn)
	ctx := context.Background()
	h.Raise(ctx, nodeDown(models.AlertSeverityCritical))
	h.flush()
	id := h.rows(t)[0].ID
	require.NoError(t, h.Close(ctx, id))
	assert.ErrorIs(t, h.Close(ctx, id), gorm.ErrRecordNotFound)
	assert.Empty(t, h.flush(), "closing by hand sends no recovery")
	// Still down on the next check: a new alert.
	h.Raise(ctx, nodeDown(models.AlertSeverityCritical))
	assert.Len(t, h.flush(), 1)
}

func TestJobFailedBecomesTheRightAlert(t *testing.T) {
	h := newAlertHarness(t, newAlertDB(t))
	h.settings(t, emailOn)
	ctx := context.Background()
	require.NoError(t, h.db.Exec(`INSERT INTO vms VALUES ('vm-1', 'web1')`).Error)

	h.JobFailed(ctx, "backup", []byte(`{"vm_id":"vm-1","backup_id":"b1"}`), "upload failed: AccessDenied")
	h.JobFailed(ctx, "vm_operation", []byte(`{"vm_id":"vm-1","operation":"create","node_id":"n1"}`), strings.Repeat("x", 900))
	h.JobFailed(ctx, "template_sync", []byte(`{}`), "no nodes")

	rows := h.rows(t)
	require.Len(t, rows, 3)
	byKey := map[string]models.Alert{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	backup := byKey["backup_failed:vm-1"]
	assert.Equal(t, models.AlertKindBackupFailed, backup.Kind)
	assert.Equal(t, "Backup failed for web1", backup.Title)
	assert.Equal(t, "upload failed: AccessDenied", backup.Detail)
	create := byKey["job_failed:vm_operation:create:vm-1"]
	assert.Equal(t, "VM create failed for web1", create.Title)
	assert.Less(t, len(create.Detail), 600, "a long reason is cut")

	// Cut inside a character, the reason would be refused by the database.
	h.JobFailed(ctx, "restore", []byte(`{"vm_id":"vm-1"}`), strings.Repeat("x", 499)+"é"+strings.Repeat("y", 50))
	var cut models.Alert
	require.NoError(t, h.db.Where("alert_key = ?", "job_failed:restore::vm-1").First(&cut).Error)
	assert.True(t, utf8.ValidString(cut.Detail))
	assert.Equal(t, "Template sync failed", byKey["job_failed:template_sync::"].Title)
}

func TestNodeHealthAfterPoll(t *testing.T) {
	for _, tc := range []struct {
		name                string
		failed              int
		reported, reachable bool
		after               int
		want                nodeCheck
	}{
		{"a report resets the count", 7, true, true, 3, nodeCheck{}},
		{"first miss is not offline", 0, false, false, 3, nodeCheck{Failed: 1}},
		{"second miss is not offline", 1, false, false, 3, nodeCheck{Failed: 2}},
		{"third miss in a row is offline", 2, false, false, 3, nodeCheck{Failed: 3, Offline: true}},
		{"and stays offline", 40, false, false, 3, nodeCheck{Failed: 41, Offline: true}},
		{"port answers but no report: silent, never offline", 2, false, true, 3, nodeCheck{Failed: 3, Silent: true}},
		{"silent needs the same patience", 0, false, true, 3, nodeCheck{Failed: 1}},
		{"a threshold of one acts at once", 0, false, false, 1, nodeCheck{Failed: 1, Offline: true}},
		{"a nonsense threshold is one", 0, false, false, 0, nodeCheck{Failed: 1, Offline: true}},
	} {
		assert.Equal(t, tc.want, nodeHealthAfterPoll(tc.failed, tc.reported, tc.reachable, tc.after), tc.name)
	}
}

func TestPoolAlertSeverity(t *testing.T) {
	const gb = 1 << 30
	for _, tc := range []struct {
		name        string
		used, total int64
		warn, crit  int
		want        string
	}{
		{"below the pool's level", 89 * gb, 100 * gb, 90, 95, ""},
		{"at the pool's level", 90 * gb, 100 * gb, 90, 95, models.AlertSeverityWarning},
		{"between the two", 94 * gb, 100 * gb, 90, 95, models.AlertSeverityWarning},
		{"at the critical level", 95 * gb, 100 * gb, 90, 95, models.AlertSeverityCritical},
		{"full", 100 * gb, 100 * gb, 90, 95, models.AlertSeverityCritical},
		{"pool level above the critical one", 96 * gb, 100 * gb, 99, 95, models.AlertSeverityCritical},
		{"pool alerts switched off", 99 * gb, 100 * gb, 0, 95, ""},
		{"not measured yet", 0, 0, 90, 95, ""},
		{"just under, no rounding up", 899, 1000, 90, 95, ""},
	} {
		assert.Equal(t, tc.want, poolAlertSeverity(tc.used, tc.total, tc.warn, tc.crit), tc.name)
	}
}

func TestPoolAlertText(t *testing.T) {
	const gb = 1 << 30
	p := &models.StoragePool{Name: "vm", TotalSpace: 1000 * gb, UsedSpace: 910 * gb, AvailableSpace: 90 * gb}
	title, detail := poolAlertText(p, "node-a", models.AlertSeverityWarning)
	assert.Equal(t, "Storage pool vm on node-a is nearly full (91% used)", title)
	assert.Contains(t, detail, "90.0 GB free of 1000.0 GB")
	assert.Contains(t, detail, "refused on it once one would leave less than 20 GB free", "says when the hard stop comes")
	assert.Contains(t, detail, "paused until space is freed")

	p.UsedSpace, p.AvailableSpace = 990*gb, 10*gb
	title, detail = poolAlertText(p, "node-a", models.AlertSeverityCritical)
	assert.Equal(t, "Storage pool vm on node-a is full (99% used)", title)
	assert.Contains(t, detail, "already refused", "past the reserve, new disks are stopped now")
}

func TestAlertWebhookIsSignedAndKeepsItsSchema(t *testing.T) {
	var got struct {
		body      []byte
		signature string
	}
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.body, _ = io.ReadAll(r.Body)
		got.signature = r.Header.Get("X-Webhook-Signature")
		w.WriteHeader(status)
	}))
	defer srv.Close()

	s := NewAlertService(newAlertDB(t), nil, alertTestKey)
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	cfg := alertConfig{webhookURL: srv.URL + "/hook/secret-token", webhookSecret: "shh", brand: "Acme", publicURL: "https://panel.example.com"}
	cfg.WebhookEnabled, cfg.WebhookFormat = true, AlertWebhookGeneric
	a := models.Alert{ID: "a1", Key: "node_offline:n1", Kind: models.AlertKindNodeOffline, Severity: models.AlertSeverityCritical,
		State: models.AlertStateFiring, Title: "Node n1 is offline", Detail: "It has missed 3 checks in a row.",
		ResourceType: "node", ResourceID: "n1", ResourceName: "n1", FiredAt: now}
	msg := buildAlertMessage(cfg, []models.Alert{a}, nil, now)

	res := s.postWebhook(context.Background(), cfg, msg)
	require.True(t, res.OK, res.Message)
	mac := hmac.New(sha256.New, []byte("shh"))
	mac.Write(got.body)
	assert.Equal(t, hex.EncodeToString(mac.Sum(nil)), got.signature)
	assert.JSONEq(t, `{
		"version": "1", "source": "Acme", "panel_url": "https://panel.example.com", "sent_at": "2026-11-19T10:00:00Z",
		"text": "Critical: Node n1 is offline\nIt has missed 3 checks in a row.",
		"alerts": [{
			"id": "a1", "key": "node_offline:n1", "kind": "node_offline", "status": "firing", "severity": "critical",
			"title": "Node n1 is offline", "detail": "It has missed 3 checks in a row.",
			"resource": {"type": "node", "id": "n1", "name": "n1"},
			"fired_at": "2026-11-19T10:00:00Z", "resolved_at": null
		}]
	}`, string(got.body))

	// Chat formats carry the text under the field each service reads.
	cfg.WebhookFormat = AlertWebhookSlack
	require.True(t, s.postWebhook(context.Background(), cfg, msg).OK)
	assert.JSONEq(t, `{"text": "[Acme] Node n1 is offline\nCritical: Node n1 is offline\nIt has missed 3 checks in a row."}`, string(got.body))
	cfg.WebhookFormat = AlertWebhookDiscord
	require.True(t, s.postWebhook(context.Background(), cfg, msg).OK)
	assert.Contains(t, string(got.body), `"content":`)

	// A refusal is a failure, and no failure repeats the address: it is a credential.
	status = http.StatusForbidden
	res = s.postWebhook(context.Background(), cfg, msg)
	assert.False(t, res.OK)
	assert.Equal(t, "the receiver answered 403 Forbidden", res.Message)
	srv.Close()
	res = s.postWebhook(context.Background(), cfg, msg)
	assert.False(t, res.OK)
	assert.NotContains(t, res.Message, "secret-token")
}

func TestAlertSettingsAreCheckedAndKeepTheirSecrets(t *testing.T) {
	db := newAlertDB(t)
	s := NewAlertService(db, nil, alertTestKey)
	ctx := context.Background()

	_, err := s.merge(ctx, AlertSettingsInput{EmailEnabled: true})
	assert.ErrorIs(t, err, ErrAlertSettings, "email on with nobody to send to")
	_, err = s.merge(ctx, AlertSettingsInput{EmailRecipients: []string{"not an address"}})
	assert.ErrorIs(t, err, ErrAlertSettings)
	_, err = s.merge(ctx, AlertSettingsInput{WebhookEnabled: true})
	assert.ErrorIs(t, err, ErrAlertSettings, "webhook on with no address")
	_, err = s.merge(ctx, AlertSettingsInput{WebhookURL: "ftp://example.com/x"})
	assert.ErrorIs(t, err, ErrAlertSettings)
	_, err = s.merge(ctx, AlertSettingsInput{StorageCriticalPercent: 20})
	assert.ErrorIs(t, err, ErrAlertSettings)

	st, err := s.merge(ctx, AlertSettingsInput{
		EmailEnabled: true, EmailRecipients: []string{" Ops <ops@example.com> ", "ops@example.com", ""},
		WebhookEnabled: true, WebhookURL: "https://hooks.example.com/services/T0/B0/s3cr3t", WebhookSecret: "shh",
		Kinds: map[string]bool{models.AlertKindJobFailed: false}, CooldownMinutes: 30,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ops@example.com"}, st.EmailRecipients)
	assert.Equal(t, DefaultStorageCriticalPct, st.StorageCriticalPercent, "a value not sent keeps the default")
	assert.Equal(t, 30, st.CooldownMinutes)

	// At rest the address and the secret are ciphertext.
	data, _ := json.Marshal(st)
	assert.NotContains(t, string(data), "s3cr3t")
	assert.NotContains(t, string(data), "shh")
	assert.NotContains(t, string(data), "hooks.example.com")
	require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES (?, ?)`, AlertSettingsSection, string(data)).Error)

	// Reading back never returns them, only that they are saved and which host.
	status := s.Status(ctx)
	out, _ := json.Marshal(status)
	assert.NotContains(t, string(out), "s3cr3t")
	assert.NotContains(t, string(out), "shh")
	assert.True(t, status.HasWebhookURL)
	assert.True(t, status.HasWebhookSecret)
	assert.Equal(t, "hooks.example.com", status.WebhookHost)
	assert.False(t, status.Kinds[models.AlertKindJobFailed])
	assert.True(t, status.Kinds[models.AlertKindNodeOffline])

	// A save that leaves both blank keeps both, and what it does not mention.
	kept, err := s.merge(ctx, AlertSettingsInput{WebhookEnabled: true})
	require.NoError(t, err)
	cfg := s.resolve(ctx, kept)
	assert.Equal(t, "https://hooks.example.com/services/T0/B0/s3cr3t", cfg.webhookURL)
	assert.Equal(t, "shh", cfg.webhookSecret)
	assert.False(t, cfg.kindOn(models.AlertKindJobFailed))
	assert.Equal(t, 30, kept.CooldownMinutes)

	gone, err := s.merge(ctx, AlertSettingsInput{RemoveWebhook: true})
	require.NoError(t, err)
	assert.Empty(t, gone.WebhookURLEnc)
	assert.Empty(t, gone.WebhookSecretEnc)
	assert.False(t, gone.WebhookEnabled)

	// A key that can no longer read the address must not send to nowhere.
	other := NewAlertService(db, nil, "ffffffffffffffffffffffffffffffff")
	assert.False(t, other.resolve(ctx, kept).webhookOn())
	_, err = other.merge(ctx, AlertSettingsInput{WebhookEnabled: true})
	assert.True(t, errors.Is(err, ErrAlertSettings))
	rotated := NewAlertService(db, nil, "ffffffffffffffffffffffffffffffff", alertTestKey)
	assert.True(t, rotated.resolve(ctx, kept).webhookOn(), "a retired key still reads it")
}

func TestAlertForARemovedNodeIsClosedWithoutARecovery(t *testing.T) {
	h := newAlertHarness(t, newAlertDB(t))
	h.settings(t, emailOn)
	ctx := context.Background()
	h.Raise(ctx, nodeDown(models.AlertSeverityCritical))
	other := nodeDown(models.AlertSeverityCritical)
	other.Key, other.ResourceID = "node_offline:n2", "n2"
	h.Raise(ctx, other)
	require.Len(t, h.flush(), 1)

	h.DropGone(ctx, "node", []string{"n2"}, "Closed: the node was removed")
	rows := h.rows(t)
	require.Len(t, rows, 2)
	for _, r := range rows {
		if r.ResourceID == "n1" {
			assert.Equal(t, models.AlertStateResolved, r.State)
			assert.Equal(t, "Closed: the node was removed", r.Delivery)
		} else {
			assert.Equal(t, models.AlertStateFiring, r.State)
		}
	}
	h.advance(time.Hour)
	assert.Empty(t, h.flush(), "a removed node is not announced as recovered")
}
