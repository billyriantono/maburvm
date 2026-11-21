package service

import (
	"context"

	"github.com/maburvm/panel/internal/shared/models"
)

// What the admin VM list does to several VMs at once that a single VM's page
// does not already have a function for.

// PlanMoves works out moving each VM to destNodeID (or the node it fits best
// when empty), counting what earlier VMs in the list take on each
// destination, as a drain plan does.
func (m *MigrationService) PlanMoves(ctx context.Context, vmIDs []string, destNodeID string) []DrainPlanItem {
	env := newCheckEnv()
	out := make([]DrainPlanItem, 0, len(vmIDs))
	for _, id := range vmIDs {
		item := DrainPlanItem{VMID: id, Blockers: []string{}, Warnings: []string{}}
		vm, err := m.vms.GetVMRecord(ctx, id)
		if err != nil {
			item.Blockers = append(item.Blockers, err.Error())
			out = append(out, item)
			continue
		}
		item.Hostname, item.Status = vm.Hostname, string(vm.Status)
		chk, err := m.check(ctx, MigrationRequest{VMID: id, DestNodeID: destNodeID}, env, "")
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
		out = append(out, item)
	}
	return out
}

// StartMoves queues the moves PlanMoves allows. They run on the long-jobs
// queue, a few at a time, like a drain's. The error of each item says why it
// was not queued.
func (m *MigrationService) StartMoves(ctx context.Context, vmIDs []string, destNodeID, requestedBy string) ([]DrainPlanItem, []error) {
	for _, id := range vmIDs {
		m.recoverOrphan(ctx, id)
	}
	items := m.PlanMoves(ctx, vmIDs, destNodeID)
	errs := make([]error, len(items))
	for i := range items {
		if !items[i].CanMove || items[i].check == nil {
			errs[i] = ErrMigrationBlocked
			continue
		}
		_, errs[i] = m.queue(ctx, items[i].check, MigrationRequest{VMID: items[i].VMID, DestNodeID: items[i].DestNodeID, RequestedBy: requestedBy}, nil)
	}
	return items, errs
}

// ApplyPlanToVM puts a VM on a plan and brings it to the plan's size, limits,
// speed, transfer quota and mail policy, as applying a plan does for the
// plan's own VMs. Without apply it only says what would change. A running VM
// keeps its size until it is stopped; nothing is restarted.
func (s *VMService) ApplyPlanToVM(ctx context.Context, vmID, planID string, net *NetworkService, apply bool) (*PlanRolloutVM, error) {
	plan, err := s.GetPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	vm, err := s.GetVMRecord(ctx, vmID)
	if err != nil {
		return nil, err
	}
	var network *models.Network
	if n, err := s.networkRepo.GetByVMID(ctx, vm.ID); err == nil {
		network = n
	}
	d := diffPlan(plan, vm, network)
	relink := vm.PlanID == nil || *vm.PlanID != plan.ID
	if relink && vm.BillingManaged() {
		d.Notes = append(d.Notes, "The billing system bills this VM: change its package there too.")
	}
	if apply && vm.Status != models.VMStatusCreating {
		if relink {
			if _, err := s.SetBillingLink(ctx, vm.ID, &plan.ID, nil); err != nil {
				return nil, err
			}
		}
		s.applyPlanDiff(ctx, plan, vm, net, &d)
		if relink && (d.Outcome == RolloutUpToDate || d.Outcome == RolloutSkipped) {
			d.Outcome = RolloutApplied
		}
	}
	if relink {
		d.Changes = append([]string{"Plan to " + plan.Name}, d.Changes...)
	}
	return &d.PlanRolloutVM, nil
}
