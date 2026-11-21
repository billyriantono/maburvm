package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
)

// A customer asks for one more address on a VM. Depending on the portal
// settings staff decide each request, or it is granted at once; either way
// the VM's plan allowance applies, and an address is only ever handed out by
// the same allocation staff use (NetworkService.AssignIPAddress, AssignIPv6Prefix).

// IP request states.
const (
	IPRequestPending  = "pending"
	IPRequestGranting = "granting" // claimed by one approval, being assigned
	IPRequestGranted  = "granted"
	IPRequestRejected = "rejected"
	IPRequestCanceled = "canceled"
)

// IP request families.
const (
	IPRequestIPv4 = "ipv4"
	IPRequestIPv6 = "ipv6"
)

var (
	ErrIPRequestsOff     = errors.New("requests for more addresses are not taken here; contact support")
	ErrIPRequestOpen     = errors.New("this VM already has an open request for that kind of address")
	ErrIPRequestNotFound = errors.New("address request not found")
	ErrIPRequestDecided  = errors.New("this request has already been decided")
)

// IPRequest is one request.
type IPRequest struct {
	ID        string     `json:"id" gorm:"primaryKey"`
	VMID      string     `json:"vm_id"`
	UserID    string     `json:"user_id"`
	Family    string     `json:"family"`
	Reason    string     `json:"reason"`
	Status    string     `json:"status"`
	Address   string     `json:"address"`
	Note      string     `json:"note"`
	DecidedBy *string    `json:"decided_by,omitempty"`
	DecidedAt *time.Time `json:"decided_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	// Filled for the staff queue.
	Hostname string `json:"hostname,omitempty" gorm:"-"`
	Email    string `json:"email,omitempty" gorm:"-"`
}

func (IPRequest) TableName() string { return "ip_requests" }

// IPRequestService takes and decides address requests.
type IPRequestService struct {
	db  *gorm.DB
	net *NetworkService
}

// NewIPRequestService wires the service; net must have address allocation.
func NewIPRequestService(db *gorm.DB, net *NetworkService) *IPRequestService {
	return &IPRequestService{db: db, net: net}
}

// checkAllowance refuses a request the VM's plan would refuse at assignment,
// so the customer hears it now rather than after a wait.
func (s *IPRequestService) checkAllowance(ctx context.Context, vm *models.VM, family string) error {
	if family == IPRequestIPv6 {
		if have, err := vmIPv6(ctx, s.db, vm.ID); err != nil {
			return err
		} else if have != nil {
			return ErrVMAlreadyHasIPv6Prefix
		}
		return checkVMAllowance(ctx, s.db, vm, 0, func(p *models.Plan) *int { return p.MaxExtraIPv6 }, "IPv6", "IPv6", "")
	}
	var have int64
	if err := s.db.WithContext(ctx).Model(&models.Network{}).Where("vm_id = ?", vm.ID).Count(&have).Error; err != nil {
		return err
	}
	if have == 0 {
		return nil
	}
	return checkVMAllowance(ctx, s.db, vm, have-1, func(p *models.Plan) *int { return p.MaxExtraIPv4 },
		"extra IPv4 address", "extra IPv4 addresses", "Release one, or ask support about a plan with more.")
}

// Request records a customer's request and, when the portal grants them at
// once, assigns the address. A grant that fails stays pending for staff.
func (s *IPRequestService) Request(ctx context.Context, vm *models.VM, requesterID, family, reason string) (*IPRequest, error) {
	mode := LoadPortalSettings(ctx, s.db).IPRequestMode()
	if mode == IPRequestsOff {
		return nil, ErrIPRequestsOff
	}
	if family != IPRequestIPv4 && family != IPRequestIPv6 {
		return nil, fmt.Errorf("family must be ipv4 or ipv6")
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > 500 {
		return nil, fmt.Errorf("the reason can be up to 500 characters")
	}
	if err := s.checkAllowance(ctx, vm, family); err != nil {
		return nil, err
	}
	var open int64
	if err := s.db.WithContext(ctx).Model(&IPRequest{}).
		Where("vm_id = ? AND family = ? AND status IN ?", vm.ID, family, []string{IPRequestPending, IPRequestGranting}).
		Count(&open).Error; err != nil {
		return nil, err
	}
	if open > 0 {
		return nil, ErrIPRequestOpen
	}
	now := time.Now().UTC()
	r := &IPRequest{ID: uuid.New().String(), VMID: vm.ID, UserID: requesterID, Family: family, Reason: reason,
		Status: IPRequestPending, CreatedAt: now, UpdatedAt: now}
	if mode == IPRequestsAuto && family == IPRequestIPv4 {
		// A plan that sets no number would let one customer empty the pool a
		// request at a time; staff decide those. (IPv6 is one per VM anyway.)
		if plan, err := vmPlan(ctx, s.db, vm); err != nil {
			return nil, err
		} else if plan == nil || plan.MaxExtraIPv4 == nil {
			mode, r.Note = IPRequestsApproval, "Waiting for staff: this VM's plan sets no number of extra IPv4 addresses to grant at once."
		}
	}
	if mode == IPRequestsAuto {
		r.Status = IPRequestGranting
	}
	if err := s.db.WithContext(ctx).Create(r).Error; err != nil {
		return nil, err
	}
	if mode == IPRequestsAuto {
		return s.grant(ctx, r, nil)
	}
	return r, nil
}

// assign gives the VM the address a request is for.
func (s *IPRequestService) assign(ctx context.Context, r *IPRequest) (string, error) {
	if s.net == nil || s.net.ipamService == nil {
		return "", fmt.Errorf("address allocation is not configured")
	}
	if r.Family == IPRequestIPv4 {
		n, err := s.net.AssignIPAddress(ctx, r.VMID, "", "")
		if n != nil {
			return hostOnlyIP(n.IPAddress), err
		}
		return "", err
	}
	var vm models.VM
	if err := s.db.WithContext(ctx).First(&vm, "id = ?", r.VMID).Error; err != nil {
		return "", ErrVMNotFound
	}
	pools, err := s.net.ipamService.ListPoolsForNode(ctx, vm.NodeID)
	if err != nil {
		return "", err
	}
	for i := range pools {
		p := pools[i]
		if p.Family != models.IPFamilyIPv6 || p.NAT {
			continue
		}
		v6, aerr := s.net.AssignIPv6Prefix(ctx, r.VMID, p.ID)
		if errors.Is(aerr, ErrNoAvailableIPv6Prefix) {
			continue // a full pool: try the next
		}
		if v6 != nil {
			if v6.Prefix != "" {
				return v6.Prefix, aerr
			}
			return v6.Address, aerr
		}
		return "", aerr
	}
	return "", fmt.Errorf("no IPv6 pool with room is available on this VM's node")
}

// grant assigns a claimed request's address and records how it went. by is
// the staff member who approved it, nil for an automatic grant.
func (s *IPRequestService) grant(ctx context.Context, r *IPRequest, by *string) (*IPRequest, error) {
	addr, err := s.assign(ctx, r)
	now := time.Now().UTC()
	if addr == "" && err != nil {
		// Nothing was assigned: back to the queue, with why.
		note := "Could not be assigned: " + err.Error()
		if len(note) > 500 {
			note = note[:500]
		}
		r.Status, r.Note, r.UpdatedAt = IPRequestPending, note, now
		_ = s.db.WithContext(ctx).Model(&IPRequest{}).Where("id = ?", r.ID).
			Updates(map[string]any{"status": r.Status, "note": r.Note, "updated_at": now}).Error
		return r, err
	}
	// assigned; err, if any, is only that the node is not configured yet,
	// which the VM's page reports.
	r.Status, r.Address, r.DecidedBy, r.DecidedAt, r.UpdatedAt = IPRequestGranted, addr, by, &now, now
	if uerr := s.db.WithContext(ctx).Model(&IPRequest{}).Where("id = ?", r.ID).
		Updates(map[string]any{"status": r.Status, "address": addr, "decided_by": by, "decided_at": now, "updated_at": now}).Error; uerr != nil {
		return r, uerr
	}
	s.tell(ctx, r, fmt.Sprintf("Your request for another %s address was granted: %s", familyWord(r.Family), addr))
	return r, nil
}

func familyWord(f string) string {
	if f == IPRequestIPv6 {
		return "IPv6"
	}
	return "IPv4"
}

// tell notifies the VM's owner, and the member who asked if that was someone else.
func (s *IPRequestService) tell(ctx context.Context, r *IPRequest, title string) {
	var owner struct{ UserID string }
	_ = s.db.WithContext(ctx).Table("vms").Select("user_id").Where("id = ?", r.VMID).Scan(&owner).Error
	addNotice(ctx, s.db, owner.UserID, "ip_request", title, "vm", r.VMID)
	if r.UserID != owner.UserID {
		addNotice(ctx, s.db, r.UserID, "ip_request", title, "vm", r.VMID)
	}
}

// claim moves a pending request to `to`, so two staff deciding at once
// cannot both act on it.
func (s *IPRequestService) claim(ctx context.Context, id, to string) (*IPRequest, error) {
	res := s.db.WithContext(ctx).Model(&IPRequest{}).Where("id = ? AND status = ?", id, IPRequestPending).
		Updates(map[string]any{"status": to, "updated_at": time.Now().UTC()})
	if res.Error != nil {
		return nil, res.Error
	}
	var r IPRequest
	if err := s.db.WithContext(ctx).First(&r, "id = ?", id).Error; err != nil {
		return nil, ErrIPRequestNotFound
	}
	if res.RowsAffected == 0 {
		return nil, ErrIPRequestDecided
	}
	return &r, nil
}

// Approve grants a pending request.
func (s *IPRequestService) Approve(ctx context.Context, id, staffID string) (*IPRequest, error) {
	r, err := s.claim(ctx, id, IPRequestGranting)
	if err != nil {
		return nil, err
	}
	return s.grant(ctx, r, &staffID)
}

// Reject turns a pending request down, with a note the customer reads.
func (s *IPRequestService) Reject(ctx context.Context, id, staffID, note string) (*IPRequest, error) {
	note = strings.TrimSpace(note)
	if len(note) > 500 {
		return nil, fmt.Errorf("the note can be up to 500 characters")
	}
	r, err := s.claim(ctx, id, IPRequestRejected)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	r.Note, r.DecidedBy, r.DecidedAt = note, &staffID, &now
	if err := s.db.WithContext(ctx).Model(&IPRequest{}).Where("id = ?", r.ID).
		Updates(map[string]any{"note": note, "decided_by": staffID, "decided_at": now}).Error; err != nil {
		return nil, err
	}
	title := fmt.Sprintf("Your request for another %s address was declined", familyWord(r.Family))
	if note != "" {
		title += ": " + note
	}
	s.tell(ctx, r, title)
	return r, nil
}

// Cancel withdraws a pending request of the VM.
func (s *IPRequestService) Cancel(ctx context.Context, vmID, id string) error {
	res := s.db.WithContext(ctx).Model(&IPRequest{}).Where("id = ? AND vm_id = ? AND status = ?", id, vmID, IPRequestPending).
		Updates(map[string]any{"status": IPRequestCanceled, "updated_at": time.Now().UTC()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrIPRequestNotFound
	}
	return nil
}

// ListForVM is a VM's requests, newest first.
func (s *IPRequestService) ListForVM(ctx context.Context, vmID string) ([]IPRequest, error) {
	rows := []IPRequest{}
	err := s.db.WithContext(ctx).Where("vm_id = ?", vmID).Order("created_at DESC").Limit(50).Find(&rows).Error
	return rows, err
}

// List is the staff queue: requests in a state ("" for all), oldest pending
// first, with the VM's name and the owner's email.
func (s *IPRequestService) List(ctx context.Context, status string, limit, offset int) ([]IPRequest, int64, error) {
	q := s.db.WithContext(ctx).Model(&IPRequest{})
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := []IPRequest{}
	order := "created_at DESC"
	if status == IPRequestPending {
		order = "created_at ASC"
	}
	if err := q.Order(order).Limit(limit).Offset(offset).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	for i := range rows {
		var v struct{ Hostname, Email string }
		_ = s.db.WithContext(ctx).Raw(`SELECT v.hostname, coalesce(u.email, '') AS email FROM vms v LEFT JOIN users u ON u.id = v.user_id WHERE v.id = ?`,
			rows[i].VMID).Scan(&v).Error
		rows[i].Hostname, rows[i].Email = v.Hostname, v.Email
	}
	return rows, total, nil
}

// PendingIPRequests counts the queue, for the admin menu.
func PendingIPRequests(ctx context.Context, db *gorm.DB) int64 {
	var n int64
	db.WithContext(ctx).Model(&IPRequest{}).Where("status = ?", IPRequestPending).Count(&n)
	return n
}
