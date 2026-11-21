//go:build pg

package server

import (
	"context"
	"testing"

	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
)

// The boot pass writes system_settings.data (jsonb) from a Go string; prove
// Postgres takes it and keeps the document intact.
func TestSealSecretsAtRestOnPostgres(t *testing.T) {
	defer models.SetSecretKeys("")
	db := pgTestDB(t)
	for _, ddl := range []string{
		`CREATE TABLE nodes (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), token varchar(255) NOT NULL)`,
		`ALTER TABLE vms ADD COLUMN vnc_password varchar(255)`, // pgTestDB has a vms table
		`CREATE TABLE system_settings (section varchar(64) PRIMARY KEY, data jsonb NOT NULL DEFAULT '{}', updated_at timestamptz)`,
		`INSERT INTO nodes (token) VALUES ('0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef')`,
		`INSERT INTO vms (vnc_password) VALUES ('Ab3dEf7h'), (NULL)`,
		`INSERT INTO system_settings (section, data) VALUES ('email', '{"smtpPassword":"hunter2","smtpPort":587}')`,
		`CREATE TABLE import_sources (id uuid PRIMARY KEY DEFAULT gen_random_uuid(), api_key text NOT NULL DEFAULT '', api_pass text NOT NULL DEFAULT '')`,
		`INSERT INTO import_sources (api_key, api_pass) VALUES ('source-key', 'source-pass')`,
	} {
		require.NoError(t, db.Exec(ddl).Error)
	}
	models.SetSecretKeys(keyOld)
	n, err := sealSecretsAtRest(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, 5, n)
	var apiPass string
	require.NoError(t, db.Raw(`SELECT api_pass FROM import_sources`).Scan(&apiPass).Error)
	require.True(t, models.IsSealed(apiPass), "an import source's credentials are sealed too")

	var pass string
	require.NoError(t, db.Raw(`SELECT data->>'smtpPassword' FROM system_settings WHERE section = 'email'`).Scan(&pass).Error)
	plain, err := models.OpenSecret(pass)
	require.NoError(t, err)
	require.Equal(t, "hunter2", plain)
	var port int
	require.NoError(t, db.Raw(`SELECT (data->>'smtpPort')::int FROM system_settings WHERE section = 'email'`).Scan(&port).Error)
	require.Equal(t, 587, port)

	var node models.Node
	require.NoError(t, db.Unscoped().Select("id", "token").First(&node).Error)
	require.Equal(t, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", node.Token)
}
