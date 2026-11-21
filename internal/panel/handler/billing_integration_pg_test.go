//go:build pg

package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
)

// The WHMCS integration on the real schema: uuid, jsonb and inet columns,
// which SQLite stores as text and never refuses.
func TestPG_BillingIntegrationOnTheRealSchema(t *testing.T) {
	db := migratedPG(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	t.Setenv("JWT_SECRET_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("BILLING_WEBHOOK_SECRET", "test-signing-secret")

	vms := service.NewVMService(db, repository.NewVMRepository(db), repository.NewNodeRepository(db), repository.NewTemplateRepository(db), nil, log)
	vms.SetRegionService(service.NewRegionService(db))
	h := &BillingHandler{vmService: vms, logger: log, db: db, apiKey: "k", webhookSecret: "s"}

	// BIL-02: the account, its quota row and its link, twice over.
	email := uuid.NewString() + "@example.test"
	code, resp := sendBilling(t, h, EventUserEnsure, WebhookData{Email: email, Name: "Dewi", ExternalRef: "whmcs-client-7"})
	require.Equal(t, http.StatusOK, code, resp.Message)
	require.Equal(t, "true", resp.Metadata["created"])
	userID := resp.Metadata["user_id"]
	code, resp = sendBilling(t, h, EventUserEnsure, WebhookData{Email: "other-" + email, ExternalRef: "whmcs-client-7"})
	require.Equal(t, http.StatusOK, code, resp.Message)
	require.Equal(t, userID, resp.Metadata["user_id"])
	require.Equal(t, "whmcs-client-7", service.BillingUserRef(ctx, db, userID))
	var quotaRows int64
	require.NoError(t, db.Table("user_quotas").Where("user_id = ?", userID).Count(&quotaRows).Error)
	require.EqualValues(t, 1, quotaRows)
	other := &models.User{Email: uuid.NewString() + "@example.test", PasswordHash: "x", Role: models.RoleClient}
	require.NoError(t, db.Create(other).Error)
	code, resp = sendBilling(t, h, EventUserEnsure, WebhookData{Email: other.Email, ExternalRef: "whmcs-client-7"})
	require.Equal(t, http.StatusOK, code, resp.Message)
	require.Equal(t, userID, resp.Metadata["user_id"], "the client's reference wins over an email that matches someone else")

	// BIL-26: the rules survive a round trip through jsonb, and a template
	// from before the migration reads as unrestricted.
	node := &models.Node{Name: "n-" + uuid.NewString()[:8], IPAddress: "192.0.2.10", Status: models.NodeStatusActive, Token: uuid.NewString()}
	require.NoError(t, db.Create(node).Error)
	planA := &models.Plan{Name: "a-" + uuid.NewString()[:8], CPU: 2, RAM: 4096, Disk: 60, BandwidthMbps: 100, DataQuotaGB: 500, IsActive: true}
	planB := &models.Plan{Name: "b-" + uuid.NewString()[:8], CPU: 2, RAM: 4096, Disk: 60, IsActive: true}
	require.NoError(t, db.Create(planA).Error)
	require.NoError(t, db.Create(planB).Error)
	var legacyID string
	require.NoError(t, db.Raw(`INSERT INTO os_templates (name, version, image_path) VALUES ('debian', ?, '/images/debian.qcow2') RETURNING id`, uuid.NewString()[:8]).Scan(&legacyID).Error)
	templates := repository.NewTemplateRepository(db)
	legacy, err := templates.GetByID(ctx, legacyID)
	require.NoError(t, err)
	require.False(t, legacy.Licensed)
	require.Empty(t, legacy.PlanIDs)
	require.True(t, legacy.OfferedOn(""))
	tsvc := service.NewTemplateService(db, templates, repository.NewNodeRepository(db), nil)
	licensed, ids := true, []string{planA.ID}
	_, err = tsvc.UpdateTemplate(ctx, legacyID, &service.UpdateTemplateRequest{Licensed: &licensed, PlanIDs: &ids})
	require.NoError(t, err)
	windows, err := templates.GetByID(ctx, legacyID)
	require.NoError(t, err)
	require.True(t, windows.Licensed)
	require.Equal(t, []string{planA.ID}, windows.PlanIDs)
	bad := []string{"not-a-plan"}
	_, err = tsvc.UpdateTemplate(ctx, legacyID, &service.UpdateTemplateRequest{PlanIDs: &bad})
	require.ErrorIs(t, err, service.ErrInvalidTemplate)

	// BIL-05: the order's region is honoured, and a template tied to plans is
	// refused on another plan, with answers the module can show.
	pool := &models.IPPool{Name: "pub-" + uuid.NewString()[:8], NodeID: &node.ID, Family: models.IPFamilyIPv4, CIDR: "203.0.113.0/24", Bridge: "br0", Gateway: "203.0.113.1", RangeStart: "203.0.113.40", RangeEnd: "203.0.113.40"}
	require.NoError(t, db.Create(pool).Error)
	require.NoError(t, db.Create(&models.IPAddress{PoolID: pool.ID, Address: "203.0.113.40", Family: models.IPFamilyIPv4, Status: models.IPAddressStatusAvailable}).Error)
	order := WebhookData{UserID: userID, Hostname: "win1", TemplateID: legacyID, PlanID: planB.ID, ExternalRef: "whmcs-90"}
	code, resp = sendBilling(t, h, EventVMCreate, order)
	require.Equal(t, http.StatusBadRequest, code)
	require.Contains(t, resp.Message, "not offered on this plan")
	order.PlanID, order.Region = planA.ID, "nowhere"
	code, resp = sendBilling(t, h, EventVMCreate, order)
	require.Equal(t, http.StatusNotFound, code, resp.Message)
	require.Contains(t, resp.Message, "region not found")
	empty := &models.Region{Slug: "empty-" + uuid.NewString()[:8], Name: "Empty", Country: "ID", Enabled: true}
	require.NoError(t, db.Create(empty).Error)
	order.Region = empty.Slug
	code, resp = sendBilling(t, h, EventVMCreate, order)
	require.Equal(t, http.StatusConflict, code, resp.Message)
	region := &models.Region{Slug: "jkt-" + uuid.NewString()[:8], Name: "Jakarta", Country: "ID", Enabled: true}
	require.NoError(t, db.Create(region).Error)
	require.NoError(t, db.Model(&models.Node{}).Where("id = ?", node.ID).Update("region_id", region.ID).Error)
	order.Region, order.BandwidthMbps, order.DataQuotaGB = region.Slug, 250, 2000
	code, created := sendBilling(t, h, EventVMCreate, order)
	require.Equal(t, http.StatusOK, code, created.Message)
	require.Equal(t, node.ID, created.Metadata["node_id"])
	var nic models.Network
	require.NoError(t, db.Where("vm_id = ?", created.VMID).First(&nic).Error)
	require.EqualValues(t, 250, nic.BandwidthLimit, "the order's own speed, over the plan's")
	require.EqualValues(t, 2000, nic.BandwidthQuotaGB)

	// BIL-04: the status the module polls, with the address as inet returns it.
	code, st := sendBilling(t, h, EventVMStatus, WebhookData{VMID: created.VMID, UserID: userID, ExternalRef: "whmcs-90"})
	require.Equal(t, http.StatusOK, code, st.Message)
	require.Equal(t, "provisioning", st.Metadata["state"])
	require.Equal(t, "203.0.113.40", st.Metadata["ip"])
	require.Equal(t, "root", st.Metadata["username"])

	code, cat := sendBilling(t, h, EventCatalogList, WebhookData{})
	require.Equal(t, http.StatusOK, code, cat.Message)
	raw, _ := json.Marshal(cat.Data)
	require.Contains(t, string(raw), planA.ID)
	require.Contains(t, string(raw), region.Slug)

	// BIL-20: the delivery log on the real table, settings read from jsonb.
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('api', ?::jsonb)
		ON CONFLICT (section) DO UPDATE SET data = EXCLUDED.data`, `{"webhookUrl":"`+srv.URL+`"}`).Error)
	service.EmitVMBillingEvent(ctx, db, nil, log, service.BillingEventVMCreated, created.VMID, nil)
	rows, total, err := service.ListBillingDeliveries(ctx, db, "", 10, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Contains(t, rows[0].Body, `"external_ref":"whmcs-90"`)
	require.Contains(t, rows[0].Body, `"user_ref":"whmcs-client-7"`)
	require.Contains(t, rows[0].Body, `"ip":"203.0.113.40"`)
	require.NoError(t, service.DeliverBillingEvent(ctx, db, rows[0].ID, false))
	require.Equal(t, 1, hits)
	rows, _, err = service.ListBillingDeliveries(ctx, db, service.DeliveryDelivered, 10, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].DeliveredAt)
}

// BIL-20 end to end: an event travels through the real job queue. The first
// attempt is refused, the queue tries again by itself, and the log shows both.
func TestPG_BillingEventIsDeliveredThroughTheQueue(t *testing.T) {
	db := migratedPG(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	t.Setenv("BILLING_WEBHOOK_SECRET", "test-signing-secret")

	var dbName string
	require.NoError(t, db.Raw("SELECT current_database()").Scan(&dbName).Error)
	u, err := url.Parse(os.Getenv("MABURVM_TEST_PG_DSN"))
	require.NoError(t, err)
	u.Path = "/" + dbName
	client, err := queue.NewClient(queue.DefaultConfig(u.String()), log)
	require.NoError(t, err)
	require.NoError(t, queue.RunMigrationsWithLogger(ctx, client.Pool(), log))
	queue.WebhookDeliverHook = func(ctx context.Context, id string, final bool) error {
		return service.DeliverBillingEvent(ctx, db, id, final)
	}
	t.Cleanup(func() { queue.WebhookDeliverHook = nil })
	require.NoError(t, client.Start(ctx))
	t.Cleanup(func() { _ = client.StopAndCancel(context.Background()) })

	var mu sync.Mutex
	var signatures []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		signatures = append(signatures, r.Header.Get("X-Webhook-Signature")+" "+service.SignBillingEvent(body, "test-signing-secret"))
		if len(signatures) == 1 {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('api', ?::jsonb)
		ON CONFLICT (section) DO UPDATE SET data = EXCLUDED.data`, `{"webhookUrl":"`+srv.URL+`"}`).Error)

	service.EmitBillingEvent(ctx, db, client.RiverClient(), log, service.BillingEventFloatingIP, "u1", map[string]any{"user_id": "u1", "billable": 2})

	var row service.WebhookDelivery
	require.Eventually(t, func() bool {
		rows, _, err := service.ListBillingDeliveries(ctx, db, "", 1, 0)
		if err != nil || len(rows) != 1 {
			return false
		}
		row = rows[0]
		return row.Status == service.DeliveryDelivered
	}, 30*time.Second, 200*time.Millisecond, "the queue retries a refused delivery without anyone asking")
	require.Equal(t, 2, row.Attempts)
	require.Equal(t, http.StatusOK, row.ResponseCode)
	mu.Lock()
	require.Len(t, signatures, 2)
	for _, pair := range signatures {
		sent, want, _ := strings.Cut(pair, " ")
		require.Equal(t, want, sent, "every attempt is signed over the body it carries")
	}
	mu.Unlock()

	// BIL-04: a creation that fails leaves its reason where vm.status reads
	// it, and the panel hears of the failed try without waiting for the last.
	var retrying sync.Map
	queue.JobRetryingHook = func(_ context.Context, kind string, args []byte, reason string) {
		retrying.Store(kind+" "+string(args), reason)
	}
	t.Cleanup(func() { queue.JobRetryingHook = nil })
	vmID := uuid.NewString()
	_, err = client.RiverClient().Insert(ctx, queue.VMOperationJob{VMID: vmID, Operation: queue.VMOpCreate, NodeID: uuid.NewString()}, nil)
	require.NoError(t, err)
	var state, reason string
	require.Eventually(t, func() bool {
		state, reason = service.VMCreateJob(ctx, db, vmID)
		return state == "retryable" && reason != ""
	}, 30*time.Second, 200*time.Millisecond, "the queue's record of a creation that failed and will be tried again")
	require.Eventually(t, func() bool {
		told := false
		retrying.Range(func(k, v any) bool {
			told = strings.HasPrefix(k.(string), "vm_operation ") && strings.Contains(k.(string), vmID) && v.(string) == reason
			return !told
		})
		return told
	}, 5*time.Second, 100*time.Millisecond, "the failed try was reported with the same reason")
	state, reason = service.VMCreateJob(ctx, db, uuid.NewString())
	require.Empty(t, state+reason, "a VM the queue holds no creation for")
}
