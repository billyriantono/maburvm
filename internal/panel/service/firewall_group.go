package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
)

var (
	ErrFirewallGroupNotFound  = errors.New("firewall group not found")
	ErrFirewallGroupInUse     = errors.New("firewall group is in use")
	ErrFirewallGroupAttached  = errors.New("this group is already attached to the VM")
	ErrFirewallGroupForbidden = errors.New("this group is managed by staff")
	ErrInvalidFirewallGroup   = errors.New("invalid firewall group")
)

// MaxFirewallGroupsPerUser is how many groups one customer may keep.
const MaxFirewallGroupsPerUser = 20

// FirewallGroupService manages firewall groups and their attachment to VMs.
// Every change that alters what a VM enforces is pushed to it through the
// network-config job.
type FirewallGroupService struct {
	db  *gorm.DB
	fw  *repository.FirewallRepository
	net *NetworkService
}

func NewFirewallGroupService(db *gorm.DB, net *NetworkService) *FirewallGroupService {
	return &FirewallGroupService{db: db, fw: repository.NewFirewallRepository(db), net: net}
}

// GroupViewer is who is asking: staff (Manage when they may change groups)
// see every group; a customer only their own.
type GroupViewer struct {
	UserID string
	Staff  bool
	Manage bool
}

// FirewallGroupSummary is a group with how big and how used it is.
type FirewallGroupSummary struct {
	models.FirewallGroup
	OwnerEmail string `json:"owner_email,omitempty" gorm:"column:owner_email"`
	RuleCount  int64  `json:"rule_count" gorm:"column:rule_count"`
	VMCount    int64  `json:"vm_count" gorm:"column:vm_count"`
}

// FirewallGroupVM is a VM a group is attached to.
type FirewallGroupVM struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	Position string `json:"position"`
}

// FirewallGroupDetail is a group with its rules and the VMs it is on.
type FirewallGroupDetail struct {
	FirewallGroupSummary
	Rules []models.FirewallGroupRule `json:"rules"`
	VMs   []FirewallGroupVM          `json:"vms"`
}

// AttachedFirewallGroup is a group as attached to one VM.
type AttachedFirewallGroup struct {
	ID        string                     `json:"id"`
	Name      string                     `json:"name"`
	Global    bool                       `json:"global"`
	Position  string                     `json:"position"`
	SortOrder int                        `json:"sort_order"`
	Rules     []models.FirewallGroupRule `json:"rules"`
}

// summaries selects groups with their owner, rule count and live VM count.
func (s *FirewallGroupService) summaries(ctx context.Context) *gorm.DB {
	return s.db.WithContext(ctx).Table("firewall_groups g").
		Select(`g.*, u.email AS owner_email,
			(SELECT COUNT(*) FROM firewall_group_rules r WHERE r.group_id = g.id) AS rule_count,
			(SELECT COUNT(*) FROM vm_firewall_groups a JOIN vms m ON m.id = a.vm_id AND m.deleted_at IS NULL WHERE a.group_id = g.id) AS vm_count`).
		Joins("LEFT JOIN users u ON u.id = g.user_id")
}

// List returns the groups the viewer may see; globalOnly narrows staff to the
// groups they manage for everyone (what a plan or any VM can use).
func (s *FirewallGroupService) List(ctx context.Context, v GroupViewer, globalOnly bool) ([]FirewallGroupSummary, error) {
	q := s.summaries(ctx).Order("g.user_id IS NOT NULL, g.name")
	switch {
	case !v.Staff:
		q = q.Where("g.user_id = ?", v.UserID)
	case globalOnly:
		q = q.Where("g.user_id IS NULL")
	}
	var out []FirewallGroupSummary
	return out, q.Scan(&out).Error
}

// load finds a group the viewer may see.
func (s *FirewallGroupService) load(ctx context.Context, v GroupViewer, id string) (*models.FirewallGroup, error) {
	var g models.FirewallGroup
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&g).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrFirewallGroupNotFound
		}
		return nil, err
	}
	if !v.Staff && (g.UserID == nil || *g.UserID != v.UserID) {
		return nil, ErrFirewallGroupNotFound
	}
	return &g, nil
}

// loadForChange finds a group the viewer may change: a customer their own,
// staff with the right to manage groups any.
func (s *FirewallGroupService) loadForChange(ctx context.Context, v GroupViewer, id string) (*models.FirewallGroup, error) {
	g, err := s.load(ctx, v, id)
	if err != nil {
		return nil, err
	}
	if v.Staff && !v.Manage {
		return nil, ErrFirewallGroupForbidden
	}
	return g, nil
}

// Get returns a group with its rules and the VMs it is attached to; a
// customer sees only their own VMs.
func (s *FirewallGroupService) Get(ctx context.Context, v GroupViewer, id string) (*FirewallGroupDetail, error) {
	if _, err := s.load(ctx, v, id); err != nil {
		return nil, err
	}
	var sums []FirewallGroupSummary
	if err := s.summaries(ctx).Where("g.id = ?", id).Scan(&sums).Error; err != nil {
		return nil, err
	}
	if len(sums) == 0 {
		return nil, ErrFirewallGroupNotFound
	}
	d := &FirewallGroupDetail{FirewallGroupSummary: sums[0], Rules: []models.FirewallGroupRule{}, VMs: []FirewallGroupVM{}}
	if err := s.db.WithContext(ctx).Where("group_id = ?", id).Order("priority ASC, created_at ASC").Find(&d.Rules).Error; err != nil {
		return nil, err
	}
	q := s.db.WithContext(ctx).Table("vm_firewall_groups a").Select("m.id, m.hostname, a.position").
		Joins("JOIN vms m ON m.id = a.vm_id AND m.deleted_at IS NULL").Where("a.group_id = ?", id).Order("m.hostname")
	if !v.Staff {
		q = q.Where("m.user_id = ?", v.UserID)
	}
	if err := q.Scan(&d.VMs).Error; err != nil {
		return nil, err
	}
	return d, nil
}

func cleanGroupName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return "", fmt.Errorf("%w: a name of 1 to 100 characters is required", ErrInvalidFirewallGroup)
	}
	return name, nil
}

// Create makes a group: staff make one for everyone, a customer one of their own.
func (s *FirewallGroupService) Create(ctx context.Context, v GroupViewer, name, description string) (*models.FirewallGroup, error) {
	name, err := cleanGroupName(name)
	if err != nil {
		return nil, err
	}
	g := &models.FirewallGroup{Name: name, Description: strings.TrimSpace(description)}
	if v.Staff {
		if !v.Manage {
			return nil, ErrFirewallGroupForbidden
		}
	} else {
		var held int64
		if err := s.db.WithContext(ctx).Model(&models.FirewallGroup{}).Where("user_id = ?", v.UserID).Count(&held).Error; err != nil {
			return nil, err
		}
		if held >= MaxFirewallGroupsPerUser {
			return nil, fmt.Errorf("%w: an account may keep at most %d groups", ErrInvalidFirewallGroup, MaxFirewallGroupsPerUser)
		}
		uid := v.UserID
		g.UserID = &uid
	}
	if err := s.db.WithContext(ctx).Create(g).Error; err != nil {
		return nil, fmt.Errorf("failed to create the group: %w", err)
	}
	return g, nil
}

// Update renames a group or changes its description.
func (s *FirewallGroupService) Update(ctx context.Context, v GroupViewer, id, name, description string) (*models.FirewallGroup, error) {
	g, err := s.loadForChange(ctx, v, id)
	if err != nil {
		return nil, err
	}
	if g.Name, err = cleanGroupName(name); err != nil {
		return nil, err
	}
	g.Description = strings.TrimSpace(description)
	if err := s.db.WithContext(ctx).Model(&models.FirewallGroup{}).Where("id = ?", id).
		Updates(map[string]interface{}{"name": g.Name, "description": g.Description, "updated_at": gorm.Expr("NOW()")}).Error; err != nil {
		return nil, err
	}
	return g, nil
}

// Delete removes a group no VM and no plan uses: removing one in use would
// change the firewall of machines nobody is looking at.
func (s *FirewallGroupService) Delete(ctx context.Context, v GroupViewer, id string) error {
	if _, err := s.loadForChange(ctx, v, id); err != nil {
		return err
	}
	var vms, plans int64
	if err := s.db.WithContext(ctx).Table("vm_firewall_groups a").Joins("JOIN vms m ON m.id = a.vm_id AND m.deleted_at IS NULL").
		Where("a.group_id = ?", id).Count(&vms).Error; err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Model(&models.Plan{}).Where("default_firewall_group_id = ?", id).Count(&plans).Error; err != nil {
		return err
	}
	switch {
	case vms > 0:
		return fmt.Errorf("%w: it is attached to %d VMs; detach it first", ErrFirewallGroupInUse, vms)
	case plans > 0:
		return fmt.Errorf("%w: %d plans give it to new VMs; choose another default on those plans first", ErrFirewallGroupInUse, plans)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Attachments of deleted VMs, then the group (its rules cascade).
		if err := tx.Where("group_id = ?", id).Delete(&models.VMFirewallGroup{}).Error; err != nil {
			return err
		}
		if err := tx.Where("group_id = ?", id).Delete(&models.FirewallGroupRule{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&models.FirewallGroup{}).Error
	})
}

// attachedVMIDs lists the live VMs a group is on.
func (s *FirewallGroupService) attachedVMIDs(ctx context.Context, groupID string) ([]string, error) {
	var ids []string
	err := s.db.WithContext(ctx).Table("vm_firewall_groups a").Joins("JOIN vms m ON m.id = a.vm_id AND m.deleted_at IS NULL").
		Where("a.group_id = ?", groupID).Order("m.id").Pluck("m.id", &ids).Error
	return ids, err
}

// AddRule adds a rule to a group and pushes it to every VM the group is on.
// It is refused when any of those VMs would pass the per-VM rule limit.
func (s *FirewallGroupService) AddRule(ctx context.Context, v GroupViewer, groupID string, req *AddFirewallRuleRequest) (*models.FirewallGroupRule, error) {
	if _, err := s.loadForChange(ctx, v, groupID); err != nil {
		return nil, err
	}
	if req.SourceIP == "" {
		req.SourceIP = "0.0.0.0/0"
	}
	rule := &models.FirewallGroupRule{
		GroupID: groupID, Protocol: req.Protocol, PortRange: req.PortRange, Action: req.Action, Direction: req.Direction,
		SourceIP: req.SourceIP, Priority: req.Priority, ICMPType: strings.TrimSpace(req.ICMPType), Log: req.Log, Family: req.Family,
	}
	asVM := rule.AsVMRule("00000000-0000-0000-0000-000000000000")
	if errs := asVM.Validate(); len(errs) > 0 {
		return nil, fmt.Errorf("%w: %v", ErrInvalidFirewallRule, errs)
	}
	limit := FirewallRulesPerVM(ctx, s.db)
	var inGroup int64
	if err := s.db.WithContext(ctx).Model(&models.FirewallGroupRule{}).Where("group_id = ?", groupID).Count(&inGroup).Error; err != nil {
		return nil, err
	}
	if inGroup >= int64(limit) {
		return nil, fmt.Errorf("%w: a group may hold at most %d rules, the per-VM limit", ErrFirewallRuleLimit, limit)
	}
	vmIDs, err := s.attachedVMIDs(ctx, groupID)
	if err != nil {
		return nil, err
	}
	over := 0
	for _, id := range vmIDs {
		held, err := s.fw.CountEffectiveByVMID(ctx, id)
		if err != nil {
			return nil, err
		}
		if held >= int64(limit) {
			over++
		}
	}
	if over > 0 {
		return nil, fmt.Errorf("%w: %d of the VMs this group is on already hold %d rules", ErrFirewallRuleLimit, over, limit)
	}
	if err := s.db.WithContext(ctx).Create(rule).Error; err != nil {
		return nil, fmt.Errorf("failed to save the rule: %w", err)
	}
	s.push(ctx, groupID, vmIDs)
	return rule, nil
}

// RemoveRule removes a rule from a group and pushes the change.
func (s *FirewallGroupService) RemoveRule(ctx context.Context, v GroupViewer, groupID, ruleID string) error {
	if _, err := s.loadForChange(ctx, v, groupID); err != nil {
		return err
	}
	res := s.db.WithContext(ctx).Where("id = ? AND group_id = ?", ruleID, groupID).Delete(&models.FirewallGroupRule{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrFirewallRuleNotFound
	}
	vmIDs, err := s.attachedVMIDs(ctx, groupID)
	if err != nil {
		return fmt.Errorf("removed, but the VMs it is on could not be listed: %w", err)
	}
	s.push(ctx, groupID, vmIDs)
	return nil
}

// push queues the group's VMs for a full network-config push. The change is
// saved already; a VM the queue misses gets it with its next push (or when
// its node comes back).
func (s *FirewallGroupService) push(ctx context.Context, groupID string, vmIDs []string) {
	if s.net == nil || len(vmIDs) == 0 {
		return
	}
	// The change is saved: a client that hangs up must not stop it reaching
	// the VMs not yet queued.
	if _, err := s.net.ResyncVMs(context.WithoutCancel(ctx), vmIDs); err != nil {
		slog.Default().Warn("firewall group change not queued for every VM", "group_id", groupID, "vms", len(vmIDs), "error", err)
	}
}

// ListForVM returns the groups attached to a VM, in enforcement order.
func (s *FirewallGroupService) ListForVM(ctx context.Context, vmID string) ([]AttachedFirewallGroup, error) {
	var rows []struct {
		models.FirewallGroup
		Position  string
		SortOrder int
	}
	if err := s.db.WithContext(ctx).Table("vm_firewall_groups a").
		Select("g.*, a.position, a.sort_order").Joins("JOIN firewall_groups g ON g.id = a.group_id").
		Where("a.vm_id = ?", vmID).Order("a.position DESC, a.sort_order, a.created_at").Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]AttachedFirewallGroup, len(rows))
	for i, r := range rows {
		out[i] = AttachedFirewallGroup{ID: r.ID, Name: r.Name, Global: r.UserID == nil, Position: r.Position, SortOrder: r.SortOrder, Rules: []models.FirewallGroupRule{}}
		if err := s.db.WithContext(ctx).Where("group_id = ?", r.ID).Order("priority ASC, created_at ASC").Find(&out[i].Rules).Error; err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Attach puts a group on a VM. A customer attaches their own groups; staff a
// group for everyone, or a customer's group to that customer's own VM. The
// VM's own rules plus every attached group's must stay within the per-VM limit.
func (s *FirewallGroupService) Attach(ctx context.Context, v GroupViewer, vmID, groupID, position string) error {
	if position == "" {
		position = models.FirewallGroupAfter
	}
	if position != models.FirewallGroupBefore && position != models.FirewallGroupAfter {
		return fmt.Errorf("%w: position must be before or after", ErrInvalidFirewallGroup)
	}
	g, err := s.load(ctx, v, groupID)
	if err != nil {
		return err
	}
	var vm models.VM
	if err := s.db.WithContext(ctx).Where("id = ?", vmID).First(&vm).Error; err != nil {
		return fmt.Errorf("VM not found")
	}
	if g.UserID != nil && *g.UserID != vm.UserID {
		return fmt.Errorf("%w: a customer's group can only go on that customer's VMs", ErrInvalidFirewallGroup)
	}
	return s.attach(ctx, &vm, g.ID, position, true)
}

// attach records the attachment and pushes it; check enforces the rule limit.
func (s *FirewallGroupService) attach(ctx context.Context, vm *models.VM, groupID, position string, check bool) error {
	var exists int64
	if err := s.db.WithContext(ctx).Model(&models.VMFirewallGroup{}).Where("vm_id = ? AND group_id = ?", vm.ID, groupID).Count(&exists).Error; err != nil {
		return err
	}
	if exists > 0 {
		return ErrFirewallGroupAttached
	}
	if check {
		var adding int64
		if err := s.db.WithContext(ctx).Model(&models.FirewallGroupRule{}).Where("group_id = ?", groupID).Count(&adding).Error; err != nil {
			return err
		}
		held, err := s.fw.CountEffectiveByVMID(ctx, vm.ID)
		if err != nil {
			return err
		}
		if limit := FirewallRulesPerVM(ctx, s.db); held+adding > int64(limit) {
			return fmt.Errorf("%w: the VM would hold %d rules, more than the %d allowed", ErrFirewallRuleLimit, held+adding, limit)
		}
	}
	var next struct{ N int }
	if err := s.db.WithContext(ctx).Model(&models.VMFirewallGroup{}).Select("COALESCE(MAX(sort_order), 0) + 1 AS n").
		Where("vm_id = ?", vm.ID).Scan(&next).Error; err != nil {
		return err
	}
	if err := s.db.WithContext(ctx).Create(&models.VMFirewallGroup{VMID: vm.ID, GroupID: groupID, Position: position, SortOrder: next.N}).Error; err != nil {
		return fmt.Errorf("failed to attach the group: %w", err)
	}
	if s.net != nil {
		if err := s.net.enqueueNetworkResync(ctx, vm); err != nil {
			slog.Default().Warn("firewall group attached but not pushed to the node", "vm_id", vm.ID, "error", err)
		}
	}
	return nil
}

// reownVMGroups gives a VM its owner's own copy of every other customer's
// group attached to it, in the same place: after a change of owner the old
// owner must not keep editing the VM's firewall. What is enforced is unchanged.
func reownVMGroups(ctx context.Context, tx *gorm.DB, vmID, owner string) error {
	if !tx.Migrator().HasTable(&models.FirewallGroup{}) {
		return nil // a test schema without groups
	}
	var foreign []models.FirewallGroup
	if err := tx.WithContext(ctx).Table("firewall_groups g").Select("g.*").
		Joins("JOIN vm_firewall_groups a ON a.group_id = g.id").
		Where("a.vm_id = ? AND g.user_id IS NOT NULL AND g.user_id <> ?", vmID, owner).Scan(&foreign).Error; err != nil {
		return fmt.Errorf("failed to read the VM's firewall groups: %w", err)
	}
	for _, g := range foreign {
		var rules []models.FirewallGroupRule
		if err := tx.WithContext(ctx).Where("group_id = ?", g.ID).Find(&rules).Error; err != nil {
			return err
		}
		uid := owner
		cp := models.FirewallGroup{UserID: &uid, Name: g.Name, Description: g.Description}
		if err := tx.WithContext(ctx).Create(&cp).Error; err != nil {
			return fmt.Errorf("failed to copy firewall group %s: %w", g.Name, err)
		}
		for i := range rules {
			rules[i].ID, rules[i].GroupID, rules[i].CreatedAt, rules[i].UpdatedAt = "", cp.ID, time.Time{}, time.Time{}
		}
		if len(rules) > 0 {
			if err := tx.WithContext(ctx).Create(&rules).Error; err != nil {
				return fmt.Errorf("failed to copy firewall group %s: %w", g.Name, err)
			}
		}
		if err := tx.WithContext(ctx).Model(&models.VMFirewallGroup{}).Where("vm_id = ? AND group_id = ?", vmID, g.ID).
			Update("group_id", cp.ID).Error; err != nil {
			return err
		}
	}
	return nil
}

// Detach takes a group off a VM. A customer may take off only their own
// groups: a group for everyone is the operator's, placed by staff or the plan.
func (s *FirewallGroupService) Detach(ctx context.Context, v GroupViewer, vmID, groupID string) error {
	var g models.FirewallGroup
	if err := s.db.WithContext(ctx).Where("id = ?", groupID).First(&g).Error; err != nil {
		return ErrFirewallGroupNotFound
	}
	if !v.Staff && (g.UserID == nil || *g.UserID != v.UserID) {
		return ErrFirewallGroupForbidden
	}
	res := s.db.WithContext(ctx).Where("vm_id = ? AND group_id = ?", vmID, groupID).Delete(&models.VMFirewallGroup{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrFirewallGroupNotFound
	}
	var vm models.VM
	if err := s.db.WithContext(ctx).Where("id = ?", vmID).First(&vm).Error; err == nil && s.net != nil {
		if err := s.net.enqueueNetworkResync(ctx, &vm); err != nil {
			slog.Default().Warn("firewall group detached but not pushed to the node", "vm_id", vmID, "error", err)
		}
	}
	return nil
}
