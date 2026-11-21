package service

import (
	"context"
	"fmt"

	"github.com/maburvm/panel/internal/shared/models"
)

// Applying a plan's current settings to the VMs already on it. A VM copies its
// plan when it is created, so an edited plan reaches existing VMs only through
// this. Allowances (snapshots, backups, extra addresses) are read from the plan
// whenever they are used and need no applying.

// Outcomes of applying a plan to one VM.
const (
	RolloutApplied  = "applied"
	RolloutPartial  = "partial"
	RolloutFailed   = "failed"
	RolloutUpToDate = "up_to_date"
	RolloutSkipped  = "skipped"
)

// PlanRolloutVM is what applying the plan does, or did, to one VM.
type PlanRolloutVM struct {
	ID       string          `json:"id"`
	Hostname string          `json:"hostname"`
	Status   models.VMStatus `json:"status"`
	// Changes are what applying does, in words, the new size included.
	Changes []string `json:"changes"`
	// NeedsStop: the new size waits until the VM is stopped. It is never
	// stopped for it; stop it and apply again.
	NeedsStop bool     `json:"needs_stop"`
	Notes     []string `json:"notes"`
	// Outcome and Errors are filled once applied.
	Outcome string   `json:"outcome,omitempty"`
	Errors  []string `json:"errors,omitempty"`
}

// PlanRollout is the preview, or the result, for every VM on a plan.
type PlanRollout struct {
	PlanID   string          `json:"plan_id"`
	PlanName string          `json:"plan_name"`
	Applied  bool            `json:"applied"`
	VMs      []PlanRolloutVM `json:"vms"`
}

// planDiff is one VM's preview with what applying it has to call.
type planDiff struct {
	PlanRolloutVM
	resize    *models.Resources
	limits    bool
	network   bool
	blockMail bool
}

func mbpsWords(v int64) string {
	if v == 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d Mbps", v)
}

func gbWords(v int64) string {
	if v == 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d GB", v)
}

// diffPlan says what applying plan p changes on vm, whose primary interface
// is n (nil when it has none).
func diffPlan(p *models.Plan, vm *models.VM, n *models.Network) planDiff {
	d := planDiff{PlanRolloutVM: PlanRolloutVM{ID: vm.ID, Hostname: vm.Hostname, Status: vm.Status, Changes: []string{}, Notes: []string{}}}
	if vm.Status == models.VMStatusCreating {
		d.Notes = append(d.Notes, "Being created: apply again once it is ready.")
		return d
	}
	change := func(f string, a ...any) { d.Changes = append(d.Changes, fmt.Sprintf(f, a...)) }

	res := vm.Resources
	res.CPU, res.RAM = p.CPU, p.RAM
	if p.Disk > res.Disk {
		res.Disk = p.Disk
	} else if p.Disk < res.Disk {
		d.Notes = append(d.Notes, fmt.Sprintf("Disk stays at %d GB: a disk only grows.", res.Disk))
	}
	if res.CPU != vm.Resources.CPU {
		change("vCPU %d to %d", vm.Resources.CPU, res.CPU)
	}
	if res.RAM != vm.Resources.RAM {
		change("Memory %d MB to %d MB", vm.Resources.RAM, res.RAM)
	}
	if res.Disk != vm.Resources.Disk {
		change("Disk %d GB to %d GB", vm.Resources.Disk, res.Disk)
	}
	if res != vm.Resources {
		d.resize = &res
		d.NeedsStop = vm.Status != models.VMStatusStopped
	}

	if vm.Limits != p.Limits {
		d.limits = true
		change("CPU and disk limits as on the plan")
	} else if vm.LimitsNote != nil {
		d.limits = true
		change("CPU and disk limits sent to the node again")
	}

	if n != nil {
		policy, have := p.OverQuotaPolicy, n.OverQuotaPolicy
		if policy == "" {
			policy = models.OverQuotaThrottle
		}
		if have == "" {
			have = models.OverQuotaThrottle
		}
		if n.BandwidthLimit != int64(p.BandwidthMbps) {
			change("Speed %s to %s", mbpsWords(n.BandwidthLimit), mbpsWords(int64(p.BandwidthMbps)))
		}
		if n.BandwidthQuotaGB != p.DataQuotaGB {
			change("Monthly transfer %s to %s", gbWords(n.BandwidthQuotaGB), gbWords(p.DataQuotaGB))
		}
		if have != policy || n.ThrottleSpeedMbps != p.ThrottleSpeedMbps {
			change("Over-quota policy as on the plan (%s)", policy)
		}
		d.network = n.BandwidthLimit != int64(p.BandwidthMbps) || n.BandwidthQuotaGB != p.DataQuotaGB ||
			have != policy || n.ThrottleSpeedMbps != p.ThrottleSpeedMbps
	}

	switch {
	case p.SMTPPolicy == models.SMTPBlocked && !vm.SMTPBlocked:
		d.blockMail = true
		change("Block outbound mail on port 25")
	case p.SMTPPolicy != models.SMTPBlocked && vm.SMTPBlocked:
		// It may have been blocked for abuse: never lifted by a plan.
		d.Notes = append(d.Notes, "Outbound mail stays blocked on this VM. Unblock it on the VM's page if that is intended.")
	}
	if d.NeedsStop {
		d.Notes = append(d.Notes, "The new size waits until the VM is stopped. Stop it, then apply again.")
	}
	return d
}

// RolloutPlan previews, or with apply makes, the changes that bring every VM
// on a plan to the plan's current size, CPU and disk limits, speed, transfer
// quota and mail policy, through the same paths a package change uses. A
// running VM keeps its size until it is stopped; nothing is restarted.
// ponytail: synchronous, one VM after another; a queue job if plans reach hundreds of VMs.
func (s *VMService) RolloutPlan(ctx context.Context, planID string, net *NetworkService, apply bool) (*PlanRollout, error) {
	plan, err := s.GetPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	var vms []models.VM
	if err := s.db.WithContext(ctx).Where("plan_id = ? AND status <> ?", plan.ID, models.VMStatusDeleting).
		Order("hostname").Find(&vms).Error; err != nil {
		return nil, fmt.Errorf("failed to list the plan's VMs: %w", err)
	}
	out := &PlanRollout{PlanID: plan.ID, PlanName: plan.Name, Applied: apply, VMs: make([]PlanRolloutVM, 0, len(vms))}
	for i := range vms {
		vm := &vms[i]
		var network *models.Network
		if n, err := s.networkRepo.GetByVMID(ctx, vm.ID); err == nil {
			network = n
		}
		d := diffPlan(plan, vm, network)
		if apply {
			s.applyPlanDiff(ctx, plan, vm, net, &d)
		}
		out.VMs = append(out.VMs, d.PlanRolloutVM)
	}
	return out, nil
}

func (s *VMService) applyPlanDiff(ctx context.Context, p *models.Plan, vm *models.VM, net *NetworkService, d *planDiff) {
	if vm.Status == models.VMStatusCreating {
		d.Outcome = RolloutSkipped
		return
	}
	if len(d.Changes) == 0 {
		d.Outcome = RolloutUpToDate
		return
	}
	tried := 0
	step := func(what string, run func() error) {
		tried++
		if err := run(); err != nil {
			d.Errors = append(d.Errors, what+": "+err.Error())
		}
	}
	if d.limits {
		step("CPU and disk limits", func() error {
			r, err := s.ApplyPlanVMLimits(ctx, vm.ID, p)
			if err == nil && r.Note != nil {
				d.Notes = append(d.Notes, "The CPU and disk limits are saved, but the node has not applied them yet: "+*r.Note)
			}
			return err
		})
	}
	if (d.network || d.blockMail) && net == nil {
		step("Network", func() error { return fmt.Errorf("network changes are not available here") })
	} else {
		if d.network {
			step("Speed and transfer quota", func() error { return net.ApplyPlanLimits(ctx, vm.ID, p) })
		}
		if d.blockMail {
			step("Outbound mail", func() error { return net.SetSMTPBlocked(ctx, vm.ID, true) })
		}
	}
	if d.resize != nil && !d.NeedsStop {
		// Billed: the plan is what was sold, so the account quota does not refuse it.
		step("Size", func() error {
			_, err := s.UpdateVM(ctx, &UpdateVMRequest{VMID: vm.ID, Resources: d.resize, Billed: true})
			return err
		})
	}
	switch {
	case tried == 0: // only a new size, waiting for the VM to stop
		d.Outcome = RolloutSkipped
	case len(d.Errors) == tried:
		d.Outcome = RolloutFailed
	case len(d.Errors) > 0:
		d.Outcome = RolloutPartial
	default:
		d.Outcome = RolloutApplied
	}
}
