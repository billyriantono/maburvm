package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/riverqueue/river"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
)

var (
	// ErrDrainActive is a second drain of a node already draining.
	ErrDrainActive = errors.New("this node is already being drained")
	// ErrNoActiveDrain is an abort with nothing to abort.
	ErrNoActiveDrain = errors.New("this node is not being drained")
)

// drainAttempts is how many times a drain tries to move one VM.
const drainAttempts = 2

// DrainPlanItem is one VM of a drain plan: where it would go, or why it stays.
type DrainPlanItem struct {
	VMID         string   `json:"vm_id"`
	Hostname     string   `json:"hostname"`
	Status       string   `json:"status"`
	Mode         string   `json:"mode"`
	DestNodeID   string   `json:"dest_node_id"`
	DestNodeName string   `json:"dest_node_name"`
	CanMove      bool     `json:"can_move"`
	Blockers     []string `json:"blockers"`
	Warnings     []string `json:"warnings"`

	check *MigrationCheck
}

// DrainPlan is what draining a node would do.
type DrainPlan struct {
	NodeID   string          `json:"node_id"`
	NodeName string          `json:"node_name"`
	Items    []DrainPlanItem `json:"items"`
	Movable  int             `json:"movable"`
	Blocked  int             `json:"blocked"`
}

// PlanDrain works out where each VM on a node would go, counting what earlier
// VMs in the plan already take on each destination. destinations are the
// operator's choices (VM id to node id).
func (m *MigrationService) PlanDrain(ctx context.Context, nodeID string, destinations map[string]string) (*DrainPlan, error) {
	node, err := m.vms.nodeRepo.GetByID(ctx, nodeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, err
	}
	vms, err := m.vms.vmRepo.ListByNodeID(ctx, nodeID, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to list the node's VMs: %w", err)
	}
	sort.Slice(vms, func(i, j int) bool {
		if vms[i].Hostname != vms[j].Hostname {
			return vms[i].Hostname < vms[j].Hostname
		}
		return vms[i].ID < vms[j].ID
	})
	plan := &DrainPlan{NodeID: node.ID, NodeName: node.Name, Items: []DrainPlanItem{}}
	env := newCheckEnv()
	for i := range vms {
		vm := &vms[i]
		item := DrainPlanItem{VMID: vm.ID, Hostname: vm.Hostname, Status: string(vm.Status), Blockers: []string{}, Warnings: []string{}}
		own := ""
		if active, _ := m.activeMigration(ctx, vm.ID); active != nil && active.DrainID != nil {
			own = active.ID // a move this drain already runs is not in its own way
		}
		chk, err := m.check(ctx, MigrationRequest{VMID: vm.ID, DestNodeID: destinations[vm.ID]}, env, own)
		if err != nil {
			item.Blockers = append(item.Blockers, err.Error())
		} else {
			item.check = chk
			item.Mode, item.DestNodeID, item.DestNodeName = chk.Mode, chk.DestNodeID, chk.DestNodeName
			item.CanMove, item.Blockers, item.Warnings = chk.CanMove, chk.Blockers, chk.Warnings
			if chk.CanMove {
				env.add(chk.DestNodeID, chk.DestPoolID, vm, chk.DiskGB)
			}
		}
		if item.CanMove {
			plan.Movable++
		} else {
			plan.Blocked++
		}
		plan.Items = append(plan.Items, item)
	}
	return plan, nil
}

// DrainRequest starts a drain.
type DrainRequest struct {
	// Concurrency is how many VMs move at once, 1 to 3.
	Concurrency  int               `json:"concurrency"`
	Destinations map[string]string `json:"destinations"`
	RequestedBy  string            `json:"-"`
}

// StartDrain pauses new VMs on the node and queues the drain.
func (m *MigrationService) StartDrain(ctx context.Context, nodeID string, req DrainRequest) (*models.NodeDrain, error) {
	if req.Concurrency == 0 {
		req.Concurrency = 1
	}
	if req.Concurrency < 1 || req.Concurrency > 3 {
		return nil, fmt.Errorf("%w: between 1 and 3 VMs can move at once", ErrMigrationBlocked)
	}
	if req.Destinations == nil {
		req.Destinations = map[string]string{}
	}
	if _, err := m.vms.nodeRepo.GetByID(ctx, nodeID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, err
	}
	m.recoverOrphanDrain(ctx, nodeID)
	d := &models.NodeDrain{NodeID: nodeID, Status: models.DrainRunning, Concurrency: req.Concurrency, Destinations: req.Destinations}
	if req.RequestedBy != "" {
		d.RequestedBy = &req.RequestedBy
	}
	if err := m.db.WithContext(ctx).Create(d).Error; err != nil {
		if active, _ := m.activeDrain(ctx, nodeID); active != nil {
			return nil, ErrDrainActive
		}
		return nil, fmt.Errorf("failed to record the drain: %w", err)
	}
	// No new VMs are placed on a node being emptied.
	if err := m.db.WithContext(ctx).Table("nodes").Where("id = ?", nodeID).Update("creation_paused", true).Error; err != nil {
		m.finishDrain(ctx, d, models.DrainAborted, "new VMs could not be paused on the node: "+err.Error())
		return nil, fmt.Errorf("failed to pause new VMs on the node: %w", err)
	}
	if m.river == nil {
		m.finishDrain(ctx, d, models.DrainAborted, "the job queue is not running")
		return nil, fmt.Errorf("the job queue is not running")
	}
	res, err := m.river.Insert(ctx, queue.DrainJob{DrainID: d.ID}, nil)
	if err != nil {
		m.finishDrain(ctx, d, models.DrainAborted, "could not be queued: "+err.Error())
		return nil, fmt.Errorf("failed to queue the drain: %w", err)
	}
	d.JobID = &res.Job.ID
	m.db.WithContext(ctx).Model(d).Update("job_id", res.Job.ID)
	return d, nil
}

func (m *MigrationService) activeDrain(ctx context.Context, nodeID string) (*models.NodeDrain, error) {
	var rows []models.NodeDrain
	if err := m.db.WithContext(ctx).Where("node_id = ? AND status IN ?", nodeID, []string{models.DrainRunning, models.DrainAborting}).
		Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// drainJobAlive reports whether a drain's job can still run it; true when
// that cannot be told.
func (m *MigrationService) drainJobAlive(ctx context.Context, d *models.NodeDrain) bool {
	if d.JobID == nil {
		return time.Since(d.StartedAt) < time.Minute
	}
	var st string
	if m.db.WithContext(ctx).Raw("SELECT state FROM river_job WHERE id = ?", *d.JobID).Scan(&st).Error != nil {
		return true
	}
	return st == "available" || st == "running" || st == "retryable" || st == "scheduled" || st == "pending"
}

// recoverOrphanDrain ends a drain whose job is gone, so it does not block the next.
func (m *MigrationService) recoverOrphanDrain(ctx context.Context, nodeID string) {
	if d, err := m.activeDrain(ctx, nodeID); err == nil && d != nil && !m.drainJobAlive(ctx, d) {
		m.finishDrain(ctx, d, models.DrainAborted, "the panel lost track of this drain")
	}
}

// AbortDrain stops a drain: no further VM starts moving, and the moves under
// way are cancelled. Every VM stays running where it is.
func (m *MigrationService) AbortDrain(ctx context.Context, nodeID string) (*models.NodeDrain, error) {
	d, err := m.activeDrain(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, ErrNoActiveDrain
	}
	m.db.WithContext(ctx).Model(&models.NodeDrain{}).Where("id = ? AND status = ?", d.ID, models.DrainRunning).
		Updates(map[string]interface{}{"status": models.DrainAborting, "updated_at": time.Now()})
	d.Status = models.DrainAborting
	m.cancelDrainMoves(ctx, d.ID)
	if !m.drainJobAlive(ctx, d) {
		m.finishDrain(ctx, d, models.DrainAborted, "")
	}
	return d, nil
}

func (m *MigrationService) cancelDrainMoves(ctx context.Context, drainID string) {
	now := time.Now()
	m.db.WithContext(ctx).Model(&models.VMMigration{}).Where("drain_id = ? AND status = ? AND started_at IS NULL", drainID, models.MigrationQueued).
		Updates(map[string]interface{}{"status": models.MigrationCancelled, "phase": "Cancelled before it started", "finished_at": now, "updated_at": now})
	m.db.WithContext(ctx).Model(&models.VMMigration{}).Where("drain_id = ? AND status IN ?", drainID, []string{models.MigrationQueued, models.MigrationRunning}).
		Updates(map[string]interface{}{"status": models.MigrationCancelling, "updated_at": now})
}

func (m *MigrationService) finishDrain(ctx context.Context, d *models.NodeDrain, st, errMsg string) {
	now := time.Now()
	d.Status, d.Error, d.FinishedAt = st, errMsg, &now
	if err := m.db.WithContext(context.WithoutCancel(ctx)).Model(&models.NodeDrain{}).Where("id = ?", d.ID).
		Updates(map[string]interface{}{"status": st, "error": errMsg, "finished_at": now, "updated_at": now}).Error; err != nil {
		m.logger.ErrorContext(ctx, "could not record the end of a drain", "drain_id", d.ID, "error", err)
	}
}

func (m *MigrationService) drainStatus(ctx context.Context, id string) string {
	var st string
	m.db.WithContext(ctx).Table("node_drains").Select("status").Where("id = ?", id).Scan(&st)
	return st
}

// RunDrainJob runs a queued drain; the queue calls it.
func (m *MigrationService) RunDrainJob(ctx context.Context, drainID string) error {
	var d models.NodeDrain
	if err := m.db.WithContext(ctx).Where("id = ?", drainID).First(&d).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return river.JobCancel(fmt.Errorf("drain %s no longer exists", drainID))
		}
		return err
	}
	if d.Status != models.DrainRunning && d.Status != models.DrainAborting {
		return nil
	}
	if err := m.drain(ctx, &d); errors.Is(err, errInterrupted) {
		return river.JobSnooze(15 * time.Second)
	} else if err != nil {
		return err
	}
	return nil
}

// drain moves the node's VMs, Concurrency at a time, until none is left that
// can move. A VM gets drainAttempts tries; one that cannot move stays put.
func (m *MigrationService) drain(ctx context.Context, d *models.NodeDrain) error {
	for {
		if ctx.Err() != nil {
			return errInterrupted
		}
		if m.drainStatus(ctx, d.ID) == models.DrainAborting {
			m.cancelDrainMoves(ctx, d.ID)
			if err := m.runMoves(ctx, d, m.drainMoves(ctx, d.ID, true)); err != nil {
				return err
			}
			return m.endDrain(ctx, d, true)
		}
		todo := m.drainMoves(ctx, d.ID, true) // moves an earlier try left under way
		tried := map[string]int{}
		for _, r := range m.drainMoves(ctx, d.ID, false) {
			if r.Status != models.MigrationCompleted && r.Status != models.MigrationSkipped {
				tried[r.VMID]++
			}
		}
		inTodo := map[string]bool{}
		for _, r := range todo {
			inTodo[r.VMID] = true
		}
		plan, err := m.PlanDrain(ctx, d.NodeID, d.Destinations)
		if err != nil {
			return errInterrupted
		}
		for _, item := range plan.Items {
			if inTodo[item.VMID] || tried[item.VMID] >= drainAttempts {
				continue
			}
			if !item.CanMove {
				m.recordSkipped(ctx, d, item)
				continue
			}
			row, err := m.queue(ctx, item.check, MigrationRequest{RequestedBy: ptrString(d.RequestedBy)}, &d.ID)
			if err != nil {
				continue // it moves, or is refused, on the next pass
			}
			// It could not move earlier and can now: the report keeps the outcome only.
			m.db.WithContext(ctx).Where("drain_id = ? AND vm_id = ? AND status = ?", d.ID, item.VMID, models.MigrationSkipped).Delete(&models.VMMigration{})
			todo = append(todo, row)
		}
		if len(todo) == 0 {
			return m.endDrain(ctx, d, false)
		}
		if err := m.runMoves(ctx, d, todo); err != nil {
			return err
		}
	}
}

// runMoves runs a drain's moves, Concurrency at a time, starting no new one
// once the drain is aborted.
func (m *MigrationService) runMoves(ctx context.Context, d *models.NodeDrain, rows []*models.VMMigration) error {
	sem := make(chan struct{}, max(1, d.Concurrency))
	var wg sync.WaitGroup
	var mu sync.Mutex
	interrupted := false
	for _, row := range rows {
		sem <- struct{}{}
		if row.Status == models.MigrationQueued && m.drainStatus(ctx, d.ID) == models.DrainAborting {
			<-sem
			continue
		}
		wg.Add(1)
		go func(row *models.VMMigration) {
			defer wg.Done()
			defer func() { <-sem }()
			if m.reload(ctx, row) && row.Active() {
				if err := m.run(ctx, row); errors.Is(err, errInterrupted) {
					mu.Lock()
					interrupted = true
					mu.Unlock()
				}
			}
		}(row)
	}
	wg.Wait()
	if interrupted || ctx.Err() != nil {
		return errInterrupted
	}
	return nil
}

// reload refreshes a move's record; false when it cannot be read.
func (m *MigrationService) reload(ctx context.Context, row *models.VMMigration) bool {
	return m.db.WithContext(ctx).Where("id = ?", row.ID).First(row).Error == nil
}

func ptrString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// drainMoves lists a drain's moves; active only the ones not finished.
func (m *MigrationService) drainMoves(ctx context.Context, drainID string, active bool) []*models.VMMigration {
	q := m.db.WithContext(ctx).Where("drain_id = ?", drainID)
	if active {
		q = q.Where("status IN ?", []string{models.MigrationQueued, models.MigrationRunning, models.MigrationCancelling})
	}
	var rows []models.VMMigration
	q.Order("created_at").Find(&rows)
	out := make([]*models.VMMigration, len(rows))
	for i := range rows {
		out[i] = &rows[i]
	}
	return out
}

// recordSkipped notes, once per drain, a VM the drain cannot move and why.
func (m *MigrationService) recordSkipped(ctx context.Context, d *models.NodeDrain, item DrainPlanItem) {
	var n int64
	m.db.WithContext(ctx).Model(&models.VMMigration{}).Where("drain_id = ? AND vm_id = ? AND status = ?", d.ID, item.VMID, models.MigrationSkipped).Count(&n)
	if n > 0 {
		return
	}
	now := time.Now()
	row := &models.VMMigration{VMID: item.VMID, SourceNodeID: d.NodeID, DrainID: &d.ID, Mode: item.Mode, Status: models.MigrationSkipped,
		Phase: "Not moved", Error: joinReasons(item.Blockers), RequestedBy: d.RequestedBy, FinishedAt: &now}
	if item.DestNodeID != "" {
		row.DestNodeID = &item.DestNodeID
	}
	if err := m.db.WithContext(ctx).Create(row).Error; err != nil {
		m.logger.WarnContext(ctx, "could not record a VM the drain cannot move", "vm_id", item.VMID, "error", err)
	}
}

func joinReasons(r []string) string {
	out := ""
	for i, s := range r {
		if i > 0 {
			out += "; "
		}
		out += s
	}
	return out
}

// endDrain records how the drain ended and raises an alert when VMs stayed.
func (m *MigrationService) endDrain(ctx context.Context, d *models.NodeDrain, aborted bool) error {
	var left int64
	m.db.WithContext(ctx).Model(&models.VM{}).Where("node_id = ?", d.NodeID).Count(&left)
	st := models.DrainCompleted
	switch {
	case aborted:
		st = models.DrainAborted
	case left > 0:
		st = models.DrainIncomplete
	}
	msg := ""
	if left > 0 {
		msg = fmt.Sprintf("%d VMs are still on the node", left)
	}
	m.finishDrain(ctx, d, st, msg)
	if st == models.DrainIncomplete {
		name := d.NodeID
		m.db.WithContext(ctx).Raw("SELECT name FROM nodes WHERE id = ?", d.NodeID).Scan(&name)
		m.alerts.Event(context.WithoutCancel(ctx), AlertInput{
			Key: "drain_incomplete:" + d.NodeID, Kind: models.AlertKindJobFailed, Severity: models.AlertSeverityWarning,
			Title: "Drain of " + name + " finished with VMs left on it", Detail: msg + "; the drain report on the node's page says why each one stayed.",
			ResourceType: "node", ResourceID: d.NodeID, ResourceName: name,
		})
	}
	return nil
}

// DrainReport is a node's latest drain and what happened to each VM.
type DrainReport struct {
	Drain     *models.NodeDrain `json:"drain"`
	Items     []DrainReportItem `json:"items"`
	Moved     int               `json:"moved"`
	Failed    int               `json:"failed"`
	Skipped   int               `json:"skipped"`
	Remaining int               `json:"remaining"`
}

// DrainReportItem is one move of a drain.
type DrainReportItem struct {
	models.VMMigration
	Hostname     string `json:"hostname"`
	DestNodeName string `json:"dest_node_name"`
}

// LatestDrain reports a node's newest drain; Drain is nil when it was never drained.
func (m *MigrationService) LatestDrain(ctx context.Context, nodeID string) (*DrainReport, error) {
	var rows []models.NodeDrain
	if err := m.db.WithContext(ctx).Where("node_id = ?", nodeID).Order("started_at DESC").Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	rep := &DrainReport{Items: []DrainReportItem{}}
	var left int64
	m.db.WithContext(ctx).Model(&models.VM{}).Where("node_id = ?", nodeID).Count(&left)
	rep.Remaining = int(left)
	if len(rows) == 0 {
		return rep, nil
	}
	rep.Drain = &rows[0]
	for _, r := range m.drainMoves(ctx, rep.Drain.ID, false) {
		item := DrainReportItem{VMMigration: *r}
		m.db.WithContext(ctx).Raw("SELECT hostname FROM vms WHERE id = ?", r.VMID).Scan(&item.Hostname)
		if r.DestNodeID != nil {
			m.db.WithContext(ctx).Raw("SELECT name FROM nodes WHERE id = ?", *r.DestNodeID).Scan(&item.DestNodeName)
		}
		switch r.Status {
		case models.MigrationCompleted:
			rep.Moved++
		case models.MigrationFailed, models.MigrationCancelled:
			rep.Failed++
		case models.MigrationSkipped:
			rep.Skipped++
		}
		rep.Items = append(rep.Items, item)
	}
	return rep, nil
}
