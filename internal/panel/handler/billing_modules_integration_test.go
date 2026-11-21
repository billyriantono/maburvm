package handler

import (
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/shared/models"
)

// BIL-17: the Paymenter and FOSSBilling modules' own code against the panel's
// own handler over HTTP, behind the error envelope as in production: what they
// sign is accepted, the account they ask for is the client's, the VM they name
// by service reference is found, and a refusal reaches them in words. Needs
// the php command; skipped without it.
func TestPaymenterAndFOSSBillingTalkToTheRealWebhook(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php is not installed")
	}
	db := billingIntegrationDB(t)
	h := newBillingTestHandler(t, db)
	tmpl := &models.OSTemplate{Name: "debian", Version: "12", ImagePath: "/images/debian.qcow2", IsActive: true}
	require.NoError(t, db.Create(tmpl).Error)
	owner := uuid.New()
	require.NoError(t, db.Create(&models.User{ID: owner, Email: "dewi@example.test", Name: "Dewi", Role: models.RoleClient}).Error)
	fossVM := seedVM(t, db, owner.String())
	require.NoError(t, db.Table("vms").Where("id = ?", fossVM).Updates(map[string]any{
		"status": models.VMStatusRunning, "os_template_id": tmpl.ID, "billing_ref": "fossbilling-vps41"}).Error)
	require.NoError(t, db.Create(&models.Network{VMID: fossVM, IPAddress: "203.0.113.20"}).Error)
	strangersVM := seedVM(t, db, seedUser(t, db, models.RoleClient))

	e := echo.New()
	e.Use(middleware.ErrorEnvelope())
	e.POST("/webhooks/billing", h.HandleWebhook, h.RequireWebhookAuth())
	srv := httptest.NewServer(e)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	run := func(script string, args ...string) map[string]any {
		t.Helper()
		out, err := exec.Command(php, append([]string{"-r", script, "--"}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
		var got map[string]any
		require.NoError(t, json.Unmarshal(out, &got), string(out))
		return got
	}

	foss := run(`require $argv[1];
$m = new Server_Manager_Maburvm(['host' => $argv[2], 'secure' => false, 'password' => $argv[3], 'accesshash' => $argv[4]]);
$a = new Server_Account('vps41', 'x', '', new Server_Package([]), new Server_Client(7, 'dewi@example.test', 'Dewi'));
$missing = new Server_Account('vps99', 'x', '', new Server_Package([]), new Server_Client(7, 'dewi@example.test', 'Dewi'));
try { $m->suspendAccount($missing); $refused = ''; } catch (Server_Exception $e) { $refused = $e->getMessage(); }
echo json_encode(['test' => $m->testConnection(), 'ip' => $m->synchronizeAccount($a)->getIp(), 'refused' => $refused,
    'gone' => $m->cancelAccount($missing)]);`,
		"../../../integrations/fossbilling/tests/bootstrap.php", host, h.apiKey, h.webhookSecret)
	assert.Equal(t, true, foss["test"])
	assert.Equal(t, "203.0.113.20", foss["ip"], "the VM is found by its account reference and its owner")
	assert.Contains(t, foss["refused"], "no VM of this user carries external_ref")
	assert.Equal(t, true, foss["gone"], "cancelling an account whose VM is gone succeeds")

	pay := run(`require $argv[1];
$x = new Paymenter\Extensions\Servers\Maburvm\Maburvm(['host' => 'http://' . $argv[2], 'api_key' => $argv[3], 'webhook_secret' => $argv[4]]);
$svc = new MaburvmTests\Service(41, (object) ['id' => 7, 'email' => 'dewi@example.test', 'first_name' => 'Dewi', 'last_name' => '']);
$props = [Paymenter\Extensions\Servers\Maburvm\Maburvm::USER_ID => $argv[5], Paymenter\Extensions\Servers\Maburvm\Maburvm::VM_ID => $argv[6]];
try { $x->suspendServer($svc, [], $props); $refused = ''; } catch (Exception $e) { $refused = $e->getMessage(); }
$gone = $x->terminateServer($svc, [], [Paymenter\Extensions\Servers\Maburvm\Maburvm::USER_ID => $argv[5],
    Paymenter\Extensions\Servers\Maburvm\Maburvm::VM_ID => '00000000-0000-0000-0000-000000000000']);
$wrong = new Paymenter\Extensions\Servers\Maburvm\Maburvm(['host' => 'http://' . $argv[2], 'api_key' => $argv[3], 'webhook_secret' => 'not-the-secret']);
echo json_encode(['test' => $x->testConfig(), 'refused' => $refused, 'gone' => $gone, 'wrong' => $wrong->testConfig()]);`,
		"../../../integrations/paymenter/tests/bootstrap.php", host, h.apiKey, h.webhookSecret, owner.String(), strangersVM)
	assert.Equal(t, true, pay["test"])
	assert.Contains(t, pay["refused"], "does not belong to the user", "another customer's VM is refused")
	assert.Equal(t, true, pay["gone"])
	assert.NotEqual(t, true, pay["wrong"], "a wrong secret fails the test")
}
