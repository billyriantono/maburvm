package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

const (
	// maxAlertAttempts is how many delivery passes an alert gets when every
	// channel fails, one pass a minute. After that an event is left to the
	// history page; an alert that is still open goes on at the slow pace.
	maxAlertAttempts = 5
	// alertSlowRetryEvery is that slow pace, in passes: about every 15 minutes.
	alertSlowRetryEvery = 15
	// alertHistoryKept is how long closed alerts stay in the history.
	alertHistoryKept = 90 * 24 * time.Hour
	// alertBatch caps one delivery pass, so a burst becomes a few messages.
	alertBatch = 50
)

// AlertService tells operators when the platform needs them: a node that
// stopped answering, a pool filling up, a backup or a job that failed for good,
// a node that no longer filters guest traffic.
//
// A lasting condition is one alert from the moment it is raised until it has
// stayed clear for the settle time, however often it flaps in between, and is
// announced once when it opens and once when it closes. Everything is kept in
// the alerts table, so a panel restart neither forgets an open alert nor
// announces it a second time.
type AlertService struct {
	db       *gorm.DB
	key      []byte
	prevKeys [][]byte
	logger   *slog.Logger
	http     *http.Client
	now      func() time.Time
	// deliver sends one message over every channel that is switched on.
	deliver func(ctx context.Context, cfg alertConfig, msg alertMessage) []AlertChannelResult

	mu      sync.Mutex // one state change at a time
	flushMu sync.Mutex // one delivery pass at a time

	cfgMu    sync.Mutex
	cached   *alertConfig
	cachedAt time.Time
}

// NewAlertService builds the service. previousAESKeys are retired keys still
// accepted for reading the saved webhook address and signing secret.
func NewAlertService(db *gorm.DB, logger *slog.Logger, aesKey string, previousAESKeys ...string) *AlertService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &AlertService{
		db:     db,
		key:    []byte(aesKey),
		logger: logger,
		http:   &http.Client{Timeout: 10 * time.Second},
		now:    time.Now,
	}
	for _, k := range previousAESKeys {
		s.prevKeys = append(s.prevKeys, []byte(k))
	}
	s.deliver = s.deliverAll
	return s
}

// AlertInput describes a condition or an event. Key names the one thing it is
// about ("node_offline:<node id>"): alerts with the same key are the same alert.
type AlertInput struct {
	Key          string
	Kind         string
	Severity     string
	Title        string
	Detail       string
	ResourceType string
	ResourceID   string
	ResourceName string
}

func (in AlertInput) alert(state string, at time.Time) *models.Alert {
	return &models.Alert{
		Key: in.Key, Kind: in.Kind, Severity: in.Severity, State: state,
		Title: in.Title, Detail: in.Detail,
		ResourceType: in.ResourceType, ResourceID: in.ResourceID, ResourceName: in.ResourceName,
		FiredAt: at,
	}
}

func alertRank(severity string) int {
	if severity == models.AlertSeverityCritical {
		return 2
	}
	return 1
}

// openAlert returns the firing alert of a key, or nil.
func (s *AlertService) openAlert(ctx context.Context, key string) (*models.Alert, error) {
	var rows []models.Alert
	if err := s.db.WithContext(ctx).Where("alert_key = ? AND state = ?", key, models.AlertStateFiring).Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// Raise says a condition holds right now. It is safe to call on every check:
// an alert that is already open stays the one alert. Only a condition that got
// worse (warning to critical) is announced again.
func (s *AlertService) Raise(ctx context.Context, in AlertInput) {
	if s == nil {
		return
	}
	kindOn := s.config(ctx).kindOn(in.Kind)
	s.mu.Lock()
	defer s.mu.Unlock()
	open, err := s.openAlert(ctx, in.Key)
	if err != nil {
		s.logger.Warn("alerts: read failed", "key", in.Key, "error", err)
		return
	}
	if !kindOn {
		if open != nil {
			s.closeQuietly(ctx, open, "Closed: alerts of this kind were switched off")
		}
		return
	}
	if open == nil {
		if err := s.db.WithContext(ctx).Create(in.alert(models.AlertStateFiring, s.now())).Error; err != nil {
			s.logger.Warn("alerts: could not record", "key", in.Key, "error", err)
		}
		return
	}
	updates := map[string]interface{}{}
	if open.ClearSince != nil {
		// Back inside the settle time: the same alert, not a new one.
		updates["clear_since"] = nil
	}
	if open.Severity != in.Severity {
		updates["severity"], updates["title"], updates["detail"] = in.Severity, in.Title, in.Detail
		if alertRank(in.Severity) > alertRank(open.Severity) {
			updates["notified_at"], updates["attempts"] = nil, 0
		}
	}
	if len(updates) > 0 {
		if err := s.db.WithContext(ctx).Model(&models.Alert{}).Where("id = ?", open.ID).Updates(updates).Error; err != nil {
			s.logger.Warn("alerts: could not update", "key", in.Key, "error", err)
		}
	}
}

// Clear says a condition does not hold right now. The alert closes, and the
// recovery is announced, once it has stayed clear for the settle time.
func (s *AlertService) Clear(ctx context.Context, key string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	open, err := s.openAlert(ctx, key)
	if err != nil || open == nil || open.ClearSince != nil {
		return
	}
	if err := s.db.WithContext(ctx).Model(&models.Alert{}).Where("id = ?", open.ID).Update("clear_since", s.now()).Error; err != nil {
		s.logger.Warn("alerts: could not update", "key", key, "error", err)
	}
}

// Event records something that happened once, such as a backup that failed.
// The same key within the settle time is kept in the history but not sent
// again, so one VM failing over and over is one message.
func (s *AlertService) Event(ctx context.Context, in AlertInput) {
	if s == nil {
		return
	}
	cfg := s.config(ctx)
	if !cfg.kindOn(in.Kind) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	a := in.alert(models.AlertStateEvent, now)
	// The last one operators were, or are about to be, told about.
	var last []models.Alert
	if err := s.db.WithContext(ctx).
		Where("alert_key = ? AND state = ? AND (delivered = ? OR notified_at IS NULL)", in.Key, models.AlertStateEvent, true).
		Order("fired_at DESC").Limit(1).Find(&last).Error; err == nil && len(last) == 1 && now.Sub(last[0].FiredAt) < cfg.cooldown {
		a.NotifiedAt = &now
		a.Delivery = fmt.Sprintf("Not sent: the same alert went out %s ago", roundAge(now.Sub(last[0].FiredAt)))
	}
	if err := s.db.WithContext(ctx).Create(a).Error; err != nil {
		s.logger.Warn("alerts: could not record", "key", in.Key, "error", err)
	}
}

func roundAge(d time.Duration) string {
	if d < time.Minute {
		return "less than a minute"
	}
	if d < time.Hour {
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
}

// closeQuietly closes an open alert without a recovery message.
func (s *AlertService) closeQuietly(ctx context.Context, a *models.Alert, why string) {
	now := s.now()
	updates := map[string]interface{}{
		"state": models.AlertStateResolved, "resolved_at": now, "recovery_notified_at": now, "delivery": why,
	}
	if a.NotifiedAt == nil {
		updates["notified_at"] = now
	}
	if err := s.db.WithContext(ctx).Model(&models.Alert{}).Where("id = ? AND state = ?", a.ID, models.AlertStateFiring).Updates(updates).Error; err != nil {
		s.logger.Warn("alerts: could not close", "key", a.Key, "error", err)
	}
}

// Drop closes the open alert of a key without a recovery message, for a
// condition that did not clear but stopped mattering.
func (s *AlertService) Drop(ctx context.Context, key, why string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if open, err := s.openAlert(ctx, key); err == nil && open != nil {
		s.closeQuietly(ctx, open, why)
	}
}

// DropGone closes, without a recovery message, the open alerts about things of
// one type that no longer exist. ids are the ones that still do.
func (s *AlertService) DropGone(ctx context.Context, resourceType string, ids []string, why string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.db.WithContext(ctx).Where("state = ? AND resource_type = ?", models.AlertStateFiring, resourceType)
	if len(ids) > 0 {
		q = q.Where("resource_id NOT IN ?", ids)
	}
	var gone []models.Alert
	if err := q.Find(&gone).Error; err != nil {
		s.logger.Warn("alerts: read failed", "error", err)
		return
	}
	for i := range gone {
		s.closeQuietly(ctx, &gone[i], why)
	}
}

// Close is an operator closing an open alert by hand. No recovery is sent; if
// the condition still holds, the next check opens a new alert.
func (s *AlertService) Close(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []models.Alert
	if err := s.db.WithContext(ctx).Where("id = ? AND state = ?", id, models.AlertStateFiring).Limit(1).Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) == 0 {
		return gorm.ErrRecordNotFound
	}
	s.closeQuietly(ctx, &rows[0], "Closed by an operator")
	return nil
}

// List returns the alert history, newest first. openOnly keeps what is firing.
func (s *AlertService) List(ctx context.Context, openOnly bool, limit, offset int) ([]models.Alert, int64, error) {
	q := s.db.WithContext(ctx).Model(&models.Alert{})
	if openOnly {
		q = q.Where("state = ?", models.AlertStateFiring)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := []models.Alert{}
	err := q.Order("fired_at DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}

// Flush closes the alerts whose condition has stayed clear, then tells
// operators everything they have not been told yet, in one message per channel.
// Called once a minute by the collector; a pass that is still sending (a mail
// server that hangs) makes the next one a no-op instead of a queue.
func (s *AlertService) Flush(ctx context.Context) {
	if s == nil || !s.flushMu.TryLock() {
		return
	}
	defer s.flushMu.Unlock()
	cfg := s.config(ctx)
	now := s.now()

	s.mu.Lock()
	s.settle(ctx, cfg.cooldown, now)
	firing, recovered, err := s.pending(ctx, now)
	s.mu.Unlock()
	if err != nil {
		s.logger.Warn("alerts: read failed", "error", err)
		return
	}
	if len(firing)+len(recovered) == 0 {
		return
	}

	var results []AlertChannelResult
	if cfg.anyChannel() {
		results = s.deliver(ctx, cfg, buildAlertMessage(cfg, firing, recovered, now))
	}
	s.mu.Lock()
	s.record(ctx, firing, recovered, results, s.now())
	s.mu.Unlock()
}

// settle resolves the alerts that have stayed clear for the settle time and
// drops history past its keep time.
func (s *AlertService) settle(ctx context.Context, cooldown time.Duration, now time.Time) {
	db := s.db.WithContext(ctx)
	var clearing []models.Alert
	if err := db.Where("state = ? AND clear_since IS NOT NULL", models.AlertStateFiring).Find(&clearing).Error; err != nil {
		s.logger.Warn("alerts: read failed", "error", err)
		return
	}
	for _, a := range clearing {
		if now.Sub(*a.ClearSince) < cooldown {
			continue
		}
		if err := db.Model(&models.Alert{}).Where("id = ? AND state = ?", a.ID, models.AlertStateFiring).
			Updates(map[string]interface{}{"state": models.AlertStateResolved, "resolved_at": now, "attempts": 0}).Error; err != nil {
			s.logger.Warn("alerts: could not resolve", "key", a.Key, "error", err)
		}
	}
	_ = db.Where("state <> ? AND fired_at < ?", models.AlertStateFiring, now.Add(-alertHistoryKept)).Delete(&models.Alert{}).Error
}

// pending returns what operators have not been told: alerts and events not
// yet announced, and announced alerts that have since resolved.
func (s *AlertService) pending(ctx context.Context, now time.Time) (firing, recovered []models.Alert, err error) {
	db := s.db.WithContext(ctx)
	var unannounced []models.Alert
	if err = db.Where("notified_at IS NULL").Order("fired_at").Limit(alertBatch).Find(&unannounced).Error; err != nil {
		return nil, nil, err
	}
	for _, a := range unannounced {
		if a.State == models.AlertStateFiring && a.Attempts >= maxAlertAttempts && a.Attempts%alertSlowRetryEvery != 0 {
			// Waiting for its next slow try; attempts counts the passes.
			_ = db.Model(&models.Alert{}).Where("id = ?", a.ID).Update("attempts", a.Attempts+1).Error
			continue
		}
		if a.State != models.AlertStateResolved {
			firing = append(firing, a)
			continue
		}
		// It opened and closed before anyone was told: nothing to say now.
		_ = db.Model(&models.Alert{}).Where("id = ?", a.ID).Updates(map[string]interface{}{
			"notified_at": now, "recovery_notified_at": now, "delivery": "Not sent: it cleared before it was announced",
		}).Error
	}
	err = db.Where("state = ? AND delivered = ? AND recovery_notified_at IS NULL", models.AlertStateResolved, true).
		Order("fired_at").Limit(alertBatch).Find(&recovered).Error
	return firing, recovered, err
}

// record writes the outcome of one delivery pass on the alerts it carried.
// results is empty when no channel is set up.
func (s *AlertService) record(ctx context.Context, firing, recovered []models.Alert, results []AlertChannelResult, now time.Time) {
	db := s.db.WithContext(ctx)
	sent := false
	parts := make([]string, 0, len(results))
	for _, r := range results {
		sent = sent || r.OK
		parts = append(parts, r.Channel+": "+r.Message)
	}
	summary := strings.Join(parts, "; ")

	for _, a := range firing {
		u := map[string]interface{}{}
		switch {
		case len(results) == 0:
			u["notified_at"], u["delivery"] = now, "Not sent: no delivery is set up"
		case sent:
			u["notified_at"], u["delivered"], u["attempts"], u["delivery"] = now, true, 0, summary
		case a.State == models.AlertStateEvent && a.Attempts+1 >= maxAlertAttempts:
			u["notified_at"], u["attempts"], u["delivery"] = now, a.Attempts+1, fmt.Sprintf("Not sent after %d tries. %s", maxAlertAttempts, summary)
		default:
			// A condition that still holds is never given up on: nobody has
			// been told, and it is still true.
			u["attempts"], u["delivery"] = a.Attempts+1, "Will be tried again. "+summary
		}
		// Matching the severity that was sent keeps an alert that got worse
		// meanwhile due for its second message.
		if err := db.Model(&models.Alert{}).Where("id = ? AND severity = ?", a.ID, a.Severity).Updates(u).Error; err != nil {
			s.logger.Warn("alerts: could not record delivery", "key", a.Key, "error", err)
		}
	}
	for _, a := range recovered {
		u := map[string]interface{}{}
		switch {
		case len(results) == 0 || sent:
			u["recovery_notified_at"] = now
			if sent {
				u["delivery"] = "Recovery: " + summary
			}
		case a.Attempts+1 >= maxAlertAttempts:
			u["recovery_notified_at"], u["attempts"], u["delivery"] = now, a.Attempts+1, fmt.Sprintf("Recovery not sent after %d tries. %s", maxAlertAttempts, summary)
		default:
			u["attempts"] = a.Attempts + 1
		}
		if err := db.Model(&models.Alert{}).Where("id = ?", a.ID).Updates(u).Error; err != nil {
			s.logger.Warn("alerts: could not record delivery", "key", a.Key, "error", err)
		}
	}
}

// jobLabels names a job kind the way an operator would.
var jobLabels = map[string]string{
	"vm_operation":       "VM",
	"restore":            "Restore",
	"image":              "Image capture",
	"snapshot_operation": "Snapshot",
	"import":             "Import",
	"template_sync":      "Template sync",
	"audit":              "Audit log write",
	"network_config":     "Network change",
	"webhook_delivery":   "Billing event delivery",
}

// JobFailed records a background job that failed for good: every try used,
// or a failure the worker judged would not change. A failed backup is its own
// kind of alert, so it can be switched on its own.
func (s *AlertService) JobFailed(ctx context.Context, kind string, args []byte, reason string) {
	if s == nil {
		return
	}
	var p struct {
		VMID      string `json:"vm_id"`
		ImageID   string `json:"image_id"`
		NodeID    string `json:"node_id"`
		Operation string `json:"operation"`
	}
	_ = json.Unmarshal(args, &p)
	if len(reason) > 500 {
		// Cut on a character: the database refuses half of one, and the alert with it.
		reason = strings.ToValidUTF8(reason[:500], "") + "…"
	}

	in := AlertInput{Kind: models.AlertKindJobFailed, Severity: models.AlertSeverityWarning, Detail: reason}
	subject := ""
	if p.VMID != "" {
		in.ResourceType, in.ResourceID = "vm", p.VMID
		_ = s.db.WithContext(ctx).Raw("SELECT hostname FROM vms WHERE id = ?", p.VMID).Scan(&in.ResourceName).Error
		if in.ResourceName == "" {
			in.ResourceName = p.VMID
		}
		subject = " for " + in.ResourceName
	}
	if kind == "backup" {
		in.Kind = models.AlertKindBackupFailed
		in.Key = "backup_failed:" + p.VMID
		in.Title = "Backup failed" + subject
	} else {
		label := jobLabels[kind]
		if label == "" {
			label = "Job " + kind
		}
		if p.Operation != "" {
			label += " " + p.Operation
		}
		in.Key = strings.Join([]string{"job_failed", kind, p.Operation, p.VMID + p.ImageID}, ":")
		in.Title = label + " failed" + subject
	}
	s.Event(ctx, in)
}
