package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupRecipeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:recipe-%s?mode=memory&cache=shared", t.Name())),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE recipes (
		id TEXT PRIMARY KEY, user_id TEXT NOT NULL, name TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '', script TEXT NOT NULL,
		is_public BOOLEAN NOT NULL DEFAULT FALSE, inputs TEXT NOT NULL DEFAULT '[]',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, deleted_at DATETIME
	)`).Error)
	return db
}

func TestRecipeServiceCRUDAndResolve(t *testing.T) {
	db := setupRecipeTestDB(t)
	svc := NewRecipeService(db)
	ctx := context.Background()
	const userID = "11111111-1111-1111-1111-111111111111"
	const otherUser = "22222222-2222-2222-2222-222222222222"

	// Create.
	const script = "#!/bin/bash\napt-get update && apt-get install -y nginx\n"
	r, err := svc.CreateRecipe(ctx, userID, RecipeRequest{Name: "nginx", Description: "web server", Script: script}, false)
	require.NoError(t, err)
	require.NotEmpty(t, r.ID)
	require.Equal(t, "nginx", r.Name)
	require.Equal(t, script, r.Script)

	// Duplicate name (same user) is rejected.
	_, err = svc.CreateRecipe(ctx, userID, RecipeRequest{Name: "nginx", Script: "echo hi"}, false)
	require.ErrorIs(t, err, ErrRecipeDuplicate)

	// A different user CAN reuse the same name (per-user namespace).
	_, err = svc.CreateRecipe(ctx, otherUser, RecipeRequest{Name: "nginx", Script: "echo other"}, false)
	require.NoError(t, err)

	// Missing name or script is invalid.
	_, err = svc.CreateRecipe(ctx, userID, RecipeRequest{Name: "", Script: script}, false)
	require.ErrorIs(t, err, ErrRecipeInvalid)
	_, err = svc.CreateRecipe(ctx, userID, RecipeRequest{Name: "blank", Script: "   "}, false)
	require.ErrorIs(t, err, ErrRecipeInvalid)

	// Resolve returns the script for the owner.
	got, err := svc.ResolveScript(ctx, userID, r.ID, nil)
	require.NoError(t, err)
	require.Equal(t, script, got)

	// Another user cannot resolve this user's recipe.
	_, err = svc.ResolveScript(ctx, otherUser, r.ID, nil)
	require.ErrorIs(t, err, ErrRecipeNotFound)

	// Update changes fields; a rename onto an existing name is rejected.
	_, err = svc.CreateRecipe(ctx, userID, RecipeRequest{Name: "docker", Script: "echo docker"}, false)
	require.NoError(t, err)
	_, err = svc.UpdateRecipe(ctx, r.ID, userID, RecipeRequest{Name: "docker", Script: script}, false)
	require.ErrorIs(t, err, ErrRecipeDuplicate)

	updated, err := svc.UpdateRecipe(ctx, r.ID, userID, RecipeRequest{Name: "nginx-v2", Description: "updated", Script: "echo v2"}, false)
	require.NoError(t, err)
	require.Equal(t, "nginx-v2", updated.Name)
	require.Equal(t, "echo v2", updated.Script)

	// Another user cannot update or delete this user's recipe.
	_, err = svc.UpdateRecipe(ctx, r.ID, otherUser, RecipeRequest{Name: "x", Script: "y"}, false)
	require.ErrorIs(t, err, ErrRecipeNotFound)
	require.ErrorIs(t, svc.DeleteRecipe(ctx, r.ID, otherUser, false), ErrRecipeNotFound)

	// List returns the owner's recipes only (nginx-v2 + docker = 2).
	list, err := svc.ListRecipes(ctx, userID)
	require.NoError(t, err)
	require.Len(t, list, 2)

	// Delete works for the owner.
	require.NoError(t, svc.DeleteRecipe(ctx, r.ID, userID, false))
	list, err = svc.ListRecipes(ctx, userID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "docker", list[0].Name)
}

func TestRecipeRequestNormalizeTrims(t *testing.T) {
	req := RecipeRequest{Name: "  hi  ", Description: "  d  ", Script: "echo x"}
	name, desc, script, err := req.normalize()
	require.NoError(t, err)
	require.Equal(t, "hi", name)
	require.Equal(t, "d", desc)
	require.Equal(t, "echo x", script)

	// Oversized script rejected.
	big := RecipeRequest{Name: "big", Script: strings.Repeat("a", maxRecipeScriptBytes+1)}
	_, _, _, err = big.normalize()
	require.ErrorIs(t, err, ErrRecipeInvalid)
}

// Published recipes are offered to everyone, fill their inputs, and only a
// publisher may publish or change them.
func TestRecipeCatalog(t *testing.T) {
	db := setupRecipeTestDB(t)
	svc := NewRecipeService(db)
	ctx := context.Background()
	const staff = "33333333-3333-3333-3333-333333333333"
	const customer = "44444444-4444-4444-4444-444444444444"

	inputs := []models.RecipeInput{{Key: "domain", Label: "Domain", Required: true}, {Key: "port", Label: "Port", Default: "8080"}}
	pub, err := svc.CreateRecipe(ctx, staff, RecipeRequest{Name: "web", Script: "serve {{domain}} on {{port}}", Inputs: inputs, IsPublic: true}, true)
	require.NoError(t, err)
	require.True(t, pub.IsPublic)

	// A customer cannot publish: the flag is dropped.
	own, err := svc.CreateRecipe(ctx, customer, RecipeRequest{Name: "mine", Script: "echo mine", IsPublic: true}, false)
	require.NoError(t, err)
	require.False(t, own.IsPublic)

	list, err := svc.ListRecipes(ctx, customer)
	require.NoError(t, err)
	names := []string{}
	for _, r := range list {
		names = append(names, r.Name)
	}
	require.ElementsMatch(t, []string{"web", "mine"}, names)

	// Inputs fill the script; a required one left out is refused, a default used.
	got, err := svc.ResolveScript(ctx, customer, pub.ID, map[string]string{"domain": "example.com"})
	require.NoError(t, err)
	require.Equal(t, "serve example.com on 8080", got)
	_, err = svc.ResolveScript(ctx, customer, pub.ID, nil)
	require.ErrorIs(t, err, ErrRecipeInvalid)
	_, err = svc.ResolveScript(ctx, customer, pub.ID, map[string]string{"domain": "a\nrm -rf /"})
	require.ErrorIs(t, err, ErrRecipeInvalid, "a value may not add lines to the script")

	// The customer cannot change or delete the published recipe; staff can.
	_, err = svc.UpdateRecipe(ctx, pub.ID, customer, RecipeRequest{Name: "web", Script: "x"}, false)
	require.ErrorIs(t, err, ErrRecipeNotFound)
	require.ErrorIs(t, svc.DeleteRecipe(ctx, pub.ID, customer, false), ErrRecipeNotFound)
	const otherStaff = "55555555-5555-5555-5555-555555555555"
	upd, err := svc.UpdateRecipe(ctx, pub.ID, otherStaff, RecipeRequest{Name: "web", Script: "serve {{domain}}", Inputs: inputs[:1], IsPublic: true}, true)
	require.NoError(t, err)
	require.Len(t, upd.Inputs, 1)

	// Unpublished, it is gone from the customer's list.
	_, err = svc.UpdateRecipe(ctx, pub.ID, staff, RecipeRequest{Name: "web", Script: "serve"}, true)
	require.NoError(t, err)
	list, err = svc.ListRecipes(ctx, customer)
	require.NoError(t, err)
	require.Len(t, list, 1)

	// Bad input keys are refused.
	_, err = svc.CreateRecipe(ctx, staff, RecipeRequest{Name: "bad", Script: "x", Inputs: []models.RecipeInput{{Key: "Bad Key"}}}, true)
	require.ErrorIs(t, err, ErrRecipeInvalid)
}
