package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/queue"
)

// notificationRetention is how long an account's notifications are kept.
const notificationRetention = 90 * 24 * time.Hour

// ErrNotificationNotFound names a notification that is not the caller's.
var ErrNotificationNotFound = errors.New("notification not found")

// UserNotification is something an account was told about in its own area of
// the panel: one of its VMs ready, suspended, a backup that failed.
type UserNotification struct {
	ID           string     `json:"id" gorm:"primaryKey"`
	UserID       string     `json:"-"`
	Kind         string     `json:"kind"`
	Title        string     `json:"title"`
	Body         string     `json:"body"`
	ResourceType string     `json:"resource_type"`
	ResourceID   string     `json:"resource_id"`
	ReadAt       *time.Time `json:"read_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

func (UserNotification) TableName() string { return "user_notifications" }

// addNotice records a notification for an account. Best-effort: the event it
// reports has already happened.
func addNotice(ctx context.Context, db *gorm.DB, userID, kind, title, resourceType, resourceID string) {
	if userID == "" {
		return
	}
	now := time.Now().UTC()
	n := &UserNotification{ID: uuid.New().String(), UserID: userID, Kind: kind, Title: title,
		ResourceType: resourceType, ResourceID: resourceID, CreatedAt: now}
	if err := db.WithContext(ctx).Create(n).Error; err != nil {
		slog.Default().Warn("could not record a notification", "user_id", userID, "kind", kind, "error", err)
		return
	}
	// ponytail: pruned on write; move to a scheduled job if the table grows large.
	db.WithContext(ctx).Where("user_id = ? AND created_at < ?", userID, now.Add(-notificationRetention)).Delete(&UserNotification{})
}

// ListNotifications returns an account's notifications, newest first, and how
// many are unread.
func ListNotifications(ctx context.Context, db *gorm.DB, userID string, limit int) ([]UserNotification, int64, error) {
	rows := []UserNotification{}
	if err := db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	var unread int64
	err := db.WithContext(ctx).Model(&UserNotification{}).Where("user_id = ? AND read_at IS NULL", userID).Count(&unread).Error
	return rows, unread, err
}

// MarkNotificationRead marks one of an account's notifications read.
func MarkNotificationRead(ctx context.Context, db *gorm.DB, userID, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return ErrNotificationNotFound
	}
	var n int64
	if err := db.WithContext(ctx).Model(&UserNotification{}).Where("id = ? AND user_id = ?", id, userID).Count(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		return ErrNotificationNotFound
	}
	return db.WithContext(ctx).Model(&UserNotification{}).Where("id = ? AND user_id = ? AND read_at IS NULL", id, userID).
		Update("read_at", time.Now().UTC()).Error
}

// MarkAllNotificationsRead marks every notification of an account read.
func MarkAllNotificationsRead(ctx context.Context, db *gorm.DB, userID string) error {
	return db.WithContext(ctx).Model(&UserNotification{}).Where("user_id = ? AND read_at IS NULL", userID).
		Update("read_at", time.Now().UTC()).Error
}

// templateFor is the operator's version of an email, or the built-in one.
func templateFor(saved map[string]EmailTemplate, key string) EmailTemplate {
	return SMTPSettings{Templates: saved}.Template(key)
}

// NotifyAccount tells an account about a change to it (password changed,
// two-factor turned off, a new API key): a notification, and an email when
// that email is on and a mail server is set up. extra fills placeholders such
// as {{who}}.
func NotifyAccount(ctx context.Context, db *gorm.DB, userID, key string, extra map[string]string) error {
	var u struct {
		Email string
		Name  string
	}
	if err := db.WithContext(ctx).Raw("SELECT email, coalesce(name, '') AS name FROM users WHERE id = ? AND deleted_at IS NULL", userID).Scan(&u).Error; err != nil {
		return err
	}
	if u.Email == "" {
		return nil
	}
	brand := LoadBranding(db.WithContext(ctx))
	publicURL := PublicURL()
	link := ""
	if publicURL != "" {
		link = publicURL + "/login"
	}
	msg := RenderEmail(templateFor(LoadEmailTemplates(db.WithContext(ctx)), key), brand, EmailData{
		Name: u.Name, Email: u.Email, Link: link, LogoURL: EmailLogoURL(brand, publicURL), Extra: extra,
	})
	addNotice(ctx, db, userID, key, msg.Subject, "user", userID)
	return queueEmail(ctx, db, key, u.Email, msg, emailRefs{userID: userID})
}

// ChangeSource is the {{who}} of an account email: where a change came from.
func ChangeSource(byAdmin bool, ip string) string {
	if byAdmin {
		return "An administrator made this change."
	}
	if ip = strings.TrimSpace(ip); ip != "" {
		return "It was made from the address " + ip + "."
	}
	return "It was made from your account."
}

// NotifyVMOperationFailed tells a customer, in their notifications only, that
// their server could not be created or reinstalled. Staff get the alert with
// the reason.
func NotifyVMOperationFailed(ctx context.Context, db *gorm.DB, args []byte) error {
	var p struct {
		VMID      string `json:"vm_id"`
		Operation string `json:"operation"`
	}
	if json.Unmarshal(args, &p) != nil || p.VMID == "" {
		return nil
	}
	what := map[string]string{"create": "created", "rebuild": "reinstalled"}[p.Operation]
	if what == "" {
		return nil
	}
	var owner struct {
		UserID   string
		Hostname string
		Role     string
	}
	if err := db.WithContext(ctx).Raw(`SELECT u.id::text AS user_id, v.hostname, u.role::text AS role
		FROM vms v JOIN users u ON u.id = v.user_id WHERE v.id = ? AND v.deleted_at IS NULL`, p.VMID).Scan(&owner).Error; err != nil || owner.Role != "client" {
		return err
	}
	addNotice(ctx, db, owner.UserID, "vm_"+p.Operation+"_failed",
		"Your server "+owner.Hostname+" could not be "+what+". Our staff have been told.", "vm", p.VMID)
	return nil
}

// NotifyBackupFailed tells a customer that a backup of their VM failed for
// good. Staff get an alert instead; the reason, which names nodes and paths,
// is for them.
func NotifyBackupFailed(ctx context.Context, db *gorm.DB, args []byte) error {
	var p struct {
		VMID      string `json:"vm_id"`
		Scheduled bool   `json:"scheduled"`
	}
	if json.Unmarshal(args, &p) != nil || p.VMID == "" {
		return nil
	}
	var role string
	if err := db.WithContext(ctx).Raw("SELECT u.role::text FROM vms v JOIN users u ON u.id = v.user_id WHERE v.id = ?", p.VMID).Scan(&role).Error; err != nil || role != "client" {
		return err
	}
	which := "A backup"
	if p.Scheduled {
		which = "The scheduled backup"
	}
	return NotifyVMOwnerWith(ctx, db, p.VMID, EmailBackupFailed, map[string]string{"backup": which})
}

// OnJobFailed is what the panel does when a background job fails for good:
// staff get an alert, the billing system its event, and the customer a notice
// for the jobs they see the effect of.
func OnJobFailed(db *gorm.DB, alerts *AlertService, rc *river.Client[pgx.Tx]) func(ctx context.Context, kind string, args []byte, reason string) {
	return func(ctx context.Context, kind string, args []byte, reason string) {
		alerts.JobFailed(ctx, kind, args, reason)
		BillingJobFailed(ctx, db, rc, kind, args, reason, false)
		switch kind {
		case (queue.BackupJob{}).Kind():
			if err := NotifyBackupFailed(ctx, db, args); err != nil {
				slog.Default().Warn("backup failed, but the VM's owner could not be told", "error", err)
			}
		case (queue.VMOperationJob{}).Kind():
			if err := NotifyVMOperationFailed(ctx, db, args); err != nil {
				slog.Default().Warn("VM job failed, but the VM's owner could not be told", "error", err)
			}
		}
	}
}
