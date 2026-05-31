package repository

import (
	"context"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// PlanRepository provides data access for VPS plans.
type PlanRepository struct {
	db *gorm.DB
}

// NewPlanRepository creates a new PlanRepository.
func NewPlanRepository(db *gorm.DB) *PlanRepository {
	return &PlanRepository{db: db}
}

func (r *PlanRepository) Create(ctx context.Context, plan *models.Plan) error {
	return r.db.WithContext(ctx).Create(plan).Error
}

func (r *PlanRepository) GetByID(ctx context.Context, id string) (*models.Plan, error) {
	var plan models.Plan
	if err := r.db.WithContext(ctx).First(&plan, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &plan, nil
}

func (r *PlanRepository) List(ctx context.Context, activeOnly bool) ([]models.Plan, error) {
	var plans []models.Plan
	q := r.db.WithContext(ctx).Order("ram ASC")
	if activeOnly {
		q = q.Where("is_active = ?", true)
	}
	return plans, q.Find(&plans).Error
}

func (r *PlanRepository) Update(ctx context.Context, plan *models.Plan) error {
	return r.db.WithContext(ctx).Save(plan).Error
}

func (r *PlanRepository) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Delete(&models.Plan{}, "id = ?", id).Error
}

func (r *PlanRepository) NameExists(ctx context.Context, name string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&models.Plan{}).Where("name = ?", name).Count(&count).Error
	return count > 0, err
}
