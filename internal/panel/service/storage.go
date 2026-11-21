package service

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
)

const bytesPerGB = 1024 * 1024 * 1024

// ErrInvalidStoragePool marks a pool the panel refuses to register.
var ErrInvalidStoragePool = errors.New("invalid storage pool")

// PoolTypeDir is a directory on the node holding qcow2 disks. A mounted ZFS
// dataset, NFS export or CephFS is registered as one too. LVM volume groups and
// thin pools are the other kinds that hold VM disks (models.PoolTypeLVM*).
const PoolTypeDir = models.PoolTypeDir

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

// CreatePool registers a directory, an LVM volume group or an LVM thin pool
// on a node as a storage pool. An LVM pool is checked on the node first: it
// must exist and must not hold the host's own system.
//
// Only the name, node, type, path and alert threshold are the operator's to
// set. Capacity and status are measured on the node by the metrics collector,
// so whatever the request says about them is dropped.
func (s *storageService) CreatePool(pool *models.StoragePool) error {
	pool.Name = strings.TrimSpace(pool.Name)
	pool.Path = strings.TrimSpace(pool.Path)
	if pool.Name == "" {
		return fmt.Errorf("%w: pool name is required", ErrInvalidStoragePool)
	}
	if pool.NodeID == "" {
		return fmt.Errorf("%w: node ID is required", ErrInvalidStoragePool)
	}
	if pool.Type == "" {
		pool.Type = PoolTypeDir
	}
	pool.FileFormat = "qcow2"
	switch {
	case pool.Type == PoolTypeDir:
		if !path.IsAbs(pool.Path) {
			return fmt.Errorf("%w: the path must be an absolute directory on the node, such as /data", ErrInvalidStoragePool)
		}
		pool.Path = path.Clean(pool.Path)
	case models.IsLVMPoolType(pool.Type):
		vg, thin, err := models.SplitLVMPoolPath(pool.Type, pool.Path)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidStoragePool, err)
		}
		pool.Path = strings.TrimSuffix(vg+"/"+thin, "/")
		pool.FileFormat = "raw"
	default:
		return fmt.Errorf("%w: pool type %q is not supported. Use a directory (mount other storage on the node and register its mount point), an LVM volume group or an LVM thin pool", ErrInvalidStoragePool, pool.Type)
	}
	siblings, err := s.repo.GetPoolsByNodeID(pool.NodeID)
	if err != nil {
		return err
	}
	for _, other := range siblings {
		if other.Type == pool.Type && path.Clean(other.Path) == pool.Path {
			return fmt.Errorf("%w: %s is already registered on this node as %q", ErrInvalidStoragePool, pool.Path, other.Name)
		}
	}
	if models.IsLVMPoolType(pool.Type) {
		if err := s.checkLVMPool(pool); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidStoragePool, err)
		}
	}
	if pool.AlertThreshold <= 0 || pool.AlertThreshold > 100 {
		pool.AlertThreshold = 90
	}

	pool.Status = "offline"
	pool.TotalSpace, pool.UsedSpace, pool.AvailableSpace, pool.Overcommit = 0, 0, 0, 0
	pool.StatusNote, pool.MetadataPercent = "", nil
	// Never primary at birth: until the node has measured it the pool is not
	// online, and a node whose primary is not online provisions onto its root
	// filesystem. It is promoted from its card once it is.
	pool.IsPrimary = false

	return s.repo.CreatePool(pool)
}

// lvmPoolUsable refuses an LVM pool its node does not report usable: an agent
// too old for LVM pools (one rolled back, say) would make or remove a volume
// there without the ownership checks.
func lvmPoolUsable(p *models.StoragePool) error {
	if p == nil || !models.IsLVMPoolType(p.Type) || p.Status == "online" {
		return nil
	}
	why := p.StatusNote
	if why == "" {
		why = "the node has not measured it yet"
	}
	return fmt.Errorf("%w: storage pool %s cannot be used now: %s", ErrInvalidStoragePool, p.Name, why)
}

// checkLVMPool asks the pool's node whether it can hold VM disks there.
func (s *storageService) checkLVMPool(pool *models.StoragePool) error {
	if s.provisioner == nil || s.nodeRepo == nil {
		return errors.New("the node cannot be asked whether it can use this pool")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	node, err := s.nodeRepo.GetByID(ctx, pool.NodeID)
	if err != nil {
		return fmt.Errorf("node not found: %w", err)
	}
	return s.provisioner.CheckLVMPool(ctx, node, pool.Type, pool.Path)
}

// UpdatePool renames a pool, changes its alert threshold, or makes it the
// node's provisioning target. Capacity and status are measured, not set.
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
	if pool.AlertThreshold > 0 && pool.AlertThreshold <= 100 {
		existing.AlertThreshold = pool.AlertThreshold
	}

	// Promoting a pool demotes its siblings, so a node always has exactly one
	// provisioning target. Demotion is deliberately not offered: clearing the
	// only primary would silently send every new VM back to the node's root
	// filesystem, which is the fault this exists to prevent.
	//
	// Promoting a pool that is already primary demotes the others too: that is
	// how a node left with two primaries is put right.
	if pool.IsPrimary {
		switch {
		case existing.Status == "online":
			if err := s.repo.SetPrimaryPool(existing.NodeID, existing.ID); err != nil {
				return err
			}
			existing.IsPrimary = true
		case !existing.IsPrimary:
			// A primary that is not online is skipped at provisioning, which
			// falls back to the root filesystem just the same.
			return fmt.Errorf("%w: %s is not online on its node, so it cannot take new VMs yet", ErrInvalidStoragePool, existing.Name)
		}
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
		if err := lvmPoolUsable(pool); err != nil {
			return err
		}
		if models.IsLVMPoolType(pool.Type) {
			volume.Format = "raw"
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
		if err := lvmPoolUsable(pool); err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()

		node, err := s.nodeRepo.GetByID(ctx, pool.NodeID)
		if err != nil {
			return fmt.Errorf("failed to get node for pool: %w", err)
		}
		if err := s.provisioner.DeleteVolume(ctx, node, pool.Type, pool.Path, existing.Path); err != nil {
			return fmt.Errorf("failed to delete volume file: %w", err)
		}
	}

	return s.repo.DeleteVolume(id)
}
