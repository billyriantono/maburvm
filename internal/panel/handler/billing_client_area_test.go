package handler

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/shared/models"
)

// BIL-08, BIL-14, BIL-17: what the billing modules' client areas and usage
// syncs ask for, and naming a VM by its service reference.
func TestBillingClientAreaEvents(t *testing.T) {
	db := billingIntegrationDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE vm_metrics (id INTEGER PRIMARY KEY AUTOINCREMENT, vm_id TEXT NOT NULL, cpu_usage REAL NOT NULL DEFAULT 0,
		memory_usage REAL NOT NULL DEFAULT 0, memory_used_bytes INTEGER NOT NULL DEFAULT 0, disk_read_bytes_per_sec INTEGER NOT NULL DEFAULT 0,
		disk_write_bytes_per_sec INTEGER NOT NULL DEFAULT 0, network_rx_bytes_per_sec INTEGER NOT NULL DEFAULT 0, network_tx_bytes_per_sec INTEGER NOT NULL DEFAULT 0,
		recorded_at DATETIME NOT NULL)`).Error)
	h := newBillingTestHandler(t, db)
	owner, stranger := seedUser(t, db, models.RoleClient), seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)
	require.NoError(t, db.Table("vms").Where("id = ?", vmID).Updates(map[string]any{
		"status": models.VMStatusRunning, "billing_ref": "fossbilling-77", "resources": `{"cpu":1,"ram":1024,"disk":25}`}).Error)
	require.NoError(t, db.Create(&models.Network{VMID: vmID, IPAddress: "203.0.113.10"}).Error)
	require.NoError(t, db.Create(&models.Network{VMID: vmID, IPAddress: "203.0.113.11"}).Error)
	now := time.Now() // SQLite compares times as text: match the zone the panel queries in
	for i, cpu := range []float64{10, 30} {
		require.NoError(t, db.Create(&models.VMMetricSample{VMID: vmID, CPUUsage: cpu, MemoryUsage: 50,
			NetworkRxBytesPerSec: 1000, RecordedAt: now.Add(-time.Duration(2-i) * time.Hour)}).Error)
	}

	// Usage: every figure is reported, as a string in metadata.
	code, resp := sendBilling(t, h, EventVMUsage, WebhookData{VMID: vmID})
	require.Equal(t, http.StatusOK, code, resp.Message)
	for _, key := range []string{"bandwidth_used_mb", "bandwidth_limit_mb", "disk_limit_mb", "disk_allocated_mb", "snapshot_count",
		"backup_count", "backup_size_mb", "extra_ips", "floating_ips", "uptime_hours", "uptime_since"} {
		assert.Contains(t, resp.Metadata, key)
	}
	assert.Equal(t, "25600", resp.Metadata["disk_limit_mb"])
	assert.Equal(t, "1", resp.Metadata["extra_ips"], "two addresses: one beyond the primary")
	assert.Equal(t, "2", resp.Metadata["uptime_hours"], "two clock hours seen running")

	// Graphs: the readings, for the owner only.
	code, resp = sendBilling(t, h, EventVMMetrics, WebhookData{VMID: vmID, UserID: owner, Hours: 6})
	require.Equal(t, http.StatusOK, code, resp.Message)
	assert.Equal(t, "6", resp.Metadata["hours"])
	points := resp.Data.(map[string]any)["points"].([]any)
	require.NotEmpty(t, points)
	assert.Contains(t, points[0], "cpu_percent")
	code, _ = sendBilling(t, h, EventVMMetrics, WebhookData{VMID: vmID, UserID: stranger})
	assert.Equal(t, http.StatusForbidden, code)
	code, _ = sendBilling(t, h, EventVMMetrics, WebhookData{VMID: vmID, Hours: 500})
	assert.Equal(t, http.StatusBadRequest, code)

	// Reinstall: refused while the VM runs, and for someone else's VM.
	code, resp = sendBilling(t, h, EventVMRebuild, WebhookData{VMID: vmID, UserID: owner})
	assert.Equal(t, http.StatusConflict, code)
	assert.Contains(t, resp.Message, "stop it first")
	code, _ = sendBilling(t, h, EventVMRebuild, WebhookData{VMID: vmID, UserID: stranger})
	assert.Equal(t, http.StatusForbidden, code)
	code, resp = sendBilling(t, h, EventVMRebuild, WebhookData{VMID: vmID, UserID: owner, Password: "short"})
	assert.Equal(t, http.StatusBadRequest, code, resp.Message)

	// A VM named by its service reference, for billing systems with nowhere
	// to keep the VM's ID: only together with its owner.
	code, resp = sendBilling(t, h, EventVMStatus, WebhookData{ExternalRef: "fossbilling-77", UserID: owner})
	require.Equal(t, http.StatusOK, code, resp.Message)
	assert.Equal(t, vmID, resp.VMID)
	code, _ = sendBilling(t, h, EventVMStatus, WebhookData{ExternalRef: "fossbilling-77", UserID: stranger})
	assert.Equal(t, http.StatusNotFound, code, "another user's reference names nothing")
	code, _ = sendBilling(t, h, EventVMStatus, WebhookData{ExternalRef: "fossbilling-77"})
	assert.Equal(t, http.StatusNotFound, code, "a reference alone never picks a VM")
	second := seedVM(t, db, owner)
	require.NoError(t, db.Table("vms").Where("id = ?", second).Update("billing_ref", "fossbilling-77").Error)
	code, _ = sendBilling(t, h, EventVMStatus, WebhookData{ExternalRef: "fossbilling-77", UserID: owner})
	assert.Equal(t, http.StatusConflict, code, "two VMs on one reference: the module must send vm_id")
}

// The console link lands on the VM's console page; anything else on the VM.
func TestBillingSSOLanding(t *testing.T) {
	const vm = "7d1c0a52-1f7e-4c55-9a4d-0f0e5b6f3c21"
	assert.Equal(t, "/client/vms/"+vm+"/console", billingSSOLanding(vm, "console"))
	assert.Equal(t, "/client/vms/"+vm, billingSSOLanding(vm, "graphs"))
	assert.Equal(t, "/client/dashboard", billingSSOLanding("", "console"))
	link, err := billingSSOLink([16]byte{1}, vm, "console", time.Now())
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(link, billingSSOPath+"?token="))
}
