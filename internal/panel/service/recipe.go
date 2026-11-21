package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

var (
	// ErrRecipeNotFound is returned when a recipe does not exist, or is
	// private to someone else (we don't distinguish, to avoid leaking IDs).
	ErrRecipeNotFound = fmt.Errorf("recipe not found")
	// ErrRecipeInvalid is returned for a recipe with a missing name or script.
	ErrRecipeInvalid = fmt.Errorf("invalid recipe: name and script are required")
	// ErrRecipeDuplicate is returned when the user already has a recipe with the
	// same name.
	ErrRecipeDuplicate = fmt.Errorf("a recipe with the same name already exists")
)

// maxRecipeScriptBytes caps a recipe script. Cloud-init user-data has practical
// size limits (the config-drive/NoCloud seed), so we keep scripts modest.
const maxRecipeScriptBytes = 65536

// Limits on a recipe's inputs and the values a customer fills in.
const (
	maxRecipeInputs     = 20
	maxRecipeInputValue = 1000
)

var recipeInputKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// RecipeService manages first-boot recipes: each account's own, and the
// public ones staff publish to everyone (the app catalog).
type RecipeService struct {
	repo *repository.RecipeRepository
	db   *gorm.DB
}

// NewRecipeService creates a RecipeService.
func NewRecipeService(db *gorm.DB) *RecipeService {
	return &RecipeService{repo: repository.NewRecipeRepository(db), db: db}
}

// RecipeRequest is the input for creating/updating a recipe.
type RecipeRequest struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Script      string               `json:"script"`
	Inputs      []models.RecipeInput `json:"inputs"`
	// IsPublic publishes the recipe to every customer. Only staff who manage
	// templates may set it; for anyone else it is ignored.
	IsPublic bool `json:"is_public"`
}

// normalize trims and validates a recipe request.
func (r *RecipeRequest) normalize() (name, description, script string, err error) {
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
	if len(r.Inputs) > maxRecipeInputs {
		return "", "", "", fmt.Errorf("%w: at most %d inputs", ErrRecipeInvalid, maxRecipeInputs)
	}
	seen := map[string]bool{}
	for i := range r.Inputs {
		in := &r.Inputs[i]
		in.Key, in.Label, in.Description = strings.TrimSpace(in.Key), strings.TrimSpace(in.Label), strings.TrimSpace(in.Description)
		if !recipeInputKey.MatchString(in.Key) {
			return "", "", "", fmt.Errorf("%w: input key %q must start with a letter and use only a-z, 0-9 and _ (up to 32)", ErrRecipeInvalid, in.Key)
		}
		if seen[in.Key] {
			return "", "", "", fmt.Errorf("%w: input key %q is listed twice", ErrRecipeInvalid, in.Key)
		}
		seen[in.Key] = true
		if in.Label == "" {
			in.Label = in.Key
		}
		if len(in.Label) > 100 || len(in.Description) > 300 {
			return "", "", "", fmt.Errorf("%w: input %q has a label over 100 or a description over 300 characters", ErrRecipeInvalid, in.Key)
		}
		if err := checkRecipeValue(in.Default); err != nil {
			return "", "", "", fmt.Errorf("%w: default of %q: %v", ErrRecipeInvalid, in.Key, err)
		}
	}
	if r.Inputs == nil {
		r.Inputs = []models.RecipeInput{}
	}
	return name, description, script, nil
}

// checkRecipeValue keeps a filled-in value on one line, so it cannot add
// lines to the script it is put into.
func checkRecipeValue(v string) error {
	if len(v) > maxRecipeInputValue {
		return fmt.Errorf("longer than %d characters", maxRecipeInputValue)
	}
	if strings.ContainsAny(v, "\r\n\x00") {
		return fmt.Errorf("must be on one line")
	}
	return nil
}

// saveCatalogFields writes the public flag and inputs, which the model only
// reads.
func (s *RecipeService) saveCatalogFields(ctx context.Context, id string, isPublic bool, inputs []models.RecipeInput) error {
	raw, err := json.Marshal(inputs)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Table("recipes").Where("id = ?", id).
		Updates(map[string]any{"is_public": isPublic, "inputs": string(raw)}).Error
}

// CreateRecipe stores a recipe. publisher says the caller may publish it to
// every customer.
func (s *RecipeService) CreateRecipe(ctx context.Context, userID string, req RecipeRequest, publisher bool) (*models.Recipe, error) {
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
	recipe.IsPublic, recipe.Inputs = publisher && req.IsPublic, req.Inputs
	if recipe.IsPublic || len(recipe.Inputs) > 0 {
		if err := s.saveCatalogFields(ctx, recipe.ID, recipe.IsPublic, recipe.Inputs); err != nil {
			return nil, err
		}
	}
	return recipe, nil
}

// editable loads a recipe the caller may change: their own, or for a
// publisher any public one.
func (s *RecipeService) editable(ctx context.Context, id, userID string, publisher bool) (*models.Recipe, error) {
	var recipe models.Recipe
	q := s.db.WithContext(ctx).Where("id = ?", id)
	if publisher {
		q = q.Where("(user_id = ? OR is_public)", userID)
	} else {
		q = q.Where("user_id = ?", userID)
	}
	if err := q.First(&recipe).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRecipeNotFound
		}
		return nil, err
	}
	return &recipe, nil
}

// UpdateRecipe updates a recipe the caller may change.
func (s *RecipeService) UpdateRecipe(ctx context.Context, id, userID string, req RecipeRequest, publisher bool) (*models.Recipe, error) {
	name, description, script, err := req.normalize()
	if err != nil {
		return nil, err
	}

	recipe, err := s.editable(ctx, id, userID, publisher)
	if err != nil {
		return nil, err
	}

	if name != recipe.Name {
		exists, eerr := s.repo.ExistsByName(ctx, recipe.UserID, name)
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
	isPublic := recipe.IsPublic
	if publisher {
		isPublic = req.IsPublic
	}
	if isPublic != recipe.IsPublic || len(req.Inputs) > 0 || len(recipe.Inputs) > 0 {
		if err := s.saveCatalogFields(ctx, recipe.ID, isPublic, req.Inputs); err != nil {
			return nil, err
		}
	}
	recipe.IsPublic, recipe.Inputs = isPublic, req.Inputs
	return recipe, nil
}

// ListRecipes returns the user's own recipes and the published ones, newest
// first.
func (s *RecipeService) ListRecipes(ctx context.Context, userID string) ([]models.Recipe, error) {
	var recipes []models.Recipe
	err := s.db.WithContext(ctx).Where("(user_id = ? OR is_public)", userID).Order("created_at DESC").Find(&recipes).Error
	if err != nil {
		// A database from before the catalog: own recipes only.
		return s.repo.ListByUserID(ctx, userID)
	}
	return recipes, nil
}

// DeleteRecipe deletes a recipe the caller may change.
func (s *RecipeService) DeleteRecipe(ctx context.Context, id, userID string, publisher bool) error {
	recipe, err := s.editable(ctx, id, userID, publisher)
	if err != nil {
		return err
	}
	return s.repo.Delete(ctx, recipe.ID, recipe.UserID)
}

// ResolveScript returns the script of a recipe the user may order with
// (their own or a published one), its {{key}} placeholders filled from
// inputs. A required input left empty, or a value that spans lines, is
// refused with ErrRecipeInvalid.
func (s *RecipeService) ResolveScript(ctx context.Context, userID, id string, inputs map[string]string) (string, error) {
	var recipe models.Recipe
	err := s.db.WithContext(ctx).Where("id = ? AND (user_id = ? OR is_public)", id, userID).First(&recipe).Error
	if err != nil {
		// A database from before the catalog has no public recipes.
		r, oerr := s.repo.GetByIDForUser(ctx, id, userID)
		if oerr != nil {
			if errors.Is(oerr, gorm.ErrRecordNotFound) {
				return "", ErrRecipeNotFound
			}
			return "", oerr
		}
		recipe = *r
	}
	return FillRecipe(&recipe, inputs)
}

// FillRecipe puts the input values into a recipe's script.
func FillRecipe(r *models.Recipe, values map[string]string) (string, error) {
	pairs := make([]string, 0, 2*len(r.Inputs))
	for _, in := range r.Inputs {
		v, ok := values[in.Key]
		if !ok || v == "" {
			v = in.Default
		}
		if v == "" && in.Required {
			return "", fmt.Errorf("%w: %s is required", ErrRecipeInvalid, in.Label)
		}
		if err := checkRecipeValue(v); err != nil {
			return "", fmt.Errorf("%w: %s %v", ErrRecipeInvalid, in.Label, err)
		}
		pairs = append(pairs, "{{"+in.Key+"}}", v)
	}
	if len(pairs) == 0 {
		return r.Script, nil
	}
	return strings.NewReplacer(pairs...).Replace(r.Script), nil
}
