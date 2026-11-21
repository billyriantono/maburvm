package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// MaxBulkVMs is how many VMs one bulk action takes.
const MaxBulkVMs = 100

// BulkRequest is one action on several VMs. Preview says what would happen
// to each without doing it. PlanID, GroupID and NodeID go with apply_plan,
// add_firewall_group and migrate (empty NodeID picks the best node per VM).
type BulkRequest struct {
	Action  string   `json:"action"`
	VMIDs   []string `json:"vm_ids"`
	Preview bool     `json:"preview"`
	PlanID  string   `json:"plan_id,omitempty"`
	GroupID string   `json:"group_id,omitempty"`
	NodeID  string   `json:"node_id,omitempty"`
}

// BulkResult is what happened, or in a preview would happen, to one VM. OK
// in a preview means the action applies to it.
type BulkResult struct {
	VMID     string   `json:"vm_id"`
	Hostname string   `json:"hostname"`
	OK       bool     `json:"ok"`
	Message  string   `json:"message"`
	Changes  []string `json:"changes,omitempty"`
}

// bulkActions are the actions and the permissions each needs, besides the
// admin area itself.
var bulkActions = map[string][]string{
	"start":              {"vm:power"},
	"stop":               {"vm:power"},
	"restart":            {"vm:power"},
	"suspend":            {"vm:lifecycle"},
	"unsuspend":          {"vm:lifecycle"},
	"lock":               {"vm:update"},
	"unlock":             {"vm:update"},
	"apply_plan":         {"vm:update", "billing:update"},
	"add_firewall_group": {"vm:update", "firewall_group:read"},
	"migrate":            {"vm:migrate"},
}

// BulkDeps are the services bulk actions call.
type BulkDeps struct {
	VMs            *service.VMService
	Migrations     *service.MigrationService
	Network        *service.NetworkService
	FirewallGroups *service.FirewallGroupService
}

// RegisterBulkVMRoutes mounts POST /api/v1/vms/bulk, staff only. Each VM is
// changed through the same function its own page uses, so every rule (a
// suspended VM does not start, a move must pass its checks) still holds.
func RegisterBulkVMRoutes(e *echo.Echo, d BulkDeps, db *gorm.DB) {
	e.POST("/api/v1/vms/bulk", func(c echo.Context) error {
		bad := func(msg string) error {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": msg})
		}
		var req BulkRequest
		if err := c.Bind(&req); err != nil {
			return bad("Invalid request body")
		}
		perms, known := bulkActions[req.Action]
		if !known {
			return bad("action must be one of start, stop, restart, suspend, unsuspend, lock, unlock, apply_plan, add_firewall_group or migrate")
		}
		u, _ := middleware.GetUserContext(c)
		for _, p := range perms {
			if !u.Can(p) {
				return c.JSON(http.StatusForbidden, map[string]interface{}{"error": "Forbidden", "message": "Your role may not " + strings.ReplaceAll(req.Action, "_", " ") + " VMs"})
			}
		}
		ids, err := bulkIDs(req.VMIDs)
		if err != nil {
			return bad(err.Error())
		}
		switch {
		case req.Action == "apply_plan" && req.PlanID == "":
			return bad("Choose the plan to apply")
		case req.Action == "add_firewall_group" && req.GroupID == "":
			return bad("Choose the firewall group to add")
		}
		ctx := c.Request().Context()
		if !req.Preview {
			// Started changes finish even if the browser goes away.
			ctx = context.WithoutCancel(ctx)
		}
		var results []BulkResult
		if req.Action == "migrate" {
			results = bulkMigrate(ctx, d, ids, req, u.Email)
		} else {
			results = runBulk(ctx, d, ids, req, u)
		}
		done := 0
		for _, r := range results {
			if r.OK {
				done++
			}
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{
			"action": req.Action, "preview": req.Preview, "results": results, "ok": done, "total": len(results),
		}})
	}, middleware.RequireAuth(db), middleware.RequirePermission(middleware.PermStaff),
		middleware.RequireAnyPermission("vm:power", "vm:lifecycle", "vm:update", "vm:migrate"))
}

func bulkIDs(in []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, id := range in {
		if _, err := uuid.Parse(id); err != nil {
			return nil, fmt.Errorf("%q is not a VM id", id)
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	switch {
	case len(out) == 0:
		return nil, errors.New("Choose at least one VM")
	case len(out) > MaxBulkVMs:
		return nil, fmt.Errorf("At most %d VMs at once", MaxBulkVMs)
	}
	return out, nil
}

// bulkConcurrency bounds how many VMs are changed at the same time.
const bulkConcurrency = 8

// runBulk applies one action to each VM, a few at a time, results in the
// order asked.
func runBulk(ctx context.Context, d BulkDeps, ids []string, req BulkRequest, u *middleware.UserContext) []BulkResult {
	results := make([]BulkResult, len(ids))
	sem := make(chan struct{}, bulkConcurrency)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id string) {
			defer func() { <-sem; wg.Done() }()
			results[i] = bulkOne(ctx, d, id, req, u)
		}(i, id)
	}
	wg.Wait()
	return results
}

func bulkOne(ctx context.Context, d BulkDeps, id string, req BulkRequest, u *middleware.UserContext) BulkResult {
	r := BulkResult{VMID: id}
	vm, err := d.VMs.GetVMRecord(ctx, id)
	if err != nil {
		r.Message = "Not found"
		return r
	}
	r.Hostname = vm.Hostname
	running := vm.Status == models.VMStatusRunning
	busy := vm.Status == models.VMStatusCreating || vm.Status == models.VMStatusDeleting
	skip := func(msg string) BulkResult { r.Message = msg; return r }
	will := func(msg string) BulkResult { r.OK, r.Message = true, msg; return r }
	finish := func(err error, done string) BulkResult {
		if err != nil {
			r.Message = err.Error()
			return r
		}
		r.OK, r.Message = true, done
		return r
	}
	lifecycle := func(cmd service.LifecycleCommand) error {
		_, err := d.VMs.ExecuteLifecycleCommand(ctx, &service.LifecycleRequest{VMID: id, Command: cmd, Async: true})
		return err
	}

	switch req.Action {
	case "start", "stop", "restart":
		switch {
		case busy:
			return skip("Busy (" + string(vm.Status) + "): try again when it is done")
		case req.Action == "start" && running:
			return skip("Already running")
		case req.Action != "start" && !running:
			return skip("Not running")
		case req.Action != "stop" && vm.Suspended:
			return skip("Suspended: unsuspend it first")
		case req.Preview:
			return will(map[string]string{"start": "Will start", "stop": "Will shut down", "restart": "Will restart"}[req.Action])
		}
		cmd := map[string]service.LifecycleCommand{"start": service.LifecycleStart, "stop": service.LifecycleStop, "restart": service.LifecycleRestart}[req.Action]
		return finish(lifecycle(cmd), "Queued")
	case "suspend", "unsuspend":
		on := req.Action == "suspend"
		switch {
		case busy:
			return skip("Busy: try again when its current job is done")
		case on && vm.Suspended:
			return skip("Already suspended")
		case !on && !vm.Suspended:
			return skip("Not suspended")
		case req.Preview && on:
			return will("Will be suspended and powered off")
		case req.Preview:
			return will("Will be unsuspended and started")
		}
		if on {
			return finish(d.VMs.SuspendVM(ctx, id), "Suspended")
		}
		return finish(d.VMs.UnsuspendVM(ctx, id), "Unsuspended")
	case "lock", "unlock":
		lock := req.Action == "lock"
		switch {
		case lock && vm.DeleteLock == models.VMDeleteLockStaff:
			return skip("Already locked by staff")
		case !lock && vm.DeleteLock == models.VMDeleteLockNone:
			return skip("Not locked")
		case req.Preview && lock:
			return will("Will be locked against deletion and reinstall")
		case req.Preview:
			return will("Will be unlocked")
		}
		_, err := d.VMs.SetDeleteLock(ctx, id, lock, true)
		return finish(err, map[bool]string{true: "Locked", false: "Unlocked"}[lock])
	case "apply_plan":
		if d.Network == nil {
			return skip("Applying a plan is not available here")
		}
		out, err := d.VMs.ApplyPlanToVM(ctx, id, req.PlanID, d.Network, !req.Preview)
		if err != nil {
			return skip(err.Error())
		}
		r.Changes = out.Changes
		notes := strings.Join(append(append([]string{}, out.Notes...), out.Errors...), " ")
		switch {
		case req.Preview && len(out.Changes) == 0:
			return skip(strings.TrimSpace("Already on the plan's settings. " + notes))
		case req.Preview:
			return will(strings.TrimSpace("Will change. " + notes))
		case out.Outcome == service.RolloutApplied || out.Outcome == service.RolloutUpToDate:
			r.OK, r.Message = true, strings.TrimSpace("Done. "+notes)
		default:
			r.Message = strings.TrimSpace(strings.ReplaceAll(out.Outcome, "_", " ") + ". " + notes)
		}
		return r
	case "add_firewall_group":
		if d.FirewallGroups == nil {
			return skip("Firewall groups are not available here")
		}
		attached, _ := d.FirewallGroups.ListForVM(ctx, id)
		for _, g := range attached {
			if g.ID == req.GroupID {
				return skip("Already has this group")
			}
		}
		if req.Preview {
			return will("Will get the group")
		}
		viewer := service.GroupViewer{UserID: u.ID.String(), Staff: true, Manage: u.Can("firewall_group:update")}
		return finish(d.FirewallGroups.Attach(ctx, viewer, id, req.GroupID, ""), "Group added")
	}
	return skip("Unknown action")
}

// bulkMigrate plans every move together, so two VMs are never both sent to
// the room only one of them fits in, then queues the ones that can go.
func bulkMigrate(ctx context.Context, d BulkDeps, ids []string, req BulkRequest, by string) []BulkResult {
	if d.Migrations == nil {
		return []BulkResult{{Message: "Moving VMs is not available here"}}
	}
	if req.Preview {
		items := d.Migrations.PlanMoves(ctx, ids, req.NodeID)
		out := make([]BulkResult, len(items))
		for i, it := range items {
			out[i] = moveResult(it, nil, true)
		}
		return out
	}
	items, errs := d.Migrations.StartMoves(ctx, ids, req.NodeID, by)
	out := make([]BulkResult, len(items))
	for i, it := range items {
		out[i] = moveResult(it, errs[i], false)
	}
	return out
}

func moveResult(it service.DrainPlanItem, err error, preview bool) BulkResult {
	r := BulkResult{VMID: it.VMID, Hostname: it.Hostname}
	if !it.CanMove || (!preview && err != nil) {
		msg := strings.Join(it.Blockers, "; ")
		if msg == "" && err != nil {
			msg = err.Error()
		}
		r.Message = msg
		return r
	}
	r.OK = true
	r.Message = fmt.Sprintf("To %s (%s)", it.DestNodeName, it.Mode)
	if !preview {
		r.Message = "Queued: " + r.Message
	}
	if len(it.Warnings) > 0 {
		r.Message += ". " + strings.Join(it.Warnings, "; ")
	}
	return r
}
