package repository

import (
	"context"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// AuditRepository provides data access for audit logs
type AuditRepository struct {
	base *BaseRepository[models.AuditLog]
	db   *gorm.DB
}

// NewAuditRepository creates a new audit repository
func NewAuditRepository(db *gorm.DB) *AuditRepository {
	return &AuditRepository{
		base: NewBaseRepository[models.AuditLog](db),
		db:   db,
	}
}

// Create inserts a new audit log entry
func (r *AuditRepository) Create(ctx context.Context, auditLog *models.AuditLog) error {
	return r.db.WithContext(ctx).Create(auditLog).Error
}

// GetByID retrieves an audit log by its ID
func (r *AuditRepository) GetByID(ctx context.Context, id string) (*models.AuditLog, error) {
	return r.base.GetByID(ctx, id)
}

// List retrieves audit logs with optional filtering and pagination
// List returns audit entries newest first.
//
// The generic base List applies no ORDER BY, so Postgres returned them in
// storage order — oldest first for an append-only table. Every reader of an
// audit log wants the opposite: the dashboard's "Recent Activity" was showing
// ten-day-old entries, and page 1 of the audit page was the beginning of time.
// The filtered variants below already ordered correctly, which is why this went
// unnoticed.
func (r *AuditRepository) List(ctx context.Context, limit, offset int) ([]models.AuditLog, error) {
	var logs []models.AuditLog
	query := r.db.WithContext(ctx).Order("created_at DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&logs).Error; err != nil {
		return nil, err
	}
	return logs, nil
}

// ListByUser retrieves audit logs for a specific user
func (r *AuditRepository) ListByUser(ctx context.Context, userID string, limit, offset int) ([]models.AuditLog, error) {
	var logs []models.AuditLog
	query := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC")

	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}

	if err := query.Find(&logs).Error; err != nil {
		return nil, err
	}
	return logs, nil
}

// ListByAction retrieves audit logs for a specific action
func (r *AuditRepository) ListByAction(ctx context.Context, action string, limit, offset int) ([]models.AuditLog, error) {
	var logs []models.AuditLog
	query := r.db.WithContext(ctx).Where("action = ?", action).Order("created_at DESC")

	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}

	if err := query.Find(&logs).Error; err != nil {
		return nil, err
	}
	return logs, nil
}

// ListByResource retrieves audit logs for a specific resource
func (r *AuditRepository) ListByResource(ctx context.Context, resourceType, resourceID string, limit, offset int) ([]models.AuditLog, error) {
	var logs []models.AuditLog
	query := r.db.WithContext(ctx).
		Where("resource_type = ? AND resource_id = ?", resourceType, resourceID).
		Order("created_at DESC")

	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}

	if err := query.Find(&logs).Error; err != nil {
		return nil, err
	}
	return logs, nil
}

// Count returns the total number of audit logs
func (r *AuditRepository) Count(ctx context.Context) (int64, error) {
	return r.base.Count(ctx)
}

// CountByUser returns the number of audit logs for a specific user
func (r *AuditRepository) CountByUser(ctx context.Context, userID string) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.AuditLog{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// CountByAction returns the number of audit logs for a specific action
func (r *AuditRepository) CountByAction(ctx context.Context, action string) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.AuditLog{}).Where("action = ?", action).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}
