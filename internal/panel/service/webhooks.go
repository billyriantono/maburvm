package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/riverqueue/river"
	"gorm.io/gorm"
)

// Outbound webhooks an operator adds under System settings, API: each its own
// URL, signing secret and set of events. Every event is stored per endpoint in
// the same delivery log as the billing system's events and goes through the
// same queue job, so retries, the log and "send again" work the same way.

// Events other than the billing ones (billing_events.go).
const (
	EventBackupCompleted = "backup.completed"
	EventBackupFailed    = "backup.failed"
	EventAlertRaised     = "alert.raised"
	EventAlertResolved   = "alert.resolved"
	EventUserCreated     = "user.created"
	EventUserSuspended   = "user.suspended"
	EventUserUnsuspended = "user.unsuspended"
	EventUserDeleted     = "user.deleted"
	// EventWebhookTest is what the Send test button sends.
	EventWebhookTest = "webhook.test"
	// WebhookAllEvents subscribes an endpoint to every event, including ones
	// added later.
	WebhookAllEvents = "*"
)

// WebhookEventInfo is one event an endpoint can subscribe to.
type WebhookEventInfo struct {
	Event string `json:"event"`
	Label string `json:"label"`
}

// WebhookEventGroup is one group of events on the endpoint form.
type WebhookEventGroup struct {
	Group  string             `json:"group"`
	Events []WebhookEventInfo `json:"events"`
}

// WebhookEventCatalog is every event the panel sends, grouped.
var WebhookEventCatalog = []WebhookEventGroup{
	{"VMs", []WebhookEventInfo{
		{BillingEventVMCreated, "A VM finished installing"},
		{BillingEventVMFailed, "A VM could not be installed"},
		{BillingEventVMSuspended, "A VM was suspended"},
		{BillingEventVMUnsuspended, "A VM was unsuspended"},
		{BillingEventVMDestroyed, "A VM was deleted"},
	}},
	{"Backups", []WebhookEventInfo{
		{EventBackupCompleted, "A backup finished"},
		{EventBackupFailed, "A backup failed for good"},
	}},
	{"Alerts", []WebhookEventInfo{
		{EventAlertRaised, "An alert opened, or an event alert fired"},
		{EventAlertResolved, "An alert cleared"},
	}},
	{"Billing", []WebhookEventInfo{
		{BillingEventOverage, "A VM went over its transfer quota"},
		{BillingEventFloatingIP, "A customer's floating IP count changed"},
	}},
	{"Accounts", []WebhookEventInfo{
		{EventUserCreated, "An account was created"},
		{EventUserSuspended, "An account was suspended"},
		{EventUserUnsuspended, "An account was restored"},
		{EventUserDeleted, "An account was deleted"},
	}},
}

func knownWebhookEvent(event string) bool {
	if event == WebhookAllEvents {
		return true
	}
	for _, g := range WebhookEventCatalog {
		for _, e := range g.Events {
			if e.Event == event {
				return true
			}
		}
	}
	return false
}

// WebhookEndpoint is one outbound webhook.
type WebhookEndpoint struct {
	ID     string   `json:"id" gorm:"type:uuid;primaryKey"`
	Name   string   `json:"name"`
	URL    string   `json:"url"`
	Secret string   `json:"-" gorm:"serializer:secret"`
	Events []string `json:"events" gorm:"type:jsonb;serializer:json"`
	// HasSecret says a secret is saved; the secret itself is never shown.
	HasSecret bool      `json:"has_secret" gorm:"-"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (WebhookEndpoint) TableName() string { return "webhook_endpoints" }

// AfterFind fills HasSecret.
func (w *WebhookEndpoint) AfterFind(*gorm.DB) error {
	w.HasSecret = w.Secret != ""
	return nil
}

// Wants reports whether the endpoint subscribed to event.
func (w *WebhookEndpoint) Wants(event string) bool {
	return w.Enabled && (event == EventWebhookTest || slices.Contains(w.Events, WebhookAllEvents) || slices.Contains(w.Events, event))
}

// WebhookEndpointRequest creates or changes an endpoint. A nil Secret keeps
// the saved one; an empty one removes it.
type WebhookEndpointRequest struct {
	Name    string   `json:"name"`
	URL     string   `json:"url"`
	Secret  *string  `json:"secret,omitempty"`
	Events  []string `json:"events"`
	Enabled *bool    `json:"enabled,omitempty"`
}

// ErrWebhookEndpointNotFound names an endpoint id that does not exist.
var ErrWebhookEndpointNotFound = errors.New("webhook endpoint not found")

// ErrWebhookEndpointInvalid wraps a refused endpoint form.
var ErrWebhookEndpointInvalid = errors.New("invalid webhook endpoint")

// minWebhookSecret matches the billing webhook's HMAC secret rule.
const minWebhookSecret = 32

func (r *WebhookEndpointRequest) apply(w *WebhookEndpoint) error {
	bad := func(msg string) error { return fmt.Errorf("%w: %s", ErrWebhookEndpointInvalid, msg) }
	w.Name = strings.TrimSpace(r.Name)
	if w.Name == "" || len(w.Name) > 100 {
		return bad("name is required, at most 100 characters")
	}
	w.URL = strings.TrimSpace(r.URL)
	u, err := url.Parse(w.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return bad("url must be an http or https address")
	}
	events := []string{}
	for _, e := range r.Events {
		e = strings.TrimSpace(e)
		if !knownWebhookEvent(e) {
			return bad(fmt.Sprintf("unknown event %q", e))
		}
		if !slices.Contains(events, e) {
			events = append(events, e)
		}
	}
	if len(events) == 0 {
		return bad("choose at least one event")
	}
	w.Events = events
	if r.Secret != nil {
		s := strings.TrimSpace(*r.Secret)
		if s != "" && len(s) < minWebhookSecret {
			return bad(fmt.Sprintf("the secret must be at least %d characters", minWebhookSecret))
		}
		w.Secret = s
	}
	if r.Enabled != nil {
		w.Enabled = *r.Enabled
	}
	return nil
}

// ListWebhookEndpoints returns every endpoint, oldest first.
func ListWebhookEndpoints(ctx context.Context, db *gorm.DB) ([]WebhookEndpoint, error) {
	rows := []WebhookEndpoint{}
	err := db.WithContext(ctx).Order("created_at ASC, id ASC").Find(&rows).Error
	return rows, err
}

// GetWebhookEndpoint returns one endpoint.
func GetWebhookEndpoint(ctx context.Context, db *gorm.DB, id string) (*WebhookEndpoint, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrWebhookEndpointNotFound
	}
	var w WebhookEndpoint
	if err := db.WithContext(ctx).First(&w, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrWebhookEndpointNotFound
		}
		return nil, err
	}
	return &w, nil
}

// CreateWebhookEndpoint adds an endpoint, switched on unless the form says not.
func CreateWebhookEndpoint(ctx context.Context, db *gorm.DB, req WebhookEndpointRequest) (*WebhookEndpoint, error) {
	w := &WebhookEndpoint{ID: uuid.New().String(), Enabled: true}
	if err := req.apply(w); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	w.CreatedAt, w.UpdatedAt = now, now
	if err := db.WithContext(ctx).Create(w).Error; err != nil {
		return nil, err
	}
	w.HasSecret = w.Secret != ""
	return w, nil
}

// UpdateWebhookEndpoint changes an endpoint.
func UpdateWebhookEndpoint(ctx context.Context, db *gorm.DB, id string, req WebhookEndpointRequest) (*WebhookEndpoint, error) {
	w, err := GetWebhookEndpoint(ctx, db, id)
	if err != nil {
		return nil, err
	}
	if err := req.apply(w); err != nil {
		return nil, err
	}
	w.UpdatedAt = time.Now().UTC()
	if err := db.WithContext(ctx).Select("name", "url", "secret", "events", "enabled", "updated_at").Updates(w).Error; err != nil {
		return nil, err
	}
	w.HasSecret = w.Secret != ""
	return w, nil
}

// DeleteWebhookEndpoint removes an endpoint and its delivery log.
func DeleteWebhookEndpoint(ctx context.Context, db *gorm.DB, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return ErrWebhookEndpointNotFound
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The foreign key cascades on PostgreSQL; this also covers databases without it.
		if err := tx.Where("endpoint_id = ?", id).Delete(&WebhookDelivery{}).Error; err != nil {
			return err
		}
		res := tx.Where("id = ?", id).Delete(&WebhookEndpoint{})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrWebhookEndpointNotFound
		}
		return nil
	})
}

// eventQueue is the job queue events are delivered through, for callers that
// do not hold the River client themselves (alerts, backups, accounts).
// ponytail: one process-wide client; pass it explicitly if a second panel
// process ever runs a different queue.
var eventQueue atomic.Pointer[river.Client[pgx.Tx]]

// SetEventQueue wires the job queue into EmitEvent. Called once at boot.
func SetEventQueue(rc *river.Client[pgx.Tx]) { eventQueue.Store(rc) }

func queueOr(rc *river.Client[pgx.Tx]) *river.Client[pgx.Tx] {
	if rc != nil {
		return rc
	}
	return eventQueue.Load()
}

// endpointsFor returns the switched-on endpoints that want event. A database
// that cannot be read (or has no such table yet) means none: an event never
// stops the action that caused it.
func endpointsFor(ctx context.Context, db *gorm.DB, event string) []WebhookEndpoint {
	var rows []WebhookEndpoint
	if err := db.WithContext(ctx).Where("enabled = ?", true).Order("created_at ASC").Find(&rows).Error; err != nil {
		return nil
	}
	out := rows[:0]
	for _, w := range rows {
		if w.Wants(event) {
			out = append(out, w)
		}
	}
	return out
}

// EmitEvent sends an event to every endpoint that subscribed to it. Like the
// billing events, it is best-effort and never fails its caller.
func EmitEvent(ctx context.Context, db *gorm.DB, event, resourceID string, fields map[string]any) {
	if db == nil {
		return
	}
	endpoints := endpointsFor(ctx, db, event)
	for _, w := range endpoints {
		recordDelivery(ctx, db, nil, slog.Default(), &w.ID, event, resourceID, fields)
	}
	if len(endpoints) > 0 {
		pruneDeliveries(ctx, db)
	}
}

// recordDelivery stores one event for one receiver (endpointID nil = the
// billing webhook) and queues it. It returns the delivery id, "" when the
// event could not be stored.
func recordDelivery(ctx context.Context, db *gorm.DB, rc *river.Client[pgx.Tx], logger *slog.Logger, endpointID *string, event, resourceID string, fields map[string]any) string {
	now := time.Now().UTC()
	payload := map[string]any{}
	for k, v := range fields {
		payload[k] = v
	}
	id := uuid.New().String()
	payload["id"], payload["event"], payload["sent_at"] = id, event, now
	body, err := json.Marshal(payload)
	if err != nil {
		logger.ErrorContext(ctx, "event: could not encode", "event", event, "error", err)
		return ""
	}
	row := &WebhookDelivery{ID: id, Event: event, Body: string(body), Status: DeliveryPending, ResourceID: resourceID, CreatedAt: now, EndpointID: endpointID}
	if err := db.WithContext(ctx).Create(row).Error; err != nil {
		logger.ErrorContext(ctx, "event: could not record", "event", event, "error", err)
		return ""
	}
	if err := enqueueDelivery(ctx, queueOr(rc), id); err != nil {
		logger.ErrorContext(ctx, "event: could not queue", "event", event, "error", err)
		db.WithContext(ctx).Model(&WebhookDelivery{}).Where("id = ?", id).
			Updates(map[string]any{"status": DeliveryFailed, "last_error": "could not be queued: " + err.Error()})
	}
	return id
}

// SendTestEvent sends a webhook.test event to one endpoint now, once, and
// returns how it went. It is kept in the endpoint's log like any event.
func SendTestEvent(ctx context.Context, db *gorm.DB, id string) (*WebhookDelivery, error) {
	w, err := GetWebhookEndpoint(ctx, db, id)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	deliveryID := uuid.New().String()
	body, _ := json.Marshal(map[string]any{"id": deliveryID, "event": EventWebhookTest, "sent_at": now,
		"endpoint": w.Name, "message": "A test event from the panel. Answer with any 2xx status."})
	row := &WebhookDelivery{ID: deliveryID, Event: EventWebhookTest, Body: string(body), Status: DeliveryPending, CreatedAt: now, EndpointID: &w.ID}
	if err := db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	_ = DeliverBillingEvent(ctx, db, deliveryID, true)
	if err := db.WithContext(ctx).First(row, "id = ?", deliveryID).Error; err != nil {
		return nil, err
	}
	return row, nil
}

// endpointTarget is where a delivery to an endpoint goes, read on every try so
// a corrected URL or secret applies to the retries. A test may reach an
// endpoint that is switched off, so it can be tried before it is turned on.
func endpointTarget(ctx context.Context, db *gorm.DB, id string, test bool) (url, secret string, err error) {
	w, err := GetWebhookEndpoint(ctx, db, id)
	if err != nil {
		return "", "", errors.New("the webhook endpoint was removed")
	}
	if !w.Enabled && !test {
		return "", "", errors.New("the webhook endpoint is switched off")
	}
	return w.URL, w.Secret, nil
}

// EmitBackupEvent tells the webhooks that a backup completed (failure "") or
// failed for good.
func EmitBackupEvent(ctx context.Context, db *gorm.DB, backupID, failure string) {
	event := EventBackupCompleted
	if failure != "" {
		event = EventBackupFailed
	}
	if db == nil || len(endpointsFor(ctx, db, event)) == 0 {
		return
	}
	var b models.Backup
	if err := db.WithContext(ctx).Unscoped().First(&b, "id = ?", backupID).Error; err != nil {
		return
	}
	fields := vmEventFields(ctx, db, b.VMID)
	fields["backup_id"], fields["backup_type"], fields["mode"], fields["size_bytes"] = b.ID, string(b.BackupType), b.Mode, b.Size
	if failure != "" {
		fields["reason"] = clipReason(failure)
	}
	EmitEvent(ctx, db, event, backupID, fields)
}

// alertEventFields describes an alert to a webhook.
func alertEventFields(a *models.Alert) map[string]any {
	return map[string]any{
		"alert_id": a.ID, "key": a.Key, "kind": a.Kind, "severity": a.Severity, "state": a.State,
		"title": a.Title, "detail": a.Detail, "resource_type": a.ResourceType, "resource_id": a.ResourceID,
		"resource_name": a.ResourceName, "fired_at": a.FiredAt,
	}
}

// EmitUserEvent tells the webhooks about an account: created, suspended,
// unsuspended or deleted. extra is merged in.
func EmitUserEvent(ctx context.Context, db *gorm.DB, event, userID string, extra map[string]any) {
	if db == nil || userID == "" || len(endpointsFor(ctx, db, event)) == 0 {
		return
	}
	fields := map[string]any{"user_id": userID}
	var u models.User
	if err := db.WithContext(ctx).Unscoped().First(&u, "id = ?", userID).Error; err == nil {
		fields["email"], fields["name"], fields["role"] = u.Email, u.Name, string(u.Role)
	}
	if ref := BillingUserRef(ctx, db, userID); ref != "" {
		fields["user_ref"] = ref
	}
	for k, v := range extra {
		fields[k] = v
	}
	EmitEvent(ctx, db, event, userID, fields)
}
