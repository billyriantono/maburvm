package repository

import (
	"context"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// SSHKeyRepository provides data access for user SSH public keys.
type SSHKeyRepository struct {
	db *gorm.DB
}

// NewSSHKeyRepository creates a new SSHKeyRepository.
func NewSSHKeyRepository(db *gorm.DB) *SSHKeyRepository {
	return &SSHKeyRepository{db: db}
}

func (r *SSHKeyRepository) Create(ctx context.Context, key *models.SSHKey) error {
	return r.db.WithContext(ctx).Create(key).Error
}

func (r *SSHKeyRepository) ListByUserID(ctx context.Context, userID string) ([]models.SSHKey, error) {
	var keys []models.SSHKey
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC").Find(&keys).Error
	return keys, err
}

func (r *SSHKeyRepository) GetByID(ctx context.Context, id string) (*models.SSHKey, error) {
	var key models.SSHKey
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&key).Error; err != nil {
		return nil, err
	}
	return &key, nil
}

// ListByIDsForUser returns the user's keys matching the given IDs
// (ownership-enforced). Returns an empty slice when ids is empty.
func (r *SSHKeyRepository) ListByIDsForUser(ctx context.Context, userID string, ids []string) ([]models.SSHKey, error) {
	var keys []models.SSHKey
	if len(ids) == 0 {
		return keys, nil
	}
	err := r.db.WithContext(ctx).Where("user_id = ? AND id IN ?", userID, ids).Find(&keys).Error
	return keys, err
}

func (r *SSHKeyRepository) Delete(ctx context.Context, id, userID string) error {
	return r.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).Delete(&models.SSHKey{}).Error
}

func (r *SSHKeyRepository) ExistsByFingerprint(ctx context.Context, userID, fingerprint string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&models.SSHKey{}).
		Where("user_id = ? AND fingerprint = ?", userID, fingerprint).
		Count(&count).Error
	return count > 0, err
}
