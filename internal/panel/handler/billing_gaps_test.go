package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// newBillingTestHandler is a billing handler on the policy test database, with
// the idempotency table and credentials set.
func newBillingTestHandler(t *testing.T, db *gorm.DB) *BillingHandler {
	t.Helper()
	require.NoError(t, db.Exec(`CREATE TABLE billing_idempotency (idempotency_key TEXT PRIMARY KEY, request_id TEXT NOT NULL,
		event TEXT NOT NULL DEFAULT '', response BLOB NOT NULL, processed_at DATETIME NOT NULL, expires_at DATETIME NOT NULL)`).Error)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &BillingHandler{
		vmService: service.NewVMService(db, repository.NewVMRepository(db), repository.NewNodeRepository(db), repository.NewTemplateRepository(db), nil, log),
		logger:    log, db: db, apiKey: "test-api-key", webhookSecret: "test-signing-secret",
	}
}

// signedRequest is a webhook request as the billing module sends it.
func signedRequest(h *BillingHandler, body []byte) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/billing", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(WebhookAPIKeyHeader, h.apiKey)
	req.Header.Set(WebhookSignatureHeader, "sha256="+h.sign(body))
	return req
}

// BIL-01, BIL-12: an order from billing asks for what a portal order gets,
// and carries its plan and service reference.
func TestBilledCreateRequest(t *testing.T) {
	req := billedCreateRequest(&WebhookData{
		UserID: "u", Hostname: "shop.example.test", TemplateID: "t", ExternalRef: " whmcs-41 ",
		VMSpecs: VMSpecs{CPU: 2, Memory: 2048, Disk: 40},
	})
	assert.True(t, req.AutoAssignIP, "a public address from the node's pools")
	assert.True(t, req.RegeneratePassword, "a root password, generated when the service has none")
	assert.Empty(t, req.Password)
	assert.True(t, req.Billed)
	assert.True(t, req.AnyHostname)
	assert.Equal(t, "whmcs-41", req.BillingRef)
	assert.Equal(t, models.Resources{CPU: 2, RAM: 2048, Disk: 40}, req.Resources)

	req = billedCreateRequest(&WebhookData{Password: "service-password", PlanID: "plan"})
	assert.Equal(t, "service-password", req.Password)
	assert.False(t, req.RegeneratePassword)
	assert.Equal(t, "plan", req.PlanID)

	req = billedCreateRequest(&WebhookData{Password: "short"})
	assert.Empty(t, req.Password, "a password too short to set is replaced, not sent to the guest")
	assert.True(t, req.RegeneratePassword)

	h := &BillingHandler{}
	require.NoError(t, h.validatePayload(&WebhookPayload{Event: EventVMCreate,
		Data: WebhookData{UserID: "u", Hostname: "h", TemplateID: "t", PlanID: "p"}}), "a plan stands in for vm_specs")
	require.NoError(t, h.validatePayload(&WebhookPayload{Event: EventVMResize, Data: WebhookData{VMID: "v", PlanID: "p"}}))
	require.Error(t, h.validatePayload(&WebhookPayload{Event: EventVMCreate, Data: WebhookData{UserID: "u", Hostname: "h", TemplateID: "t"}}))
	require.Error(t, h.validatePayload(&WebhookPayload{Event: EventUserSuspend}))
	require.ErrorContains(t, h.validatePayload(&WebhookPayload{Event: EventVMCreate,
		Data: WebhookData{UserID: "u", Hostname: "h", TemplateID: "t", PlanID: "p", ExternalRef: strings.Repeat("x", 256)}}), "external_ref")
	require.NoError(t, h.validatePayload(&WebhookPayload{Event: EventUserSuspend, Data: WebhookData{UserID: "u"}}))
}

// BIL-11 / SEC-16: with a secret configured, the signed timestamp is required
// and bounded, and a signed request is accepted exactly once.
func TestWebhookReplayProtectionIsMandatory(t *testing.T) {
	h := newBillingTestHandler(t, newPolicyTestDB(t))
	reached := 0
	guarded := h.RequireWebhookAuth()(func(c echo.Context) error {
		reached++
		return c.NoContent(http.StatusOK)
	})
	send := func(req *http.Request) (int, string) {
		rec := httptest.NewRecorder()
		require.NoError(t, guarded(echo.New().NewContext(req, rec)))
		return rec.Code, rec.Body.String()
	}
	body := func(timestamp, nonce string) []byte {
		p := map[string]interface{}{"event": "vm.suspend", "data": map[string]string{"vm_id": "v", "user_id": "u"}}
		if timestamp != "" {
			p["timestamp"] = timestamp
		}
		if nonce != "" {
			p["nonce"] = nonce
		}
		b, _ := json.Marshal(p)
		return b
	}
	now := time.Now().UTC()
	stamp := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }

	code, msg := send(signedRequest(h, body("", "")))
	assert.Equal(t, http.StatusBadRequest, code, "no timestamp in the signed body")
	assert.Contains(t, msg, "timestamp is required")

	// The header alone was enough to pass before; it is not signed.
	req := signedRequest(h, body("", ""))
	req.Header.Set(WebhookTimestampHeader, stamp(0))
	code, _ = send(req)
	assert.Equal(t, http.StatusBadRequest, code)

	for _, d := range []time.Duration{-MaxWebhookAge - time.Minute, MaxWebhookAge + time.Minute} {
		code, msg = send(signedRequest(h, body(stamp(d), "")))
		assert.Equal(t, http.StatusBadRequest, code, "timestamp %s from now", d)
		assert.Contains(t, msg, "clock")
	}
	code, _ = send(signedRequest(h, []byte(`{"event":"vm.suspend","timestamp":"yesterday"}`)))
	assert.Equal(t, http.StatusBadRequest, code)
	assert.Zero(t, reached)

	// A module that sends no nonce still works: the request is new once.
	first := body(stamp(0), "")
	code, _ = send(signedRequest(h, first))
	assert.Equal(t, http.StatusOK, code)
	code, msg = send(signedRequest(h, first))
	assert.Equal(t, http.StatusConflict, code, "the same signed request again is a replay")
	assert.Contains(t, msg, "already received")
	assert.Equal(t, 1, reached)

	// A deliberate repeat in the same second carries a new nonce and passes.
	code, _ = send(signedRequest(h, body(stamp(0), "n-1")))
	assert.Equal(t, http.StatusOK, code)
	code, _ = send(signedRequest(h, body(stamp(0), "n-2")))
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, 3, reached)

	// Tampering, a wrong key and a missing secret are still refused first.
	bad := signedRequest(h, body(stamp(0), "n-3"))
	bad.Header.Set(WebhookSignatureHeader, "sha256="+h.sign([]byte("something else")))
	code, _ = send(bad)
	assert.Equal(t, http.StatusUnauthorized, code)
	bad = signedRequest(h, body(stamp(0), "n-4"))
	bad.Header.Set(WebhookAPIKeyHeader, "wrong")
	code, _ = send(bad)
	assert.Equal(t, http.StatusUnauthorized, code)
	h.webhookSecret = ""
	code, _ = send(signedRequest(h, body(stamp(0), "n-5")))
	assert.Equal(t, http.StatusServiceUnavailable, code)

	// A caller cannot read or occupy the replay rows through the idempotency key.
	h.webhookSecret = "test-signing-secret"
	probe := httptest.NewRequest(http.MethodPost, "/webhooks/billing", bytes.NewReader(first))
	probe.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	probe.Header.Set(IdempotencyKeyHeader, replayKeyPrefix+h.sign(first))
	rec := httptest.NewRecorder()
	require.NoError(t, h.HandleWebhook(echo.New().NewContext(probe, rec)))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// BIL-10: Terminate then Create on the same service the same day is a new
// order. The stored result of the first create is not replayed once its VM
// has been destroyed.
func TestRecreateAfterDestroyIsANewOrder(t *testing.T) {
	db := newPolicyTestDB(t)
	h := newBillingTestHandler(t, db)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)
	const key = "whmcs-create-41"
	ctx := httptest.NewRequest(http.MethodPost, "/", nil).Context()
	h.storeIdempotency(ctx, key, string(EventVMCreate), &IdempotencyRecord{
		RequestID: uuid.NewString(), ProcessedAt: time.Now().UTC(),
		Response: WebhookResponse{Success: true, Message: "VM created successfully", VMID: vmID},
	})
	// A create that cannot get as far as placing a VM: what matters is whether
	// it is answered from the store or treated as an order.
	create := func() (int, WebhookResponse) {
		body, _ := json.Marshal(WebhookPayload{Event: EventVMCreate, Timestamp: time.Now(),
			Data: WebhookData{UserID: owner, Hostname: "shop.example.test", TemplateID: uuid.NewString(), PlanID: uuid.NewString()}})
		req := httptest.NewRequest(http.MethodPost, "/webhooks/billing", bytes.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		req.Header.Set(IdempotencyKeyHeader, key)
		rec := httptest.NewRecorder()
		require.NoError(t, h.HandleWebhook(echo.New().NewContext(req, rec)))
		var resp WebhookResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		return rec.Code, resp
	}
	stored := func() int64 {
		var n int64
		require.NoError(t, db.Model(&billingIdempotencyRow{}).Where("idempotency_key = ?", key).Count(&n).Error)
		return n
	}

	code, resp := create()
	assert.Equal(t, http.StatusOK, code, "a retry while the VM exists gets the first answer")
	assert.Equal(t, vmID, resp.VMID)

	// Being destroyed counts as gone, as does destroyed.
	for _, destroy := range []func(){
		func() { // suspended for non-payment, then terminated
			require.NoError(t, db.Table("vms").Where("id = ?", vmID).
				Updates(map[string]interface{}{"status": models.VMStatusDeleting, "suspended": true}).Error)
		},
		func() { require.NoError(t, db.Delete(&models.VM{}, "id = ?", vmID).Error) },
	} {
		destroy()
		code, resp = create()
		assert.NotEqual(t, http.StatusOK, code, "the order is processed, not answered from the store")
		assert.Empty(t, resp.VMID, "and never with the VM that is gone")
		assert.Zero(t, stored(), "the stale result is dropped")
		h.storeIdempotency(ctx, key, string(EventVMCreate), &IdempotencyRecord{
			RequestID: uuid.NewString(), ProcessedAt: time.Now().UTC(), Response: WebhookResponse{Success: true, VMID: vmID},
		})
	}
}

// BIL-12 with BIL-25: once a VM carries its billing service, an event from a
// different service of the same customer is refused and audited.
func TestBillingEventMustComeFromTheVMsService(t *testing.T) {
	db := newPolicyTestDB(t)
	h := newBillingTestHandler(t, db)
	owner := seedUser(t, db, models.RoleClient)
	linked, unlinked := seedVM(t, db, owner), seedVM(t, db, owner)
	require.NoError(t, db.Table("vms").Where("id = ?", linked).Update("billing_ref", "whmcs-41").Error)
	ctx := httptest.NewRequest(http.MethodPost, "/", nil).Context()
	check := func(vmID, ref string) error {
		return h.checkEventOwner(ctx, EventVMDestroy, &WebhookData{VMID: vmID, UserID: owner, ExternalRef: ref})
	}

	require.NoError(t, check(linked, "whmcs-41"))
	require.NoError(t, check(linked, " whmcs-41 "))
	require.ErrorIs(t, check(linked, "whmcs-42"), errBillingServiceMismatch)
	require.NoError(t, check(linked, ""), "an event that names no service is held to the owner check alone")
	require.NoError(t, check(unlinked, "whmcs-42"), "a VM with no service on record is held to the owner check alone")

	body, _ := json.Marshal(WebhookPayload{Event: EventVMDestroy, Timestamp: time.Now(),
		Data: WebhookData{VMID: linked, UserID: owner, ExternalRef: "whmcs-42"}})
	req := httptest.NewRequest(http.MethodPost, "/webhooks/billing", bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	require.NoError(t, h.HandleWebhook(echo.New().NewContext(req, rec)))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "different billing service")
	assert.Equal(t, []string{"billing.service_mismatch"}, auditActions(t, db))
	var vm models.VM
	require.NoError(t, db.First(&vm, "id = ?", linked).Error, "the VM is still there")
}

// BIL-21: the billing system can lock a customer account and let it back in.
func TestBillingSuspendsAndRestoresAnAccount(t *testing.T) {
	db := newPolicyTestDB(t)
	h := newBillingTestHandler(t, db)
	customer := seedUser(t, db, models.RoleClient)
	admin := seedUser(t, db, models.RoleAdmin)
	send := func(event WebhookEventType, userID string) int {
		body, _ := json.Marshal(WebhookPayload{Event: event, Timestamp: time.Now(), Data: WebhookData{UserID: userID, ExternalRef: "client-9"}})
		req := httptest.NewRequest(http.MethodPost, "/webhooks/billing", bytes.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		require.NoError(t, h.HandleWebhook(echo.New().NewContext(req, rec)))
		return rec.Code
	}
	suspended := func() bool {
		var u models.User
		require.NoError(t, db.First(&u, "id = ?", customer).Error)
		return u.Suspended()
	}

	assert.Equal(t, http.StatusOK, send(EventUserSuspend, customer))
	assert.True(t, suspended())
	assert.Equal(t, http.StatusOK, send(EventUserUnsuspend, customer))
	assert.False(t, suspended())
	assert.Equal(t, http.StatusBadRequest, send(EventUserSuspend, admin), "an administrator is not suspended by billing")
	assert.Equal(t, http.StatusNotFound, send(EventUserSuspend, uuid.NewString()))
	assert.Equal(t, http.StatusNotFound, send(EventUserSuspend, "41"), "a billing client number is not a panel user")
	assert.Equal(t, []string{"user.suspend", "user.unsuspend"}, auditActions(t, db))
}

// CLI-21: the owner's delete of a billing-managed VM is refused with a reason
// they can act on.
func TestOwnerDeleteOfBillingManagedVMIsRefused(t *testing.T) {
	db := newPolicyTestDB(t)
	h := newTestVMHandler(t, db)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)
	require.NoError(t, db.Table("vms").Where("id = ?", vmID).Update("billing_ref", "whmcs-41").Error)

	c, rec := reqAs(t, http.MethodDelete, owner, models.RoleClient, nil)
	c.SetParamNames("id")
	c.SetParamValues(vmID)
	require.NoError(t, h.DeleteVM(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "billing_managed")
	var vm models.VM
	require.NoError(t, db.First(&vm, "id = ?", vmID).Error, "the VM is still there")
	assert.NotEqual(t, models.VMStatusDeleting, vm.Status)
}

// The billing module in integrations/whmcs and the panel must agree on the
// signature, the timestamp and the nonce. This sends real requests from the
// module's own code; it needs the php command and is skipped without it.
func TestWHMCSModuleRequestsPassWebhookAuth(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php is not installed")
	}
	h := newBillingTestHandler(t, newPolicyTestDB(t))
	e := echo.New()
	var nonces []string
	e.POST("/webhooks/billing", func(c echo.Context) error {
		var p WebhookPayload
		if err := c.Bind(&p); err != nil {
			return err
		}
		nonces = append(nonces, p.Nonce)
		return c.JSON(http.StatusOK, WebhookResponse{Success: true})
	}, h.RequireWebhookAuth())
	srv := httptest.NewServer(e)
	defer srv.Close()

	// Two identical events in the same second, as a double click sends them.
	script := `define('WHMCS', true);
require $argv[1];
$p = ['serversecure' => '', 'serverhostname' => $argv[2], 'serverip' => '', 'serverpassword' => $argv[3], 'serveraccesshash' => $argv[4]];
$codes = [];
foreach ([1, 2] as $i) {
    list($code, $resp) = maburvm_sendEvent($p, 'vm.usage', ['vm_id' => 'v'], 'usage');
    $codes[] = $code . ':' . (empty($resp['success']) ? ($resp['message'] ?? 'refused') : 'ok');
}
echo implode(' ', $codes);`
	out, err := exec.Command(php, "-r", script, "--", "../../../integrations/whmcs/modules/servers/maburvm/maburvm.php",
		strings.TrimPrefix(srv.URL, "http://"), h.apiKey, h.webhookSecret).CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Equal(t, "200:ok 200:ok", string(out))
	require.Len(t, nonces, 2)
	assert.NotEmpty(t, nonces[0])
	assert.NotEqual(t, nonces[0], nonces[1], "each request carries a nonce of its own")
}
