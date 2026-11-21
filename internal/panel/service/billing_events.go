package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
	"github.com/riverqueue/river"
	"gorm.io/gorm"
)

// Events the panel sends to the billing system's webhook (the URL and secret
// under System settings, API). Each is stored first, then delivered by a queue
// job with retries, so the rows are also the delivery log.
const (
	BillingEventVMCreated     = "vm.created"
	BillingEventVMFailed      = "vm.failed"
	BillingEventVMSuspended   = "vm.suspended"
	BillingEventVMUnsuspended = "vm.unsuspended"
	BillingEventVMDestroyed   = "vm.destroyed"
	BillingEventOverage       = "bandwidth.overage"
	BillingEventFloatingIP    = "floating_ip.usage"
)

// Delivery states.
const (
	DeliveryPending   = "pending"
	DeliveryRetrying  = "retrying"
	DeliveryDelivered = "delivered"
	DeliveryFailed    = "failed"
)

// deliveryLogRetention is how long delivery rows are kept.
const deliveryLogRetention = 90 * 24 * time.Hour

// ErrDeliveryNotFound names a delivery id that is not in the log.
var ErrDeliveryNotFound = errors.New("delivery not found")

// WebhookDelivery is one outbound event and what became of it.
type WebhookDelivery struct {
	ID    string `json:"id" gorm:"primaryKey"`
	Event string `json:"event"`
	// Body is the JSON exactly as sent: a retry signs the same bytes.
	Body          string     `json:"body"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	ResponseCode  int        `json:"response_code"`
	LastError     string     `json:"last_error"`
	ResourceID    string     `json:"resource_id"`
	CreatedAt     time.Time  `json:"created_at"`
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	DeliveredAt   *time.Time `json:"delivered_at"`
	// EndpointID is the outbound webhook it is for; nil is the billing system's.
	EndpointID *string `json:"endpoint_id,omitempty" gorm:"type:uuid"`
}

func (WebhookDelivery) TableName() string { return "webhook_deliveries" }

// SignBillingEvent is the X-Webhook-Signature of an outbound event: the hex
// HMAC-SHA256 of the body, with no prefix.
func SignBillingEvent(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// EmitBillingEvent records an event for the billing system and queues its
// delivery. Best-effort by design: a billing endpoint that is down, or not
// configured at all, never stops the action that caused the event.
func EmitBillingEvent(ctx context.Context, db *gorm.DB, rc *river.Client[pgx.Tx], logger *slog.Logger, event, resourceID string, fields map[string]any) {
	if db == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	// The operator's own webhooks that asked for this event get it too.
	endpoints := endpointsFor(ctx, db, event)
	for _, w := range endpoints {
		recordDelivery(ctx, db, rc, logger, &w.ID, event, resourceID, fields)
	}
	url, _ := resolveBillingWebhook(ctx, db)
	if url != "" {
		recordDelivery(ctx, db, rc, logger, nil, event, resourceID, fields)
	} else {
		logger.InfoContext(ctx, "billing event not sent: no webhook URL configured", "event", event, "resource_id", resourceID)
	}
	if url != "" || len(endpoints) > 0 {
		pruneDeliveries(ctx, db)
	}
}

// pruneDeliveries drops delivery rows past the retention, every receiver's.
// ponytail: pruned on write; move to a scheduled job if the log grows large.
func pruneDeliveries(ctx context.Context, db *gorm.DB) {
	db.WithContext(ctx).Where("created_at < ?", time.Now().UTC().Add(-deliveryLogRetention)).Delete(&WebhookDelivery{})
}

func enqueueDelivery(ctx context.Context, rc *river.Client[pgx.Tx], id string) error {
	if rc == nil {
		return errors.New("the job queue is not running")
	}
	_, err := rc.Insert(ctx, queue.WebhookDeliveryJob{DeliveryID: id}, nil)
	return err
}

var billingEventHTTP = &http.Client{Timeout: 10 * time.Second}

// DeliverBillingEvent makes one attempt at a stored event. An error asks the
// queue for another try; final says this was the last one.
func DeliverBillingEvent(ctx context.Context, db *gorm.DB, id string, final bool) error {
	var row WebhookDelivery
	if err := db.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // pruned or removed: nothing left to send
		}
		return err
	}
	if row.Status == DeliveryDelivered {
		return nil
	}
	now := time.Now().UTC()
	update := map[string]any{"attempts": row.Attempts + 1, "last_attempt_at": now}
	finish := func(sendErr error, code int) error {
		update["response_code"] = code
		switch {
		case sendErr == nil:
			update["status"], update["last_error"], update["delivered_at"] = DeliveryDelivered, "", now
		case final:
			update["status"], update["last_error"] = DeliveryFailed, sendErr.Error()
		default:
			update["status"], update["last_error"] = DeliveryRetrying, sendErr.Error()
		}
		if err := db.WithContext(ctx).Model(&WebhookDelivery{}).Where("id = ?", id).Updates(update).Error; err != nil {
			return err
		}
		return sendErr
	}

	// Read on every attempt, so a corrected URL or secret applies to the retries.
	var url, secret string
	if row.EndpointID != nil {
		var err error
		if url, secret, err = endpointTarget(ctx, db, *row.EndpointID, row.Event == EventWebhookTest); err != nil {
			final = true
			_ = finish(err, 0)
			return nil
		}
	} else if url, secret = resolveBillingWebhook(ctx, db); url == "" {
		final = true
		_ = finish(errors.New("no webhook URL is configured"), 0)
		return nil // a retry would find the same
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(row.Body)))
	if err != nil {
		final = true
		_ = finish(fmt.Errorf("the webhook URL is not usable: %w", err), 0)
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Event", row.Event)
	req.Header.Set("X-Webhook-Id", row.ID)
	if secret != "" {
		req.Header.Set("X-Webhook-Signature", SignBillingEvent([]byte(row.Body), secret))
	}
	resp, err := billingEventHTTP.Do(req)
	if err != nil {
		return finish(err, 0)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		reply, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return finish(fmt.Errorf("the receiver answered HTTP %d: %s", resp.StatusCode,
			strings.ToValidUTF8(strings.TrimSpace(string(reply)), "")), resp.StatusCode)
	}
	return finish(nil, resp.StatusCode)
}

// ListBillingDeliveries returns the billing webhook's delivery log, newest first.
func ListBillingDeliveries(ctx context.Context, db *gorm.DB, status string, limit, offset int) ([]WebhookDelivery, int64, error) {
	return listDeliveries(ctx, db, nil, status, limit, offset)
}

// ListEndpointDeliveries returns one outbound webhook's delivery log, newest first.
func ListEndpointDeliveries(ctx context.Context, db *gorm.DB, endpointID, status string, limit, offset int) ([]WebhookDelivery, int64, error) {
	return listDeliveries(ctx, db, &endpointID, status, limit, offset)
}

// scopeDeliveries narrows to one receiver's rows: an endpoint's, or (nil) the billing webhook's.
func scopeDeliveries(q *gorm.DB, endpointID *string) *gorm.DB {
	if endpointID == nil {
		return q.Where("endpoint_id IS NULL")
	}
	return q.Where("endpoint_id = ?", *endpointID)
}

func listDeliveries(ctx context.Context, db *gorm.DB, endpointID *string, status string, limit, offset int) ([]WebhookDelivery, int64, error) {
	q := scopeDeliveries(db.WithContext(ctx).Model(&WebhookDelivery{}), endpointID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := []WebhookDelivery{}
	err := q.Order("created_at DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}

// RetryBillingDelivery queues a stored billing event again, with a full set of tries.
func RetryBillingDelivery(ctx context.Context, db *gorm.DB, rc *river.Client[pgx.Tx], id string) error {
	return retryDelivery(ctx, db, rc, nil, id)
}

// RetryEndpointDelivery queues one outbound webhook's stored event again.
func RetryEndpointDelivery(ctx context.Context, db *gorm.DB, rc *river.Client[pgx.Tx], endpointID, id string) error {
	return retryDelivery(ctx, db, rc, &endpointID, id)
}

func retryDelivery(ctx context.Context, db *gorm.DB, rc *river.Client[pgx.Tx], endpointID *string, id string) error {
	res := scopeDeliveries(db.WithContext(ctx).Model(&WebhookDelivery{}), endpointID).Where("id = ? AND status <> ?", id, DeliveryDelivered).
		Updates(map[string]any{"status": DeliveryPending, "last_error": ""})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrDeliveryNotFound
	}
	return enqueueDelivery(ctx, queueOr(rc), id)
}

// vmEventFields describes a VM to the billing system: who owns it, which
// service pays for it, and how it is reached.
func vmEventFields(ctx context.Context, db *gorm.DB, vmID string) map[string]any {
	fields := map[string]any{"vm_id": vmID}
	var vm models.VM
	if err := db.WithContext(ctx).Unscoped().First(&vm, "id = ?", vmID).Error; err != nil {
		return fields
	}
	fields["user_id"], fields["hostname"], fields["status"] = vm.UserID, vm.Hostname, string(vm.Status)
	if vm.BillingRef != nil {
		fields["external_ref"] = *vm.BillingRef
	}
	if vm.PlanID != nil {
		fields["plan_id"] = *vm.PlanID
	}
	if ref := BillingUserRef(ctx, db, vm.UserID); ref != "" {
		fields["user_ref"] = ref
	}
	access := VMAccess(ctx, db, &vm)
	fields["ip"], fields["ips"], fields["username"] = access.IP, access.IPs, access.Username
	return fields
}

// EmitVMBillingEvent sends a lifecycle event about one VM. extra is merged in.
func EmitVMBillingEvent(ctx context.Context, db *gorm.DB, rc *river.Client[pgx.Tx], logger *slog.Logger, event, vmID string, extra map[string]any) {
	if db == nil {
		return
	}
	if url, _ := resolveBillingWebhook(ctx, db); url == "" && len(endpointsFor(ctx, db, event)) == 0 {
		return // skip the lookups: nobody is listening
	}
	fields := vmEventFields(ctx, db, vmID)
	for k, v := range extra {
		fields[k] = v
	}
	EmitBillingEvent(ctx, db, rc, logger, event, vmID, fields)
}

// BillingJobFailed tells the billing system about a VM whose creation failed,
// so the order is not left looking provisioned. retrying marks a failed try
// the queue will repeat: it is reported once, when the VM first shows as
// failed. Without it the billing system would hear nothing until every try is
// used, which can be weeks.
func BillingJobFailed(ctx context.Context, db *gorm.DB, rc *river.Client[pgx.Tx], kind string, args []byte, reason string, retrying bool) {
	var p struct {
		VMID      string `json:"vm_id"`
		Operation string `json:"operation"`
	}
	if db == nil || kind != (queue.VMOperationJob{}).Kind() || json.Unmarshal(args, &p) != nil ||
		p.VMID == "" || p.Operation != string(queue.VMOpCreate) {
		return
	}
	extra := map[string]any{"reason": clipReason(reason)}
	if retrying {
		var status string
		if err := db.WithContext(ctx).Model(&models.VM{}).Where("id = ?", p.VMID).Pluck("status", &status).Error; err != nil ||
			status != string(models.VMStatusError) {
			return // a try lost to a busy or unreachable node is not a failed installation
		}
		var told int64
		if db.WithContext(ctx).Model(&WebhookDelivery{}).Where("event = ? AND resource_id = ?", BillingEventVMFailed, p.VMID).
			Count(&told); told > 0 {
			return
		}
		extra["retrying"] = true
	}
	EmitVMBillingEvent(ctx, db, rc, nil, BillingEventVMFailed, p.VMID, extra)
}

func clipReason(reason string) string {
	if len(reason) > 500 {
		return strings.ToValidUTF8(reason[:500], "") + "…"
	}
	return reason
}

// VMCreateJob reads a VM's creation job from the queue: its state and the last
// error it hit. Both are empty once the queue no longer keeps the job. A var
// so tests on SQLite, which has no queue tables, can answer for it.
var VMCreateJob = func(ctx context.Context, db *gorm.DB, vmID string) (state, lastError string) {
	var row struct {
		State     string
		LastError string
	}
	if db == nil {
		return "", ""
	}
	_ = db.WithContext(ctx).Raw(`SELECT state::text AS state, COALESCE(errors[array_upper(errors, 1)]->>'error', '') AS last_error
		FROM river_job WHERE kind = ? AND args->>'vm_id' = ? AND args->>'operation' = ?
		ORDER BY id DESC LIMIT 1`, (queue.VMOperationJob{}).Kind(), vmID, string(queue.VMOpCreate)).Scan(&row).Error
	return row.State, clipReason(row.LastError)
}

func (s *VMService) emitVMEvent(ctx context.Context, event, vmID string) {
	EmitVMBillingEvent(ctx, s.db, s.riverClient, s.logger, event, vmID, nil)
}

// VMAccessInfo is how a VM is reached: what a billing system shows its customer.
type VMAccessInfo struct {
	IP       string   `json:"ip"`
	IPs      []string `json:"ips"`
	Username string   `json:"username"`
}

// VMAccess reads a VM's addresses (the primary one first) and login user.
func VMAccess(ctx context.Context, db *gorm.DB, vm *models.VM) VMAccessInfo {
	info := VMAccessInfo{IPs: []string{}, Username: models.OSLoginUser("")}
	var osType string
	if err := db.WithContext(ctx).Raw("SELECT os_type FROM os_templates WHERE id = ?", vm.OSTemplateID).Scan(&osType).Error; err == nil {
		info.Username = models.OSLoginUser(osKind(osType))
	}
	var nets []models.Network
	if err := db.WithContext(ctx).Where("vm_id = ?", vm.ID).Order("created_at ASC").Find(&nets).Error; err == nil {
		for i := range nets {
			// inet values can come back with a /32 or /128 suffix.
			info.IPs = append(info.IPs, strings.SplitN(nets[i].IPAddress, "/", 2)[0])
		}
	}
	if len(info.IPs) > 0 {
		info.IP = info.IPs[0]
	}
	return info
}
