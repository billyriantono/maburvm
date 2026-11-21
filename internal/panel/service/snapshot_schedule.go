package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// Snapshot schedules: one per VM, a snapshot every few hours, keeping the
// newest few of those it took. Snapshots taken by hand are never removed by a
// schedule, but count against the plan's allowance like any other.

// Bounds of a schedule.
const (
	MaxScheduleIntervalHours = 720
	maxScheduledKeep         = MaxSnapshotsPerVM
)

// ErrSnapshotScheduleNotFound is a VM without a snapshot schedule.
var ErrSnapshotScheduleNotFound = errors.New("the VM has no snapshot schedule")

// SnapshotScheduleRequest sets a VM's schedule.
type SnapshotScheduleRequest struct {
	IntervalHours int  `json:"interval_hours"`
	Keep          int  `json:"keep"`
	DiskOnly      bool `json:"disk_only"`
	Quiesce       bool `json:"quiesce"`
	// Enabled nil keeps the schedule running (on for a new one).
	Enabled *bool `json:"enabled"`
}

// GetSnapshotSchedule returns a VM's schedule.
func (s *SnapshotService) GetSnapshotSchedule(ctx context.Context, vmID string) (*models.SnapshotSchedule, error) {
	var sch models.SnapshotSchedule
	if err := s.db.WithContext(ctx).Where("vm_id = ?", vmID).First(&sch).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSnapshotScheduleNotFound
		}
		return nil, err
	}
	return &sch, nil
}

// SetSnapshotSchedule creates or replaces a VM's schedule. Keep is held to the
// VM's plan allowance, counting the snapshots taken by hand it already has.
func (s *SnapshotService) SetSnapshotSchedule(ctx context.Context, vmID string, req SnapshotScheduleRequest) (*models.SnapshotSchedule, error) {
	if req.IntervalHours < 1 || req.IntervalHours > MaxScheduleIntervalHours {
		return nil, fmt.Errorf("%w: a snapshot can be taken every 1 to %d hours", ErrInvalidSchedule, MaxScheduleIntervalHours)
	}
	if req.Keep < 1 || req.Keep > maxScheduledKeep {
		return nil, fmt.Errorf("%w: a schedule keeps 1 to %d snapshots", ErrInvalidSchedule, maxScheduledKeep)
	}
	if req.Quiesce && !req.DiskOnly {
		return nil, fmt.Errorf("%w: quiescing applies to disk-only snapshots", ErrInvalidSchedule)
	}
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, err
	}
	if VMOnBlockStorage(ctx, s.db, vm) {
		return nil, ErrSnapshotsNeedQcow2
	}
	if req.DiskOnly && !s.nodeCan(ctx, vm.NodeID, models.FeatureSnapshotOptions) {
		return nil, ErrSnapshotOptionsUnsupported
	}
	plan, err := vmPlan(ctx, s.db, vm)
	if err != nil {
		return nil, err
	}
	if plan != nil && plan.MaxSnapshots != nil {
		var manual int64
		if err := s.db.WithContext(ctx).Model(&models.Snapshot{}).Where("vm_id = ? AND schedule_id IS NULL", vmID).Count(&manual).Error; err != nil {
			return nil, err
		}
		if room := int64(*plan.MaxSnapshots) - manual; int64(req.Keep) > room {
			if room <= 0 {
				return nil, &AllowanceError{Message: fmt.Sprintf("This VM's plan includes %d snapshots and it has %d taken by hand, which leaves none for a schedule.", *plan.MaxSnapshots, manual)}
			}
			return nil, &AllowanceError{Message: fmt.Sprintf("This VM's plan includes %d snapshots and it has %d taken by hand, so a schedule can keep %d.", *plan.MaxSnapshots, manual, room)}
		}
	}

	now := time.Now()
	sch, err := s.GetSnapshotSchedule(ctx, vmID)
	switch {
	case errors.Is(err, ErrSnapshotScheduleNotFound):
		sch = &models.SnapshotSchedule{VMID: vmID, Enabled: true, NextRunAt: now.Add(time.Duration(req.IntervalHours) * time.Hour)}
	case err != nil:
		return nil, err
	case sch.IntervalHours != req.IntervalHours:
		sch.NextRunAt = now.Add(time.Duration(req.IntervalHours) * time.Hour)
	}
	sch.IntervalHours, sch.Keep, sch.DiskOnly, sch.Quiesce = req.IntervalHours, req.Keep, req.DiskOnly, req.Quiesce
	if req.Enabled != nil {
		sch.Enabled = *req.Enabled
	}
	sch.UpdatedAt = now
	// Save writes zero values too (Enabled false, Quiesce false).
	if err := s.db.WithContext(ctx).Save(sch).Error; err != nil {
		return nil, fmt.Errorf("failed to save the schedule: %w", err)
	}
	return sch, nil
}

// DeleteSnapshotSchedule removes a VM's schedule. The snapshots it took stay
// and become the VM's own.
func (s *SnapshotService) DeleteSnapshotSchedule(ctx context.Context, vmID string) error {
	sch, err := s.GetSnapshotSchedule(ctx, vmID)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Table("snapshots").Where("schedule_id = ?", sch.ID).Update("schedule_id", nil).Error; err != nil {
			return err
		}
		return tx.Delete(&models.SnapshotSchedule{}, "id = ?", sch.ID).Error
	})
}

// ErrInvalidSchedule is a schedule out of bounds.
var ErrInvalidSchedule = errors.New("invalid snapshot schedule")

// RunSnapshotSchedules takes the snapshots that are due, every minute until
// ctx ends.
func (s *SnapshotService) RunSnapshotSchedules(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.runDueSchedules(ctx, time.Now())
		}
	}
}

// runDueSchedules takes each due schedule's snapshot. A VM that cannot be
// snapshotted right now (busy, moving) is tried again at the next tick; one
// that cannot be at all records why and waits for its next interval.
func (s *SnapshotService) runDueSchedules(ctx context.Context, now time.Time) {
	var due []models.SnapshotSchedule
	if err := s.db.WithContext(ctx).Where("enabled AND next_run_at <= ?", now).Order("next_run_at").Limit(100).Find(&due).Error; err != nil {
		s.logger.WarnContext(ctx, "snapshot schedules could not be read", "error", err)
		return
	}
	for i := range due {
		sch := &due[i]
		retry, err := s.runSchedule(ctx, sch, now)
		if retry {
			continue
		}
		updates := map[string]any{"next_run_at": now.Add(time.Duration(sch.IntervalHours) * time.Hour), "last_run_at": now, "last_error": "", "updated_at": now}
		if err != nil {
			updates["last_error"] = truncate(err.Error(), 500)
		}
		if uerr := s.db.WithContext(ctx).Model(&models.SnapshotSchedule{}).Where("id = ?", sch.ID).Updates(updates).Error; uerr != nil {
			s.logger.WarnContext(ctx, "could not record a scheduled snapshot", "vm_id", sch.VMID, "error", uerr)
		}
	}
}

// runSchedule makes room under keep and the plan allowance by removing the
// oldest snapshots the schedule took, then takes the next one. retry says to
// try again at the next tick rather than wait an interval.
func (s *SnapshotService) runSchedule(ctx context.Context, sch *models.SnapshotSchedule, now time.Time) (retry bool, err error) {
	vm, err := s.vmRepo.GetByID(ctx, sch.VMID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// The VM is gone; so is its schedule.
		_ = s.db.WithContext(ctx).Delete(&models.SnapshotSchedule{}, "id = ?", sch.ID).Error
		return true, nil
	}
	if err != nil {
		return true, nil
	}
	switch {
	case vm.Status != models.VMStatusRunning && vm.Status != models.VMStatusStopped:
		return false, fmt.Errorf("the VM was %s, so this snapshot was skipped", vm.Status)
	case VMMigrating(ctx, s.db, vm.ID) || diskMoveActive(ctx, s.db, vm.ID):
		return true, nil
	}
	// Nothing is removed to make room for a snapshot that cannot be taken now:
	// pruning first lost one snapshot per run for as long as that lasted.
	// Disks on RBD are snapshotted without the memory whatever the schedule says.
	diskOnly := (sch.DiskOnly || VMOnRBD(ctx, s.db, vm)) && vm.Status == models.VMStatusRunning
	if err := s.snapshotRefusal(ctx, vm, diskOnly); err != nil {
		return false, err
	}
	var pending int64
	if err := s.db.WithContext(ctx).Model(&models.Snapshot{}).Where("vm_id = ? AND status = ?", vm.ID, models.SnapshotStatusPending).
		Count(&pending).Error; err != nil || pending > 0 {
		return true, nil
	}
	// Failed ones first: they hold a place in the allowance and nothing else.
	var mine []models.Snapshot
	if err := s.db.WithContext(ctx).Where("vm_id = ? AND schedule_id = ? AND status IN ?", vm.ID, sch.ID,
		[]models.SnapshotStatus{models.SnapshotStatusComplete, models.SnapshotStatusFailed}).
		Order("status = '" + string(models.SnapshotStatusComplete) + "', created_at").Find(&mine).Error; err != nil {
		return true, nil
	}
	limit := sch.Keep
	plan, err := vmPlan(ctx, s.db, vm)
	if err != nil {
		return true, nil
	}
	var total int64
	if err := s.db.WithContext(ctx).Model(&models.Snapshot{}).Where("vm_id = ?", vm.ID).Count(&total).Error; err != nil {
		return true, nil
	}
	room := int64(MaxSnapshotsPerVM)
	if plan != nil && plan.MaxSnapshots != nil {
		room = min(room, int64(*plan.MaxSnapshots))
	}
	if total-int64(len(mine)) >= room {
		return false, fmt.Errorf("the VM already holds %d snapshots, its allowance; delete one taken by hand to let the schedule run", total)
	}
	// Removed oldest first until one more fits under both the schedule's keep
	// and the VM's allowance.
	for len(mine) > 0 && (len(mine) >= limit || total >= room) {
		if err := s.DeleteSnapshot(ctx, &DeleteSnapshotRequest{SnapshotID: mine[0].ID, VMID: vm.ID, UserID: vm.UserID}); err != nil {
			return false, fmt.Errorf("the oldest scheduled snapshot could not be removed: %w", err)
		}
		mine, total = mine[1:], total-1
	}
	if total >= room {
		return false, fmt.Errorf("the VM already holds %d snapshots, its allowance; delete one taken by hand to let the schedule run", total)
	}
	_, err = s.CreateSnapshot(ctx, &CreateSnapshotRequest{
		VMID: vm.ID, UserID: vm.UserID, Name: "auto-" + now.UTC().Format("20060102-1504"),
		DiskOnly: diskOnly, Quiesce: sch.Quiesce && diskOnly,
		ScheduleID: sch.ID,
	})
	switch {
	case errors.Is(err, ErrSnapshotInProgress), errors.Is(err, ErrDiskMoveActive):
		return true, nil
	case errors.Is(err, ErrSnapshotNameExists):
		return false, nil
	}
	return false, err
}
