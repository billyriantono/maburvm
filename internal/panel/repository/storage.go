package repository

import (
	"errors"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// StorageRepository defines the interface for storage data operations
type StorageRepository interface {
	// Pool operations
	GetPools() ([]models.StoragePool, error)
	GetPoolByID(id string) (*models.StoragePool, error)
	GetPoolsByNodeID(nodeID string) ([]models.StoragePool, error)
	CreatePool(pool *models.StoragePool) error
	UpdatePool(pool *models.StoragePool) error
	SetPrimaryPool(nodeID, poolID string) error
	DeletePool(id string) error

	// Volume operations
	GetVolumes(poolID string) ([]models.StorageVolume, error)
	GetVolumeByID(id string) (*models.StorageVolume, error)
	CreateVolume(volume *models.StorageVolume) error
	DeleteVolume(id string) error
}

// storageRepository implements StorageRepository
type storageRepository struct {
	db *gorm.DB
}

// SetPrimaryPool makes one pool the node's provisioning target and clears the
// flag on its siblings, in one transaction.
//
// Exactly one primary per node is the whole point: two would make the
// destination of a new VM depend on row order, and none would send every VM to
// the node's root filesystem.
func (r *storageRepository) SetPrimaryPool(nodeID, poolID string) error {
	return r.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.StoragePool{}).
			Where("node_id = ? AND id <> ?", nodeID, poolID).
			Update("is_primary", false).Error; err != nil {
			return err
		}
		return tx.Model(&models.StoragePool{}).
			Where("id = ?", poolID).
			Update("is_primary", true).Error
	})
}

// NewStorageRepository creates a new storage repository
func NewStorageRepository(db *gorm.DB) StorageRepository {
	return &storageRepository{db: db}
}

// GetPools retrieves all storage pools
func (r *storageRepository) GetPools() ([]models.StoragePool, error) {
	var pools []models.StoragePool
	if err := r.db.Find(&pools).Error; err != nil {
		return nil, err
	}
	return pools, nil
}

// GetPoolByID retrieves a storage pool by ID
func (r *storageRepository) GetPoolByID(id string) (*models.StoragePool, error) {
	var pool models.StoragePool
	if err := r.db.Where("id = ?", id).First(&pool).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &pool, nil
}

// GetPoolsByNodeID retrieves storage pools by node ID
func (r *storageRepository) GetPoolsByNodeID(nodeID string) ([]models.StoragePool, error) {
	var pools []models.StoragePool
	if err := r.db.Where("node_id = ?", nodeID).Find(&pools).Error; err != nil {
		return nil, err
	}
	return pools, nil
}

// CreatePool creates a new storage pool
func (r *storageRepository) CreatePool(pool *models.StoragePool) error {
	return r.db.Create(pool).Error
}

// UpdatePool updates an existing storage pool
func (r *storageRepository) UpdatePool(pool *models.StoragePool) error {
	return r.db.Save(pool).Error
}

// DeletePool soft deletes a storage pool
func (r *storageRepository) DeletePool(id string) error {
	return r.db.Where("id = ?", id).Delete(&models.StoragePool{}).Error
}

// GetVolumes retrieves all volumes for a pool
func (r *storageRepository) GetVolumes(poolID string) ([]models.StorageVolume, error) {
	var volumes []models.StorageVolume
	if err := r.db.Where("pool_id = ?", poolID).Find(&volumes).Error; err != nil {
		return nil, err
	}
	return volumes, nil
}

// GetVolumeByID retrieves a volume by ID
func (r *storageRepository) GetVolumeByID(id string) (*models.StorageVolume, error) {
	var volume models.StorageVolume
	if err := r.db.Where("id = ?", id).First(&volume).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &volume, nil
}

// CreateVolume creates a new storage volume
func (r *storageRepository) CreateVolume(volume *models.StorageVolume) error {
	return r.db.Create(volume).Error
}

// DeleteVolume soft deletes a storage volume
func (r *storageRepository) DeleteVolume(id string) error {
	return r.db.Where("id = ?", id).Delete(&models.StorageVolume{}).Error
}
