package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

var (
	// ErrRecipeNotFound is returned when a recipe does not exist (or isn't owned).
	ErrRecipeNotFound = fmt.Errorf("recipe not found")
	// ErrRecipeInvalid is returned when the recipe payload is invalid.
	ErrRecipeInvalid = fmt.Errorf("invalid recipe: name and script are required")
	// ErrRecipeDuplicate is returned when the user already has a recipe with the name.
	ErrRecipeDuplicate = fmt.Errorf("a recipe with the same name already exists")
)

// maxRecipeScriptBytes bounds a recipe script to the same size as VM user-data.
const maxRecipeScriptBytes = 65536

// RecipeService manages a user's saved first-boot recipes.
type RecipeService struct {
	repo *repository.RecipeRepository
}

// NewRecipeService creates a new RecipeService.
func NewRecipeService(db *gorm.DB) *RecipeService {
	return &RecipeService{repo: repository.NewRecipeRepository(db)}
}

// RecipeRequest is the payload for creating or updating a recipe.
type RecipeRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Script      string `json:"script"`
}

func (r RecipeRequest) normalize() (name, description, script string, err error) {
	name = strings.TrimSpace(r.Name)
	description = strings.TrimSpace(r.Description)
	script = r.Script
	if name == "" || strings.TrimSpace(script) == "" {
		return "", "", "", ErrRecipeInvalid
	}
	if len(name) > 100 {
		name = name[:100]
	}
	if len(description) > 500 {
		description = description[:500]
	}
	if len(script) > maxRecipeScriptBytes {
		return "", "", "", fmt.Errorf("%w: script exceeds %d bytes", ErrRecipeInvalid, maxRecipeScriptBytes)
	}
	return name, description, script, nil
}

// CreateRecipe validates and stores a user's recipe.
func (s *RecipeService) CreateRecipe(ctx context.Context, userID string, req RecipeRequest) (*models.Recipe, error) {
	name, description, script, err := req.normalize()
	if err != nil {
		return nil, err
	}

	exists, err := s.repo.ExistsByName(ctx, userID, name)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrRecipeDuplicate
	}

	recipe := &models.Recipe{
		UserID:      userID,
		Name:        name,
		Description: description,
		Script:      script,
	}
	if err := s.repo.Create(ctx, recipe); err != nil {
		return nil, err
	}
	return recipe, nil
}

// UpdateRecipe updates an existing recipe owned by the user.
func (s *RecipeService) UpdateRecipe(ctx context.Context, id, userID string, req RecipeRequest) (*models.Recipe, error) {
	name, description, script, err := req.normalize()
	if err != nil {
		return nil, err
	}

	recipe, err := s.repo.GetByIDForUser(ctx, id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRecipeNotFound
		}
		return nil, err
	}

	// Reject a rename that collides with another of the user's recipes.
	if name != recipe.Name {
		exists, eerr := s.repo.ExistsByName(ctx, userID, name)
		if eerr != nil {
			return nil, eerr
		}
		if exists {
			return nil, ErrRecipeDuplicate
		}
	}

	recipe.Name = name
	recipe.Description = description
	recipe.Script = script
	if err := s.repo.Update(ctx, recipe); err != nil {
		return nil, err
	}
	return recipe, nil
}

// ListRecipes returns all of the user's saved recipes (newest first).
func (s *RecipeService) ListRecipes(ctx context.Context, userID string) ([]models.Recipe, error) {
	return s.repo.ListByUserID(ctx, userID)
}

// DeleteRecipe removes a user's recipe.
func (s *RecipeService) DeleteRecipe(ctx context.Context, id, userID string) error {
	if _, err := s.repo.GetByIDForUser(ctx, id, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrRecipeNotFound
		}
		return err
	}
	return s.repo.Delete(ctx, id, userID)
}

// ResolveScript returns the script for a recipe owned by the user. Returns
// ErrRecipeNotFound when the recipe is missing or not owned by the user.
func (s *RecipeService) ResolveScript(ctx context.Context, userID, id string) (string, error) {
	recipe, err := s.repo.GetByIDForUser(ctx, id, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrRecipeNotFound
		}
		return "", err
	}
	return recipe.Script, nil
}
