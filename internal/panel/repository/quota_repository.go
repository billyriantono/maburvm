package repository

import (
	"context"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// QuotaRepository provides data access for per-user resource quotas.
type QuotaRepository struct {
	db *gorm.DB
}

// NewQuotaRepository creates a new QuotaRepository.
func NewQuotaRepository(db *gorm.DB) *QuotaRepository {
	return &QuotaRepository{db: db}
}

// GetByUserID returns the quota row for a user, or gorm.ErrRecordNotFound.
func (r *QuotaRepository) GetByUserID(ctx context.Context, userID string) (*models.UserQuota, error) {
	var q models.UserQuota
	if err := r.db.WithContext(ctx).First(&q, "user_id = ?", userID).Error; err != nil {
		return nil, err
	}
	return &q, nil
}

// Upsert creates or updates the quota row for a user.
func (r *QuotaRepository) Upsert(ctx context.Context, q *models.UserQuota) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"max_vms", "max_vcpu", "max_ram_mb", "max_disk_gb", "updated_at"}),
	}).Create(q).Error
}
