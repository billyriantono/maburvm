package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
)

const bytesPerGB = 1024 * 1024 * 1024

// StorageService defines the interface for storage business logic
type StorageService interface {
	GetPools() ([]models.StoragePool, error)
	GetPoolByID(id string) (*models.StoragePool, error)
	GetPoolsByNodeID(nodeID string) ([]models.StoragePool, error)
	CreatePool(pool *models.StoragePool) error
	UpdatePool(id string, pool *models.StoragePool) error
	DeletePool(id string) error

	GetVolumes(poolID string) ([]models.StorageVolume, error)
	GetVolumeByID(id string) (*models.StorageVolume, error)
	CreateVolume(volume *models.StorageVolume) error
	DeleteVolume(id string) error
}

// storageService implements StorageService
type storageService struct {
	repo        repository.StorageRepository
	nodeRepo    *repository.NodeRepository
	provisioner VolumeProvisioner
}

// NewStorageService creates a storage service that persists metadata only.
func NewStorageService(repo repository.StorageRepository) StorageService {
	return &storageService{
		repo: repo,
	}
}

// NewStorageServiceWithProvisioner creates a storage service that provisions
// real volumes on node agents (and removes them on delete).
func NewStorageServiceWithProvisioner(repo repository.StorageRepository, nodeRepo *repository.NodeRepository, provisioner VolumeProvisioner) StorageService {
	return &storageService{
		repo:        repo,
		nodeRepo:    nodeRepo,
		provisioner: provisioner,
	}
}

// GetPools retrieves all storage pools
func (s *storageService) GetPools() ([]models.StoragePool, error) {
	return s.repo.GetPools()
}

// GetPoolByID retrieves a storage pool by ID
func (s *storageService) GetPoolByID(id string) (*models.StoragePool, error) {
	if id == "" {
		return nil, errors.New("pool ID is required")
	}
	return s.repo.GetPoolByID(id)
}

// GetPoolsByNodeID retrieves storage pools by node ID
func (s *storageService) GetPoolsByNodeID(nodeID string) ([]models.StoragePool, error) {
	if nodeID == "" {
		return nil, errors.New("node ID is required")
	}
	return s.repo.GetPoolsByNodeID(nodeID)
}

// CreatePool creates a new storage pool
func (s *storageService) CreatePool(pool *models.StoragePool) error {
	if pool.Name == "" {
		return errors.New("pool name is required")
	}
	if pool.NodeID == "" {
		return errors.New("node ID is required")
	}
	if pool.Path == "" {
		return errors.New("pool path is required")
	}

	// Set defaults
	if pool.Type == "" {
		pool.Type = "dir"
	}
	if pool.Status == "" {
		pool.Status = "offline"
	}

	return s.repo.CreatePool(pool)
}

// UpdatePool updates an existing storage pool
func (s *storageService) UpdatePool(id string, pool *models.StoragePool) error {
	if id == "" {
		return errors.New("pool ID is required")
	}

	existing, err := s.repo.GetPoolByID(id)
	if err != nil {
		return err
	}
	if existing == nil {
		return errors.New("pool not found")
	}

	// Update fields
	if pool.Name != "" {
		existing.Name = pool.Name
	}
	if pool.Status != "" {
		existing.Status = pool.Status
	}
	if pool.TotalSpace > 0 {
		existing.TotalSpace = pool.TotalSpace
	}
	if pool.UsedSpace > 0 {
		existing.UsedSpace = pool.UsedSpace
	}
	if pool.AvailableSpace > 0 {
		existing.AvailableSpace = pool.AvailableSpace
	}

	return s.repo.UpdatePool(existing)
}

// DeletePool deletes a storage pool
func (s *storageService) DeletePool(id string) error {
	if id == "" {
		return errors.New("pool ID is required")
	}

	existing, err := s.repo.GetPoolByID(id)
	if err != nil {
		return err
	}
	if existing == nil {
		return errors.New("pool not found")
	}

	return s.repo.DeletePool(id)
}

// GetVolumes retrieves all volumes for a pool
func (s *storageService) GetVolumes(poolID string) ([]models.StorageVolume, error) {
	if poolID == "" {
		return nil, errors.New("pool ID is required")
	}
	return s.repo.GetVolumes(poolID)
}

// GetVolumeByID retrieves a volume by ID
func (s *storageService) GetVolumeByID(id string) (*models.StorageVolume, error) {
	if id == "" {
		return nil, errors.New("volume ID is required")
	}
	return s.repo.GetVolumeByID(id)
}

// CreateVolume creates a new storage volume. When a provisioner is wired, it
// first provisions a real disk image on the pool's node (recording the actual
// path) and only persists the record if provisioning succeeds.
func (s *storageService) CreateVolume(volume *models.StorageVolume) error {
	if volume.Name == "" {
		return errors.New("volume name is required")
	}
	if volume.PoolID == "" {
		return errors.New("pool ID is required")
	}

	// Set defaults
	if volume.Format == "" {
		volume.Format = "qcow2"
	}

	if s.provisioner != nil && s.nodeRepo != nil {
		pool, err := s.repo.GetPoolByID(volume.PoolID)
		if err != nil {
			return err
		}
		if pool == nil {
			return errors.New("pool not found")
		}

		sizeGB := volume.Size / bytesPerGB
		if volume.Size%bytesPerGB != 0 {
			sizeGB++ // round up to the next whole GB
		}
		if sizeGB <= 0 {
			return errors.New("volume size must be at least 1 GB")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		node, err := s.nodeRepo.GetByID(ctx, pool.NodeID)
		if err != nil {
			return fmt.Errorf("failed to get node for pool: %w", err)
		}
		path, _, err := s.provisioner.CreateVolume(ctx, node, pool.Type, pool.Path, volume.Name, volume.Format, sizeGB)
		if err != nil {
			return fmt.Errorf("failed to provision volume: %w", err)
		}
		volume.Path = path
	}

	return s.repo.CreateVolume(volume)
}

// DeleteVolume deletes a storage volume. When a provisioner is wired, it removes
// the real disk image from the node first and fails (keeping the record) if that
// cannot be done, so the DB never claims a volume is gone while its file remains.
func (s *storageService) DeleteVolume(id string) error {
	if id == "" {
		return errors.New("volume ID is required")
	}

	existing, err := s.repo.GetVolumeByID(id)
	if err != nil {
		return err
	}
	if existing == nil {
		return errors.New("volume not found")
	}

	if s.provisioner != nil && s.nodeRepo != nil && existing.Path != "" {
		pool, err := s.repo.GetPoolByID(existing.PoolID)
		if err != nil {
			return err
		}
		if pool == nil {
			return errors.New("pool not found")
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		node, err := s.nodeRepo.GetByID(ctx, pool.NodeID)
		if err != nil {
			return fmt.Errorf("failed to get node for pool: %w", err)
		}
		if err := s.provisioner.DeleteVolume(ctx, node, pool.Type, existing.Path); err != nil {
			return fmt.Errorf("failed to delete volume file: %w", err)
		}
	}

	return s.repo.DeleteVolume(id)
}
