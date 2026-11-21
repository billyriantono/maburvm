package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/config"
	"github.com/maburvm/panel/internal/shared/models"
)

// BIL-07: the billing module gets a one-time link for its customer, and the
// link signs the browser in, once, within a minute, on the customer's VM.
func TestBillingSingleSignOn(t *testing.T) {
	t.Setenv("JWT_SECRET_KEY", "test-jwt-secret-key-for-billing-sso-0123456789")
	t.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("PANEL_PUBLIC_URL", "https://cp.example.test/")
	db := billingIntegrationDB(t)
	for _, ddl := range []string{
		`ALTER TABLE users ADD COLUMN failed_login_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE users ADD COLUMN locked_until DATETIME`,
		`ALTER TABLE users ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0`,
		`CREATE TABLE user_sessions (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, method TEXT NOT NULL, ip_address TEXT, user_agent TEXT,
			impersonator_id TEXT, created_at DATETIME, last_seen_at DATETIME, expires_at DATETIME, revoked_at DATETIME)`,
		`CREATE TABLE sign_in_events (id TEXT PRIMARY KEY, user_id TEXT, email TEXT, method TEXT, success BOOLEAN, note TEXT,
			ip_address TEXT, user_agent TEXT, created_at DATETIME)`,
	} {
		require.NoError(t, db.Exec(ddl).Error)
	}
	h := newBillingTestHandler(t, db)
	users, err := service.NewUserService(db, "0123456789abcdef0123456789abcdef", "", "test")
	require.NoError(t, err)
	e := echo.New()
	oidc := NewOIDCHandler(config.OIDCConfig{}, users)
	e.GET(billingSSOPath, oidc.BillingSSO)
	e.POST("/api/v1/auth/oidc/2fa", oidc.Verify2FA)

	customer, stranger, admin := seedUser(t, db, models.RoleClient), seedUser(t, db, models.RoleClient), seedUser(t, db, models.RoleAdmin)
	vmID, strangersVM := seedVM(t, db, customer), seedVM(t, db, stranger)

	link := func(user, vm string) (int, WebhookResponse) {
		return sendBilling(t, h, EventUserSSO, WebhookData{UserID: user, VMID: vm})
	}
	open := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	sessionCookie := func(rec *httptest.ResponseRecorder) *http.Cookie {
		for _, c := range rec.Result().Cookies() {
			if c.Name == middleware.AccessTokenCookieName {
				return c
			}
		}
		return nil
	}

	code, resp := link(customer, vmID)
	require.Equal(t, http.StatusOK, code, resp.Message)
	path := resp.Metadata["path"]
	require.True(t, strings.HasPrefix(path, "/api/v1/auth/billing-sso?token="), path)
	assert.Equal(t, "https://cp.example.test"+path, resp.Metadata["url"])
	assert.Equal(t, "60", resp.Metadata["expires_in"])

	rec := open(path)
	require.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/client/vms/"+vmID, rec.Header().Get("Location"))
	ck := sessionCookie(rec)
	require.NotNil(t, ck, "an ordinary session in an HttpOnly cookie")
	assert.True(t, ck.HttpOnly)
	assert.NotEmpty(t, ck.Value)
	var methods []string
	require.NoError(t, db.Table("user_sessions").Where("user_id = ?", customer).Pluck("method", &methods).Error)
	assert.Equal(t, []string{"billing_sso"}, methods)
	var signIns []models.SignInEvent
	require.NoError(t, db.Where("user_id = ?", customer).Find(&signIns).Error)
	require.Len(t, signIns, 1)
	assert.True(t, signIns[0].Success)
	assert.Equal(t, "billing_sso", signIns[0].Method)
	var audit models.AuditLog
	require.NoError(t, db.Where("action = ? AND resource_id = ?", "user.sign_in", customer).First(&audit).Error)
	assert.Equal(t, "billing_sso", audit.Details["method"])

	assert.Equal(t, "/login?sso=billing", open(path).Header().Get("Location"), "a link works once")

	// No VM, or a VM that is gone: the dashboard.
	_, resp = link(customer, "")
	assert.Equal(t, "/client/dashboard", open(resp.Metadata["path"]).Header().Get("Location"))
	_, resp = link(customer, uuid.NewString())
	assert.Equal(t, "/client/dashboard", open(resp.Metadata["path"]).Header().Get("Location"))

	// Refused when asking: someone else's VM, staff, an account that is suspended or locked.
	code, _ = link(customer, strangersVM)
	assert.Equal(t, http.StatusForbidden, code, "never a link to someone else's VM")
	code, resp = link(admin, "")
	assert.Equal(t, http.StatusForbidden, code)
	assert.Contains(t, resp.Message, "staff account")
	code, _ = link(uuid.NewString(), "")
	assert.Equal(t, http.StatusNotFound, code)
	code, _ = link(customer, "not-a-vm")
	assert.Equal(t, http.StatusBadRequest, code)

	_, resp = link(stranger, "")
	pending := resp.Metadata["path"]
	require.NoError(t, db.Table("users").Where("id = ?", stranger).Update("suspended_at", time.Now()).Error)
	code, resp = link(stranger, "")
	assert.Equal(t, http.StatusForbidden, code)
	assert.Contains(t, resp.Message, "suspended")
	assert.Equal(t, "/login?sso=suspended", open(pending).Header().Get("Location"), "suspended after the link was made")
	require.NoError(t, db.Table("users").Where("id = ?", stranger).Updates(map[string]any{"suspended_at": nil, "locked_until": time.Now().Add(time.Hour)}).Error)
	code, resp = link(stranger, "")
	assert.Equal(t, http.StatusForbidden, code)
	assert.Contains(t, resp.Message, "locked")

	// Forged, expired, or signed for something else.
	expired, err := signJSON(billingSSOPurpose, billingSSOToken{UserID: uuid.MustParse(customer), Exp: time.Now().Add(-time.Second).Unix(), Nonce: "n1"})
	require.NoError(t, err)
	ticket, err := signJSON("sso-2fa", sso2FATicket{UserID: uuid.MustParse(customer), Exp: time.Now().Add(time.Minute).Unix()})
	require.NoError(t, err)
	for _, token := range []string{expired, ticket, "e30.AAAA", ""} {
		rec := open(billingSSOPath + "?token=" + url.QueryEscape(token))
		assert.Equal(t, "/login?sso=billing", rec.Header().Get("Location"))
		assert.Nil(t, sessionCookie(rec))
	}

	// Panel two-factor still asks for its code, then lands on the VM.
	setup, err := users.Setup2FA(&service.Setup2FARequest{UserID: uuid.MustParse(customer)})
	require.NoError(t, err)
	now, err := totp.GenerateCode(setup.Secret, time.Now())
	require.NoError(t, err)
	require.NoError(t, users.Verify2FASetup(&service.Verify2FASetupRequest{UserID: uuid.MustParse(customer), TOTPCode: now}))
	require.NoError(t, db.Table("users").Where("id = ?", customer).Update("totp_last_step", 0).Error)

	_, resp = link(customer, vmID)
	rec = open(resp.Metadata["path"])
	location := rec.Header().Get("Location")
	require.True(t, strings.HasPrefix(location, "/login?sso=2fa#ticket="), location)
	if ck := sessionCookie(rec); assert.NotNil(t, ck, "another account's session in this browser is cleared") {
		assert.Empty(t, ck.Value)
	}
	frag, err := url.ParseQuery(strings.SplitN(location, "#", 2)[1])
	require.NoError(t, err)
	body, _ := json.Marshal(map[string]string{"ticket": frag.Get("ticket"), "totpCode": now})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oidc/2fa", strings.NewReader(string(body)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out LoginResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, "/client/vms/"+vmID, out.Redirect)
	require.NotNil(t, sessionCookie(rec))
	require.NoError(t, db.Table("user_sessions").Where("user_id = ?", customer).Order("created_at").Pluck("method", &methods).Error)
	assert.Equal(t, []string{"billing_sso", "billing_sso", "billing_sso", "billing_sso"}, methods)
	var audits int64
	require.NoError(t, db.Model(&models.AuditLog{}).Where("action = ? AND resource_id = ?", "user.sign_in", customer).Count(&audits).Error)
	assert.EqualValues(t, 4, audits, "every finished sign-in from billing is on record")
}

// BIL-07: the module's own ServiceSingleSignOn against the panel's own
// handlers, over HTTP: the link it hands WHMCS signs the customer in on the
// service's VM. Needs the php command; skipped without it.
func TestWHMCSSingleSignOnAgainstThePanel(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php is not installed")
	}
	t.Setenv("JWT_SECRET_KEY", "test-jwt-secret-key-for-billing-sso-0123456789")
	t.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	db := billingIntegrationDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE user_sessions (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, method TEXT NOT NULL, ip_address TEXT, user_agent TEXT,
		impersonator_id TEXT, created_at DATETIME, last_seen_at DATETIME, expires_at DATETIME, revoked_at DATETIME)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE sign_in_events (id TEXT PRIMARY KEY, user_id TEXT, email TEXT, method TEXT, success BOOLEAN, note TEXT,
		ip_address TEXT, user_agent TEXT, created_at DATETIME)`).Error)
	h := newBillingTestHandler(t, db)
	users, err := service.NewUserService(db, "0123456789abcdef0123456789abcdef", "", "test")
	require.NoError(t, err)
	customer := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, customer)
	e := echo.New()
	e.POST("/webhooks/billing", h.HandleWebhook, h.RequireWebhookAuth())
	e.GET(billingSSOPath, NewOIDCHandler(config.OIDCConfig{}, users).BillingSSO)
	srv := httptest.NewServer(e)
	defer srv.Close()

	script := `require $argv[1];
$p = ['serversecure' => '', 'serverhostname' => $argv[2], 'serverip' => '', 'serverpassword' => $argv[3], 'serveraccesshash' => $argv[4],
    'serviceid' => 41, 'userid' => 7, 'customfields' => ['Panel User ID' => $argv[5], 'VM ID' => $argv[6]], 'model' => null];
echo json_encode(maburvm_ServiceSingleSignOn($p));`
	out, err := exec.Command(php, "-r", script, "--", "../../../integrations/whmcs/tests/bootstrap.php",
		strings.TrimPrefix(srv.URL, "http://"), h.apiKey, h.webhookSecret, customer, vmID).CombinedOutput()
	require.NoError(t, err, string(out))
	var got struct {
		Success    bool   `json:"success"`
		RedirectTo string `json:"redirectTo"`
		ErrorMsg   string `json:"errorMsg"`
	}
	require.NoError(t, json.Unmarshal(out, &got), string(out))
	require.True(t, got.Success, got.ErrorMsg)
	require.True(t, strings.HasPrefix(got.RedirectTo, srv.URL+billingSSOPath+"?token="), got.RedirectTo)

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Get(got.RedirectTo)
	require.NoError(t, err)
	res.Body.Close()
	assert.Equal(t, http.StatusFound, res.StatusCode)
	assert.Equal(t, "/client/vms/"+vmID, res.Header.Get("Location"))
}
