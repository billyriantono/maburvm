package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newNoticeDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	for _, q := range []string{
		`CREATE TABLE system_settings (section TEXT PRIMARY KEY, data TEXT, updated_at DATETIME)`,
		`CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT, name TEXT, role TEXT, deleted_at DATETIME)`,
		`CREATE TABLE email_log (id TEXT PRIMARY KEY, template TEXT NOT NULL DEFAULT '', recipient TEXT NOT NULL, subject TEXT NOT NULL DEFAULT '',
			body TEXT NOT NULL DEFAULT '', user_id TEXT, vm_id TEXT, status TEXT NOT NULL DEFAULT 'queued', attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '', created_at DATETIME NOT NULL, last_attempt_at DATETIME, sent_at DATETIME)`,
		`CREATE TABLE user_notifications (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, kind TEXT NOT NULL, title TEXT NOT NULL,
			body TEXT NOT NULL DEFAULT '', resource_type TEXT NOT NULL DEFAULT '', resource_id TEXT NOT NULL DEFAULT '',
			read_at DATETIME, created_at DATETIME NOT NULL)`,
		`INSERT INTO users (id, email, name, role) VALUES ('11111111-1111-1111-1111-111111111111', 'dewi@example.com', 'Dewi', 'client')`,
	} {
		require.NoError(t, db.Exec(q).Error)
	}
	return db
}

func stubMail(t *testing.T, send func(to string, msg RenderedEmail) error) {
	orig := sendMailNow
	sendMailNow = func(_ SMTPSettings, to string, msg RenderedEmail) error { return send(to, msg) }
	t.Cleanup(func() { sendMailNow = orig })
}

func TestEmailsAreLoggedAndRetried(t *testing.T) {
	db := newNoticeDB(t)
	ctx := context.Background()
	sent := make(chan string, 4)
	stubMail(t, func(to string, msg RenderedEmail) error { sent <- to + " " + msg.Subject; return nil })

	// No mail server: nothing is sent, and nothing is logged.
	require.NoError(t, QueueEmail(ctx, db, EmailVMReady, "dewi@example.com", RenderedEmail{Subject: "Hi"}))
	var n int64
	require.NoError(t, db.Model(&EmailLogEntry{}).Count(&n).Error)
	assert.Zero(t, n)

	// With one, and no queue running, the email gets one try in the background.
	require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('email', ?)`, `{"smtpHost":"mail.example.com","smtpPort":587,"smtpFrom":"panel@example.com"}`).Error)
	require.NoError(t, QueueEmail(ctx, db, EmailVMReady, "dewi@example.com", RenderedEmail{Subject: "Your server is ready", Text: "t", HTML: "h"}))
	select {
	case got := <-sent:
		assert.Equal(t, "dewi@example.com Your server is ready", got)
	case <-time.After(2 * time.Second):
		t.Fatal("the email was not sent")
	}
	require.Eventually(t, func() bool {
		var row EmailLogEntry
		return db.First(&row).Error == nil && row.Status == EmailSent && row.Attempts == 1 && row.SentAt != nil
	}, 2*time.Second, 20*time.Millisecond)

	// A refused send is retried, then given up on, and says why.
	var row EmailLogEntry
	require.NoError(t, db.First(&row).Error)
	require.NoError(t, db.Model(&EmailLogEntry{}).Where("id = ?", row.ID).Updates(map[string]any{"status": EmailQueued, "attempts": 0}).Error)
	stubMail(t, func(string, RenderedEmail) error { return errors.New("451 try later") })
	assert.Error(t, DeliverEmail(ctx, db, row.ID, false))
	require.NoError(t, db.First(&row, "id = ?", row.ID).Error)
	assert.Equal(t, EmailRetrying, row.Status)
	assert.Equal(t, "451 try later", row.LastError)
	assert.Error(t, DeliverEmail(ctx, db, row.ID, true))
	require.NoError(t, db.First(&row, "id = ?", row.ID).Error)
	assert.Equal(t, EmailFailed, row.Status)
	assert.Equal(t, 2, row.Attempts)

	// The log lists it, newest first, and never the message body.
	rows, total, err := ListEmailLog(ctx, db, EmailFailed, 10, 0)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	assert.Equal(t, "Your server is ready", rows[0].Subject)

	// The mail server removed: the email fails for good instead of retrying.
	require.NoError(t, db.Model(&EmailLogEntry{}).Where("id = ?", row.ID).Update("status", EmailRetrying).Error)
	require.NoError(t, db.Exec(`DELETE FROM system_settings`).Error)
	assert.NoError(t, DeliverEmail(ctx, db, row.ID, false))
	require.NoError(t, db.First(&row, "id = ?", row.ID).Error)
	assert.Equal(t, EmailFailed, row.Status)
}

func TestAccountNoticesAndReadState(t *testing.T) {
	db := newNoticeDB(t)
	ctx := context.Background()
	const dewi = "11111111-1111-1111-1111-111111111111"

	require.NoError(t, NotifyAccount(ctx, db, dewi, EmailPasswordChanged, map[string]string{"who": ChangeSource(false, "203.0.113.9")}))
	require.NoError(t, NotifyAccount(ctx, db, dewi, EmailTwoFactorDisabled, map[string]string{"who": ChangeSource(true, "")}))
	rows, unread, err := ListNotifications(ctx, db, dewi, 10)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.EqualValues(t, 2, unread)
	titles := []string{rows[0].Title, rows[1].Title}
	assert.Contains(t, titles, "Your "+DefaultBrandName+" password was changed")

	// Someone else's notification is not found, and is not marked.
	assert.ErrorIs(t, MarkNotificationRead(ctx, db, "22222222-2222-2222-2222-222222222222", rows[0].ID), ErrNotificationNotFound)
	assert.ErrorIs(t, MarkNotificationRead(ctx, db, dewi, "not-a-uuid"), ErrNotificationNotFound)
	require.NoError(t, MarkNotificationRead(ctx, db, dewi, rows[0].ID))
	_, unread, _ = ListNotifications(ctx, db, dewi, 10)
	assert.EqualValues(t, 1, unread)
	require.NoError(t, MarkAllNotificationsRead(ctx, db, dewi))
	_, unread, _ = ListNotifications(ctx, db, dewi, 10)
	assert.Zero(t, unread)
}

func TestChangeSource(t *testing.T) {
	assert.Equal(t, "An administrator made this change.", ChangeSource(true, "203.0.113.9"))
	assert.Equal(t, "It was made from the address 203.0.113.9.", ChangeSource(false, " 203.0.113.9 "))
	assert.Equal(t, "It was made from your account.", ChangeSource(false, ""))
}

func TestRescueSettings(t *testing.T) {
	for in, ok := range map[string]bool{
		"":                                 true,
		"https://example.com/rescue.iso":   true,
		"http://mirror.example.com/r.iso":  true,
		"/var/lib/libvirt/isos/rescue.iso": true,
		"/var/lib/../../etc/shadow":        false,
		"ftp://example.com/rescue.iso":     false,
		"rescue.iso":                       false,
		"https://":                         false,
	} {
		r := RescueSettings{ISOURL: "  " + in + " "}
		err := r.Normalize()
		assert.Equal(t, ok, err == nil, "%q: %v", in, err)
		if ok {
			assert.Equal(t, in, r.ISOURL)
		}
	}

	db := newNoticeDB(t)
	ctx := context.Background()
	t.Setenv("RESCUE_ISO_URL", "")
	iso, source := RescueISO(ctx, db)
	assert.Empty(t, iso+source)
	t.Setenv("RESCUE_ISO_URL", "https://example.com/env.iso")
	iso, source = RescueISO(ctx, db)
	assert.Equal(t, "https://example.com/env.iso", iso)
	assert.Equal(t, "environment", source)
	require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('rescue', '{"isoUrl":"https://example.com/saved.iso"}')`).Error)
	iso, source = RescueISO(ctx, db)
	assert.Equal(t, "https://example.com/saved.iso", iso)
	assert.Equal(t, "settings", source)

	// Rescue with nothing set says so instead of queueing a job.
	require.NoError(t, db.Exec(`DELETE FROM system_settings`).Error)
	t.Setenv("RESCUE_ISO_URL", "")
	_, err := (&VMService{db: db}).RescueVM(ctx, "vm", "", false)
	assert.ErrorIs(t, err, ErrNoRescueImage)
}

func TestDetachRefusesToLeaveAnUntrackedVolume(t *testing.T) {
	assert.ErrorIs(t, (&VMService{}).DetachDisk(context.Background(), "vm", "vdb", false), ErrDetachKeepsNoVolume)
}
