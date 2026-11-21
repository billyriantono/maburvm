package repository

import (
	"context"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// MetricsRepository provides data access for persisted node metric samples.
type MetricsRepository struct {
	db *gorm.DB
}

// NewMetricsRepository creates a new MetricsRepository.
func NewMetricsRepository(db *gorm.DB) *MetricsRepository {
	return &MetricsRepository{db: db}
}

// InsertNodeSample stores one node metric sample.
func (r *MetricsRepository) InsertNodeSample(ctx context.Context, sample *models.NodeMetricSample) error {
	return r.db.WithContext(ctx).Create(sample).Error
}

// ListNodeSamples returns a node's samples recorded at or after `since`, oldest
// first, capped at `limit` (most recent kept when capped). limit <= 0 means no cap.
func (r *MetricsRepository) ListNodeSamples(ctx context.Context, nodeID string, since time.Time, limit int) ([]models.NodeMetricSample, error) {
	// Fetch the newest rows within the window (so a cap keeps recent data),
	// then return them in chronological order for charting.
	var rows []models.NodeMetricSample
	q := r.db.WithContext(ctx).
		Where("node_id = ? AND recorded_at >= ?", nodeID, since).
		Order("recorded_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	// Reverse into ascending (oldest first).
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, nil
}

// PruneNodeSamplesBefore deletes node samples older than cutoff, returning the count removed.
func (r *MetricsRepository) PruneNodeSamplesBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res := r.db.WithContext(ctx).
		Where("recorded_at < ?", cutoff).
		Delete(&models.NodeMetricSample{})
	return res.RowsAffected, res.Error
}

// InsertVMSample stores one VM metric sample.
func (r *MetricsRepository) InsertVMSample(ctx context.Context, sample *models.VMMetricSample) error {
	return r.db.WithContext(ctx).Create(sample).Error
}

// ListVMSamples returns a VM's samples recorded at or after `since`, oldest first,
// capped at `limit` (most recent kept when capped). limit <= 0 means no cap.
func (r *MetricsRepository) ListVMSamples(ctx context.Context, vmID string, since time.Time, limit int) ([]models.VMMetricSample, error) {
	var rows []models.VMMetricSample
	q := r.db.WithContext(ctx).
		Where("vm_id = ? AND recorded_at >= ?", vmID, since).
		Order("recorded_at DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, nil
}

// PruneVMSamplesBefore deletes VM samples older than cutoff, returning the count removed.
func (r *MetricsRepository) PruneVMSamplesBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res := r.db.WithContext(ctx).
		Where("recorded_at < ?", cutoff).
		Delete(&models.VMMetricSample{})
	return res.RowsAffected, res.Error
}

// RollupVMHours averages raw VM samples recorded in [from, to) into hourly
// rows (UTC hours). An hour already rolled up is replaced, so running over
// the same hours again is harmless. Postgres only.
func (r *MetricsRepository) RollupVMHours(ctx context.Context, from, to time.Time) error {
	return r.db.WithContext(ctx).Exec(`INSERT INTO vm_metrics_hourly (vm_id, bucket, samples, cpu_usage, memory_usage, memory_used_bytes,
			disk_read_bytes_per_sec, disk_write_bytes_per_sec, network_rx_bytes_per_sec, network_tx_bytes_per_sec)
		SELECT vm_id, date_trunc('hour', recorded_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', count(*),
			avg(cpu_usage), avg(memory_usage), avg(memory_used_bytes)::bigint,
			avg(disk_read_bytes_per_sec)::bigint, avg(disk_write_bytes_per_sec)::bigint,
			avg(network_rx_bytes_per_sec)::bigint, avg(network_tx_bytes_per_sec)::bigint
		FROM vm_metrics WHERE recorded_at >= ? AND recorded_at < ?
		GROUP BY 1, 2
		ON CONFLICT (vm_id, bucket) DO UPDATE SET samples = EXCLUDED.samples, cpu_usage = EXCLUDED.cpu_usage,
			memory_usage = EXCLUDED.memory_usage, memory_used_bytes = EXCLUDED.memory_used_bytes,
			disk_read_bytes_per_sec = EXCLUDED.disk_read_bytes_per_sec, disk_write_bytes_per_sec = EXCLUDED.disk_write_bytes_per_sec,
			network_rx_bytes_per_sec = EXCLUDED.network_rx_bytes_per_sec, network_tx_bytes_per_sec = EXCLUDED.network_tx_bytes_per_sec`,
		from.UTC(), to.UTC()).Error
}

// ListVMHours returns a VM's hourly averages from `since`, oldest first, as
// samples stamped at the end of their hour.
func (r *MetricsRepository) ListVMHours(ctx context.Context, vmID string, since time.Time) ([]models.VMMetricSample, error) {
	var rows []struct {
		Bucket                                                     time.Time
		CPUUsage, MemoryUsage                                      float64
		MemoryUsedBytes, DiskReadBytesPerSec, DiskWriteBytesPerSec int64
		NetworkRxBytesPerSec, NetworkTxBytesPerSec                 int64
	}
	if err := r.db.WithContext(ctx).Raw(`SELECT bucket, cpu_usage, memory_usage, memory_used_bytes, disk_read_bytes_per_sec,
			disk_write_bytes_per_sec, network_rx_bytes_per_sec, network_tx_bytes_per_sec
		FROM vm_metrics_hourly WHERE vm_id = ? AND bucket >= ? ORDER BY bucket`, vmID, since.UTC().Truncate(time.Hour)).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]models.VMMetricSample, len(rows))
	for i, h := range rows {
		out[i] = models.VMMetricSample{VMID: vmID, CPUUsage: h.CPUUsage, MemoryUsage: h.MemoryUsage, MemoryUsedBytes: h.MemoryUsedBytes,
			DiskReadBytesPerSec: h.DiskReadBytesPerSec, DiskWriteBytesPerSec: h.DiskWriteBytesPerSec,
			NetworkRxBytesPerSec: h.NetworkRxBytesPerSec, NetworkTxBytesPerSec: h.NetworkTxBytesPerSec,
			RecordedAt: h.Bucket.Add(time.Hour - time.Second)}
	}
	return out, nil
}

// PruneVMHoursBefore deletes hourly averages older than cutoff.
func (r *MetricsRepository) PruneVMHoursBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res := r.db.WithContext(ctx).Exec(`DELETE FROM vm_metrics_hourly WHERE bucket < ?`, cutoff.UTC())
	return res.RowsAffected, res.Error
}
