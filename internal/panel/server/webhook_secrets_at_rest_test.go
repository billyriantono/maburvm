package server

import (
	"context"
	"testing"

	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
)

// An outbound webhook's signing secret follows an AES key rotation like every
// other stored credential, so dropping the retired key never loses it.
func TestSealSecretsAtRestCoversWebhookSecrets(t *testing.T) {
	defer models.SetSecretKeys("")
	db := secretsDB(t)
	models.SetSecretKeys(keyOld)
	sealed, err := models.SealSecret("receiver-shared-secret-0123456789abcdef")
	require.NoError(t, err)
	require.NoError(t, db.Exec(`INSERT INTO webhook_endpoints VALUES ('w1', ?), ('w2', '')`, sealed).Error)

	models.SetSecretKeys(keyNew, keyOld)
	n, err := sealSecretsAtRest(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	models.SetSecretKeys(keyNew)
	got, err := models.OpenSecret(rawCol(t, db, `SELECT secret FROM webhook_endpoints WHERE id = 'w1'`))
	require.NoError(t, err)
	require.Equal(t, "receiver-shared-secret-0123456789abcdef", got)
	require.Equal(t, "", rawCol(t, db, `SELECT secret FROM webhook_endpoints WHERE id = 'w2'`))
}
