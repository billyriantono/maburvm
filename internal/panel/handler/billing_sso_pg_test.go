//go:build pg

package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/config"
	"github.com/maburvm/panel/internal/shared/models"
)

// BIL-07 and ADM-15 on the real schema: uuid, inet, numeric and the sessions,
// sign-in history and replay guard rows the link writes.
func TestPG_BillingSSOAndPlanAllowancesOnTheRealSchema(t *testing.T) {
	db := migratedPG(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Setenv("JWT_SECRET_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")

	vms := service.NewVMService(db, repository.NewVMRepository(db), repository.NewNodeRepository(db), repository.NewTemplateRepository(db), nil, log)
	h := &BillingHandler{vmService: vms, logger: log, db: db, apiKey: "k", webhookSecret: "s"}
	users, err := service.NewUserService(db, "0123456789abcdef0123456789abcdef", "", "test")
	require.NoError(t, err)
	e := echo.New()
	e.GET(billingSSOPath, NewOIDCHandler(config.OIDCConfig{}, users).BillingSSO)

	customer := &models.User{Email: uuid.NewString() + "@example.test", PasswordHash: "x", Role: models.RoleClient}
	require.NoError(t, db.Create(customer).Error)
	code, resp := sendBilling(t, h, EventUserSSO, WebhookData{UserID: customer.ID.String()})
	require.Equal(t, http.StatusOK, code, resp.Message)
	open := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, resp.Metadata["path"], nil))
		return rec
	}
	require.Equal(t, "/client/dashboard", open().Header().Get("Location"))
	require.Equal(t, "/login?sso=billing", open().Header().Get("Location"))
	var sessions, signIns, audits int64
	require.NoError(t, db.Table("user_sessions").Where("user_id = ? AND method = ?", customer.ID, "billing_sso").Count(&sessions).Error)
	require.NoError(t, db.Table("sign_in_events").Where("user_id = ? AND method = ? AND success", customer.ID, "billing_sso").Count(&signIns).Error)
	require.NoError(t, db.Table("audit_logs").Where("resource_id = ? AND action = ?", customer.ID.String(), "user.sign_in").Count(&audits).Error)
	require.EqualValues(t, 1, sessions)
	require.EqualValues(t, 1, signIns)
	require.EqualValues(t, 1, audits)

	// A plan's price survives numeric, and its allowances reach the VMs on it.
	plans := service.NewPlanService(repository.NewPlanRepository(db))
	price, two := 9.99, 2
	plan, err := plans.CreatePlan(ctx, &service.PlanRequest{Name: "p-" + uuid.NewString()[:8], CPU: 2, RAM: 2048, Disk: 40,
		PriceMonthly: &price, Currency: "EUR", MaxSnapshots: &two})
	require.NoError(t, err)
	got, err := plans.GetPlan(ctx, plan.ID)
	require.NoError(t, err)
	require.InDelta(t, 9.99, *got.PriceMonthly, 1e-9)
	require.Equal(t, "EUR", got.Currency)
	require.Nil(t, got.MaxBackups)

	node := &models.Node{Name: "n-" + uuid.NewString()[:8], IPAddress: "192.0.2.10", Status: models.NodeStatusActive, Token: uuid.NewString()}
	require.NoError(t, db.Create(node).Error)
	var tmplID string
	require.NoError(t, db.Raw(`INSERT INTO os_templates (name, version, image_path) VALUES ('debian', ?, '/images/debian.qcow2') RETURNING id`, uuid.NewString()[:8]).Scan(&tmplID).Error)
	vm := &models.VM{UserID: customer.ID.String(), NodeID: node.ID, Hostname: "web", OSTemplateID: tmplID,
		Resources: models.Resources{CPU: 1, RAM: 2048, Disk: 40}, Status: models.VMStatusRunning}
	require.NoError(t, db.Create(vm).Error)
	_, err = vms.SetBillingLink(ctx, vm.ID, &plan.ID, nil)
	require.NoError(t, err)
	for _, name := range []string{"one", "two"} {
		require.NoError(t, db.Create(&models.Snapshot{VMID: vm.ID, Name: name, DiskPath: "/s/" + name, Status: models.SnapshotStatusComplete}).Error)
	}
	snaps := service.NewSnapshotService(db, repository.NewSnapshotRepository(db), repository.NewVMRepository(db), repository.NewNodeRepository(db), nil, log)
	_, err = snaps.CreateSnapshot(ctx, &service.CreateSnapshotRequest{VMID: vm.ID, Name: "three", UserID: customer.ID.String()})
	var allowance *service.AllowanceError
	require.True(t, errors.As(err, &allowance), "%v", err)

	preview, err := vms.RolloutPlan(ctx, plan.ID, nil, false)
	require.NoError(t, err)
	require.Len(t, preview.VMs, 1)
	require.Equal(t, []string{"vCPU 1 to 2"}, preview.VMs[0].Changes)
	require.True(t, preview.VMs[0].NeedsStop)

	// The link to the VM checks it is the customer's on uuid columns.
	code, resp = sendBilling(t, h, EventUserSSO, WebhookData{UserID: customer.ID.String(), VMID: vm.ID})
	require.Equal(t, http.StatusOK, code, resp.Message)
	require.Equal(t, "/client/vms/"+vm.ID, open().Header().Get("Location"))
}
