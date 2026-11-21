package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupAPIKeyTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:apikey-%s?mode=memory&cache=shared", t.Name())),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE api_keys (
		id TEXT PRIMARY KEY, user_id TEXT NOT NULL, name TEXT NOT NULL,
		key_hash TEXT NOT NULL, prefix TEXT NOT NULL,
		last_used_at DATETIME, expires_at DATETIME,
		is_active BOOLEAN DEFAULT 1, role TEXT, read_only BOOLEAN NOT NULL DEFAULT 0, scope TEXT NOT NULL DEFAULT '', allowed_ips TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, deleted_at DATETIME
	)`).Error)
	return db
}

func TestAPIKeyServiceLifecycle(t *testing.T) {
	db := setupAPIKeyTestDB(t)
	svc := NewAPIKeyService(db)
	ctx := context.Background()
	const userID = "11111111-1111-1111-1111-111111111111"

	// Create returns a plaintext token exactly once.
	key, token, err := svc.CreateAPIKey(ctx, userID, models.RoleClient, CreateAPIKeyRequest{Name: "ci-bot"})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(token, models.APITokenPrefix), "token must carry mvk_ prefix")
	require.Equal(t, token[:12], key.Prefix, "display prefix is the token head")
	require.NotEmpty(t, key.KeyHash, "hash is stored")
	require.NotEqual(t, token, key.KeyHash, "plaintext token is never persisted")

	// Empty name rejected.
	_, _, err = svc.CreateAPIKey(ctx, userID, models.RoleClient, CreateAPIKeyRequest{Name: "   "})
	require.Error(t, err)

	// Authenticate accepts the real token and records last-used.
	got, err := svc.Authenticate(ctx, token)
	require.NoError(t, err)
	require.Equal(t, key.ID, got.ID)
	require.NotNil(t, got.LastUsedAt, "last_used_at is recorded on auth")

	// Authenticate rejects unknown / malformed tokens.
	_, err = svc.Authenticate(ctx, token+"tampered")
	require.ErrorIs(t, err, ErrAPIKeyInvalid)
	_, err = svc.Authenticate(ctx, "not-a-maburvm-token")
	require.ErrorIs(t, err, ErrAPIKeyInvalid)

	// List returns the user's keys.
	keys, err := svc.ListAPIKeys(ctx, userID)
	require.NoError(t, err)
	require.Len(t, keys, 1)

	// A different user cannot revoke this key.
	require.ErrorIs(t, svc.RevokeAPIKey(ctx, key.ID, "22222222-2222-2222-2222-222222222222"), ErrAPIKeyNotFound)

	// Owner revokes; the token then fails to authenticate and disappears from the list.
	require.NoError(t, svc.RevokeAPIKey(ctx, key.ID, userID))
	_, err = svc.Authenticate(ctx, token)
	require.ErrorIs(t, err, ErrAPIKeyInvalid)
	keys, err = svc.ListAPIKeys(ctx, userID)
	require.NoError(t, err)
	require.Empty(t, keys)
}

func TestAPIKeyServiceExpiry(t *testing.T) {
	db := setupAPIKeyTestDB(t)
	svc := NewAPIKeyService(db)
	ctx := context.Background()
	const userID = "33333333-3333-3333-3333-333333333333"

	past := time.Now().Add(-time.Hour)
	_, _, err := svc.CreateAPIKey(ctx, userID, models.RoleClient, CreateAPIKeyRequest{Name: "expired", ExpiresAt: &past})
	require.Error(t, err, "a key that would be born expired is refused")

	soon := time.Now().Add(time.Hour)
	key, token, err := svc.CreateAPIKey(ctx, userID, models.RoleClient, CreateAPIKeyRequest{Name: "expiring", ExpiresAt: &soon})
	require.NoError(t, err)
	require.NoError(t, db.Model(&models.APIKey{}).Where("id = ?", key.ID).Update("expires_at", past).Error)

	_, err = svc.Authenticate(ctx, token)
	require.ErrorIs(t, err, ErrAPIKeyInvalid, "expired key must not authenticate")
}

// Scopes and address lists are checked when a key is made, and the older
// read_only flag is the read_only scope.
func TestAPIKeyServiceScopesAndAddresses(t *testing.T) {
	db := setupAPIKeyTestDB(t)
	svc := NewAPIKeyService(db)
	ctx := context.Background()
	const userID = "55555555-5555-5555-5555-555555555555"

	cases := []struct {
		name      string
		req       CreateAPIKeyRequest
		wantScope string
		wantErr   bool
	}{
		{"default is full", CreateAPIKeyRequest{Name: "a"}, models.APIKeyScopeFull, false},
		{"read_only flag", CreateAPIKeyRequest{Name: "b", ReadOnly: true}, models.APIKeyScopeReadOnly, false},
		{"power scope", CreateAPIKeyRequest{Name: "c", Scope: models.APIKeyScopeVMPower}, models.APIKeyScopeVMPower, false},
		{"billing scope", CreateAPIKeyRequest{Name: "d", Scope: models.APIKeyScopeBilling}, models.APIKeyScopeBilling, false},
		{"unknown scope", CreateAPIKeyRequest{Name: "e", Scope: "root"}, "", true},
		{"flag contradicts scope", CreateAPIKeyRequest{Name: "f", ReadOnly: true, Scope: models.APIKeyScopeVMPower}, "", true},
		{"bad address", CreateAPIKeyRequest{Name: "g", AllowedIPs: []string{"10.0.0.0/8", "not-an-ip"}}, "", true},
	}
	for _, tc := range cases {
		key, _, err := svc.CreateAPIKey(ctx, userID, models.RoleClient, tc.req)
		if tc.wantErr {
			require.Error(t, err, tc.name)
			continue
		}
		require.NoError(t, err, tc.name)
		require.Equal(t, tc.wantScope, key.Scope, tc.name)
		require.Equal(t, tc.wantScope == models.APIKeyScopeReadOnly, key.ReadOnly, tc.name)
	}

	key, _, err := svc.CreateAPIKey(ctx, userID, models.RoleClient, CreateAPIKeyRequest{Name: "office", AllowedIPs: []string{" 203.0.113.7 ", "", "2001:db8::/32"}})
	require.NoError(t, err)
	require.Equal(t, []string{"203.0.113.7", "2001:db8::/32"}, key.AllowedIPs)
	keys, err := svc.ListAPIKeys(ctx, userID)
	require.NoError(t, err)
	var stored *models.APIKey
	for i := range keys {
		if keys[i].ID == key.ID {
			stored = &keys[i]
		}
	}
	require.NotNil(t, stored)
	require.Equal(t, key.AllowedIPs, stored.AllowedIPs)
}

func TestAPIKeyServiceRoleCap(t *testing.T) {
	db := setupAPIKeyTestDB(t)
	svc := NewAPIKeyService(db)
	ctx := context.Background()
	const userID = "44444444-4444-4444-4444-444444444444"

	// A client can't grant a key the admin role.
	_, _, err := svc.CreateAPIKey(ctx, userID, models.RoleClient, CreateAPIKeyRequest{Name: "x", Role: models.RoleAdmin})
	require.Error(t, err)

	// An admin can narrow a key to client; asking for its own role stores "inherit".
	k, _, err := svc.CreateAPIKey(ctx, userID, models.RoleAdmin, CreateAPIKeyRequest{Name: "c", Role: models.RoleClient, ReadOnly: true})
	require.NoError(t, err)
	require.Equal(t, models.RoleClient, k.EffectiveRole(models.RoleAdmin))
	require.True(t, k.ReadOnly)
	k, _, err = svc.CreateAPIKey(ctx, userID, models.RoleAdmin, CreateAPIKeyRequest{Name: "a", Role: models.RoleAdmin})
	require.NoError(t, err)
	require.Empty(t, k.Role)
	// If the owner is later demoted, an inheriting key follows.
	require.Equal(t, models.RoleClient, k.EffectiveRole(models.RoleClient))
}
