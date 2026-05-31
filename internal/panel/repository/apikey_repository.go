package repository

import (
	"context"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// APIKeyRepository provides data access for API keys.
type APIKeyRepository struct {
	db *gorm.DB
}

// NewAPIKeyRepository creates a new APIKeyRepository.
func NewAPIKeyRepository(db *gorm.DB) *APIKeyRepository {
	return &APIKeyRepository{db: db}
}

func (r *APIKeyRepository) Create(ctx context.Context, key *models.APIKey) error {
	return r.db.WithContext(ctx).Create(key).Error
}

func (r *APIKeyRepository) ListByUserID(ctx context.Context, userID string) ([]models.APIKey, error) {
	var keys []models.APIKey
	return keys, r.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC").Find(&keys).Error
}

func (r *APIKeyRepository) GetByHash(ctx context.Context, keyHash string) (*models.APIKey, error) {
	var key models.APIKey
	if err := r.db.WithContext(ctx).First(&key, "key_hash = ?", keyHash).Error; err != nil {
		return nil, err
	}
	return &key, nil
}

func (r *APIKeyRepository) GetByID(ctx context.Context, id string) (*models.APIKey, error) {
	var key models.APIKey
	if err := r.db.WithContext(ctx).First(&key, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &key, nil
}

func (r *APIKeyRepository) Delete(ctx context.Context, id, userID string) error {
	return r.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).Delete(&models.APIKey{}).Error
}

func (r *APIKeyRepository) TouchLastUsed(ctx context.Context, id string) error {
	now := time.Now()
	return r.db.WithContext(ctx).Model(&models.APIKey{}).Where("id = ?", id).
		Update("last_used_at", now).Error
}
