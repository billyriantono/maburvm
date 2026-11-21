package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// billingIntegrationDB adds what the account, status and catalog events read.
func billingIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newPolicyTestDB(t)
	for _, s := range []string{
		`CREATE UNIQUE INDEX users_email ON users (email)`,
		`CREATE TABLE user_quotas (user_id TEXT PRIMARY KEY, max_vms INTEGER DEFAULT 0, max_vcpu INTEGER DEFAULT 0, max_ram_mb INTEGER DEFAULT 0, max_disk_gb INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, quota_mode TEXT NOT NULL DEFAULT 'legacy', policy_id TEXT, policy_version INTEGER,
			policy_name TEXT, policy_assigned_at DATETIME, policy_assigned_by TEXT, cap_revision_id TEXT)`,
		`CREATE TABLE billing_user_links (external_ref TEXT PRIMARY KEY, user_id TEXT NOT NULL, created_at DATETIME)`,
		`CREATE TABLE os_templates (id TEXT PRIMARY KEY, name TEXT NOT NULL, version TEXT NOT NULL, image_path TEXT NOT NULL, size_bytes BIGINT NOT NULL DEFAULT 0,
			os_type TEXT NOT NULL DEFAULT 'linux', is_active BOOLEAN, description TEXT, licensed BOOLEAN NOT NULL DEFAULT 0, plan_ids TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE plans (id TEXT PRIMARY KEY, name TEXT NOT NULL, cpu INTEGER, ram INTEGER, disk INTEGER, bandwidth_mbps INTEGER DEFAULT 0, data_quota_gb INTEGER DEFAULT 0,
			over_quota_policy TEXT DEFAULT 'throttle', throttle_speed_mbps INTEGER DEFAULT 0, smtp_policy TEXT NOT NULL DEFAULT 'allowed', limits TEXT NOT NULL DEFAULT '{}', description TEXT, is_active BOOLEAN, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE regions (id TEXT PRIMARY KEY, slug TEXT NOT NULL, name TEXT NOT NULL, country TEXT NOT NULL DEFAULT '', enabled BOOLEAN, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE networks (id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, ip_address TEXT NOT NULL, bandwidth_limit INTEGER DEFAULT 0, bandwidth_quota_gb INTEGER DEFAULT 0,
			over_quota_policy TEXT DEFAULT 'throttle', throttle_speed_mbps INTEGER DEFAULT 0, throttled BOOLEAN DEFAULT 0, vlan_id INTEGER, anti_spoofing BOOLEAN DEFAULT 1,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE vm_operations (id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, operation TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'running', current_step INTEGER NOT NULL DEFAULT 0,
			total_steps INTEGER NOT NULL DEFAULT 0, step_label TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', started_at DATETIME, updated_at DATETIME, completed_at DATETIME)`,
	} {
		require.NoError(t, db.Exec(s).Error)
	}
	return db
}

// sendBilling runs one event through the webhook handler and decodes the answer.
func sendBilling(t *testing.T, h *BillingHandler, event WebhookEventType, data WebhookData) (int, WebhookResponse) {
	t.Helper()
	body, _ := json.Marshal(WebhookPayload{Event: event, Timestamp: time.Now(), Data: data})
	req := httptest.NewRequest(http.MethodPost, "/webhooks/billing", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	require.NoError(t, h.HandleWebhook(echo.New().NewContext(req, rec)))
	var resp WebhookResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), rec.Body.String())
	return rec.Code, resp
}

// BIL-02: an order needs no panel user pasted in. The module asks for the
// account of its client and gets the same one every time.
func TestBillingEnsuresACustomerAccount(t *testing.T) {
	db := billingIntegrationDB(t)
	h := newBillingTestHandler(t, db)

	code, resp := sendBilling(t, h, EventUserEnsure, WebhookData{Email: "dewi@example.test", Name: "Dewi", ExternalRef: "whmcs-client-7"})
	require.Equal(t, http.StatusOK, code, resp.Message)
	require.Equal(t, "true", resp.Metadata["created"])
	userID := resp.Metadata["user_id"]
	var user models.User
	require.NoError(t, db.First(&user, "id = ?", userID).Error)
	assert.Equal(t, models.RoleClient, user.Role)
	assert.Equal(t, "Dewi", user.Name)
	assert.Equal(t, []string{"user.create"}, auditActions(t, db))

	code, resp = sendBilling(t, h, EventUserEnsure, WebhookData{Email: "changed@example.test", ExternalRef: "whmcs-client-7"})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, userID, resp.Metadata["user_id"], "the client's own reference finds the account after an email change")
	assert.Equal(t, "false", resp.Metadata["created"])
	assert.Equal(t, []string{"user.create"}, auditActions(t, db), "finding an account is not creating one")

	// The account it returns is one vm.create accepts as the owner.
	vmID := seedVM(t, db, userID)
	require.NoError(t, h.checkEventOwner(httptest.NewRequest(http.MethodPost, "/", nil).Context(), EventVMSuspend, &WebhookData{VMID: vmID, UserID: userID}))

	admin := seedUser(t, db, models.RoleAdmin)
	var adminEmail string
	require.NoError(t, db.Table("users").Where("id = ?", admin).Pluck("email", &adminEmail).Error)
	code, resp = sendBilling(t, h, EventUserEnsure, WebhookData{Email: adminEmail})
	assert.Equal(t, http.StatusConflict, code, "a staff account is never handed to billing")
	assert.Contains(t, resp.Message, "staff account")

	code, _ = sendBilling(t, h, EventUserEnsure, WebhookData{Email: "not-an-address"})
	assert.Equal(t, http.StatusBadRequest, code)
	code, _ = sendBilling(t, h, EventUserEnsure, WebhookData{})
	assert.Equal(t, http.StatusBadRequest, code)
}

// BIL-04: the module can learn that provisioning finished, or failed and why,
// and how the VM is reached.
func TestBillingVMStatus(t *testing.T) {
	db := billingIntegrationDB(t)
	h := newBillingTestHandler(t, db)
	owner, stranger := seedUser(t, db, models.RoleClient), seedUser(t, db, models.RoleClient)
	tmpl := &models.OSTemplate{Name: "windows", Version: "2025", ImagePath: "/images/win.qcow2", OSType: models.OSTypeWindows, IsActive: true}
	require.NoError(t, db.Create(tmpl).Error)
	vmID := seedVM(t, db, owner)
	set := func(status models.VMStatus) {
		require.NoError(t, db.Table("vms").Where("id = ?", vmID).Updates(map[string]any{"status": status, "os_template_id": tmpl.ID}).Error)
	}
	status := func() map[string]string {
		code, resp := sendBilling(t, h, EventVMStatus, WebhookData{VMID: vmID, UserID: owner})
		require.Equal(t, http.StatusOK, code, resp.Message)
		return resp.Metadata
	}

	set(models.VMStatusCreating)
	assert.Equal(t, "provisioning", status()["state"])

	require.NoError(t, db.Create(&models.Network{VMID: vmID, IPAddress: "203.0.113.10"}).Error)
	set(models.VMStatusRunning)
	got := status()
	assert.Equal(t, "ready", got["state"])
	assert.Equal(t, "203.0.113.10", got["ip"])
	assert.Equal(t, "203.0.113.10", got["ips"])
	assert.Equal(t, "Administrator", got["username"])
	assert.NotContains(t, got, "password")

	// A VM in "error": the queue's record of its creation says whether the
	// order failed. (The real query needs PostgreSQL; see the pg test.)
	job := [2]string{"retryable", "the node ran out of disk"}
	restore := service.VMCreateJob
	service.VMCreateJob = func(context.Context, *gorm.DB, string) (string, string) { return job[0], job[1] }
	t.Cleanup(func() { service.VMCreateJob = restore })
	set(models.VMStatusError)
	got = status()
	assert.Equal(t, "failed", got["state"])
	assert.Equal(t, "the node ran out of disk", got["error"])
	assert.Equal(t, "true", got["retrying"], "the panel is still trying")

	job[0] = "discarded"
	got = status()
	assert.Equal(t, "failed", got["state"])
	assert.NotContains(t, got, "retrying")

	// Installed long ago, and a later start or reinstall failed: not a failed order.
	for _, state := range []string{"completed", ""} {
		job = [2]string{state, ""}
		got = status()
		assert.Equal(t, "error", got["state"], "create job %q", state)
		assert.NotContains(t, got, "error")
	}

	set(models.VMStatusStopped)
	assert.Equal(t, "ready", status()["state"], "a VM its owner stopped has still been provisioned")

	code, _ := sendBilling(t, h, EventVMStatus, WebhookData{VMID: vmID, UserID: stranger})
	assert.Equal(t, http.StatusForbidden, code, "a named user must own the VM")
	code, _ = sendBilling(t, h, EventVMStatus, WebhookData{VMID: vmID})
	assert.Equal(t, http.StatusOK, code, "the sync of services that predate account mapping names no user")
}

// BIL-05, BIL-26: the module can offer plans, regions and templates by name,
// and sees which templates are licensed or tied to plans.
func TestBillingCatalog(t *testing.T) {
	db := billingIntegrationDB(t)
	h := newBillingTestHandler(t, db)
	plan := &models.Plan{Name: "small", CPU: 2, RAM: 2048, Disk: 40, BandwidthMbps: 100, DataQuotaGB: 500, IsActive: true}
	require.NoError(t, db.Create(plan).Error)
	require.NoError(t, db.Create(&models.OSTemplate{Name: "debian", Version: "12", ImagePath: "/images/debian.qcow2", IsActive: true}).Error)
	require.NoError(t, db.Create(&models.OSTemplate{Name: "windows", Version: "2025", ImagePath: "/images/win.qcow2", OSType: models.OSTypeWindows,
		IsActive: true, Licensed: true, PlanIDs: []string{plan.ID}}).Error)
	retired := &models.OSTemplate{Name: "centos", Version: "6", ImagePath: "/images/centos.qcow2", IsActive: true}
	require.NoError(t, db.Create(retired).Error)
	require.NoError(t, db.Model(retired).Update("is_active", false).Error)
	require.NoError(t, db.Create(&models.Region{Slug: "jakarta", Name: "Jakarta", Country: "ID", Enabled: true}).Error)

	code, resp := sendBilling(t, h, EventCatalogList, WebhookData{})
	require.Equal(t, http.StatusOK, code, resp.Message)
	raw, _ := json.Marshal(resp.Data)
	var catalog struct {
		Templates []struct {
			Name     string   `json:"name"`
			Licensed bool     `json:"licensed"`
			PlanIDs  []string `json:"plan_ids"`
		} `json:"templates"`
		Plans   []map[string]any `json:"plans"`
		Regions []map[string]any `json:"regions"`
	}
	require.NoError(t, json.Unmarshal(raw, &catalog))
	require.Len(t, catalog.Templates, 2, "a template that is switched off is not offered")
	assert.Equal(t, "debian", catalog.Templates[0].Name)
	assert.Empty(t, catalog.Templates[0].PlanIDs)
	assert.True(t, catalog.Templates[1].Licensed)
	assert.Equal(t, []string{plan.ID}, catalog.Templates[1].PlanIDs)
	require.Len(t, catalog.Plans, 1)
	assert.Equal(t, plan.ID, catalog.Plans[0]["id"])
	require.Len(t, catalog.Regions, 1)
	assert.Equal(t, "jakarta", catalog.Regions[0]["slug"])
}

// BIL-05: an order carries its region and its own speed and quota, and a
// template tied to plans is refused on another plan with a message that says so.
func TestBilledOrderCarriesRegionAndLimits(t *testing.T) {
	req := billedCreateRequest(&WebhookData{UserID: "u", Hostname: "h", TemplateID: "t", PlanID: "p",
		Region: " jakarta ", BandwidthMbps: 250, DataQuotaGB: 2000})
	assert.Equal(t, "jakarta", req.Region)
	assert.Equal(t, 250, req.BandwidthMbps)
	assert.EqualValues(t, 2000, req.DataQuotaGB)
	assert.False(t, req.RegionRequired, "an order that names no region lands in the default one")

	h := &BillingHandler{}
	assert.Error(t, h.validatePayload(&WebhookPayload{Event: EventVMCreate, Data: WebhookData{UserID: "u", Hostname: "h", TemplateID: "t", PlanID: "p", BandwidthMbps: -1}}))
	assert.NoError(t, h.validatePayload(&WebhookPayload{Event: EventCatalogList}))
	assert.True(t, EventVMStatus.aboutVM())
	assert.False(t, EventUserEnsure.aboutVM())
	assert.False(t, EventCatalogList.aboutVM())
}

// The module's own code against the panel's own handler, over HTTP: the
// account, status and catalog answers have the shape the module reads. Needs
// the php command; skipped without it.
func TestWHMCSModuleReadsThePanelsAnswers(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php is not installed")
	}
	db := billingIntegrationDB(t)
	h := newBillingTestHandler(t, db)
	tmpl := &models.OSTemplate{Name: "debian", Version: "12", ImagePath: "/images/debian.qcow2", IsActive: true}
	require.NoError(t, db.Create(tmpl).Error)
	vmID := seedVM(t, db, seedUser(t, db, models.RoleClient))
	require.NoError(t, db.Table("vms").Where("id = ?", vmID).Updates(map[string]any{"status": models.VMStatusRunning, "os_template_id": tmpl.ID}).Error)
	require.NoError(t, db.Create(&models.Network{VMID: vmID, IPAddress: "203.0.113.10"}).Error)
	e := echo.New()
	e.POST("/webhooks/billing", h.HandleWebhook, h.RequireWebhookAuth())
	srv := httptest.NewServer(e)
	defer srv.Close()

	script := `require $argv[1];
$p = ['serversecure' => '', 'serverhostname' => $argv[2], 'serverip' => '', 'serverpassword' => $argv[3], 'serveraccesshash' => $argv[4],
    'serviceid' => 41, 'userid' => 7, 'customfields' => [], 'model' => null,
    'clientsdetails' => ['email' => 'dewi@example.test', 'firstname' => 'Dewi', 'lastname' => 'Lestari']];
$first = maburvm_panelUserId($p, true);
$again = maburvm_panelUserId($p, true);
$status = maburvm_vmStatus($p, $argv[5], '');
$gone = maburvm_vmStatus($p, '00000000-0000-0000-0000-000000000000', '');
echo json_encode(['same' => $first === $again, 'user' => $first, 'state' => $status['state'], 'ip' => $status['ip'],
    'username' => $status['username'], 'gone' => $gone['state'], 'templates' => maburvm_loadTemplates($p),
    'test' => maburvm_TestConnection($p)['success']]);`
	out, err := exec.Command(php, "-r", script, "--", "../../../integrations/whmcs/tests/bootstrap.php",
		strings.TrimPrefix(srv.URL, "http://"), h.apiKey, h.webhookSecret, vmID).CombinedOutput()
	require.NoError(t, err, string(out))
	var got struct {
		Same      bool              `json:"same"`
		User      string            `json:"user"`
		State     string            `json:"state"`
		IP        string            `json:"ip"`
		Username  string            `json:"username"`
		Gone      string            `json:"gone"`
		Templates map[string]string `json:"templates"`
		Test      bool              `json:"test"`
	}
	require.NoError(t, json.Unmarshal(out, &got), string(out))
	assert.True(t, got.Same, "asking twice finds the same account")
	var user models.User
	require.NoError(t, db.First(&user, "id = ?", got.User).Error)
	assert.Equal(t, "dewi@example.test", user.Email)
	assert.Equal(t, "Dewi Lestari", user.Name)
	assert.Equal(t, "whmcs-client-7", service.BillingUserRef(context.Background(), db, got.User))
	assert.Equal(t, "ready", got.State)
	assert.Equal(t, "203.0.113.10", got.IP)
	assert.Equal(t, "root", got.Username)
	assert.Equal(t, "missing", got.Gone)
	assert.Equal(t, map[string]string{tmpl.ID: "debian 12"}, got.Templates)
	assert.True(t, got.Test)
}

// BIL-20: the delivery log is for staff. It names customers and their servers.
func TestBillingDeliveryLogIsStaffOnly(t *testing.T) {
	t.Setenv("JWT_SECRET_KEY", "test-jwt-secret-key-for-billing-delivery-tests")
	t.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	db := billingIntegrationDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE webhook_deliveries (id TEXT PRIMARY KEY, event TEXT NOT NULL, body TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending',
		attempts INTEGER NOT NULL DEFAULT 0, response_code INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '', resource_id TEXT NOT NULL DEFAULT '',
		created_at DATETIME, last_attempt_at DATETIME, delivered_at DATETIME)`).Error)
	failed := &service.WebhookDelivery{ID: "0b0e7f0a-3c59-4d0c-9d5c-6a1f1f0a2b11", Event: service.BillingEventVMCreated, Body: `{"hostname":"shop"}`,
		Status: service.DeliveryFailed, Attempts: 10, LastError: "the billing system answered HTTP 503", CreatedAt: time.Now()}
	require.NoError(t, db.Create(failed).Error)
	e := echo.New()
	RegisterBillingDeliveryRoutes(e, db, nil)

	call := func(method, path string, role models.UserRole) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		if role != "" {
			var user models.User
			require.NoError(t, db.First(&user, "id = ?", seedUser(t, db, role)).Error)
			token, err := middleware.GenerateAccessToken(&user)
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	assert.Equal(t, http.StatusUnauthorized, call(http.MethodGet, "/api/v1/billing/deliveries", "").Code)
	assert.Equal(t, http.StatusForbidden, call(http.MethodGet, "/api/v1/billing/deliveries", models.RoleClient).Code)
	assert.Equal(t, http.StatusForbidden, call(http.MethodPost, "/api/v1/billing/deliveries/"+failed.ID+"/retry", models.RoleClient).Code)

	rec := call(http.MethodGet, "/api/v1/billing/deliveries?status=failed", models.RoleAdmin)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var page struct {
		Success bool `json:"success"`
		Data    struct {
			Deliveries []service.WebhookDelivery `json:"deliveries"`
			Total      int64                     `json:"total"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	assert.True(t, page.Success)
	require.EqualValues(t, 1, page.Data.Total)
	assert.Equal(t, "the billing system answered HTTP 503", page.Data.Deliveries[0].LastError)

	assert.Equal(t, http.StatusNotFound, call(http.MethodPost, "/api/v1/billing/deliveries/not-an-id/retry", models.RoleAdmin).Code)
	// No queue in this test: the row is reset, and the answer says it could not be queued.
	rec = call(http.MethodPost, "/api/v1/billing/deliveries/"+failed.ID+"/retry", models.RoleAdmin)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "queue is not running")
}
