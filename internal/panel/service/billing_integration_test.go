package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
)

// BIL-26: a licensed system, or one restricted to plans, is not the
// customer's to pick on any other plan. Billing has priced a licensed system,
// so only a plan list refuses its orders. Staff install anything.
func TestTemplateAvailabilityRules(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	user, node, open := seedVMIPAMDependencies(t, db)
	planA, planB := seedPlan(t, db, true), seedPlan(t, db, true)
	seedPublicPool(t, db, node.ID, "203.0.113.10", "203.0.113.11", "203.0.113.12", "203.0.113.13", "203.0.113.14", "203.0.113.15")
	licensed := &models.OSTemplate{Name: "windows", Version: "2025", ImagePath: "/images/win.qcow2", IsActive: true, Licensed: true}
	require.NoError(t, db.Create(licensed).Error)
	onPlanA := &models.OSTemplate{Name: "windows-a", Version: "2025", ImagePath: "/images/win.qcow2", IsActive: true, Licensed: true, PlanIDs: []string{planA.ID}}
	require.NoError(t, db.Create(onPlanA).Error)
	svc := newPolicyVMService(db)

	n := 0
	order := func(tmpl *models.OSTemplate, planID string, customer, billed bool) error {
		n++
		_, err := svc.CreateVM(ctx, &CreateVMRequest{
			UserID: user.ID.String(), Hostname: "h" + string(rune('a'+n)) + ".example.test", OSTemplateID: tmpl.ID, PlanID: planID,
			PlanRequired: customer, Billed: billed, AutoAssignIP: true, RegeneratePassword: true,
		})
		return err
	}
	require.NoError(t, order(open, planA.ID, true, false), "an ordinary template is open to every plan")
	require.ErrorIs(t, order(licensed, planA.ID, true, false), ErrTemplateNotOffered, "a licensed template on no plan is not a customer's to pick")
	require.NoError(t, order(licensed, planA.ID, false, true), "billing has priced it")
	require.NoError(t, order(licensed, planA.ID, false, false), "staff install anything")
	require.ErrorIs(t, order(onPlanA, planB.ID, true, false), ErrTemplateNotOffered)
	require.ErrorIs(t, order(onPlanA, planB.ID, false, true), ErrTemplateNotOffered, "a plan list binds billing orders too")
	require.NoError(t, order(onPlanA, planA.ID, true, false))
	require.NoError(t, order(onPlanA, planA.ID, false, true))

	// Reinstall: the customer keeps the system they have, and cannot move to a licensed one.
	linux := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "linux.example.test", OSTemplateID: open.ID,
		Resources: models.Resources{CPU: 4, RAM: 8192, Disk: 80}, Status: models.VMStatusStopped}
	require.NoError(t, db.Create(linux).Error)
	// This service has no job queue: a reinstall the rules let through gets as
	// far as queueing and panics there, which is "allowed" for this test.
	rebuild := func(vmID, templateID string, byCustomer bool) (err error) {
		defer func() {
			if recover() != nil {
				err = nil
			}
		}()
		_, err = svc.RebuildVM(ctx, &RebuildVMRequest{VMID: vmID, TemplateID: templateID, ByCustomer: byCustomer})
		return err
	}
	require.ErrorIs(t, rebuild(linux.ID, licensed.ID, true), ErrTemplateNotOffered)
	require.ErrorIs(t, rebuild(linux.ID, onPlanA.ID, true), ErrTemplateNotOffered, "a VM with no plan is on none of the template's plans")
	require.NoError(t, rebuild(linux.ID, licensed.ID, false), "staff may")
	require.NoError(t, db.Table("vms").Where("id = ?", linux.ID).Updates(map[string]any{"os_template_id": open.ID, "plan_id": planA.ID}).Error)
	require.NoError(t, rebuild(linux.ID, onPlanA.ID, true), "the VM's plan includes it")

	windows := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "win.example.test", OSTemplateID: licensed.ID,
		Resources: models.Resources{CPU: 4, RAM: 8192, Disk: 80}, Status: models.VMStatusStopped}
	require.NoError(t, db.Create(windows).Error)
	require.NoError(t, rebuild(windows.ID, licensed.ID, true), "reinstalling the system it already runs is allowed")

	// A copy of a licensed system is one more licence.
	_, err := svc.CloneVM(ctx, &CloneVMRequest{SourceVMID: windows.ID, ByCustomer: true})
	require.ErrorIs(t, err, ErrTemplateNotOffered)
	_, err = svc.CloneVM(ctx, &CloneVMRequest{SourceVMID: windows.ID})
	require.NotErrorIs(t, err, ErrTemplateNotOffered)
}

func billingUserTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupVMIPAMTestDB(t)
	for _, stmt := range []string{
		`CREATE TABLE billing_user_links (external_ref TEXT PRIMARY KEY, user_id TEXT NOT NULL, created_at DATETIME)`,
		`CREATE TABLE password_reset_tokens (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at DATETIME NOT NULL, consumed_at DATETIME, attempt_count INTEGER NOT NULL DEFAULT 0, last_attempt_at DATETIME, created_at DATETIME, updated_at DATETIME)`,
		`CREATE UNIQUE INDEX users_email ON users (email)`,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	return db
}

// BIL-02: the billing system gets a customer account for its client without
// staff creating one: found by its own reference, then by email, else created.
func TestEnsureBillingUser(t *testing.T) {
	db := billingUserTestDB(t)
	ctx := context.Background()
	t.Setenv("PANEL_PUBLIC_URL", "https://panel.example.test/")
	var links []string
	restore := deliverNewAccountLink
	deliverNewAccountLink = func(_ *gorm.DB, to, _, link string) { links = append(links, to+" "+link) }
	t.Cleanup(func() { deliverNewAccountLink = restore })

	user, created, err := EnsureBillingUser(ctx, db, " Dewi@Example.Test ", "Dewi", "whmcs-client-7")
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "dewi@example.test", user.Email)
	require.Equal(t, models.RoleClient, user.Role)
	require.Len(t, links, 1, "the new owner is sent a link to choose a password")
	require.True(t, strings.HasPrefix(links[0], "dewi@example.test https://panel.example.test/reset-password?token="), links[0])
	var quotaRows, tokens int64
	require.NoError(t, db.Table("user_quotas").Where("user_id = ?", user.ID.String()).Count(&quotaRows).Error)
	require.EqualValues(t, 1, quotaRows, "a new account starts with a quota row, like one staff create")
	require.NoError(t, db.Table("password_reset_tokens").Where("user_id = ?", user.ID.String()).Count(&tokens).Error)
	require.EqualValues(t, 1, tokens)
	require.Equal(t, "whmcs-client-7", BillingUserRef(ctx, db, user.ID.String()))

	// The same client again, after changing their email in the billing system.
	again, created, err := EnsureBillingUser(ctx, db, "dewi.new@example.test", "Dewi", "whmcs-client-7")
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, user.ID, again.ID)

	// An account that already exists is found by email, and linked.
	existing, _, _ := seedVMIPAMDependencies(t, db)
	found, created, err := EnsureBillingUser(ctx, db, strings.ToUpper(existing.Email), "", "whmcs-client-8")
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, existing.ID, found.ID)
	require.Equal(t, "whmcs-client-8", BillingUserRef(ctx, db, existing.ID.String()))
	require.Len(t, links, 1, "an existing account is not emailed")

	// A link to an account that is gone is replaced, not followed.
	require.NoError(t, db.Exec("DELETE FROM users WHERE id = ?", user.ID.String()).Error)
	fresh, created, err := EnsureBillingUser(ctx, db, "dewi.new@example.test", "Dewi", "whmcs-client-7")
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, user.ID, fresh.ID)

	admin := &models.User{Email: "ops@example.test", PasswordHash: "hash", Role: models.RoleAdmin}
	require.NoError(t, db.Create(admin).Error)
	_, _, err = EnsureBillingUser(ctx, db, "ops@example.test", "", "whmcs-client-9")
	require.ErrorIs(t, err, ErrBillingStaffAccount)
	require.Empty(t, BillingUserRef(ctx, db, admin.ID.String()), "a refused account is not linked")

	for _, bad := range []string{"", "not-an-address", "Dewi <dewi@example.test>", "a@b.test, c@d.test"} {
		_, _, err = EnsureBillingUser(ctx, db, bad, "", "")
		require.ErrorIs(t, err, ErrBillingUserInvalid, bad)
	}
}

// BIL-20, BIL-09: an event is stored, signed the way the receiver checks it,
// retried while the billing system is away, and its fate is on record.
func TestBillingEventDelivery(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	t.Setenv("BILLING_WEBHOOK_SECRET", "test-signing-secret")
	require.NoError(t, db.Exec(`CREATE TABLE system_settings (section TEXT PRIMARY KEY, data TEXT, updated_at DATETIME)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE webhook_deliveries (id TEXT PRIMARY KEY, event TEXT NOT NULL, body TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending',
		attempts INTEGER NOT NULL DEFAULT 0, response_code INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '', resource_id TEXT NOT NULL DEFAULT '',
		created_at DATETIME, last_attempt_at DATETIME, delivered_at DATETIME)`).Error)
	count := func() (n int64) {
		require.NoError(t, db.Model(&WebhookDelivery{}).Count(&n).Error)
		return n
	}
	only := func() WebhookDelivery {
		var row WebhookDelivery
		require.NoError(t, db.First(&row).Error)
		return row
	}

	EmitBillingEvent(ctx, db, nil, nil, BillingEventFloatingIP, "u1", map[string]any{"billable": 2})
	require.Zero(t, count(), "with no webhook URL there is nobody to tell, and nothing is stored")

	var mu sync.Mutex
	status := http.StatusServiceUnavailable
	var gotSig, gotBody, gotID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		gotSig, gotBody, gotID = r.Header.Get("X-Webhook-Signature"), string(b), r.Header.Get("X-Webhook-Id")
		w.WriteHeader(status)
		_, _ = w.Write([]byte("maintenance"))
	}))
	defer srv.Close()
	require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('api', ?)`, `{"webhookUrl":"`+srv.URL+`"}`).Error)

	EmitBillingEvent(ctx, db, nil, nil, BillingEventFloatingIP, "u1", map[string]any{"billable": 2, "user_id": "u1"})
	row := only()
	require.Equal(t, DeliveryFailed, row.Status, "no queue to deliver it: on record as failed, not lost")
	require.Contains(t, row.LastError, "could not be queued")
	require.Contains(t, row.Body, `"event":"floating_ip.usage"`)
	require.Contains(t, row.Body, `"id":"`+row.ID+`"`)

	// The billing system is down: each try is recorded, the last one for good.
	require.Error(t, DeliverBillingEvent(ctx, db, row.ID, false))
	row = only()
	require.Equal(t, DeliveryRetrying, row.Status)
	require.Equal(t, 1, row.Attempts)
	require.Equal(t, http.StatusServiceUnavailable, row.ResponseCode)
	require.Contains(t, row.LastError, "HTTP 503: maintenance")
	require.Equal(t, SignBillingEvent([]byte(row.Body), "test-signing-secret"), gotSig)
	require.Equal(t, row.Body, gotBody)
	require.Equal(t, row.ID, gotID)

	require.Error(t, DeliverBillingEvent(ctx, db, row.ID, true))
	require.Equal(t, DeliveryFailed, only().Status)

	// It is back: an operator sends the event again, with the same body.
	require.ErrorContains(t, RetryBillingDelivery(ctx, db, nil, row.ID), "queue is not running")
	require.Equal(t, DeliveryPending, only().Status)
	status = http.StatusOK
	require.NoError(t, DeliverBillingEvent(ctx, db, row.ID, false))
	row = only()
	require.Equal(t, DeliveryDelivered, row.Status)
	require.Equal(t, 3, row.Attempts)
	require.Empty(t, row.LastError)
	require.NotNil(t, row.DeliveredAt)
	require.Equal(t, row.Body, gotBody)

	gotBody = ""
	require.NoError(t, DeliverBillingEvent(ctx, db, row.ID, false))
	require.Empty(t, gotBody, "a delivered event is not sent twice")
	require.ErrorIs(t, RetryBillingDelivery(ctx, db, nil, row.ID), ErrDeliveryNotFound)
	require.NoError(t, DeliverBillingEvent(ctx, db, "missing", false))

	rows, total, err := ListBillingDeliveries(ctx, db, DeliveryDelivered, 10, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Len(t, rows, 1)

	// A VM event says whose VM it is, which service pays for it and how it is reached.
	user, node, tmpl := seedVMIPAMDependencies(t, db)
	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "shop.example.test", OSTemplateID: tmpl.ID, Status: models.VMStatusRunning}
	require.NoError(t, db.Create(vm).Error)
	require.NoError(t, db.Table("vms").Where("id = ?", vm.ID).Update("billing_ref", "whmcs-41").Error)
	require.NoError(t, db.Create(&models.Network{VMID: vm.ID, IPAddress: "203.0.113.10"}).Error)
	start, create := []byte(`{"vm_id":"`+vm.ID+`","operation":"start"}`), []byte(`{"vm_id":"`+vm.ID+`","operation":"create"}`)
	failures := func() (n int64) {
		require.NoError(t, db.Model(&WebhookDelivery{}).Where("event = ?", BillingEventVMFailed).Count(&n).Error)
		return n
	}
	BillingJobFailed(ctx, db, nil, "vm_operation", start, "boom", false)
	require.Zero(t, failures(), "only a failed creation is the billing system's business")

	// A try the queue will repeat: said once, and only when the VM shows as failed.
	BillingJobFailed(ctx, db, nil, "vm_operation", create, "the node did not answer", true)
	require.Zero(t, failures(), "a try lost to an unreachable node is not a failed installation")
	require.NoError(t, db.Table("vms").Where("id = ?", vm.ID).Update("status", models.VMStatusError).Error)
	BillingJobFailed(ctx, db, nil, "vm_operation", create, "the node ran out of disk", true)
	BillingJobFailed(ctx, db, nil, "vm_operation", create, "the node ran out of disk", true)
	require.EqualValues(t, 1, failures(), "the first failure is told at once, the retries after it are not")
	var failed WebhookDelivery
	require.NoError(t, db.Where("event = ?", BillingEventVMFailed).First(&failed).Error)
	for _, want := range []string{`"external_ref":"whmcs-41"`, `"ip":"203.0.113.10"`, `"username":"root"`, `"reason":"the node ran out of disk"`, `"retrying":true`, `"user_id":"` + user.ID.String() + `"`} {
		require.Contains(t, failed.Body, want)
	}

	// Every try used: said again, as final.
	BillingJobFailed(ctx, db, nil, "vm_operation", create, "the node ran out of disk", false)
	require.EqualValues(t, 2, failures())
	var final int64
	require.NoError(t, db.Model(&WebhookDelivery{}).Where("event = ? AND body NOT LIKE ?", BillingEventVMFailed, `%"retrying"%`).Count(&final).Error)
	require.EqualValues(t, 1, final)
}

// The receiver in integrations/whmcs asserts the same vector, so the two
// sides cannot drift apart on how an outbound event is signed.
func TestBillingEventSignatureVector(t *testing.T) {
	require.Equal(t, "eaca18e228437956e41580c76c5485a88bb20d9c4e0fdf2da23d55af5ab17799",
		SignBillingEvent([]byte(`{"event":"floating_ip.usage","id":"e1"}`), "test-signing-secret"))
}

// BIL-09: an event exactly as the panel stores and signs it is accepted and
// applied by the WHMCS addon's own code (run against stand-ins for WHMCS).
// Needs the php command; skipped without it.
func TestWHMCSReceiverAcceptsPanelEvents(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php is not installed")
	}
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.Exec(`CREATE TABLE system_settings (section TEXT PRIMARY KEY, data TEXT, updated_at DATETIME)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('api', '{"webhookUrl":"http://billing.example.test/hook","hmacSecret":"panel-own-secret"}')`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE webhook_deliveries (id TEXT PRIMARY KEY, event TEXT NOT NULL, body TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending',
		attempts INTEGER NOT NULL DEFAULT 0, response_code INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '', resource_id TEXT NOT NULL DEFAULT '',
		created_at DATETIME, last_attempt_at DATETIME, delivered_at DATETIME)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE billing_user_links (external_ref TEXT PRIMARY KEY, user_id TEXT NOT NULL, created_at DATETIME)`).Error)
	user, _, _ := seedVMIPAMDependencies(t, db)
	require.NoError(t, db.Exec(`INSERT INTO billing_user_links (external_ref, user_id) VALUES ('whmcs-client-7', ?)`, user.ID.String()).Error)
	newPolicyVMService(db).notifyFloatingIPBilling(ctx, user.ID.String(), "203.0.113.50", "ordered")
	var row WebhookDelivery
	require.NoError(t, db.First(&row).Error)
	require.Contains(t, row.Body, `"user_ref":"whmcs-client-7"`)

	script := `require $argv[1];
use WHMCS\Database\Capsule;
Capsule::table('tbladdonmodules')->insert(['module' => 'maburvm_events', 'setting' => 'webhook_secret', 'value' => 'panel-own-secret']);
Capsule::table('tbladdonmodules')->insert(['module' => 'maburvm_events', 'setting' => 'floating_ip_price', 'value' => '2.50']);
Capsule::table('tblclients')->insert(['id' => 7]);
[$status, $body] = maburvm_events_handle(stream_get_contents(STDIN), $argv[2]);
echo $status, ' ', $body['message'];`
	run := func(signature string) string {
		cmd := exec.Command(php, "-r", script, "--", "../../../integrations/whmcs/tests/bootstrap.php", signature)
		cmd.Stdin = strings.NewReader(row.Body)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return string(out)
	}
	// The account holds no floating address here, so the count sent is zero:
	// what matters is that the event was accepted, matched to its client and understood.
	require.Equal(t, "200 client #7 has no billable floating IP addresses; the recurring charge is removed",
		run(SignBillingEvent([]byte(row.Body), "panel-own-secret")))
	require.True(t, strings.HasPrefix(run(SignBillingEvent([]byte(row.Body), "another-secret")), "401 "))
}
