package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupSSHKeyTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:sshkey-%s?mode=memory&cache=shared", t.Name())),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE ssh_keys (
		id TEXT PRIMARY KEY, user_id TEXT NOT NULL, name TEXT NOT NULL,
		public_key TEXT NOT NULL, fingerprint TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, deleted_at DATETIME
	)`).Error)
	return db
}

// newTestPublicKey generates a real ed25519 authorized_keys line for tests.
func newTestPublicKey(t *testing.T, comment string) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	if comment != "" {
		line += " " + comment
	}
	return line
}

func TestSSHKeyServiceCreateAndResolve(t *testing.T) {
	db := setupSSHKeyTestDB(t)
	svc := NewSSHKeyService(db)
	ctx := context.Background()
	const userID = "11111111-1111-1111-1111-111111111111"

	pub := newTestPublicKey(t, "alice@laptop")
	key, err := svc.CreateSSHKey(ctx, userID, CreateSSHKeyRequest{PublicKey: pub})
	require.NoError(t, err)
	require.NotEmpty(t, key.Fingerprint)
	require.Equal(t, "alice@laptop", key.Name, "name should default to the key comment")

	// Duplicate (same fingerprint) is rejected.
	_, err = svc.CreateSSHKey(ctx, userID, CreateSSHKeyRequest{Name: "dup", PublicKey: pub})
	require.ErrorIs(t, err, ErrSSHKeyDuplicate)

	// Invalid key is rejected.
	_, err = svc.CreateSSHKey(ctx, userID, CreateSSHKeyRequest{PublicKey: "not-a-key"})
	require.ErrorIs(t, err, ErrSSHKeyInvalid)

	// Resolve returns the stored line for owned IDs and ignores unknown ones.
	lines, err := svc.ResolvePublicKeys(ctx, userID, []string{key.ID, "00000000-0000-0000-0000-000000000000"})
	require.NoError(t, err)
	require.Len(t, lines, 1)
	require.Contains(t, lines[0], "ssh-ed25519")

	// Another user cannot resolve this user's key.
	other, err := svc.ResolvePublicKeys(ctx, "22222222-2222-2222-2222-222222222222", []string{key.ID})
	require.NoError(t, err)
	require.Empty(t, other)

	// Delete removes it.
	require.NoError(t, svc.DeleteSSHKey(ctx, key.ID, userID))
	remaining, err := svc.ListSSHKeys(ctx, userID)
	require.NoError(t, err)
	require.Empty(t, remaining)
}
