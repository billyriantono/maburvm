package service

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Turning encryption on makes a key and sends it with backups; rotating makes
// a new one for new backups and keeps the old for restores; off sends no key to
// write with but every key to read with. Keys are stored sealed, and only an
// export shows them.
func TestBackupDestinationEncryptionKeys(t *testing.T) {
	db := newDestinationTestDB(t)
	ctx := context.Background()
	d := NewBackupDestinations(db, nil, testAESKey)
	v := createDest(t, d, validDestination())
	assert.False(t, v.Encrypt)
	store, _, err := d.ObjectStore(ctx, v.ID)
	require.NoError(t, err)
	assert.Nil(t, store.GetEncryption(), "a destination that does not encrypt sends no key")
	assert.Empty(t, store.GetDecryptionKeys())

	on, err := d.SetEncryption(ctx, v.ID, true)
	require.NoError(t, err)
	require.True(t, on.Encrypt)
	require.Equal(t, 1, on.EncryptionKeys)
	first := on.EncryptionKeyID
	store, _, err = d.ObjectStore(ctx, v.ID)
	require.NoError(t, err)
	require.NotNil(t, store.GetEncryption())
	assert.Equal(t, first, store.GetEncryption().GetId())
	assert.Len(t, store.GetEncryption().GetKey(), 32)

	var raw string
	require.NoError(t, db.Raw(`SELECT encryption_keys FROM backup_destinations WHERE id = ?`, v.ID).Scan(&raw).Error)
	assert.NotContains(t, raw, hex.EncodeToString(store.GetEncryption().GetKey()), "the key is stored sealed")

	again, err := d.SetEncryption(ctx, v.ID, true)
	require.NoError(t, err)
	assert.Equal(t, first, again.EncryptionKeyID, "turning it on again keeps the key")

	rotated, err := d.RotateKey(ctx, v.ID)
	require.NoError(t, err)
	require.NotEqual(t, first, rotated.EncryptionKeyID)
	require.Equal(t, 2, rotated.EncryptionKeys)
	store, _, err = d.ObjectStore(ctx, v.ID)
	require.NoError(t, err)
	assert.Equal(t, rotated.EncryptionKeyID, store.GetEncryption().GetId(), "new backups use the new key")
	ids := []string{}
	for _, k := range store.GetDecryptionKeys() {
		ids = append(ids, k.GetId())
	}
	assert.Equal(t, []string{first, rotated.EncryptionKeyID}, ids, "older backups still restore")

	off, err := d.SetEncryption(ctx, v.ID, false)
	require.NoError(t, err)
	assert.False(t, off.Encrypt)
	assert.Equal(t, 2, off.EncryptionKeys)
	store, _, err = d.ObjectStore(ctx, v.ID)
	require.NoError(t, err)
	assert.Nil(t, store.GetEncryption())
	assert.Len(t, store.GetDecryptionKeys(), 2)

	exported, err := d.ExportKeys(ctx, v.ID)
	require.NoError(t, err)
	require.Len(t, exported, 2)
	assert.Equal(t, first, exported[0].ID)
	assert.Len(t, exported[0].Key, 64)
	assert.False(t, exported[1].Current, "off: no key is current")

	// A panel whose AES key changed cannot read the keys: that is an error, not
	// backups quietly sent in the clear.
	other := NewBackupDestinations(db, nil, strings.Repeat("z", 32))
	_, _, err = other.ObjectStore(ctx, v.ID)
	assert.ErrorIs(t, err, ErrBackupDestinationUnreadable)
	_, err = d.RotateKey(ctx, "00000000-0000-4000-8000-000000000009")
	assert.ErrorIs(t, err, ErrBackupDestinationNotFound)
}
