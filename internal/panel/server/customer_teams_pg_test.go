//go:build pg

package server

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// 087 runs again on top of itself, down and up again; then teams, projects,
// the recipe catalog, plan extras and hourly graphs work on the real schema.
func TestCustomerTeamsPortalOnPostgres(t *testing.T) {
	db, raw, _ := pgMigratedDB(t)
	up, down := migrationSQL(t, "087_customer_teams_portal.up.sql"), migrationSQL(t, "087_customer_teams_portal.down.sql")
	_, err := raw.Exec(up)
	require.NoError(t, err, "087 run twice")
	_, err = raw.Exec(down)
	require.NoError(t, err, "087 down")
	_, err = raw.Exec(up)
	require.NoError(t, err, "087 up after down")

	ctx := context.Background()
	exec := func(q string, args ...interface{}) {
		t.Helper()
		require.NoError(t, db.Exec(q, args...).Error, q)
	}
	owner, tech, viewer := uuid.NewString(), uuid.NewString(), uuid.NewString()
	node, tmpl := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO users (id, email, password_hash) VALUES (?, 'owner@example.com', 'x'), (?, 'tech@example.com', 'x'), (?, 'viewer@example.com', 'x')`,
		owner, tech, viewer)
	exec(`INSERT INTO nodes (id, name, ip_address, token) VALUES (?, 'n1', '192.0.2.1', 't')`, node)
	exec(`INSERT INTO os_templates (id, name, version, image_path, checksum) VALUES (?, 'debian', '12', '/x', 'c')`, tmpl)
	vm1, vm2 := uuid.NewString(), uuid.NewString()
	exec(`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id, status) VALUES (?, ?, ?, 'web-1', ?, 'running'), (?, ?, ?, 'web-2', ?, 'running')`,
		vm1, owner, node, tmpl, vm2, owner, node, tmpl)

	// Projects and a member limited to one.
	p, err := service.CreateProject(ctx, db, owner, service.ProjectInput{Name: "Shop"})
	require.NoError(t, err)
	var vm models.VM
	require.NoError(t, db.First(&vm, "id = ?", vm2).Error)
	require.NoError(t, service.SetVMProject(ctx, db, &vm, p.ID))
	require.NoError(t, db.First(&vm, "id = ?", vm2).Error)
	require.NotNil(t, vm.ProjectID, "the model reads the project back")

	m, err := service.InviteMember(ctx, db, owner, owner, service.MemberInput{Email: "tech@example.com", Role: service.MemberRoleTechnical, ProjectIDs: []string{p.ID}})
	require.NoError(t, err)
	token := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	exec(`UPDATE account_members SET invite_token_hash = encode(sha256(convert_to(?, 'UTF8')), 'hex') WHERE id = ?`, token, m.ID)
	_, userID, err := service.AcceptInvitation(ctx, db, service.AcceptInput{Token: token})
	require.NoError(t, err)
	require.Equal(t, tech, userID)
	vm2Row := models.VM{ID: vm2, UserID: owner, Status: models.VMStatusRunning}
	require.NoError(t, service.CheckVMAccess(ctx, db, &vm2Row, tech, false, service.AccessChange), "the project's VM")
	vm1Row := models.VM{ID: vm1, UserID: owner, Status: models.VMStatusRunning}
	require.ErrorIs(t, service.CheckVMAccess(ctx, db, &vm1Row, tech, false, service.AccessRead), service.ErrVMNotFound)

	_, err = service.UpdateMember(ctx, db, owner, owner, m.ID, service.MemberInput{Role: service.MemberRoleReadOnly, VMIDs: []string{vm1}})
	require.NoError(t, err)
	require.NoError(t, service.CheckVMAccess(ctx, db, &vm1Row, tech, false, service.AccessRead))
	require.ErrorIs(t, service.CheckVMAccess(ctx, db, &vm1Row, tech, false, service.AccessChange), service.ErrMemberReadOnly)

	scope := service.CustomerVMScope(ctx, db, tech)
	vms, err := repository.NewVMRepository(db).ListFiltered(ctx, repository.VMFilter{Access: &scope}, 0, 0)
	require.NoError(t, err)
	require.Len(t, vms, 1)
	require.Equal(t, vm1, vms[0].ID)

	// Handing the account's VMs to another account takes them out of its projects.
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		_, err := service.ReassignUserVMs(ctx, tx, owner, viewer)
		return err
	}))
	var project *string
	require.NoError(t, db.Raw(`SELECT project_id FROM vms WHERE id = ?`, vm2).Scan(&project).Error)
	require.Nil(t, project)

	// The recipe catalog's columns take JSON.
	recipes := service.NewRecipeService(db)
	r, err := recipes.CreateRecipe(ctx, owner, service.RecipeRequest{Name: "web", Script: "echo {{site}}",
		Inputs: []models.RecipeInput{{Key: "site", Label: "Site", Required: true}}, IsPublic: true}, true)
	require.NoError(t, err)
	script, err := recipes.ResolveScript(ctx, viewer, r.ID, map[string]string{"site": "example.com"})
	require.NoError(t, err)
	require.Equal(t, "echo example.com", script)

	// Plan extras round-trip through jsonb.
	plans := service.NewPlanService(repository.NewPlanRepository(db))
	two := 2
	plan, err := plans.CreatePlan(ctx, &service.PlanRequest{Name: "S", CPU: 1, RAM: 1024, Disk: 10, Currency: "USD", MaxImages: &two,
		AddonPrices: map[string]float64{models.AddonPriceBackup: 1.5}})
	require.NoError(t, err)
	got, err := plans.GetPlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Equal(t, 2, *got.MaxImages)
	require.Equal(t, 1.5, got.AddonPrices[models.AddonPriceBackup])

	// Hourly graph averages: rolled up once per hour, read for long windows.
	metrics := repository.NewMetricsRepository(db)
	hour := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	for i, cpu := range []float64{10, 20, 30} {
		exec(`INSERT INTO vm_metrics (vm_id, cpu_usage, recorded_at) VALUES (?, ?, ?)`, vm1, cpu, hour.Add(time.Duration(i*10)*time.Minute))
	}
	require.NoError(t, metrics.RollupVMHours(ctx, hour.Add(-time.Hour), hour.Add(2*time.Hour)))
	require.NoError(t, metrics.RollupVMHours(ctx, hour.Add(-time.Hour), hour.Add(2*time.Hour)), "an hour rolled up twice is replaced")
	hours, err := metrics.ListVMHours(ctx, vm1, hour.Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, hours, 1)
	require.InDelta(t, 20, hours[0].CPUUsage, 0.001)
	history, err := service.NewMetricsService(db).VMHistory(ctx, vm1, 30*24*time.Hour, 500)
	require.NoError(t, err)
	require.Len(t, history, 1, "a 30-day graph reads the hourly averages")
	removed, err := metrics.PruneVMHoursBefore(ctx, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 1, removed)

	// Profile fields are written by table name and read by the model.
	tz := "Asia/Jakarta"
	require.NoError(t, service.UpdateOwnProfile(ctx, db, owner, service.ProfileInput{Timezone: &tz}))
	var u models.User
	require.NoError(t, db.First(&u, "id = ?", owner).Error)
	require.Equal(t, tz, u.Timezone)
}
