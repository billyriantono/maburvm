//go:build pg

package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// Panel settings override their environment variables at once, secrets are
// stored encrypted, and clearing one hands it back to the environment.
func TestPanelSettingsOnPostgres(t *testing.T) {
	db, raw, _ := pgMigratedDB(t)
	ctx := context.Background()
	models.SetSecretKeys("0123456789abcdef0123456789abcdef")
	t.Cleanup(func() {
		models.SetSecretKeys("")
		_ = service.LoadPanelSettings(ctx, nil)
	})
	t.Setenv("PANEL_PUBLIC_URL", "https://env.example.com")
	t.Setenv("BILLING_API_KEY", "")
	require.NoError(t, service.LoadPanelSettings(ctx, db))
	assert.Equal(t, "https://env.example.com", service.PublicURL())

	str := func(s string) *string { return &s }
	require.NoError(t, service.SavePanelSettings(ctx, db, map[string]*string{
		"publicUrl":     str("https://saved.example.com/"),
		"billingApiKey": str("billing-key-0123456789abcdef"),
	}))
	assert.Equal(t, "https://saved.example.com", service.PublicURL())
	v, src := service.PanelValue("billingApiKey")
	assert.Equal(t, "billing-key-0123456789abcdef", v)
	assert.Equal(t, service.PanelFromSetting, src)

	var stored string
	require.NoError(t, raw.QueryRow(`SELECT data->>'billingApiKey' FROM system_settings WHERE section = 'panel'`).Scan(&stored))
	assert.True(t, strings.HasPrefix(stored, models.SealedPrefix), "the key is stored encrypted, not %q", stored)

	// Another boot reads the same values back.
	require.NoError(t, service.LoadPanelSettings(ctx, db))
	v, _ = service.PanelValue("billingApiKey")
	assert.Equal(t, "billing-key-0123456789abcdef", v)

	// A key left out is untouched; an empty value returns to the environment.
	require.NoError(t, service.SavePanelSettings(ctx, db, map[string]*string{"publicUrl": str(""), "billingApiKey": nil}))
	assert.Equal(t, "https://env.example.com", service.PublicURL())
	v, _ = service.PanelValue("billingApiKey")
	assert.Equal(t, "billing-key-0123456789abcdef", v)

	assert.Error(t, service.SavePanelSettings(ctx, db, map[string]*string{"billingApiKey": str("short")}), "a guessable key is refused")
	assert.Error(t, service.SavePanelSettings(ctx, db, map[string]*string{"databasePassword": str("x")}), "unknown keys are refused")

	// The boot pass that re-seals secrets on key rotation knows these fields.
	n, err := sealSecretsAtRest(ctx, db)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "already sealed under the current key")
}

// The first administrator is created once, with the token from the data
// volume, which is then removed.
func TestFirstRunOnPostgres(t *testing.T) {
	db, _, _ := pgMigratedDB(t)
	ctx := context.Background()
	dir := t.TempDir()
	t.Setenv("MABURVM_DATA_DIR", dir)

	open, err := service.PrepareFirstRun(ctx, db)
	require.NoError(t, err)
	require.True(t, open)
	raw, err := os.ReadFile(filepath.Join(dir, "first-run-token"))
	require.NoError(t, err)
	token := strings.TrimSpace(string(raw))
	require.Len(t, token, 64)
	open, _ = service.PrepareFirstRun(ctx, db)
	require.True(t, open)
	again, _ := os.ReadFile(filepath.Join(dir, "first-run-token"))
	require.Equal(t, string(raw), string(again), "a restart keeps the same link")

	const pw = "Correct-Horse-9-Battery"
	_, err = service.CreateFirstAdmin(ctx, db, "wrong", "Owner", "owner@example.com", pw)
	require.ErrorIs(t, err, service.ErrFirstRunToken)
	_, err = service.CreateFirstAdmin(ctx, db, token, "Owner", "owner@example.com", "weak")
	require.ErrorIs(t, err, service.ErrWeakPassword)

	u, err := service.CreateFirstAdmin(ctx, db, token, "Owner", "Owner@Example.com", pw)
	require.NoError(t, err)
	assert.Equal(t, models.RoleAdmin, u.Role)
	assert.Equal(t, "owner@example.com", u.Email)
	_, err = os.Stat(filepath.Join(dir, "first-run-token"))
	assert.True(t, os.IsNotExist(err), "the token is gone once used")
	assert.False(t, service.FirstRunOpen(ctx, db))

	_, err = service.CreateFirstAdmin(ctx, db, token, "Second", "second@example.com", pw)
	assert.ErrorIs(t, err, service.ErrFirstRunClosed)
	open, err = service.PrepareFirstRun(ctx, db)
	require.NoError(t, err)
	assert.False(t, open, "never reopened while an administrator exists")
}
