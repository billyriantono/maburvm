package repository

import (
	"context"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// ManagedNetworkRepository provides data access for administrator-defined networks.
type ManagedNetworkRepository struct {
	db *gorm.DB
}

// NewManagedNetworkRepository creates a new ManagedNetworkRepository.
func NewManagedNetworkRepository(db *gorm.DB) *ManagedNetworkRepository {
	return &ManagedNetworkRepository{db: db}
}

func (r *ManagedNetworkRepository) Create(ctx context.Context, n *models.ManagedNetwork) error {
	return r.db.WithContext(ctx).Create(n).Error
}

func (r *ManagedNetworkRepository) List(ctx context.Context) ([]models.ManagedNetwork, error) {
	var nets []models.ManagedNetwork
	return nets, r.db.WithContext(ctx).Order("created_at DESC").Find(&nets).Error
}

func (r *ManagedNetworkRepository) GetByID(ctx context.Context, id string) (*models.ManagedNetwork, error) {
	var n models.ManagedNetwork
	if err := r.db.WithContext(ctx).First(&n, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &n, nil
}

func (r *ManagedNetworkRepository) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&models.ManagedNetwork{}).Error
}
