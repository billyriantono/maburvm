package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	keyOld = "0123456789abcdef0123456789abcdef"
	keyNew = "fedcba9876543210fedcba9876543210"
)

func secretsDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	for _, ddl := range []string{
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, token TEXT)`,
		`CREATE TABLE vms (id TEXT PRIMARY KEY, vnc_password TEXT)`,
		`CREATE TABLE import_sources (id TEXT PRIMARY KEY, api_key TEXT, api_pass TEXT)`,
		`CREATE TABLE system_settings (section TEXT PRIMARY KEY, data TEXT)`,
	} {
		require.NoError(t, db.Exec(ddl).Error)
	}
	return db
}

func rawCol(t *testing.T, db *gorm.DB, q string) string {
	t.Helper()
	var v string
	require.NoError(t, db.Raw(q).Scan(&v).Error)
	return v
}

func TestSealSecretsAtRestUpgradesThenRotates(t *testing.T) {
	defer models.SetSecretKeys("")
	db := secretsDB(t)
	require.NoError(t, db.Exec(`INSERT INTO nodes VALUES ('n1', 'agent-token'), ('n2', '')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO vms VALUES ('v1', 'vncpass1'), ('v2', NULL)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO system_settings VALUES ('email', '{"smtpHost":"mail.example","smtpPassword":"hunter2"}'),
		('api', '{"hmacSecret":"h-secret","abuseipdbToken":"","enableApi":true}')`).Error)

	// Upgrade: plaintext from before encryption at rest.
	models.SetSecretKeys(keyOld)
	n, err := sealSecretsAtRest(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, 4, n, "token, vnc password, smtp password, hmac secret")

	token := rawCol(t, db, `SELECT token FROM nodes WHERE id = 'n1'`)
	require.True(t, models.IsSealed(token))
	require.Equal(t, "", rawCol(t, db, `SELECT token FROM nodes WHERE id = 'n2'`))
	var email map[string]any
	require.NoError(t, json.Unmarshal([]byte(rawCol(t, db, `SELECT data FROM system_settings WHERE section = 'email'`)), &email))
	require.True(t, models.IsSealed(email["smtpPassword"].(string)))
	require.Equal(t, "mail.example", email["smtpHost"], "other fields survive")
	plain, err := models.OpenSecret(email["smtpPassword"].(string))
	require.NoError(t, err)
	require.Equal(t, "hunter2", plain)

	// Idempotent: nothing is encrypted twice.
	n, err = sealSecretsAtRest(context.Background(), db)
	require.NoError(t, err)
	require.Zero(t, n)
	require.Equal(t, token, rawCol(t, db, `SELECT token FROM nodes WHERE id = 'n1'`))

	// Rotation: the old key moves to AES_KEY_PREVIOUS; values follow the new key.
	models.SetSecretKeys(keyNew, keyOld)
	n, err = sealSecretsAtRest(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, 4, n)
	models.SetSecretKeys(keyNew) // the retired key can now be dropped
	got, err := models.OpenSecret(rawCol(t, db, `SELECT token FROM nodes WHERE id = 'n1'`))
	require.NoError(t, err)
	require.Equal(t, "agent-token", got)
	got, err = models.OpenSecret(rawCol(t, db, `SELECT vnc_password FROM vms WHERE id = 'v1'`))
	require.NoError(t, err)
	require.Equal(t, "vncpass1", got)

	// A value no key opens is reported and left as it is.
	models.SetSecretKeys(keyOld)
	before := rawCol(t, db, `SELECT token FROM nodes WHERE id = 'n1'`)
	_, err = sealSecretsAtRest(context.Background(), db)
	require.Error(t, err)
	require.Equal(t, before, rawCol(t, db, `SELECT token FROM nodes WHERE id = 'n1'`))
}

func TestSettingsSaveSealsNewSecrets(t *testing.T) {
	defer models.SetSecretKeys("")
	models.SetSecretKeys(keyOld)
	stored, err := sealSettingSecrets(json.RawMessage(`{"smtpPassword":"hunter2"}`))
	require.NoError(t, err)
	// A blank save keeps the stored (sealed) value and does not seal it again.
	kept, err := sealSettingSecrets(keepSettingSecrets(json.RawMessage(`{"smtpPassword":"","smtpHost":"m"}`), stored))
	require.NoError(t, err)
	var a, b map[string]any
	require.NoError(t, json.Unmarshal(stored, &a))
	require.NoError(t, json.Unmarshal(kept, &b))
	require.True(t, models.IsSealed(a["smtpPassword"].(string)))
	require.Equal(t, a["smtpPassword"], b["smtpPassword"])
	// The masked read never shows the sealed value either.
	require.NotContains(t, string(maskSettingSecrets(kept)), "enc:v1:")
}
