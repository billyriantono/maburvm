package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
	"gorm.io/gorm"
)

// A VM's CPU and disk caps: its own limits (from its plan, or set by staff)
// and an operator's throttle on top. The panel stores them, then has the node
// apply them; a VM's limits_note says when the node does not have them.

var (
	// ErrVMLimitsBusy refuses a change while the VM is being created or
	// deleted: the job in flight would not carry it.
	ErrVMLimitsBusy = errors.New("the VM is being created or deleted; change its limits once that is done")
	// ErrInvalidLimits is a limit out of range, or a throttle that caps nothing.
	ErrInvalidLimits = errors.New("invalid limits")
)

// LimitsResult is a VM's limits after a change, and whether its node has them.
type LimitsResult struct {
	Limits    models.VMLimits  `json:"limits"`
	Throttle  *models.VMLimits `json:"throttle"`
	Effective models.VMLimits  `json:"effective"`
	Applied   bool             `json:"applied"`
	Note      *string          `json:"note"`
}

func limitsJSON(l *models.VMLimits) interface{} {
	if l == nil {
		return nil
	}
	b, _ := json.Marshal(l)
	return string(b)
}

// writeVMLimits stores a VM's own limits. By table name: the field is
// read-only on the model.
func writeVMLimits(tx *gorm.DB, vmID string, l models.VMLimits) error {
	if err := tx.Table("vms").Where("id = ?", vmID).Update("limits", limitsJSON(&l)).Error; err != nil {
		return fmt.Errorf("failed to store the VM's limits: %w", err)
	}
	return nil
}

func (s *VMService) limitsVM(ctx context.Context, vmID string) (*models.VM, error) {
	vm, err := s.GetVMRecord(ctx, vmID)
	if err != nil {
		return nil, err
	}
	if vm.Status == models.VMStatusCreating || vm.Status == models.VMStatusDeleting {
		return nil, ErrVMLimitsBusy
	}
	return vm, nil
}

// SetVMLimits replaces a VM's own limits and has its node apply them. A
// throttle in place stays on top.
func (s *VMService) SetVMLimits(ctx context.Context, vmID string, l models.VMLimits) (*LimitsResult, error) {
	if err := l.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidLimits, err)
	}
	vm, err := s.limitsVM(ctx, vmID)
	if err != nil {
		return nil, err
	}
	if err := writeVMLimits(s.db.WithContext(ctx), vm.ID, l); err != nil {
		return nil, err
	}
	vm.Limits = l
	return s.applyLimits(ctx, vm), nil
}

// ApplyPlanVMLimits gives a VM its plan's limits, as a package change does. A
// VM that already has them, and whose node has them, is left alone.
func (s *VMService) ApplyPlanVMLimits(ctx context.Context, vmID string, plan *models.Plan) (*LimitsResult, error) {
	vm, err := s.GetVMRecord(ctx, vmID)
	if err != nil {
		return nil, err
	}
	if vm.Limits == plan.Limits && vm.LimitsNote == nil {
		return limitsResult(vm), nil
	}
	return s.SetVMLimits(ctx, vmID, plan.Limits)
}

// ThrottleVM puts an operator's caps on a VM on top of its own limits, until
// UnthrottleVM. A throttle is totals only: a CPU cap, disk IOPS, disk MB/s and
// optionally a CPU priority.
func (s *VMService) ThrottleVM(ctx context.Context, vmID string, t models.VMLimits) (*LimitsResult, error) {
	if t.ReadIOPS != 0 || t.WriteIOPS != 0 || t.ReadMBps != 0 || t.WriteMBps != 0 {
		return nil, fmt.Errorf("%w: a throttle takes a total IOPS and throughput, not read and write limits", ErrInvalidLimits)
	}
	if t.CPUCapPercent == 0 && t.IOPS == 0 && t.MBps == 0 {
		return nil, fmt.Errorf("%w: a throttle needs a CPU cap, an IOPS limit or a throughput limit", ErrInvalidLimits)
	}
	if err := t.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidLimits, err)
	}
	vm, err := s.limitsVM(ctx, vmID)
	if err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Table("vms").Where("id = ?", vm.ID).Update("throttle", limitsJSON(&t)).Error; err != nil {
		return nil, fmt.Errorf("failed to store the throttle: %w", err)
	}
	vm.Throttle = &t
	return s.applyLimits(ctx, vm), nil
}

// UnthrottleVM lifts an operator's throttle: the VM is back on its own limits.
func (s *VMService) UnthrottleVM(ctx context.Context, vmID string) (*LimitsResult, error) {
	vm, err := s.limitsVM(ctx, vmID)
	if err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Table("vms").Where("id = ?", vm.ID).Update("throttle", nil).Error; err != nil {
		return nil, fmt.Errorf("failed to lift the throttle: %w", err)
	}
	vm.Throttle = nil
	return s.applyLimits(ctx, vm), nil
}

// ApplyVMLimits sends a VM's limits to its node again, after a node that did
// not answer is back or its agent was updated.
func (s *VMService) ApplyVMLimits(ctx context.Context, vmID string) (*LimitsResult, error) {
	vm, err := s.limitsVM(ctx, vmID)
	if err != nil {
		return nil, err
	}
	return s.applyLimits(ctx, vm), nil
}

func limitsResult(vm *models.VM) *LimitsResult {
	return &LimitsResult{
		Limits: vm.Limits, Throttle: vm.Throttle, Effective: vm.Limits.WithThrottle(vm.Throttle),
		Applied: vm.LimitsNote == nil, Note: vm.LimitsNote,
	}
}

// applyLimits has the VM's node apply what it should run under now, and
// records whether it did. Never an error: the limits are stored either way,
// and the note says what is left to do.
func (s *VMService) applyLimits(ctx context.Context, vm *models.VM) *LimitsResult {
	req := &pb.SetVMLimitsRequest{VmId: vm.ID, Limits: queue.LimitsProto(vm.Limits.WithThrottle(vm.Throttle))}
	nodeName := vm.NodeID
	var resp *pb.SetVMLimitsResponse
	node, err := s.nodeRepo.GetByID(ctx, vm.NodeID)
	if err == nil {
		nodeName = node.Name
		send := s.sendLimits
		if send == nil {
			send = s.sendLimitsToNode
		}
		resp, err = send(ctx, vm.NodeID, req)
	}
	vm.LimitsNote = queue.LimitsNote(nodeName, resp, err)
	if werr := s.db.WithContext(ctx).Table("vms").Where("id = ?", vm.ID).Update("limits_note", vm.LimitsNote).Error; werr != nil {
		s.logger.ErrorContext(ctx, "failed to record whether the node has the VM's limits", "vm_id", vm.ID, "error", werr)
	}
	if vm.LimitsNote != nil {
		s.logger.WarnContext(ctx, "VM limits not applied on its node", "vm_id", vm.ID, "note", *vm.LimitsNote)
	}
	return limitsResult(vm)
}

func (s *VMService) sendLimitsToNode(ctx context.Context, nodeID string, req *pb.SetVMLimitsRequest) (*pb.SetVMLimitsResponse, error) {
	client, err := s.getAgentClient(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	authCtx, err := s.agentAuthContext(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	authCtx, cancel := context.WithTimeout(authCtx, 15*time.Second)
	defer cancel()
	return client.SetVMLimits(authCtx, req)
}
