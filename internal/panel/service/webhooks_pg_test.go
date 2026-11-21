//go:build pg

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/shared/models"
)

// BIL-14 on the real schema: every part of the usage report reads columns
// that exist, so none is silently zero.
func TestVMUsageReportOnPostgres(t *testing.T) {
	db := migrationDB(t)
	const (
		user = "c0000000-0000-0000-0000-000000000001"
		node = "c0000000-0000-0000-0000-000000000002"
		tmpl = "c0000000-0000-0000-0000-000000000003"
		vmID = "c0000000-0000-0000-0000-000000000004"
		pool = "c0000000-0000-0000-0000-000000000005"
	)
	now := time.Now().UTC()
	for _, q := range []string{
		`INSERT INTO users (id, email, password_hash, role) VALUES ('` + user + `', 'usage@example.test', 'x', 'client')`,
		`INSERT INTO nodes (id, name, ip_address, status, token) VALUES ('` + node + `', 'node-1', '192.0.2.10', 'active', 't')`,
		`INSERT INTO os_templates (id, name, version, image_path) VALUES ('` + tmpl + `', 'Debian', '12', '/t.qcow2')`,
		`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id, status, resources) VALUES ('` + vmID + `', '` + user + `', '` + node + `', 'usage', '` + tmpl + `', 'running', '{"cpu":1,"ram":1024,"disk":20}')`,
		`INSERT INTO vm_disks (vm_id, device, size_gb, path) VALUES ('` + vmID + `', 'vdb', 10, '/d/b.qcow2')`,
		`INSERT INTO vm_disks (vm_id, device, size_gb, path, deleted_at) VALUES ('` + vmID + `', 'vdc', 99, '/d/c.qcow2', NOW())`,
		`INSERT INTO snapshots (vm_id, name, disk_path, status) VALUES ('` + vmID + `', 'a', '/s/a', 'completed'), ('` + vmID + `', 'b', '/s/b', 'failed')`,
		`INSERT INTO backups (vm_id, storage_provider, storage_path, status, size) VALUES
			('` + vmID + `', 'local', '/b/1', 'completed', 3145728), ('` + vmID + `', 'local', '/b/2', 'completed', 1048576), ('` + vmID + `', 'local', '/b/3', 'failed', 999999999)`,
		`INSERT INTO networks (vm_id, ip_address) VALUES ('` + vmID + `', '203.0.113.10'), ('` + vmID + `', '203.0.113.11')`,
		`INSERT INTO ip_pools (id, name, cidr, gateway) VALUES ('` + pool + `', 'p', '203.0.113.0/24', '203.0.113.1')`,
		`INSERT INTO ip_addresses (pool_id, address, status, vm_id, delivery_mode) VALUES ('` + pool + `', '203.0.113.50', 'assigned', '` + vmID + `', 'floating')`,
	} {
		require.NoError(t, db.Exec(q).Error, q)
	}
	period := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Exec(`INSERT INTO bandwidth_usages (vm_id, node_id, period_start, period_end, total_bytes, quota_bytes) VALUES (?, ?, ?, ?, ?, ?)`,
		vmID, node, period, period.AddDate(0, 1, 0), int64(5)<<30, int64(100)<<30).Error)
	// Seen running in two different hours of the last day; two samples in one hour count once.
	for _, at := range []time.Time{now.Add(-3 * time.Hour), now.Add(-3*time.Hour + time.Minute), now.Add(-time.Hour)} {
		require.NoError(t, db.Exec(`INSERT INTO vm_metrics (vm_id, recorded_at) VALUES (?, ?)`, vmID, at).Error)
	}

	var vm models.VM
	require.NoError(t, db.First(&vm, "id = ?", vmID).Error)
	r := VMUsage(context.Background(), db, &vm)
	assert.Equal(t, int64(5*1024), r.BandwidthUsedMB)
	assert.Equal(t, int64(100*1024), r.BandwidthLimitMB)
	assert.Equal(t, int64(20*1024), r.DiskLimitMB)
	assert.Equal(t, int64(30*1024), r.DiskAllocatedMB, "a removed data disk is not counted")
	assert.Equal(t, int64(1), r.SnapshotCount)
	assert.Equal(t, int64(2), r.BackupCount)
	assert.Equal(t, int64(4), r.BackupSizeMB)
	assert.Equal(t, int64(1), r.ExtraIPs)
	assert.Equal(t, int64(1), r.FloatingIPs)
	assert.Equal(t, int64(2), r.UptimeHours)
}

// ADM-30 and ADM-29 on the real schema: an endpoint's secret is sealed at
// rest, its events are logged apart from the billing webhook's and go with it,
// and an API key's scope and addresses survive the jsonb round trip.
func TestOutboundWebhooksAndKeyLimitsOnPostgres(t *testing.T) {
	db := migrationDB(t)
	defer models.SetSecretKeys("")
	models.SetSecretKeys("0123456789abcdef0123456789abcdef")
	ctx := context.Background()

	secret := "an-operator-secret-of-32-characters!"
	w, err := CreateWebhookEndpoint(ctx, db, WebhookEndpointRequest{Name: "ops", URL: "https://hooks.example.test/panel",
		Secret: &secret, Events: []string{EventBackupFailed, EventUserCreated}})
	require.NoError(t, err)
	var stored string
	require.NoError(t, db.Raw(`SELECT secret FROM webhook_endpoints WHERE id = ?`, w.ID).Scan(&stored).Error)
	assert.True(t, models.IsSealed(stored), "the secret is encrypted at rest")
	got, err := GetWebhookEndpoint(ctx, db, w.ID)
	require.NoError(t, err)
	assert.Equal(t, secret, got.Secret)
	assert.Equal(t, []string{EventBackupFailed, EventUserCreated}, got.Events)

	EmitEvent(ctx, db, EventUserCreated, "u1", map[string]any{"user_id": "u1"})
	EmitEvent(ctx, db, EventAlertRaised, "a1", nil)
	rows, total, err := ListEndpointDeliveries(ctx, db, w.ID, "", 10, 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	assert.Equal(t, EventUserCreated, rows[0].Event)
	_, billing, err := ListBillingDeliveries(ctx, db, "", 10, 0)
	require.NoError(t, err)
	assert.Zero(t, billing, "an endpoint's event stays out of the billing log")

	require.NoError(t, DeleteWebhookEndpoint(ctx, db, w.ID))
	var left int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM webhook_deliveries`).Scan(&left).Error)
	assert.Zero(t, left)

	const owner = "c0000000-0000-0000-0000-000000000010"
	require.NoError(t, db.Exec(`INSERT INTO users (id, email, password_hash, role) VALUES ('`+owner+`', 'keys@example.test', 'x', 'client')`).Error)
	keys := NewAPIKeyService(db)
	key, token, err := keys.CreateAPIKey(ctx, owner, models.RoleClient, CreateAPIKeyRequest{Name: "power", Scope: models.APIKeyScopeVMPower,
		AllowedIPs: []string{"203.0.113.10", "198.51.100.0/24"}})
	require.NoError(t, err)
	back, err := keys.Authenticate(ctx, token)
	require.NoError(t, err)
	assert.Equal(t, key.ID, back.ID)
	assert.Equal(t, models.APIKeyScopeVMPower, back.Scope)
	assert.Equal(t, []string{"203.0.113.10", "198.51.100.0/24"}, back.AllowedIPs)

	// A key made before scopes reads as what it was.
	require.NoError(t, db.Exec(`INSERT INTO api_keys (user_id, name, key_hash, prefix, read_only) VALUES (?, 'old', 'h-old', 'mvk_old', true)`, owner).Error)
	var old models.APIKey
	require.NoError(t, db.First(&old, "key_hash = 'h-old'").Error)
	assert.Equal(t, models.APIKeyScopeReadOnly, old.Scope)
	assert.Empty(t, old.AllowedIPs)
}
