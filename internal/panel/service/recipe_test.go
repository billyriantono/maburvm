package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

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
	r, err := svc.CreateRecipe(ctx, userID, RecipeRequest{Name: "nginx", Description: "web server", Script: script})
	require.NoError(t, err)
	require.NotEmpty(t, r.ID)
	require.Equal(t, "nginx", r.Name)
	require.Equal(t, script, r.Script)

	// Duplicate name (same user) is rejected.
	_, err = svc.CreateRecipe(ctx, userID, RecipeRequest{Name: "nginx", Script: "echo hi"})
	require.ErrorIs(t, err, ErrRecipeDuplicate)

	// A different user CAN reuse the same name (per-user namespace).
	_, err = svc.CreateRecipe(ctx, otherUser, RecipeRequest{Name: "nginx", Script: "echo other"})
	require.NoError(t, err)

	// Missing name or script is invalid.
	_, err = svc.CreateRecipe(ctx, userID, RecipeRequest{Name: "", Script: script})
	require.ErrorIs(t, err, ErrRecipeInvalid)
	_, err = svc.CreateRecipe(ctx, userID, RecipeRequest{Name: "blank", Script: "   "})
	require.ErrorIs(t, err, ErrRecipeInvalid)

	// Resolve returns the script for the owner.
	got, err := svc.ResolveScript(ctx, userID, r.ID)
	require.NoError(t, err)
	require.Equal(t, script, got)

	// Another user cannot resolve this user's recipe.
	_, err = svc.ResolveScript(ctx, otherUser, r.ID)
	require.ErrorIs(t, err, ErrRecipeNotFound)

	// Update changes fields; a rename onto an existing name is rejected.
	_, err = svc.CreateRecipe(ctx, userID, RecipeRequest{Name: "docker", Script: "echo docker"})
	require.NoError(t, err)
	_, err = svc.UpdateRecipe(ctx, r.ID, userID, RecipeRequest{Name: "docker", Script: script})
	require.ErrorIs(t, err, ErrRecipeDuplicate)

	updated, err := svc.UpdateRecipe(ctx, r.ID, userID, RecipeRequest{Name: "nginx-v2", Description: "updated", Script: "echo v2"})
	require.NoError(t, err)
	require.Equal(t, "nginx-v2", updated.Name)
	require.Equal(t, "echo v2", updated.Script)

	// Another user cannot update or delete this user's recipe.
	_, err = svc.UpdateRecipe(ctx, r.ID, otherUser, RecipeRequest{Name: "x", Script: "y"})
	require.ErrorIs(t, err, ErrRecipeNotFound)
	require.ErrorIs(t, svc.DeleteRecipe(ctx, r.ID, otherUser), ErrRecipeNotFound)

	// List returns the owner's recipes only (nginx-v2 + docker = 2).
	list, err := svc.ListRecipes(ctx, userID)
	require.NoError(t, err)
	require.Len(t, list, 2)

	// Delete works for the owner.
	require.NoError(t, svc.DeleteRecipe(ctx, r.ID, userID))
	list, err = svc.ListRecipes(ctx, userID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "docker", list[0].Name)
}

func TestRecipeRequestNormalizeTrims(t *testing.T) {
	name, desc, script, err := RecipeRequest{Name: "  hi  ", Description: "  d  ", Script: "echo x"}.normalize()
	require.NoError(t, err)
	require.Equal(t, "hi", name)
	require.Equal(t, "d", desc)
	require.Equal(t, "echo x", script)

	// Oversized script rejected.
	_, _, _, err = RecipeRequest{Name: "big", Script: strings.Repeat("a", maxRecipeScriptBytes+1)}.normalize()
	require.ErrorIs(t, err, ErrRecipeInvalid)
}
