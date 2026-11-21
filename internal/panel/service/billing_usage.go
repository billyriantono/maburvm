package service

import (
	"context"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// VMUsageReport is what the billing usage sync (vm.usage) reports for a VM,
// in the units billing systems store: megabytes and whole hours.
type VMUsageReport struct {
	BandwidthUsedMB  int64 `json:"bandwidth_used_mb"`
	BandwidthLimitMB int64 `json:"bandwidth_limit_mb"`
	// DiskLimitMB is the primary disk; DiskAllocatedMB adds the data disks.
	// Disks are thin, so what the guest has written is not measured.
	DiskLimitMB     int64 `json:"disk_limit_mb"`
	DiskAllocatedMB int64 `json:"disk_allocated_mb"`
	SnapshotCount   int64 `json:"snapshot_count"`
	BackupCount     int64 `json:"backup_count"`
	BackupSizeMB    int64 `json:"backup_size_mb"`
	// ExtraIPs are the VM's addresses beyond its primary one; FloatingIPs the
	// floating addresses pointed at it.
	ExtraIPs    int64 `json:"extra_ips"`
	FloatingIPs int64 `json:"floating_ips"`
	// UptimeHours counts the clock hours since UptimeSince in which the VM was
	// seen running (any part of an hour counts). UptimeSince is the start of
	// the transfer period, or the oldest kept sample when that is later.
	UptimeHours int64     `json:"uptime_hours"`
	UptimeSince time.Time `json:"uptime_since"`
}

// usageSampleWindow is how far back running samples are kept (the metrics
// collector's retention), so how far back uptime can be counted.
const usageSampleWindow = 7 * 24 * time.Hour

// VMUsage measures one VM for the billing usage sync. Each part that cannot
// be read is left at zero: a usage report never fails for one missing table.
func VMUsage(ctx context.Context, db *gorm.DB, vm *models.VM) VMUsageReport {
	const mb = 1 << 20
	r := VMUsageReport{DiskLimitMB: int64(vm.Resources.Disk) * 1024}
	r.DiskAllocatedMB = r.DiskLimitMB
	now := time.Now().UTC()
	r.UptimeSince = now.Add(-usageSampleWindow)
	if db == nil {
		return r
	}
	q := func() *gorm.DB { return db.WithContext(ctx) }

	var usage models.BandwidthUsage
	if err := q().Where("vm_id = ?", vm.ID).Order("period_start DESC").First(&usage).Error; err == nil {
		r.BandwidthUsedMB, r.BandwidthLimitMB = usage.TotalBytes/mb, usage.QuotaBytes/mb
		if usage.PeriodStart.After(r.UptimeSince) {
			r.UptimeSince = usage.PeriodStart.UTC()
		}
	}
	var dataGB int64
	_ = q().Table("vm_disks").Where("vm_id = ? AND deleted_at IS NULL", vm.ID).Select("COALESCE(SUM(size_gb), 0)").Scan(&dataGB).Error
	r.DiskAllocatedMB += dataGB * 1024

	_ = q().Model(&models.Snapshot{}).Where("vm_id = ? AND status = ?", vm.ID, models.SnapshotStatusComplete).Count(&r.SnapshotCount).Error
	var backups struct {
		N    int64
		Size int64
	}
	_ = q().Model(&models.Backup{}).Where("vm_id = ? AND status = ?", vm.ID, models.BackupStatusCompleted).
		Select("COUNT(*) AS n, COALESCE(SUM(size), 0) AS size").Scan(&backups).Error
	r.BackupCount, r.BackupSizeMB = backups.N, backups.Size/mb

	var addresses int64
	_ = q().Model(&models.Network{}).Where("vm_id = ?", vm.ID).Count(&addresses).Error
	if addresses > 1 {
		r.ExtraIPs = addresses - 1
	}
	_ = q().Model(&models.IPAddress{}).Where("vm_id = ? AND delivery_mode = ?", vm.ID, models.IPDeliveryFloating).Count(&r.FloatingIPs).Error

	hour := "strftime('%Y-%m-%d %H', recorded_at)"
	if db.Dialector.Name() == "postgres" {
		hour = "date_trunc('hour', recorded_at)"
	}
	_ = q().Model(&models.VMMetricSample{}).Where("vm_id = ? AND recorded_at >= ?", vm.ID, r.UptimeSince).
		Select("COUNT(DISTINCT " + hour + ")").Scan(&r.UptimeHours).Error
	return r
}
