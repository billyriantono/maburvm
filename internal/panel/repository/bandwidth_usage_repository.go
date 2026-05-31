package repository

import (
	"context"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/maburvm/panel/internal/shared/models"
)

// BandwidthUsageRepository handles bandwidth usage data persistence
type BandwidthUsageRepository struct {
	db *gorm.DB
}

// NewBandwidthUsageRepository creates a new BandwidthUsageRepository
func NewBandwidthUsageRepository(db *gorm.DB) *BandwidthUsageRepository {
	return &BandwidthUsageRepository{db: db}
}

// GetCurrentPeriod returns the bandwidth usage record for a VM in the current billing period.
// If no record exists, it creates one.
func (r *BandwidthUsageRepository) GetCurrentPeriod(ctx context.Context, vmID, nodeID string) (*models.BandwidthUsage, error) {
	now := time.Now()
	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0).Add(-time.Second)

	var usage models.BandwidthUsage
	err := r.db.WithContext(ctx).
		Where("vm_id = ? AND period_start = ?", vmID, periodStart).
		First(&usage).Error

	if err == gorm.ErrRecordNotFound {
		// Create new period record
		usage = models.BandwidthUsage{
			VMID:        vmID,
			NodeID:      nodeID,
			PeriodStart: periodStart,
			PeriodEnd:   periodEnd,
			LastReportedAt: now,
		}

		// Look up quota from network config
		var network models.Network
		if err := r.db.WithContext(ctx).Where("vm_id = ?", vmID).First(&network).Error; err == nil {
			usage.QuotaBytes = network.BandwidthQuotaGB * 1024 * 1024 * 1024
		}

		if err := r.db.WithContext(ctx).Create(&usage).Error; err != nil {
			return nil, err
		}
		return &usage, nil
	}

	return &usage, err
}

// AccumulateUsage adds delta bytes to the current period's usage.
// Uses upsert to handle concurrent updates safely.
func (r *BandwidthUsageRepository) AccumulateUsage(ctx context.Context, vmID, nodeID string, rxDelta, txDelta int64) (*models.BandwidthUsage, error) {
	now := time.Now()
	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0).Add(-time.Second)

	// Look up quota
	var quotaBytes int64
	var network models.Network
	if err := r.db.WithContext(ctx).Where("vm_id = ?", vmID).First(&network).Error; err == nil {
		quotaBytes = network.BandwidthQuotaGB * 1024 * 1024 * 1024
	}

	usage := models.BandwidthUsage{
		VMID:           vmID,
		NodeID:         nodeID,
		PeriodStart:    periodStart,
		PeriodEnd:      periodEnd,
		RxBytes:        rxDelta,
		TxBytes:        txDelta,
		TotalBytes:     rxDelta + txDelta,
		QuotaBytes:     quotaBytes,
		LastReportedAt: now,
	}

	// Upsert: on conflict (vm_id, period_start), accumulate
	err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "vm_id"}, {Name: "period_start"}},
			DoUpdates: clause.Assignments(map[string]interface{}{
				"rx_bytes":        gorm.Expr("bandwidth_usages.rx_bytes + ?", rxDelta),
				"tx_bytes":        gorm.Expr("bandwidth_usages.tx_bytes + ?", txDelta),
				"total_bytes":     gorm.Expr("bandwidth_usages.total_bytes + ?", rxDelta+txDelta),
				"quota_bytes":     quotaBytes,
				"last_reported_at": now,
				"updated_at":      now,
			}),
		}).
		Create(&usage).Error

	if err != nil {
		return nil, err
	}

	// Re-read to get current totals
	return r.GetCurrentPeriod(ctx, vmID, nodeID)
}

// GetByVMID returns all bandwidth usage records for a VM (all periods)
func (r *BandwidthUsageRepository) GetByVMID(ctx context.Context, vmID string) ([]models.BandwidthUsage, error) {
	var usages []models.BandwidthUsage
	err := r.db.WithContext(ctx).
		Where("vm_id = ?", vmID).
		Order("period_start DESC").
		Find(&usages).Error
	return usages, err
}

// GetByNodeID returns current period usage for all VMs on a node
func (r *BandwidthUsageRepository) GetByNodeID(ctx context.Context, nodeID string) ([]models.BandwidthUsage, error) {
	now := time.Now()
	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	var usages []models.BandwidthUsage
	err := r.db.WithContext(ctx).
		Where("node_id = ? AND period_start = ?", nodeID, periodStart).
		Find(&usages).Error
	return usages, err
}

// MarkExceeded marks a VM's bandwidth as exceeded and records the block time
func (r *BandwidthUsageRepository) MarkExceeded(ctx context.Context, vmID string) error {
	now := time.Now()
	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	return r.db.WithContext(ctx).
		Model(&models.BandwidthUsage{}).
		Where("vm_id = ? AND period_start = ?", vmID, periodStart).
		Updates(map[string]interface{}{
			"exceeded":   true,
			"blocked_at": now,
		}).Error
}

// SetQuota updates a VM's monthly bandwidth quota (GB) on its network config and
// applies it to the current period. When the new quota is unlimited (0) or the
// VM is now back under quota, the exceeded flag is cleared so raising the quota
// lifts a bandwidth suspension (the VM still has to be started again manually).
func (r *BandwidthUsageRepository) SetQuota(ctx context.Context, vmID string, quotaGB int64) error {
	quotaBytes := quotaGB * 1024 * 1024 * 1024
	now := time.Now()
	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Persist on the VM's network interface(s) — the source of truth.
		if err := tx.Model(&models.Network{}).Where("vm_id = ?", vmID).
			Update("bandwidth_quota_gb", quotaGB).Error; err != nil {
			return err
		}
		// Apply to the current period record (if one exists yet).
		if err := tx.Model(&models.BandwidthUsage{}).
			Where("vm_id = ? AND period_start = ?", vmID, periodStart).
			Update("quota_bytes", quotaBytes).Error; err != nil {
			return err
		}
		// Clear overage flag when unlimited or now under the new quota.
		return tx.Model(&models.BandwidthUsage{}).
			Where("vm_id = ? AND period_start = ? AND (? = 0 OR total_bytes < ?)", vmID, periodStart, quotaBytes, quotaBytes).
			Updates(map[string]interface{}{"exceeded": false, "blocked_at": nil}).Error
	})
}

// ResetPeriod creates a new period record (called at billing cycle reset)
func (r *BandwidthUsageRepository) ResetPeriod(ctx context.Context, vmID, nodeID string) error {
	now := time.Now()
	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0).Add(-time.Second)

	var quotaBytes int64
	var network models.Network
	if err := r.db.WithContext(ctx).Where("vm_id = ?", vmID).First(&network).Error; err == nil {
		quotaBytes = network.BandwidthQuotaGB * 1024 * 1024 * 1024
	}

	usage := models.BandwidthUsage{
		VMID:           vmID,
		NodeID:         nodeID,
		PeriodStart:    periodStart,
		PeriodEnd:      periodEnd,
		QuotaBytes:     quotaBytes,
		LastReportedAt: now,
	}

	return r.db.WithContext(ctx).Create(&usage).Error
}
