//go:build pg

package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	sharedb "github.com/maburvm/panel/internal/shared/db"
	"github.com/maburvm/panel/internal/shared/models"
)

// migratedPG is a throwaway PostgreSQL database with every migration applied,
// one file per statement batch as the panel's own runner does. It needs
// MABURVM_TEST_PG_DSN (any database on a server where the role may create one).
func migratedPG(t *testing.T) *gorm.DB {
	t.Helper()
	base := os.Getenv("MABURVM_TEST_PG_DSN")
	if base == "" {
		t.Skip("MABURVM_TEST_PG_DSN is not set")
	}
	u, err := url.Parse(base)
	require.NoError(t, err)
	name := "maburvm_billing_" + uuid.NewString()[:8]
	admin, err := sql.Open("pgx", base)
	require.NoError(t, err)
	_, err = admin.Exec("CREATE DATABASE " + name)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.Exec("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1", name)
		_, _ = admin.Exec("DROP DATABASE IF EXISTS " + name)
		_ = admin.Close()
	})
	u.Path = "/" + name
	raw, err := sql.Open("pgx", u.String())
	require.NoError(t, err)
	defer raw.Close()
	files, err := fs.Glob(sharedb.MigrationsFS, "migrations/*.up.sql")
	require.NoError(t, err)
	sort.Strings(files)
	for _, f := range files {
		contents, err := fs.ReadFile(sharedb.MigrationsFS, f)
		require.NoError(t, err)
		_, err = raw.Exec(string(contents))
		require.NoError(t, err, f)
	}
	db, err := gorm.Open(postgres.Open(u.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	return db
}

// The billing changes on the real schema. SQLite accepts what PostgreSQL
// refuses: uuid and jsonb columns, and a quota in bytes compared to a bare 0,
// which is how setting a quota of 2 GB or more used to fail.
func TestPG_BillingOnTheRealSchema(t *testing.T) {
	db := migratedPG(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	user := &models.User{Email: uuid.NewString() + "@example.test", PasswordHash: "x", Role: models.RoleClient}
	require.NoError(t, db.Create(user).Error)
	node := &models.Node{Name: "n-" + uuid.NewString()[:8], IPAddress: "192.0.2.10", Status: models.NodeStatusActive, Token: uuid.NewString()}
	require.NoError(t, db.Create(node).Error)
	tmpl := &models.OSTemplate{Name: "debian", Version: uuid.NewString()[:8], ImagePath: "/images/debian.qcow2", IsActive: true}
	require.NoError(t, db.Create(tmpl).Error)
	plan := &models.Plan{Name: "p-" + uuid.NewString()[:8], CPU: 2, RAM: 2048, Disk: 40, BandwidthMbps: 100, DataQuotaGB: 500, OverQuotaPolicy: models.OverQuotaSuspend, ThrottleSpeedMbps: 5, IsActive: true}
	require.NoError(t, db.Create(plan).Error)

	vms := service.NewVMService(db, repository.NewVMRepository(db), repository.NewNodeRepository(db), repository.NewTemplateRepository(db), nil, log)
	resp, err := vms.CreateVM(ctx, &service.CreateVMRequest{UserID: user.ID.String(), Hostname: "Web1", OSTemplateID: tmpl.ID, PlanID: plan.ID,
		NodeID: node.ID, Billed: true, BillingRef: "whmcs-41", AnyHostname: true, RegeneratePassword: true})
	require.NoError(t, err)
	_, err = vms.CreateVM(ctx, &service.CreateVMRequest{UserID: user.ID.String(), Hostname: "web1", OSTemplateID: tmpl.ID, PlanID: plan.ID, NodeID: node.ID})
	require.ErrorIs(t, err, service.ErrHostnameExists)
	vm, err := vms.GetVMRecord(ctx, resp.VM.ID)
	require.NoError(t, err)
	require.Equal(t, plan.ID, *vm.PlanID)
	require.Equal(t, "whmcs-41", *vm.BillingRef)
	empty := ""
	vm, err = vms.SetBillingLink(ctx, vm.ID, &empty, nil)
	require.NoError(t, err)
	require.Nil(t, vm.PlanID)
	require.ErrorIs(t, vms.DeleteVMByOwner(ctx, vm.ID, user.ID.String()), service.ErrBillingManaged)

	// plan limits
	network := &models.Network{VMID: vm.ID, IPAddress: "203.0.113.20", BandwidthLimit: 50, Throttled: true}
	require.NoError(t, db.Create(network).Error)
	now := time.Now()
	period := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&models.BandwidthUsage{VMID: vm.ID, NodeID: node.ID, PeriodStart: period, PeriodEnd: period.AddDate(0, 1, 0), TotalBytes: 1, QuotaBytes: 1, Exceeded: true, LastReportedAt: now}).Error)
	ns := service.NewNetworkService(db, repository.NewNetworkRepository(db), repository.NewFirewallRepository(db), repository.NewVMRepository(db), repository.NewNodeRepository(db), nil)
	require.NoError(t, ns.ApplyPlanLimits(ctx, vm.ID, plan))
	var got models.Network
	require.NoError(t, db.First(&got, "id = ?", network.ID).Error)
	require.EqualValues(t, 100, got.BandwidthLimit)
	require.EqualValues(t, 500, got.BandwidthQuotaGB)
	require.Equal(t, models.OverQuotaSuspend, got.OverQuotaPolicy)

	// account suspension + audit
	res, err := vms.SetAccountSuspended(ctx, user.ID.String(), true, false)
	require.NoError(t, err)
	require.True(t, res.User.Suspended())
	var u models.User
	require.NoError(t, db.First(&u, "id = ?", user.ID).Error)
	require.True(t, u.Suspended())
	require.NotNil(t, u.TokenRevokedAt)
	service.AuditAccountSuspension(ctx, db, true, "", user.ID.String(), "198.51.100.8", "ua", map[string]any{"source": "billing", "vms_failed": []string{}})
	_, err = vms.SetAccountSuspended(ctx, user.ID.String(), false, true)
	require.NoError(t, err)
	var after models.User
	require.NoError(t, db.First(&after, "id = ?", user.ID).Error)
	require.False(t, after.Suspended())
	var audits int64
	require.NoError(t, db.Model(&models.AuditLog{}).Where("action = 'user.suspend' AND resource_id = ?", user.ID.String()).Count(&audits).Error)
	require.EqualValues(t, 1, audits)

	// replay guard + stale create
	h := &BillingHandler{vmService: vms, logger: log, db: db, apiKey: "k", webhookSecret: "s"}
	sig := h.sign([]byte(uuid.NewString()))
	fresh, err := h.firstUse(ctx, sig)
	require.NoError(t, err)
	require.True(t, fresh)
	fresh, err = h.firstUse(ctx, sig)
	require.NoError(t, err)
	require.False(t, fresh)
	key := "whmcs-create-" + uuid.NewString()
	h.storeIdempotency(ctx, key, string(EventVMCreate), &IdempotencyRecord{RequestID: uuid.NewString(), ProcessedAt: time.Now().UTC(), Response: WebhookResponse{Success: true, VMID: vm.ID}})
	rec, ok := h.checkIdempotency(ctx, key)
	require.True(t, ok)
	require.False(t, h.createdVMGone(ctx, rec))
	require.NoError(t, db.Table("vms").Where("id = ?", vm.ID).Update("status", models.VMStatusDeleting).Error)
	require.True(t, h.createdVMGone(ctx, rec))
	h.dropIdempotency(ctx, key)
	_, ok = h.checkIdempotency(ctx, key)
	require.False(t, ok)
	h.cleanupOldIdempotency(ctx)

	// BIL-01 end to end: a signed order through the webhook gets an address
	// from the node's pool, a stored root password, its plan and its service.
	t.Setenv("JWT_SECRET_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	pool := &models.IPPool{Name: "pub-" + uuid.NewString()[:8], NodeID: &node.ID, Family: models.IPFamilyIPv4, CIDR: "203.0.113.0/24", Bridge: "br0", Gateway: "203.0.113.1", RangeStart: "203.0.113.30", RangeEnd: "203.0.113.30"}
	require.NoError(t, db.Create(pool).Error)
	require.NoError(t, db.Create(&models.IPAddress{PoolID: pool.ID, Address: "203.0.113.30", Family: models.IPFamilyIPv4, Status: models.IPAddressStatusAvailable}).Error)
	e := echo.New()
	e.POST("/webhooks/billing", h.HandleWebhook, h.RequireWebhookAuth())
	order, _ := json.Marshal(map[string]any{"event": "vm.create", "timestamp": time.Now().UTC().Format(time.RFC3339), "nonce": uuid.NewString(),
		"data": map[string]any{"user_id": user.ID.String(), "hostname": "Web1", "template_id": tmpl.ID, "plan_id": plan.ID, "external_ref": "whmcs-77"}})
	post := func() (int, WebhookResponse) {
		req := signedRequest(h, order)
		req.Header.Set(IdempotencyKeyHeader, "whmcs-create-77")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		var out WebhookResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
		return rec.Code, out
	}
	code, created := post()
	require.Equal(t, http.StatusOK, code, created.Message)
	var nic models.Network
	require.NoError(t, db.Where("vm_id = ?", created.VMID).First(&nic).Error)
	require.Equal(t, "203.0.113.30", nic.IPAddress)
	require.EqualValues(t, plan.BandwidthMbps, nic.BandwidthLimit)
	ordered, err := vms.GetVMRecord(ctx, created.VMID)
	require.NoError(t, err)
	require.Equal(t, "whmcs-77", *ordered.BillingRef)
	require.Equal(t, plan.ID, *ordered.PlanID)
	_, _, err = service.RevealInitialPassword(ctx, db, created.VMID)
	require.NoError(t, err, "the generated root password is kept for the owner")
	code, _ = post()
	require.Equal(t, http.StatusConflict, code, "the same signed request again is a replay")
}
