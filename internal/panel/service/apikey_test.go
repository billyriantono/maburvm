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
		is_active BOOLEAN DEFAULT 1, created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
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
	key, token, err := svc.CreateAPIKey(ctx, userID, CreateAPIKeyRequest{Name: "ci-bot"})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(token, models.APITokenPrefix), "token must carry mvk_ prefix")
	require.Equal(t, token[:12], key.Prefix, "display prefix is the token head")
	require.NotEmpty(t, key.KeyHash, "hash is stored")
	require.NotEqual(t, token, key.KeyHash, "plaintext token is never persisted")

	// Empty name rejected.
	_, _, err = svc.CreateAPIKey(ctx, userID, CreateAPIKeyRequest{Name: "   "})
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
	_, token, err := svc.CreateAPIKey(ctx, userID, CreateAPIKeyRequest{Name: "expired", ExpiresAt: &past})
	require.NoError(t, err)

	_, err = svc.Authenticate(ctx, token)
	require.ErrorIs(t, err, ErrAPIKeyInvalid, "expired key must not authenticate")
}
