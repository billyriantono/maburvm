package repository

import (
	"context"

	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
)

// TemplateRepository provides data access for OS templates
type TemplateRepository struct {
	base *BaseRepository[models.OSTemplate]
	db   *gorm.DB
}

// NewTemplateRepository creates a new TemplateRepository instance
func NewTemplateRepository(db *gorm.DB) *TemplateRepository {
	return &TemplateRepository{
		base: NewBaseRepository[models.OSTemplate](db),
		db:   db,
	}
}

// GetByID retrieves an OS template by ID
func (r *TemplateRepository) GetByID(ctx context.Context, id string) (*models.OSTemplate, error) {
	return r.base.GetByID(ctx, id)
}

// GetByName retrieves an OS template by name
func (r *TemplateRepository) GetByName(ctx context.Context, name string) (*models.OSTemplate, error) {
	var template models.OSTemplate
	if err := r.db.WithContext(ctx).Where("name = ?", name).First(&template).Error; err != nil {
		return nil, err
	}
	return &template, nil
}

// GetByNameAndVersion retrieves an OS template by name and version
func (r *TemplateRepository) GetByNameAndVersion(ctx context.Context, name, version string) (*models.OSTemplate, error) {
	var template models.OSTemplate
	if err := r.db.WithContext(ctx).Where("name = ? AND version = ?", name, version).First(&template).Error; err != nil {
		return nil, err
	}
	return &template, nil
}

// GetByImagePath retrieves an OS template by image path
func (r *TemplateRepository) GetByImagePath(ctx context.Context, imagePath string) (*models.OSTemplate, error) {
	var template models.OSTemplate
	if err := r.db.WithContext(ctx).Where("image_path = ?", imagePath).First(&template).Error; err != nil {
		return nil, err
	}
	return &template, nil
}

// List retrieves all OS templates with optional pagination
func (r *TemplateRepository) List(ctx context.Context, limit, offset int) ([]models.OSTemplate, error) {
	return r.base.List(ctx, limit, offset)
}

// ListActive retrieves all active OS templates
func (r *TemplateRepository) ListActive(ctx context.Context, limit, offset int) ([]models.OSTemplate, error) {
	var templates []models.OSTemplate
	query := r.db.WithContext(ctx).Where("is_active = ?", true)
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&templates).Error; err != nil {
		return nil, err
	}
	return templates, nil
}

// ListInactive retrieves all inactive OS templates
func (r *TemplateRepository) ListInactive(ctx context.Context, limit, offset int) ([]models.OSTemplate, error) {
	var templates []models.OSTemplate
	query := r.db.WithContext(ctx).Where("is_active = ?", false)
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&templates).Error; err != nil {
		return nil, err
	}
	return templates, nil
}

// Create inserts a new OS template
func (r *TemplateRepository) Create(ctx context.Context, template *models.OSTemplate) error {
	return r.base.Create(ctx, template)
}

// Update updates an existing OS template
func (r *TemplateRepository) Update(ctx context.Context, template *models.OSTemplate) error {
	return r.base.Update(ctx, template)
}

// Delete removes an OS template by ID (hard delete as per PRD compliance requirements)
func (r *TemplateRepository) Delete(ctx context.Context, id string) error {
	return r.base.Delete(ctx, id)
}

// Count returns the total number of OS templates
func (r *TemplateRepository) Count(ctx context.Context) (int64, error) {
	return r.base.Count(ctx)
}

// CountActive returns the number of active OS templates
func (r *TemplateRepository) CountActive(ctx context.Context) (int64, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.OSTemplate{}).Where("is_active = ?", true).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

// UpdateActiveStatus updates an OS template's active status
func (r *TemplateRepository) UpdateActiveStatus(ctx context.Context, id string, isActive bool) error {
	return r.db.WithContext(ctx).Model(&models.OSTemplate{}).Where("id = ?", id).Update("is_active", isActive).Error
}

// UpdateImagePath updates an OS template's image path
func (r *TemplateRepository) UpdateImagePath(ctx context.Context, id string, imagePath string) error {
	return r.db.WithContext(ctx).Model(&models.OSTemplate{}).Where("id = ?", id).Update("image_path", imagePath).Error
}

// UpdateVersion updates an OS template's version
func (r *TemplateRepository) UpdateVersion(ctx context.Context, id string, version string) error {
	return r.db.WithContext(ctx).Model(&models.OSTemplate{}).Where("id = ?", id).Update("version", version).Error
}

// NameExists checks if a template name already exists
func (r *TemplateRepository) NameExists(ctx context.Context, name string) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.OSTemplate{}).Where("name = ?", name).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// NameAndVersionExists checks if a template with the same name and version already exists
func (r *TemplateRepository) NameAndVersionExists(ctx context.Context, name, version string) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.OSTemplate{}).Where("name = ? AND version = ?", name, version).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// ImagePathExists checks if an image path is already in use
func (r *TemplateRepository) ImagePathExists(ctx context.Context, imagePath string) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&models.OSTemplate{}).Where("image_path = ?", imagePath).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetIDs returns all OS template IDs
func (r *TemplateRepository) GetIDs(ctx context.Context) ([]string, error) {
	var ids []string
	if err := r.db.WithContext(ctx).Model(&models.OSTemplate{}).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// GetIDsByActiveStatus returns all OS template IDs filtered by active status
func (r *TemplateRepository) GetIDsByActiveStatus(ctx context.Context, isActive bool) ([]string, error) {
	var ids []string
	if err := r.db.WithContext(ctx).Model(&models.OSTemplate{}).Where("is_active = ?", isActive).Pluck("id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}
