package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
)

// maxMaintenanceMessage bounds the banner text.
const maxMaintenanceMessage = 500

// SetMaintenance turns panel maintenance mode on or off. With holdJobs the
// job queue stops starting new jobs (running ones finish; queued ones wait)
// until maintenance ends; rc may be nil where no queue runs.
func SetMaintenance(ctx context.Context, db *gorm.DB, rc *river.Client[pgx.Tx], on bool, message string, holdJobs bool, by string) (middleware.Maintenance, error) {
	message = strings.TrimSpace(message)
	if len([]rune(message)) > maxMaintenanceMessage {
		return middleware.Maintenance{}, fmt.Errorf("the message is longer than %d characters", maxMaintenanceMessage)
	}
	m := middleware.Maintenance{Enabled: on, Message: message, HoldJobs: on && holdJobs}
	if on {
		m.By = by
		if prev := middleware.ReadMaintenance(db.WithContext(ctx)); prev.Enabled && prev.Since != nil {
			m.Since = prev.Since
		} else {
			now := time.Now().UTC()
			m.Since = &now
		}
	}
	// Holding first and releasing last: no job starts between the banner and
	// the hold, and none is held after the banner is gone.
	if m.HoldJobs {
		if err := setJobHold(ctx, rc, true); err != nil {
			return middleware.Maintenance{}, err
		}
	}
	data, _ := json.Marshal(m)
	if err := db.WithContext(ctx).Exec(
		"INSERT INTO system_settings (section, data, updated_at) VALUES (?, ?::jsonb, NOW()) "+
			"ON CONFLICT (section) DO UPDATE SET data = EXCLUDED.data, updated_at = NOW()",
		middleware.MaintenanceSection, string(data)).Error; err != nil {
		return middleware.Maintenance{}, fmt.Errorf("could not save maintenance mode: %w", err)
	}
	middleware.RememberMaintenance(m)
	if !m.HoldJobs {
		if err := setJobHold(ctx, rc, false); err != nil {
			return m, err
		}
	}
	return m, nil
}

func setJobHold(ctx context.Context, rc *river.Client[pgx.Tx], hold bool) error {
	if rc == nil {
		return nil
	}
	var err error
	if hold {
		err = rc.QueuePause(ctx, "*", nil)
	} else {
		err = rc.QueueResume(ctx, "*", nil)
	}
	if err != nil {
		return fmt.Errorf("could not %s the job queue: %w", map[bool]string{true: "hold", false: "release"}[hold], err)
	}
	return nil
}

// SyncMaintenanceHold makes the job queue match maintenance mode when the
// panel starts: held while it says so, running otherwise.
func SyncMaintenanceHold(ctx context.Context, db *gorm.DB, rc *river.Client[pgx.Tx]) error {
	return setJobHold(ctx, rc, middleware.ReadMaintenance(db.WithContext(ctx)).HoldJobs)
}

// RunningJob is a job still running, as maintenance mode reports it.
type RunningJob struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Subject   string    `json:"subject"`
	AttemptAt time.Time `json:"attempted_at"`
}

// RunningJobs lists the jobs running now, oldest first.
func RunningJobs(ctx context.Context, db *gorm.DB) ([]RunningJob, error) {
	out := []RunningJob{}
	err := db.WithContext(ctx).Raw(`SELECT id, kind,
		TRIM(COALESCE(args->>'operation', args->>'type', '') || ' ' || COALESCE(args->>'vm_id', '')) AS subject,
		COALESCE(attempted_at, created_at) AS attempt_at
		FROM river_job WHERE state = 'running' ORDER BY attempted_at NULLS FIRST, id`).Scan(&out).Error
	return out, err
}
