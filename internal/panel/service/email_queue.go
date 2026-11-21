package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
	"github.com/riverqueue/river"
	"gorm.io/gorm"
)

// Email states in the log.
const (
	EmailQueued   = "queued"
	EmailRetrying = "retrying"
	EmailSent     = "sent"
	EmailFailed   = "failed"
)

// emailLogRetention is how long sent-email rows are kept.
const emailLogRetention = 90 * 24 * time.Hour

// ErrEmailNotFound names an email id that is not in the log.
var ErrEmailNotFound = errors.New("email not found")

// EmailLogEntry is one email the panel sent, or tried to. Body is the rendered
// message, encrypted, and never leaves the panel: it can hold a reset link.
type EmailLogEntry struct {
	ID            string     `json:"id" gorm:"primaryKey"`
	Template      string     `json:"template"`
	Recipient     string     `json:"recipient"`
	Subject       string     `json:"subject"`
	Body          string     `json:"-"`
	UserID        *string    `json:"user_id"`
	VMID          *string    `json:"vm_id" gorm:"column:vm_id"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	LastError     string     `json:"last_error"`
	CreatedAt     time.Time  `json:"created_at"`
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	SentAt        *time.Time `json:"sent_at"`
}

func (EmailLogEntry) TableName() string { return "email_log" }

// emailQueue is the job queue emails go through, set once the queue runs.
// Without it (tools, tests) an email gets one try, in the background.
var emailQueue atomic.Pointer[river.Client[pgx.Tx]]

// SetEmailQueue routes emails through the job queue.
func SetEmailQueue(rc *river.Client[pgx.Tx]) { emailQueue.Store(rc) }

// emailRefs ties a logged email to the account and VM it is about.
type emailRefs struct{ userID, vmID string }

// QueueEmail logs a rendered email and hands it to the queue, which retries a
// mail server that refuses or is down. key is the template; an optional email
// switched off, or no mail server set up, sends nothing and logs nothing.
func QueueEmail(ctx context.Context, db *gorm.DB, key, to string, msg RenderedEmail) error {
	return queueEmail(ctx, db, key, to, msg, emailRefs{})
}

func queueEmail(ctx context.Context, db *gorm.DB, key, to string, msg RenderedEmail, refs emailRefs) error {
	cfg, ok, err := LoadSMTPSettings(db)
	if err != nil || !ok || !cfg.Enabled(key) {
		return err
	}
	return queueRendered(ctx, db, key, to, msg, refs)
}

func queueRendered(ctx context.Context, db *gorm.DB, key, to string, msg RenderedEmail, refs emailRefs) error {
	body, _ := json.Marshal(map[string]string{"text": msg.Text, "html": msg.HTML})
	sealed, err := models.SealSecret(string(body))
	if err != nil {
		return err
	}
	row := &EmailLogEntry{
		ID: uuid.New().String(), Template: key, Recipient: oneLine(to), Subject: oneLine(msg.Subject), Body: sealed,
		UserID: strPtrOrNil(refs.userID), VMID: strPtrOrNil(refs.vmID), Status: EmailQueued, CreatedAt: time.Now().UTC(),
	}
	if err := db.WithContext(ctx).Create(row).Error; err != nil {
		return err
	}
	// ponytail: pruned on write; move to a scheduled job if the log grows large.
	db.WithContext(ctx).Where("created_at < ?", row.CreatedAt.Add(-emailLogRetention)).Delete(&EmailLogEntry{})

	rc := emailQueue.Load()
	if rc == nil {
		go func() { _ = DeliverEmail(context.WithoutCancel(ctx), db, row.ID, true) }()
		return nil
	}
	if _, err := rc.Insert(ctx, queue.EmailDeliveryJob{EmailID: row.ID}, nil); err != nil {
		db.WithContext(ctx).Model(&EmailLogEntry{}).Where("id = ?", row.ID).
			Updates(map[string]any{"status": EmailFailed, "last_error": "could not be queued: " + err.Error()})
		return err
	}
	return nil
}

// DeliverEmail makes one attempt at a logged email. An error asks the queue
// for another try; final says this was the last one.
func DeliverEmail(ctx context.Context, db *gorm.DB, id string, final bool) error {
	var row EmailLogEntry
	if err := db.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // pruned: nothing left to send
		}
		return err
	}
	if row.Status == EmailSent {
		return nil
	}
	now := time.Now().UTC()
	update := map[string]any{"attempts": row.Attempts + 1, "last_attempt_at": now}
	finish := func(sendErr error) error {
		switch {
		case sendErr == nil:
			update["status"], update["last_error"], update["sent_at"] = EmailSent, "", now
		case final:
			update["status"], update["last_error"] = EmailFailed, sendErr.Error()
		default:
			update["status"], update["last_error"] = EmailRetrying, sendErr.Error()
		}
		if err := db.WithContext(ctx).Model(&EmailLogEntry{}).Where("id = ?", id).Updates(update).Error; err != nil {
			slog.Default().Warn("email log: could not record a delivery attempt", "email_id", id, "error", err)
		}
		return sendErr
	}

	// Read on every attempt, so a corrected mail server applies to the retries.
	cfg, ok, err := LoadSMTPSettings(db.WithContext(ctx))
	if err != nil {
		return finish(err)
	}
	if !ok {
		final = true
		_ = finish(errors.New("no mail server is set up any more"))
		return nil // a retry would find the same
	}
	plain, err := models.OpenSecret(row.Body)
	var parts map[string]string
	if err == nil {
		err = json.Unmarshal([]byte(plain), &parts)
	}
	if err != nil {
		final = true
		_ = finish(errors.New("the stored message cannot be read: " + err.Error()))
		return nil
	}
	return finish(sendMailNow(cfg, row.Recipient, RenderedEmail{Subject: row.Subject, Text: parts["text"], HTML: parts["html"]}))
}

// sendMailNow is a var so tests can stand in for the mail server.
var sendMailNow = SendMail

// ListEmailLog returns the sent-email log, newest first.
func ListEmailLog(ctx context.Context, db *gorm.DB, status string, limit, offset int) ([]EmailLogEntry, int64, error) {
	q := db.WithContext(ctx).Model(&EmailLogEntry{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := []EmailLogEntry{}
	err := q.Order("created_at DESC").Limit(limit).Offset(offset).Find(&rows).Error
	return rows, total, err
}

// RetryEmail queues a logged email that was not sent again, with a full set of tries.
func RetryEmail(ctx context.Context, db *gorm.DB, id string) error {
	res := db.WithContext(ctx).Model(&EmailLogEntry{}).Where("id = ? AND status = ?", id, EmailFailed).
		Updates(map[string]any{"status": EmailQueued, "last_error": ""})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrEmailNotFound
	}
	rc := emailQueue.Load()
	if rc == nil {
		return errors.New("the job queue is not running")
	}
	_, err := rc.Insert(ctx, queue.EmailDeliveryJob{EmailID: id}, nil)
	return err
}
