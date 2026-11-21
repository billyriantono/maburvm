package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/shared/models"
)

// ADM-30: an operator's webhooks get the events they chose, each signed with
// its own secret and logged apart from the billing system's; a test is sent
// at once, and removing a webhook removes its log.
func TestOutboundWebhooks(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.Exec(`CREATE TABLE system_settings (section TEXT PRIMARY KEY, data TEXT, updated_at DATETIME)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE webhook_deliveries (id TEXT PRIMARY KEY, event TEXT NOT NULL, body TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending',
		attempts INTEGER NOT NULL DEFAULT 0, response_code INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '', resource_id TEXT NOT NULL DEFAULT '',
		created_at DATETIME, last_attempt_at DATETIME, delivered_at DATETIME, endpoint_id TEXT)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE webhook_endpoints (id TEXT PRIMARY KEY, name TEXT NOT NULL, url TEXT NOT NULL, secret TEXT NOT NULL DEFAULT '',
		events TEXT NOT NULL DEFAULT '[]', enabled BOOLEAN NOT NULL DEFAULT 1, created_at DATETIME, updated_at DATETIME)`).Error)

	var mu sync.Mutex
	var gotSig, gotBody, gotEvent string
	status := http.StatusNoContent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		gotSig, gotBody, gotEvent = r.Header.Get("X-Webhook-Signature"), string(b), r.Header.Get("X-Webhook-Event")
		w.WriteHeader(status)
	}))
	defer srv.Close()

	secret := "an-operator-secret-of-32-characters!"
	short := "short"
	bad := []WebhookEndpointRequest{
		{Name: "", URL: srv.URL, Events: []string{EventBackupFailed}},
		{Name: "x", URL: "ftp://example.test", Events: []string{EventBackupFailed}},
		{Name: "x", URL: srv.URL, Events: []string{"vm.exploded"}},
		{Name: "x", URL: srv.URL},
		{Name: "x", URL: srv.URL, Events: []string{EventBackupFailed}, Secret: &short},
	}
	for _, req := range bad {
		_, err := CreateWebhookEndpoint(ctx, db, req)
		require.ErrorIs(t, err, ErrWebhookEndpointInvalid, "%+v", req)
	}

	ops, err := CreateWebhookEndpoint(ctx, db, WebhookEndpointRequest{Name: "ops", URL: srv.URL, Secret: &secret,
		Events: []string{EventBackupFailed, EventUserCreated, EventBackupFailed}})
	require.NoError(t, err)
	require.True(t, ops.Enabled)
	require.True(t, ops.HasSecret)
	require.Equal(t, []string{EventBackupFailed, EventUserCreated}, ops.Events, "duplicates dropped, order kept")
	everything, err := CreateWebhookEndpoint(ctx, db, WebhookEndpointRequest{Name: "all", URL: srv.URL, Events: []string{WebhookAllEvents}})
	require.NoError(t, err)

	byEndpoint := func(id string) []WebhookDelivery {
		rows, _, err := ListEndpointDeliveries(ctx, db, id, "", 50, 0)
		require.NoError(t, err)
		return rows
	}

	// Fan-out: only the subscribers get an event; with no queue each is on
	// record as failed to queue rather than lost.
	EmitEvent(ctx, db, EventUserCreated, "u1", map[string]any{"user_id": "u1"})
	EmitEvent(ctx, db, EventAlertRaised, "a1", map[string]any{"title": "node down"})
	require.Len(t, byEndpoint(ops.ID), 1)
	require.Equal(t, EventUserCreated, byEndpoint(ops.ID)[0].Event)
	require.Len(t, byEndpoint(everything.ID), 2)
	require.Contains(t, byEndpoint(ops.ID)[0].LastError, "could not be queued")

	// A billing event reaches the subscribed webhooks even with no billing URL,
	// and stays out of the billing log.
	EmitBillingEvent(ctx, db, nil, nil, BillingEventFloatingIP, "u1", map[string]any{"billable": 1})
	billing, total, err := ListBillingDeliveries(ctx, db, "", 50, 0)
	require.NoError(t, err)
	require.Zero(t, total)
	require.Empty(t, billing)
	require.Len(t, byEndpoint(everything.ID), 3)

	// A delivery goes to its own webhook, signed with that webhook's secret.
	row := byEndpoint(ops.ID)[0]
	require.ErrorIs(t, RetryEndpointDelivery(ctx, db, nil, everything.ID, row.ID), ErrDeliveryNotFound,
		"a retry through another webhook finds nothing")
	require.NoError(t, DeliverBillingEvent(ctx, db, row.ID, false))
	mu.Lock()
	require.Equal(t, SignBillingEvent([]byte(row.Body), secret), gotSig)
	require.Equal(t, row.Body, gotBody)
	require.Equal(t, EventUserCreated, gotEvent)
	mu.Unlock()
	require.Equal(t, DeliveryDelivered, byEndpoint(ops.ID)[0].Status)

	// The test button: sent now, once, even while switched off, and logged.
	off := false
	_, err = UpdateWebhookEndpoint(ctx, db, ops.ID, WebhookEndpointRequest{Name: "ops", URL: srv.URL, Events: ops.Events, Enabled: &off})
	require.NoError(t, err)
	stored, err := GetWebhookEndpoint(ctx, db, ops.ID)
	require.NoError(t, err)
	require.False(t, stored.Enabled)
	require.True(t, stored.HasSecret, "leaving the secret out keeps it")
	mu.Lock()
	status = http.StatusTeapot
	mu.Unlock()
	d, err := SendTestEvent(ctx, db, ops.ID)
	require.NoError(t, err)
	require.Equal(t, DeliveryFailed, d.Status)
	require.Equal(t, http.StatusTeapot, d.ResponseCode)
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(d.Body), &body))
	require.Equal(t, EventWebhookTest, body["event"])

	// Switched off: new events skip it, and an old one fails for good.
	EmitEvent(ctx, db, EventUserCreated, "u2", nil)
	require.Len(t, byEndpoint(ops.ID), 2, "the delivered event and the test")

	require.NoError(t, DeleteWebhookEndpoint(ctx, db, ops.ID))
	require.ErrorIs(t, DeleteWebhookEndpoint(ctx, db, ops.ID), ErrWebhookEndpointNotFound)
	var left int64
	require.NoError(t, db.Model(&WebhookDelivery{}).Where("endpoint_id = ?", ops.ID).Count(&left).Error)
	require.Zero(t, left, "its log went with it")
}

// BIL-14: the usage report counts what the VM holds beyond its traffic.
func TestVMUsageReport(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	user, node, _ := seedVMIPAMDependencies(t, db)
	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "usage", Resources: models.Resources{CPU: 1, RAM: 1024, Disk: 20}}
	require.NoError(t, db.Create(vm).Error)

	r := VMUsage(ctx, db, vm)
	require.Equal(t, int64(20*1024), r.DiskLimitMB)
	require.Equal(t, r.DiskLimitMB, r.DiskAllocatedMB)
	require.Zero(t, r.ExtraIPs)
	require.Zero(t, r.UptimeHours)
	require.False(t, r.UptimeSince.IsZero())
}
