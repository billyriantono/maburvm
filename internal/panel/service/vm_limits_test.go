package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/maburvm/panel/internal/panel/repository"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// limitsNode stands in for a node: it records what it was sent and answers
// with resp and err.
type limitsNode struct {
	sent []*pb.SetVMLimitsRequest
	resp *pb.SetVMLimitsResponse
	err  error
}

func (n *limitsNode) send(_ context.Context, _ string, req *pb.SetVMLimitsRequest) (*pb.SetVMLimitsResponse, error) {
	n.sent = append(n.sent, req)
	return n.resp, n.err
}

func limitsService(t *testing.T) (*VMService, *gorm.DB, *limitsNode) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	for _, stmt := range []string{
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, name TEXT NOT NULL, ip_address TEXT, status TEXT, token TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE vms (id TEXT PRIMARY KEY, user_id TEXT, node_id TEXT, hostname TEXT, os_template_id TEXT, resources TEXT, status TEXT,
			limits TEXT NOT NULL DEFAULT '{}', throttle TEXT, limits_note TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`INSERT INTO nodes (id, name, status, token) VALUES ('n1', 'node-a', 'active', 'tok')`,
		`INSERT INTO vms (id, node_id, hostname, resources, status) VALUES ('vm-1', 'n1', 'web', '{"cpu":2,"ram":2048,"disk":20}', 'running')`,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	node := &limitsNode{resp: &pb.SetVMLimitsResponse{Success: true}}
	return &VMService{db: db, vmRepo: repository.NewVMRepository(db), nodeRepo: repository.NewNodeRepository(db),
		logger: slog.Default(), sendLimits: node.send}, db, node
}

func TestSetVMLimits(t *testing.T) {
	ctx := context.Background()
	s, db, node := limitsService(t)

	res, err := s.SetVMLimits(ctx, "vm-1", models.VMLimits{CPUCapPercent: 50, IOPS: 800, MBps: 100})
	require.NoError(t, err)
	assert.True(t, res.Applied)
	require.Len(t, node.sent, 1)
	assert.Equal(t, int32(50), node.sent[0].Limits.CpuCapPercent)
	assert.Equal(t, int64(800), node.sent[0].Limits.TotalIopsSec)
	assert.Equal(t, int64(100*1000000), node.sent[0].Limits.TotalBytesSec)

	vm, err := s.GetVMRecord(ctx, "vm-1")
	require.NoError(t, err)
	assert.Equal(t, models.VMLimits{CPUCapPercent: 50, IOPS: 800, MBps: 100}, vm.Limits, "stored, though the field is read-only on the model")
	assert.Nil(t, vm.LimitsNote)

	_, err = s.SetVMLimits(ctx, "vm-1", models.VMLimits{IOPS: 500, ReadIOPS: 100})
	assert.ErrorIs(t, err, ErrInvalidLimits)
	assert.Len(t, node.sent, 1, "nothing sent for refused limits")

	// An old agent: the limits are kept and the VM says what is left to do.
	node.resp, node.err = nil, status.Error(codes.Unimplemented, "unknown method SetVMLimits")
	res, err = s.SetVMLimits(ctx, "vm-1", models.VMLimits{CPUWeight: 50})
	require.NoError(t, err)
	assert.False(t, res.Applied)
	require.NotNil(t, res.Note)
	assert.Contains(t, *res.Note, "node-a is older than the panel")
	var note *string
	require.NoError(t, db.Raw(`SELECT limits_note FROM vms WHERE id = 'vm-1'`).Scan(&note).Error)
	require.NotNil(t, note)

	// Applied again once the agent is updated: the note goes.
	node.resp, node.err = &pb.SetVMLimitsResponse{Success: true}, nil
	res, err = s.ApplyVMLimits(ctx, "vm-1")
	require.NoError(t, err)
	assert.True(t, res.Applied)
	require.NoError(t, db.Raw(`SELECT limits_note FROM vms WHERE id = 'vm-1'`).Scan(&note).Error)
	assert.Nil(t, note)

	require.NoError(t, db.Exec(`UPDATE vms SET status = 'creating' WHERE id = 'vm-1'`).Error)
	_, err = s.SetVMLimits(ctx, "vm-1", models.VMLimits{})
	assert.ErrorIs(t, err, ErrVMLimitsBusy, "a create in flight would not carry the change")

	_, err = s.SetVMLimits(ctx, "missing", models.VMLimits{})
	assert.ErrorIs(t, err, ErrVMNotFound)
}

func TestThrottleVM(t *testing.T) {
	ctx := context.Background()
	s, _, node := limitsService(t)
	_, err := s.SetVMLimits(ctx, "vm-1", models.VMLimits{CPUCapPercent: 80, ReadIOPS: 1000, WriteIOPS: 200})
	require.NoError(t, err)

	_, err = s.ThrottleVM(ctx, "vm-1", models.VMLimits{})
	assert.ErrorIs(t, err, ErrInvalidLimits, "a throttle that caps nothing")
	_, err = s.ThrottleVM(ctx, "vm-1", models.VMLimits{ReadIOPS: 10})
	assert.ErrorIs(t, err, ErrInvalidLimits, "a throttle is totals only")

	res, err := s.ThrottleVM(ctx, "vm-1", models.VMLimits{CPUCapPercent: 20, IOPS: 300})
	require.NoError(t, err)
	assert.Equal(t, models.VMLimits{CPUCapPercent: 80, ReadIOPS: 1000, WriteIOPS: 200}, res.Limits, "the VM's own limits are kept for the undo")
	assert.Equal(t, models.VMLimits{CPUCapPercent: 20, ReadIOPS: 300, WriteIOPS: 200}, res.Effective)
	last := node.sent[len(node.sent)-1].Limits
	assert.Equal(t, int32(20), last.CpuCapPercent)
	assert.Equal(t, int64(300), last.ReadIopsSec)

	vm, err := s.GetVMRecord(ctx, "vm-1")
	require.NoError(t, err)
	require.NotNil(t, vm.Throttle)

	// Changing the VM's own limits keeps the throttle on top.
	res, err = s.SetVMLimits(ctx, "vm-1", models.VMLimits{CPUCapPercent: 10})
	require.NoError(t, err)
	assert.Equal(t, 10, res.Effective.CPUCapPercent)
	assert.Equal(t, 300, res.Effective.IOPS)

	res, err = s.UnthrottleVM(ctx, "vm-1")
	require.NoError(t, err)
	assert.Nil(t, res.Throttle)
	assert.Equal(t, models.VMLimits{CPUCapPercent: 10}, res.Effective, "back on the VM's own limits")
	last = node.sent[len(node.sent)-1].Limits
	assert.Equal(t, int64(0), last.TotalIopsSec, "the throttle's disk cap is lifted on the node")
	vm, err = s.GetVMRecord(ctx, "vm-1")
	require.NoError(t, err)
	assert.Nil(t, vm.Throttle)

	// The node did not answer: still lifted in the panel, and the VM says so.
	node.resp, node.err = nil, errors.New("connection refused")
	_, err = s.ThrottleVM(ctx, "vm-1", models.VMLimits{CPUCapPercent: 5})
	require.NoError(t, err)
	vm, _ = s.GetVMRecord(ctx, "vm-1")
	require.NotNil(t, vm.LimitsNote)
	assert.Contains(t, *vm.LimitsNote, "did not answer")
}

func TestApplyPlanVMLimits(t *testing.T) {
	ctx := context.Background()
	s, _, node := limitsService(t)
	plan := &models.Plan{Limits: models.VMLimits{CPUCapPercent: 50, IOPS: 1000}}

	res, err := s.ApplyPlanVMLimits(ctx, "vm-1", plan)
	require.NoError(t, err)
	assert.Equal(t, plan.Limits, res.Limits)
	assert.Len(t, node.sent, 1)

	_, err = s.ApplyPlanVMLimits(ctx, "vm-1", plan)
	require.NoError(t, err)
	assert.Len(t, node.sent, 1, "a VM that already has the plan's limits is not sent them again")
}

// A VM ordered on a plan gets the plan's limits; staff may give their own.
func TestCreateVMTakesThePlansLimits(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	user, node, tmpl := seedVMIPAMDependencies(t, db)
	plan := seedPlan(t, db, true)
	require.NoError(t, db.Model(plan).Update("limits", `{"cpu_cap_percent":50,"iops":1000}`).Error)
	svc := newPolicyVMService(db)
	stored := func(id string) models.VMLimits {
		vm, err := svc.GetVMRecord(ctx, id)
		require.NoError(t, err)
		return vm.Limits
	}

	resp, err := svc.CreateVM(ctx, &CreateVMRequest{UserID: user.ID.String(), Hostname: "a.example.test", OSTemplateID: tmpl.ID,
		NodeID: node.ID, PlanID: plan.ID, PlanRequired: true, Limits: &models.VMLimits{}})
	require.NoError(t, err)
	assert.Equal(t, models.VMLimits{CPUCapPercent: 50, IOPS: 1000}, stored(resp.VM.ID), "a customer gets the plan's, whatever the request says")

	resp, err = svc.CreateVM(ctx, &CreateVMRequest{UserID: user.ID.String(), Hostname: "b.example.test", OSTemplateID: tmpl.ID,
		NodeID: node.ID, PlanID: plan.ID, Limits: &models.VMLimits{CPUWeight: 200}})
	require.NoError(t, err)
	assert.Equal(t, models.VMLimits{CPUWeight: 200}, stored(resp.VM.ID), "staff limits win over the plan's")

	iops := 500
	resp, err = svc.CreateVM(ctx, &CreateVMRequest{UserID: user.ID.String(), Hostname: "c.example.test", OSTemplateID: tmpl.ID,
		NodeID: node.ID, Resources: models.Resources{CPU: 1, RAM: 512, Disk: 10, IOPS: &iops}})
	require.NoError(t, err)
	assert.Equal(t, models.VMLimits{IOPS: 500}, stored(resp.VM.ID), "the IOPS figure the API always accepted is now applied")

	_, err = svc.CreateVM(ctx, &CreateVMRequest{UserID: user.ID.String(), Hostname: "d.example.test", OSTemplateID: tmpl.ID,
		NodeID: node.ID, Resources: models.Resources{CPU: 1, RAM: 512, Disk: 10}, Limits: &models.VMLimits{CPUCapPercent: 500}})
	assert.ErrorIs(t, err, ErrInvalidResources)
}

// A copy keeps its source's limits, throttle included.
func TestCopiesKeepTheLimits(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	user, node, tmpl := seedVMIPAMDependencies(t, db)
	src := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "src.example.test", OSTemplateID: tmpl.ID,
		Resources: models.Resources{CPU: 1, RAM: 512, Disk: 10}, Status: models.VMStatusStopped}
	require.NoError(t, db.Create(src).Error)
	require.NoError(t, db.Exec(`UPDATE vms SET limits = '{"cpu_cap_percent":80,"iops":2000}', throttle = '{"cpu_cap_percent":10}' WHERE id = ?`, src.ID).Error)
	svc := newPolicyVMService(db)

	req := &CreateVMRequest{UserID: user.ID.String(), Hostname: "copy.example.test", OSTemplateID: tmpl.ID, NodeID: node.ID,
		Resources: models.Resources{CPU: 1, RAM: 512, Disk: 10}}
	svc.inheritNetworkLimits(ctx, src.ID, req)
	resp, err := svc.CreateVM(ctx, req)
	require.NoError(t, err)
	vm, err := svc.GetVMRecord(ctx, resp.VM.ID)
	require.NoError(t, err)
	assert.Equal(t, models.VMLimits{CPUCapPercent: 10, IOPS: 2000}, vm.Limits)
}
