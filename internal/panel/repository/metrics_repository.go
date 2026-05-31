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
