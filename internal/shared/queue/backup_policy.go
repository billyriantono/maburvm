package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
)

// BackupPolicySection is the system_settings row with the fleet-wide backup
// rules: when scheduled backups may start and how many run on a node at once.
const BackupPolicySection = "backup_policy"

// DefaultBackupsPerNode is how many backups export on one node at once when
// the operator has not said. Each export reads a whole disk and compresses it
// on the node, competing with its customers' disk I/O.
const DefaultBackupsPerNode = 2

// maxBackupsPerNode caps what the setting accepts.
const maxBackupsPerNode = 20

// BackupPolicy is the shape stored under BackupPolicySection.
type BackupPolicy struct {
	// WindowStart and WindowEnd ("HH:MM", server time) bound when scheduled
	// backups start. Both empty, or equal, is any time. A window may cross
	// midnight (22:00 to 06:00).
	WindowStart string `json:"window_start"`
	WindowEnd   string `json:"window_end"`
	// PerNode is how many backups may export on one node at once; 0 is the
	// default.
	PerNode int `json:"per_node"`
}

// LoadBackupPolicy reads the operator's backup rules. A missing row or a bad
// value falls back to the defaults: no window, DefaultBackupsPerNode.
func LoadBackupPolicy(ctx context.Context, db *gorm.DB) BackupPolicy {
	var p BackupPolicy
	if db == nil {
		return p
	}
	var raw string
	if err := db.WithContext(ctx).Raw("SELECT CAST(data AS TEXT) FROM system_settings WHERE section = ?", BackupPolicySection).
		Scan(&raw).Error; err == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), &p)
	}
	return p
}

// NodeLimit is how many backups may export on one node at once.
func (p BackupPolicy) NodeLimit() int {
	if p.PerNode < 1 {
		return DefaultBackupsPerNode
	}
	return min(p.PerNode, maxBackupsPerNode)
}

// window returns the window's bounds in minutes after midnight, and false
// when there is no window (unset, equal or unreadable).
func (p BackupPolicy) window() (start, end int, ok bool) {
	s, serr := clockMinutes(p.WindowStart)
	e, eerr := clockMinutes(p.WindowEnd)
	if serr != nil || eerr != nil || s == e {
		return 0, 0, false
	}
	return s, e, true
}

// ValidateWindow says what is wrong with a window an operator typed.
func (p BackupPolicy) ValidateWindow() error {
	if p.WindowStart == "" && p.WindowEnd == "" {
		return nil
	}
	if _, err := clockMinutes(p.WindowStart); err != nil {
		return fmt.Errorf("window start: %w", err)
	}
	if _, err := clockMinutes(p.WindowEnd); err != nil {
		return fmt.Errorf("window end: %w", err)
	}
	return nil
}

// InWindow reports whether a scheduled backup may start at now.
func (p BackupPolicy) InWindow(now time.Time) bool {
	return p.UntilWindow(now) == 0
}

// UntilWindow is how long until the window next opens; 0 when now is inside
// it, or there is none.
func (p BackupPolicy) UntilWindow(now time.Time) time.Duration {
	start, end, ok := p.window()
	if !ok {
		return 0
	}
	m := now.Hour()*60 + now.Minute()
	inside := start <= m && m < end
	if start > end { // crosses midnight
		inside = m >= start || m < end
	}
	if inside {
		return 0
	}
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	open := midnight.Add(time.Duration(start) * time.Minute)
	if !open.After(now) {
		open = open.AddDate(0, 0, 1)
	}
	return open.Sub(now)
}

func clockMinutes(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a time of day (HH:MM)", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// staleBackupAge is when an "in progress" backup stops counting against its
// node's limit: longer than any job may run, so its worker is gone.
const staleBackupAge = 12 * time.Hour

// claimBackupSlot marks a backup in progress if its node has a free export
// slot, and reports whether it did. The count and the mark are one step per
// node, so two workers cannot both take the last slot.
func claimBackupSlot(ctx context.Context, db *gorm.DB, backupID, nodeID string, limit int, now time.Time) (bool, error) {
	claimed := false
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "backup-slot:"+nodeID).Error; err != nil {
				return err
			}
		}
		var running int64
		if err := tx.Table("backups").
			Joins("JOIN vms ON vms.id = backups.vm_id").
			Where("vms.node_id = ? AND backups.status = ? AND backups.id <> ? AND backups.deleted_at IS NULL AND backups.started_at > ?",
				nodeID, models.BackupStatusInProgress, backupID, now.Add(-staleBackupAge)).
			Count(&running).Error; err != nil {
			return err
		}
		if running >= int64(limit) {
			return nil
		}
		claimed = true
		return tx.Model(&models.Backup{}).Where("id = ?", backupID).Updates(map[string]interface{}{
			"status":        models.BackupStatusInProgress,
			"started_at":    now,
			"error_message": "",
		}).Error
	})
	return claimed, err
}
