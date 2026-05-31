package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// ErrQuotaExceeded is returned when creating/resizing a VM would exceed a user's quota.
var ErrQuotaExceeded = errors.New("quota exceeded")

// QuotaService enforces per-user resource limits.
type QuotaService struct {
	repo   *repository.QuotaRepository
	vmRepo *repository.VMRepository
}

// NewQuotaService creates a new QuotaService.
func NewQuotaService(db *gorm.DB, vmRepo *repository.VMRepository) *QuotaService {
	return &QuotaService{repo: repository.NewQuotaRepository(db), vmRepo: vmRepo}
}

// QuotaUsage is a user's current consumption across all their VMs.
type QuotaUsage struct {
	VMs    int `json:"vms"`
	VCPU   int `json:"vcpu"`
	RAMMB  int `json:"ram_mb"`
	DiskGB int `json:"disk_gb"`
}

// SetQuotaRequest is the admin input for setting a user's quota. Zero = unlimited.
type SetQuotaRequest struct {
	MaxVMs    int `json:"max_vms" validate:"min=0"`
	MaxVCPU   int `json:"max_vcpu" validate:"min=0"`
	MaxRAMMB  int `json:"max_ram_mb" validate:"min=0"`
	MaxDiskGB int `json:"max_disk_gb" validate:"min=0"`
}

// QuotaStatus bundles a user's limits with their current usage for display.
type QuotaStatus struct {
	Quota models.UserQuota `json:"quota"`
	Usage QuotaUsage       `json:"usage"`
}

// GetQuota returns a user's quota, defaulting to all-unlimited when none is set.
func (s *QuotaService) GetQuota(ctx context.Context, userID string) (*models.UserQuota, error) {
	q, err := s.repo.GetByUserID(ctx, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &models.UserQuota{UserID: userID}, nil // all zero => unlimited
		}
		return nil, err
	}
	return q, nil
}

// SetQuota creates or updates a user's quota.
func (s *QuotaService) SetQuota(ctx context.Context, userID string, req *SetQuotaRequest) (*models.UserQuota, error) {
	q := &models.UserQuota{
		UserID:    userID,
		MaxVMs:    req.MaxVMs,
		MaxVCPU:   req.MaxVCPU,
		MaxRAMMB:  req.MaxRAMMB,
		MaxDiskGB: req.MaxDiskGB,
	}
	if err := s.repo.Upsert(ctx, q); err != nil {
		return nil, err
	}
	return q, nil
}

// GetUsage computes a user's current resource consumption from their VMs.
func (s *QuotaService) GetUsage(ctx context.Context, userID string) (QuotaUsage, error) {
	vms, err := s.vmRepo.ListByUserID(ctx, userID, 0, 0)
	if err != nil {
		return QuotaUsage{}, err
	}
	usage := QuotaUsage{VMs: len(vms)}
	for i := range vms {
		usage.VCPU += vms[i].Resources.CPU
		usage.RAMMB += vms[i].Resources.RAM
		usage.DiskGB += vms[i].Resources.Disk
	}
	return usage, nil
}

// GetStatus returns a user's quota together with current usage.
func (s *QuotaService) GetStatus(ctx context.Context, userID string) (*QuotaStatus, error) {
	q, err := s.GetQuota(ctx, userID)
	if err != nil {
		return nil, err
	}
	usage, err := s.GetUsage(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &QuotaStatus{Quota: *q, Usage: usage}, nil
}

// CheckCanCreate returns ErrQuotaExceeded (wrapped with detail) if allocating
// `add` for one new VM would push the user past any non-zero limit.
func (s *QuotaService) CheckCanCreate(ctx context.Context, userID string, add models.Resources) error {
	q, err := s.GetQuota(ctx, userID)
	if err != nil {
		return err
	}
	usage, err := s.GetUsage(ctx, userID)
	if err != nil {
		return err
	}
	return evaluateQuota(q, usage, add)
}

// CheckCanResize returns ErrQuotaExceeded if changing one VM's resources from
// oldRes to newRes would push the user past any non-zero limit. A resize leaves
// the VM count unchanged; current usage already includes oldRes.
func (s *QuotaService) CheckCanResize(ctx context.Context, userID string, oldRes, newRes models.Resources) error {
	q, err := s.GetQuota(ctx, userID)
	if err != nil {
		return err
	}
	usage, err := s.GetUsage(ctx, userID)
	if err != nil {
		return err
	}
	after := QuotaUsage{
		VMs:    usage.VMs,
		VCPU:   usage.VCPU - oldRes.CPU + newRes.CPU,
		RAMMB:  usage.RAMMB - oldRes.RAM + newRes.RAM,
		DiskGB: usage.DiskGB - oldRes.Disk + newRes.Disk,
	}
	return evaluateTotals(q, after)
}

// evaluateQuota is the pure create-time core: it checks the effect of adding
// exactly one VM consuming `add` on top of current usage.
func evaluateQuota(q *models.UserQuota, used QuotaUsage, add models.Resources) error {
	return evaluateTotals(q, QuotaUsage{
		VMs:    used.VMs + 1,
		VCPU:   used.VCPU + add.CPU,
		RAMMB:  used.RAMMB + add.RAM,
		DiskGB: used.DiskGB + add.Disk,
	})
}

// evaluateTotals is the pure limit-checking core (no I/O): it rejects when any
// projected total exceeds its corresponding non-zero limit. A zero limit means
// unlimited.
func evaluateTotals(q *models.UserQuota, t QuotaUsage) error {
	switch {
	case q.MaxVMs > 0 && t.VMs > q.MaxVMs:
		return fmt.Errorf("%w: VM count %d/%d", ErrQuotaExceeded, t.VMs, q.MaxVMs)
	case q.MaxVCPU > 0 && t.VCPU > q.MaxVCPU:
		return fmt.Errorf("%w: vCPU %d/%d", ErrQuotaExceeded, t.VCPU, q.MaxVCPU)
	case q.MaxRAMMB > 0 && t.RAMMB > q.MaxRAMMB:
		return fmt.Errorf("%w: RAM %d/%d MB", ErrQuotaExceeded, t.RAMMB, q.MaxRAMMB)
	case q.MaxDiskGB > 0 && t.DiskGB > q.MaxDiskGB:
		return fmt.Errorf("%w: disk %d/%d GB", ErrQuotaExceeded, t.DiskGB, q.MaxDiskGB)
	}
	return nil
}
