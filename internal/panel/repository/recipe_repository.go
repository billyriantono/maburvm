package repository

import (
	"context"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// RecipeRepository provides data access for user first-boot recipes.
type RecipeRepository struct {
	db *gorm.DB
}

// NewRecipeRepository creates a new RecipeRepository.
func NewRecipeRepository(db *gorm.DB) *RecipeRepository {
	return &RecipeRepository{db: db}
}

func (r *RecipeRepository) Create(ctx context.Context, recipe *models.Recipe) error {
	return r.db.WithContext(ctx).Create(recipe).Error
}

func (r *RecipeRepository) Update(ctx context.Context, recipe *models.Recipe) error {
	return r.db.WithContext(ctx).
		Model(&models.Recipe{}).
		Where("id = ? AND user_id = ?", recipe.ID, recipe.UserID).
		Updates(map[string]interface{}{
			"name":        recipe.Name,
			"description": recipe.Description,
			"script":      recipe.Script,
		}).Error
}

func (r *RecipeRepository) ListByUserID(ctx context.Context, userID string) ([]models.Recipe, error) {
	var recipes []models.Recipe
	err := r.db.WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC").Find(&recipes).Error
	return recipes, err
}

// GetByIDForUser returns a recipe only if it is owned by the given user.
func (r *RecipeRepository) GetByIDForUser(ctx context.Context, id, userID string) (*models.Recipe, error) {
	var recipe models.Recipe
	if err := r.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).First(&recipe).Error; err != nil {
		return nil, err
	}
	return &recipe, nil
}

func (r *RecipeRepository) Delete(ctx context.Context, id, userID string) error {
	return r.db.WithContext(ctx).Where("id = ? AND user_id = ?", id, userID).Delete(&models.Recipe{}).Error
}

func (r *RecipeRepository) ExistsByName(ctx context.Context, userID, name string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&models.Recipe{}).
		Where("user_id = ? AND name = ?", userID, name).
		Count(&count).Error
	return count > 0, err
}
