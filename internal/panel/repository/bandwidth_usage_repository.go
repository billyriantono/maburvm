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

// TransferTerms is how a VM's transfer is counted: the day its allowance
// starts again (the account's choice over the plan's) and which direction
// counts. A VM without a plan, or a database without these columns, counts
// both directions from the 1st, as before they existed.
func (r *BandwidthUsageRepository) TransferTerms(ctx context.Context, vmID string) (resetDay int, counts string) {
	var row struct {
		ResetDay int
		Counts   string
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT COALESCE(u.transfer_reset_day, p.transfer_reset_day, 1) AS reset_day,
			COALESCE(p.transfer_counts, 'both') AS counts
		FROM vms v
		LEFT JOIN plans p ON CAST(p.id AS TEXT) = CAST(v.plan_id AS TEXT)
		LEFT JOIN users u ON CAST(u.id AS TEXT) = CAST(v.user_id AS TEXT)
		WHERE CAST(v.id AS TEXT) = ?`, vmID).Scan(&row).Error; err != nil {
		return 1, models.TransferCountBoth
	}
	if !models.ValidTransferCount(row.Counts) {
		row.Counts = models.TransferCountBoth
	}
	return row.ResetDay, row.Counts
}

// currentPeriod is the VM's allowance period now.
func (r *BandwidthUsageRepository) currentPeriod(ctx context.Context, vmID string) (start, end time.Time) {
	day, _ := r.TransferTerms(ctx, vmID)
	return models.TransferPeriod(time.Now(), day)
}

// GetCurrentPeriod returns the bandwidth usage record for a VM in the current billing period.
// If no record exists, it creates one.
func (r *BandwidthUsageRepository) GetCurrentPeriod(ctx context.Context, vmID, nodeID string) (*models.BandwidthUsage, error) {
	now := time.Now()
	periodStart, periodEnd := r.currentPeriod(ctx, vmID)

	var usage models.BandwidthUsage
	err := r.db.WithContext(ctx).
		Where("vm_id = ? AND period_start = ?", vmID, periodStart).
		First(&usage).Error

	if err == gorm.ErrRecordNotFound {
		// A reader (the VM page) does not know the node, only the collector
		// does. Opening the page for a VM the collector has not reached yet used
		// to insert node_id "" and fail on the uuid column, so the usage card
		// errored for every new VM. The VM row knows where it lives.
		if nodeID == "" {
			if err := r.db.WithContext(ctx).Table("vms").Select("node_id").Where("id = ?", vmID).Scan(&nodeID).Error; err != nil {
				return nil, err
			}
		}

		// Create new period record
		usage = models.BandwidthUsage{
			VMID:           vmID,
			NodeID:         nodeID,
			PeriodStart:    periodStart,
			PeriodEnd:      periodEnd,
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

// AccumulateUsage adds delta bytes to the current period's usage. RxBytes and
// TxBytes always take both directions; TotalBytes, which the allowance is
// measured against, only what the VM's plan counts.
// Uses upsert to handle concurrent updates safely.
func (r *BandwidthUsageRepository) AccumulateUsage(ctx context.Context, vmID, nodeID string, rxDelta, txDelta int64) (*models.BandwidthUsage, error) {
	now := time.Now()
	day, counts := r.TransferTerms(ctx, vmID)
	periodStart, periodEnd := models.TransferPeriod(now, day)
	counted := models.CountedBytes(counts, rxDelta, txDelta)

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
		TotalBytes:     counted,
		QuotaBytes:     quotaBytes,
		LastReportedAt: now,
	}

	// Upsert: on conflict (vm_id, period_start), accumulate
	err := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "vm_id"}, {Name: "period_start"}},
			DoUpdates: clause.Assignments(map[string]interface{}{
				"rx_bytes":         gorm.Expr("bandwidth_usages.rx_bytes + ?", rxDelta),
				"tx_bytes":         gorm.Expr("bandwidth_usages.tx_bytes + ?", txDelta),
				"total_bytes":      gorm.Expr("bandwidth_usages.total_bytes + ?", counted),
				"quota_bytes":      quotaBytes,
				"last_reported_at": now,
				"updated_at":       now,
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

// GetByNodeID returns current period usage for all VMs on a node. Periods
// differ per VM, so "current" is the period that contains today.
func (r *BandwidthUsageRepository) GetByNodeID(ctx context.Context, nodeID string) ([]models.BandwidthUsage, error) {
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	var usages []models.BandwidthUsage
	err := r.db.WithContext(ctx).
		Where("node_id = ? AND period_start <= ? AND period_end >= ?", nodeID, today, today).
		Find(&usages).Error
	return usages, err
}

// MarkExceeded marks a VM's bandwidth as exceeded and records the block time
func (r *BandwidthUsageRepository) MarkExceeded(ctx context.Context, vmID string) error {
	now := time.Now()
	periodStart, _ := r.currentPeriod(ctx, vmID)

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
	periodStart, _ := r.currentPeriod(ctx, vmID)

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
		// Clear overage flag when unlimited or now under the new quota. The cast
		// is needed: compared to a bare 0, Postgres takes the parameter for an
		// int4 and refuses any quota of 2 GB or more.
		return tx.Model(&models.BandwidthUsage{}).
			Where("vm_id = ? AND period_start = ? AND (CAST(? AS BIGINT) = 0 OR total_bytes < ?)", vmID, periodStart, quotaBytes, quotaBytes).
			Updates(map[string]interface{}{"exceeded": false, "blocked_at": nil}).Error
	})
}

// ResetPeriod creates a new period record (called at billing cycle reset)
func (r *BandwidthUsageRepository) ResetPeriod(ctx context.Context, vmID, nodeID string) error {
	now := time.Now()
	periodStart, periodEnd := r.currentPeriod(ctx, vmID)

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

// LoadCounters returns the cumulative counters last read from a VM, so
// accounting after a panel restart differences against them. ok is false when
// none were kept.
func (r *BandwidthUsageRepository) LoadCounters(ctx context.Context, vmID string) (rx, tx int64, ok bool) {
	var rows []struct{ RxBytes, TxBytes int64 }
	if err := r.db.WithContext(ctx).Raw(`SELECT rx_bytes, tx_bytes FROM vm_traffic_counters WHERE vm_id = ?`, vmID).
		Scan(&rows).Error; err != nil || len(rows) == 0 {
		return 0, 0, false
	}
	return rows[0].RxBytes, rows[0].TxBytes, true
}

// SaveCounters keeps the cumulative counters just read from a VM.
func (r *BandwidthUsageRepository) SaveCounters(ctx context.Context, vmID string, rx, tx int64) error {
	return r.db.WithContext(ctx).Exec(`INSERT INTO vm_traffic_counters (vm_id, rx_bytes, tx_bytes, updated_at)
		VALUES (?, ?, ?, ?) ON CONFLICT (vm_id) DO UPDATE SET rx_bytes = EXCLUDED.rx_bytes,
		tx_bytes = EXCLUDED.tx_bytes, updated_at = EXCLUDED.updated_at`, vmID, rx, tx, time.Now()).Error
}

// ForgetCounters drops the counters kept for a VM, so a drop in its next
// reading starts a new baseline rather than counting as a restart.
func (r *BandwidthUsageRepository) ForgetCounters(ctx context.Context, vmID string) error {
	return r.db.WithContext(ctx).Exec(`DELETE FROM vm_traffic_counters WHERE vm_id = ?`, vmID).Error
}

// PoolUsage is an account's pooled transfer this period: the VMs with an
// allowance share the sum of them.
type PoolUsage struct {
	Pooled     bool
	UsedBytes  int64
	QuotaBytes int64
	// UsageIDs are the period rows in the pool, VMs without an allowance
	// being outside it.
	UsageIDs []string
}

// AccountPool returns the pool a VM's usage belongs to. Pooled is false when
// the VM's account does not pool its transfer, or the VM has no allowance.
func (r *BandwidthUsageRepository) AccountPool(ctx context.Context, vmID string) PoolUsage {
	var acct []struct {
		UserID string
		Pooled bool
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT CAST(u.id AS TEXT) AS user_id, u.transfer_pooled AS pooled
		FROM vms v JOIN users u ON CAST(u.id AS TEXT) = CAST(v.user_id AS TEXT) WHERE CAST(v.id AS TEXT) = ?`, vmID).
		Scan(&acct).Error; err != nil || len(acct) == 0 || !acct[0].Pooled {
		return PoolUsage{}
	}
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	var rows []struct {
		ID         string
		VMID       string
		TotalBytes int64
		QuotaBytes int64
	}
	// One row per VM: after a reset day changes, the old period and the new
	// one both contain today for a while, and only the one still counting
	// (the last updated) is the VM's.
	if err := r.db.WithContext(ctx).Raw(`SELECT DISTINCT ON (CAST(b.vm_id AS TEXT)) CAST(b.id AS TEXT) AS id,
			CAST(b.vm_id AS TEXT) AS vm_id, b.total_bytes, b.quota_bytes
		FROM bandwidth_usages b JOIN vms v ON CAST(v.id AS TEXT) = CAST(b.vm_id AS TEXT)
		WHERE CAST(v.user_id AS TEXT) = ? AND v.deleted_at IS NULL AND b.deleted_at IS NULL
			AND b.quota_bytes > 0 AND b.period_start <= ? AND b.period_end >= ?
		ORDER BY CAST(b.vm_id AS TEXT), b.updated_at DESC`, acct[0].UserID, today, today).
		Scan(&rows).Error; err != nil {
		return PoolUsage{}
	}
	var p PoolUsage
	in := false
	for _, row := range rows {
		p.UsedBytes += row.TotalBytes
		p.QuotaBytes += row.QuotaBytes
		p.UsageIDs = append(p.UsageIDs, row.ID)
		in = in || row.VMID == vmID
	}
	p.Pooled = in
	return p
}

// MarkPoolExceeded flags every row of a pool over its allowance and returns
// the VMs that were not already flagged.
func (r *BandwidthUsageRepository) MarkPoolExceeded(ctx context.Context, usageIDs []string) ([]string, error) {
	if len(usageIDs) == 0 {
		return nil, nil
	}
	var vmIDs []string
	err := r.db.WithContext(ctx).Raw(`UPDATE bandwidth_usages SET exceeded = TRUE, blocked_at = ?
		WHERE CAST(id AS TEXT) IN ? AND NOT exceeded RETURNING CAST(vm_id AS TEXT)`, time.Now(), usageIDs).
		Scan(&vmIDs).Error
	return vmIDs, err
}
