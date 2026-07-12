package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

var (
	// ErrPlanNotFound is returned when a plan does not exist.
	ErrPlanNotFound = errors.New("plan not found")
	// ErrPlanNameExists is returned when a plan name is already taken.
	ErrPlanNameExists = errors.New("plan name already exists")
)

// PlanService provides business logic for VPS plans.
type PlanService struct {
	repo *repository.PlanRepository
}

// NewPlanService creates a new PlanService.
func NewPlanService(repo *repository.PlanRepository) *PlanService {
	return &PlanService{repo: repo}
}

// PlanRequest contains create/update parameters for a plan.
type PlanRequest struct {
	Name              string `json:"name"`
	CPU               int    `json:"cpu"`
	RAM               int    `json:"ram"`
	Disk              int    `json:"disk"`
	BandwidthMbps     int    `json:"bandwidth_mbps"`
	DataQuotaGB       int64  `json:"data_quota_gb"`
	OverQuotaPolicy   string `json:"over_quota_policy"`
	ThrottleSpeedMbps int    `json:"throttle_speed_mbps"`
	Description       string `json:"description"`
	IsActive          *bool  `json:"is_active"`
}

func (r *PlanRequest) validate() error {
	if r.Name == "" {
		return fmt.Errorf("name is required")
	}
	if r.CPU < 1 || r.CPU > 128 {
		return fmt.Errorf("cpu must be between 1 and 128")
	}
	if r.RAM < 128 {
		return fmt.Errorf("ram must be at least 128 MB")
	}
	if r.Disk < 1 {
		return fmt.Errorf("disk must be at least 1 GB")
	}
	if r.BandwidthMbps < 0 {
		return fmt.Errorf("bandwidth_mbps cannot be negative")
	}
	if r.DataQuotaGB < 0 {
		return fmt.Errorf("data_quota_gb cannot be negative")
	}
	if r.ThrottleSpeedMbps < 0 {
		return fmt.Errorf("throttle_speed_mbps cannot be negative")
	}
	switch r.OverQuotaPolicy {
	case "", models.OverQuotaThrottle, models.OverQuotaOverage, models.OverQuotaSuspend:
	default:
		return fmt.Errorf("over_quota_policy must be throttle, overage, or suspend")
	}
	return nil
}

// normalizedOverQuotaPolicy returns the request's policy, defaulting to throttle.
func (r *PlanRequest) normalizedOverQuotaPolicy() string {
	if r.OverQuotaPolicy == "" {
		return models.OverQuotaThrottle
	}
	return r.OverQuotaPolicy
}

// CreatePlan creates a new plan.
func (s *PlanService) CreatePlan(ctx context.Context, req *PlanRequest) (*models.Plan, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	exists, err := s.repo.NameExists(ctx, req.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to check plan name: %w", err)
	}
	if exists {
		return nil, ErrPlanNameExists
	}
	active := true
	if req.IsActive != nil {
		active = *req.IsActive
	}
	plan := &models.Plan{
		Name:              req.Name,
		CPU:               req.CPU,
		RAM:               req.RAM,
		Disk:              req.Disk,
		BandwidthMbps:     req.BandwidthMbps,
		DataQuotaGB:       req.DataQuotaGB,
		OverQuotaPolicy:   req.normalizedOverQuotaPolicy(),
		ThrottleSpeedMbps: req.ThrottleSpeedMbps,
		Description:       req.Description,
		IsActive:          active,
	}
	if err := s.repo.Create(ctx, plan); err != nil {
		return nil, fmt.Errorf("failed to create plan: %w", err)
	}
	return plan, nil
}

// ListPlans returns all plans (or only active ones).
func (s *PlanService) ListPlans(ctx context.Context, activeOnly bool) ([]models.Plan, error) {
	return s.repo.List(ctx, activeOnly)
}

// GetPlan returns a plan by ID.
func (s *PlanService) GetPlan(ctx context.Context, id string) (*models.Plan, error) {
	plan, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPlanNotFound
		}
		return nil, err
	}
	return plan, nil
}

// UpdatePlan updates an existing plan.
func (s *PlanService) UpdatePlan(ctx context.Context, id string, req *PlanRequest) (*models.Plan, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	plan, err := s.GetPlan(ctx, id)
	if err != nil {
		return nil, err
	}
	plan.Name = req.Name
	plan.CPU = req.CPU
	plan.RAM = req.RAM
	plan.Disk = req.Disk
	plan.BandwidthMbps = req.BandwidthMbps
	plan.DataQuotaGB = req.DataQuotaGB
	plan.OverQuotaPolicy = req.normalizedOverQuotaPolicy()
	plan.ThrottleSpeedMbps = req.ThrottleSpeedMbps
	plan.Description = req.Description
	if req.IsActive != nil {
		plan.IsActive = *req.IsActive
	}
	if err := s.repo.Update(ctx, plan); err != nil {
		return nil, fmt.Errorf("failed to update plan: %w", err)
	}
	return plan, nil
}

// DeletePlan removes a plan.
func (s *PlanService) DeletePlan(ctx context.Context, id string) error {
	if _, err := s.GetPlan(ctx, id); err != nil {
		return err
	}
	return s.repo.Delete(ctx, id)
}
