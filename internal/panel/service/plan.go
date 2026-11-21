package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

var (
	// ErrPlanNotFound is returned when a plan does not exist.
	ErrPlanNotFound = errors.New("plan not found")
	// ErrPlanNameExists is returned when a plan name is already taken.
	ErrPlanNameExists = errors.New("plan name already exists")
)

// PlanService provides business logic for VPS plans.
type PlanService struct {
	repo *repository.PlanRepository
}

// NewPlanService creates a new PlanService.
func NewPlanService(repo *repository.PlanRepository) *PlanService {
	return &PlanService{repo: repo}
}

// PlanRequest contains create/update parameters for a plan.
type PlanRequest struct {
	Name              string `json:"name"`
	CPU               int    `json:"cpu"`
	RAM               int    `json:"ram"`
	Disk              int    `json:"disk"`
	BandwidthMbps     int    `json:"bandwidth_mbps"`
	DataQuotaGB       int64  `json:"data_quota_gb"`
	OverQuotaPolicy   string `json:"over_quota_policy"`
	ThrottleSpeedMbps int    `json:"throttle_speed_mbps"`
	// SMTPPolicy is models.SMTPAllowed or SMTPBlocked; empty keeps the plan's
	// current policy (allowed for a new plan).
	SMTPPolicy string `json:"smtp_policy"`
	// Limits are the CPU and disk caps of new VMs on the plan; nil keeps the
	// plan's current ones (none for a new plan).
	Limits *models.VMLimits `json:"limits"`
	// Display price and per-VM allowances, replaced as a whole like the other
	// fields: nil clears a price or lifts a limit.
	PriceMonthly *float64 `json:"price_monthly"`
	PriceHourly  *float64 `json:"price_hourly"`
	Currency     string   `json:"currency"`
	MaxSnapshots *int     `json:"max_snapshots"`
	MaxBackups   *int     `json:"max_backups"`
	MaxExtraIPv4 *int     `json:"max_extra_ipv4"`
	MaxExtraIPv6 *int     `json:"max_extra_ipv6"`
	// More allowances and the extras' display prices, replaced as a whole
	// the same way.
	MaxExtraDisks  *int               `json:"max_extra_disks"`
	MaxImages      *int               `json:"max_images"`
	MaxFloatingIPs *int               `json:"max_floating_ips"`
	AddonPrices    map[string]float64 `json:"addon_prices"`
	// DefaultFirewallGroupID is a group for everyone that new VMs on the plan
	// get instead of the built-in rules; "" goes back to those, and leaving it
	// out keeps the plan's current choice.
	DefaultFirewallGroupID *string `json:"default_firewall_group_id"`
	// NetworkType is models.PlanNetwork*; empty keeps the plan's network
	// settings (public for a new plan). NATPorts (0 is the default block) and
	// NATHostnames apply to NAT plans.
	NetworkType  string `json:"network_type"`
	NATPorts     int    `json:"nat_ports"`
	NATHostnames int    `json:"nat_hostnames"`
	// NodeGroup is the hypervisor group new VMs on the plan are placed in;
	// "" places them on any node, and leaving it out keeps the plan's choice.
	NodeGroup *string `json:"node_group"`
	// StorageTier puts new VMs' disks only in pools of that tier; "" uses
	// each node's primary pool, and leaving it out keeps the plan's choice.
	StorageTier *string `json:"storage_tier"`
	Description string  `json:"description"`
	IsActive    *bool   `json:"is_active"`
}

// maxPrice bounds a display price, well inside numeric(14,4).
const maxPrice = 1e9

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// validatePriceAndAllowances checks the display price and the per-VM
// allowances, and puts the currency code in upper case.
func (r *PlanRequest) validatePriceAndAllowances() error {
	r.Currency = strings.ToUpper(strings.TrimSpace(r.Currency))
	for _, p := range []*float64{r.PriceMonthly, r.PriceHourly} {
		if p != nil && (math.IsNaN(*p) || *p < 0 || *p > maxPrice) {
			return fmt.Errorf("a price must be between 0 and 1000000000")
		}
	}
	if (r.PriceMonthly != nil || r.PriceHourly != nil) && r.Currency == "" {
		return fmt.Errorf("a price needs a currency (a three-letter code such as USD or IDR)")
	}
	if r.Currency != "" && !currencyCode.MatchString(r.Currency) {
		return fmt.Errorf("currency must be a three-letter code such as USD or IDR")
	}
	for _, a := range []struct {
		v    *int
		max  int
		what string
	}{
		{r.MaxSnapshots, MaxSnapshotsPerVM, "max_snapshots"},
		{r.MaxBackups, 1000, "max_backups"},
		{r.MaxExtraIPv4, 256, "max_extra_ipv4"},
		// A VM holds one IPv6 (a prefix or an address) at most.
		{r.MaxExtraIPv6, 1, "max_extra_ipv6"},
		{r.MaxExtraDisks, 16, "max_extra_disks"},
		{r.MaxImages, 1000, "max_images"},
		{r.MaxFloatingIPs, 256, "max_floating_ips"},
	} {
		if a.v != nil && (*a.v < 0 || *a.v > a.max) {
			return fmt.Errorf("%s must be between 0 and %d, or empty for no limit", a.what, a.max)
		}
	}
	for k, v := range r.AddonPrices {
		if !slices.Contains(models.AddonPriceKeys, k) {
			return fmt.Errorf("addon_prices: %q is not an extra a price can be shown for", k)
		}
		if math.IsNaN(v) || v < 0 || v > maxPrice {
			return fmt.Errorf("a price must be between 0 and 1000000000")
		}
	}
	if len(r.AddonPrices) > 0 && r.Currency == "" {
		return fmt.Errorf("a price needs a currency (a three-letter code such as USD or IDR)")
	}
	return nil
}

// applyPriceAndAllowances copies the display price, allowances, default
// firewall group and network type onto plan.
func (r *PlanRequest) applyPriceAndAllowances(plan *models.Plan) {
	plan.PriceMonthly, plan.PriceHourly, plan.Currency = r.PriceMonthly, r.PriceHourly, r.Currency
	plan.MaxSnapshots, plan.MaxBackups = r.MaxSnapshots, r.MaxBackups
	plan.MaxExtraIPv4, plan.MaxExtraIPv6 = r.MaxExtraIPv4, r.MaxExtraIPv6
	if r.DefaultFirewallGroupID != nil {
		plan.DefaultFirewallGroupID = r.DefaultFirewallGroupID
		if *r.DefaultFirewallGroupID == "" {
			plan.DefaultFirewallGroupID = nil
		}
	}
	if r.NetworkType != "" {
		plan.NetworkType, plan.NATPorts, plan.NATHostnames = r.NetworkType, r.NATPorts, r.NATHostnames
	}
	if plan.NetworkType == "" {
		plan.NetworkType = models.PlanNetworkPublic
	}
}

func (r *PlanRequest) validate() error {
	if r.Name == "" {
		return fmt.Errorf("name is required")
	}
	if r.CPU < 1 || r.CPU > 128 {
		return fmt.Errorf("cpu must be between 1 and 128")
	}
	if r.RAM < 128 {
		return fmt.Errorf("ram must be at least 128 MB")
	}
	if r.Disk < 1 {
		return fmt.Errorf("disk must be at least 1 GB")
	}
	if r.BandwidthMbps < 0 {
		return fmt.Errorf("bandwidth_mbps cannot be negative")
	}
	if r.DataQuotaGB < 0 {
		return fmt.Errorf("data_quota_gb cannot be negative")
	}
	if r.ThrottleSpeedMbps < 0 {
		return fmt.Errorf("throttle_speed_mbps cannot be negative")
	}
	switch r.OverQuotaPolicy {
	case "", models.OverQuotaThrottle, models.OverQuotaOverage, models.OverQuotaSuspend:
	default:
		return fmt.Errorf("over_quota_policy must be throttle, overage, or suspend")
	}
	switch r.SMTPPolicy {
	case "", models.SMTPAllowed, models.SMTPBlocked:
	default:
		return fmt.Errorf("smtp_policy must be allowed or blocked")
	}
	if r.Limits != nil {
		if err := r.Limits.Validate(); err != nil {
			return err
		}
	}
	switch r.NetworkType {
	case "", models.PlanNetworkPublic, models.PlanNetworkNAT:
	default:
		return fmt.Errorf("network_type must be public or nat")
	}
	if r.NetworkType == models.PlanNetworkNAT && r.NATPorts == 0 {
		r.NATPorts = DefaultNATPorts
	}
	if r.NATPorts < 0 || r.NATPorts > MaxNATPorts {
		return fmt.Errorf("nat_ports must be between 1 and %d", MaxNATPorts)
	}
	if r.NATHostnames < 0 || r.NATHostnames > 100 {
		return fmt.Errorf("nat_hostnames must be between 0 and 100")
	}
	if r.NodeGroup != nil {
		g, err := NormalizeNodeGroup(*r.NodeGroup)
		if err != nil {
			return err
		}
		r.NodeGroup = &g
	}
	if r.StorageTier != nil {
		t, err := NormalizeStorageTier(*r.StorageTier)
		if err != nil {
			return err
		}
		r.StorageTier = &t
	}
	return r.validatePriceAndAllowances()
}

// checkDefaultGroup refuses a default group that is not one for everyone: a
// customer's group cannot go on other customers' VMs.
func (s *PlanService) checkDefaultGroup(ctx context.Context, r *PlanRequest) error {
	if r.DefaultFirewallGroupID == nil || *r.DefaultFirewallGroupID == "" {
		return nil
	}
	ok, err := s.repo.GlobalFirewallGroupExists(ctx, *r.DefaultFirewallGroupID)
	if err != nil {
		return fmt.Errorf("failed to check the firewall group: %w", err)
	}
	if !ok {
		return fmt.Errorf("default_firewall_group_id must name a firewall group staff manage for everyone")
	}
	return nil
}

// normalizedOverQuotaPolicy returns the request's policy, defaulting to throttle.
func (r *PlanRequest) normalizedOverQuotaPolicy() string {
	if r.OverQuotaPolicy == "" {
		return models.OverQuotaThrottle
	}
	return r.OverQuotaPolicy
}

// CreatePlan creates a new plan.
func (s *PlanService) CreatePlan(ctx context.Context, req *PlanRequest) (*models.Plan, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	if err := s.checkDefaultGroup(ctx, req); err != nil {
		return nil, err
	}
	exists, err := s.repo.NameExists(ctx, req.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to check plan name: %w", err)
	}
	if exists {
		return nil, ErrPlanNameExists
	}
	active := true
	if req.IsActive != nil {
		active = *req.IsActive
	}
	plan := &models.Plan{
		Name:              req.Name,
		CPU:               req.CPU,
		RAM:               req.RAM,
		Disk:              req.Disk,
		BandwidthMbps:     req.BandwidthMbps,
		DataQuotaGB:       req.DataQuotaGB,
		OverQuotaPolicy:   req.normalizedOverQuotaPolicy(),
		ThrottleSpeedMbps: req.ThrottleSpeedMbps,
		SMTPPolicy:        models.SMTPAllowed,
		Description:       req.Description,
		IsActive:          active,
	}
	if req.SMTPPolicy != "" {
		plan.SMTPPolicy = req.SMTPPolicy
	}
	if req.Limits != nil {
		plan.Limits = *req.Limits
	}
	req.applyPriceAndAllowances(plan)
	if err := s.repo.Create(ctx, plan); err != nil {
		return nil, fmt.Errorf("failed to create plan: %w", err)
	}
	if err := s.saveNodeGroup(ctx, plan, req.NodeGroup); err != nil {
		return nil, err
	}
	if err := s.saveStorageTier(ctx, plan, req.StorageTier); err != nil {
		return nil, err
	}
	if err := s.saveExtras(ctx, plan, req); err != nil {
		return nil, err
	}
	return plan, nil
}

// ListPlans returns all plans (or only active ones).
func (s *PlanService) ListPlans(ctx context.Context, activeOnly bool) ([]models.Plan, error) {
	return s.repo.List(ctx, activeOnly)
}

// GetPlan returns a plan by ID.
func (s *PlanService) GetPlan(ctx context.Context, id string) (*models.Plan, error) {
	plan, err := s.repo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPlanNotFound
		}
		return nil, err
	}
	return plan, nil
}

// UpdatePlan updates an existing plan.
func (s *PlanService) UpdatePlan(ctx context.Context, id string, req *PlanRequest) (*models.Plan, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	if err := s.checkDefaultGroup(ctx, req); err != nil {
		return nil, err
	}
	plan, err := s.GetPlan(ctx, id)
	if err != nil {
		return nil, err
	}
	plan.Name = req.Name
	plan.CPU = req.CPU
	plan.RAM = req.RAM
	plan.Disk = req.Disk
	plan.BandwidthMbps = req.BandwidthMbps
	plan.DataQuotaGB = req.DataQuotaGB
	plan.OverQuotaPolicy = req.normalizedOverQuotaPolicy()
	plan.ThrottleSpeedMbps = req.ThrottleSpeedMbps
	if req.SMTPPolicy != "" {
		plan.SMTPPolicy = req.SMTPPolicy
	}
	if req.Limits != nil {
		plan.Limits = *req.Limits
	}
	req.applyPriceAndAllowances(plan)
	plan.Description = req.Description
	if req.IsActive != nil {
		plan.IsActive = *req.IsActive
	}
	if err := s.repo.Update(ctx, plan); err != nil {
		return nil, fmt.Errorf("failed to update plan: %w", err)
	}
	if err := s.saveNodeGroup(ctx, plan, req.NodeGroup); err != nil {
		return nil, err
	}
	if err := s.saveStorageTier(ctx, plan, req.StorageTier); err != nil {
		return nil, err
	}
	if err := s.saveExtras(ctx, plan, req); err != nil {
		return nil, err
	}
	return plan, nil
}

// saveNodeGroup writes a changed hypervisor group; an unchanged or omitted one
// costs nothing.
func (s *PlanService) saveNodeGroup(ctx context.Context, plan *models.Plan, group *string) error {
	if group == nil || *group == plan.NodeGroup {
		return nil
	}
	if err := s.repo.SetNodeGroup(ctx, plan.ID, *group); err != nil {
		return fmt.Errorf("failed to save the plan's node group: %w", err)
	}
	plan.NodeGroup = *group
	return nil
}

// saveStorageTier writes a changed storage tier.
func (s *PlanService) saveStorageTier(ctx context.Context, plan *models.Plan, tier *string) error {
	if tier == nil || *tier == plan.StorageTier {
		return nil
	}
	if err := s.repo.SetColumns(ctx, plan.ID, map[string]any{"storage_tier": *tier}); err != nil {
		return fmt.Errorf("failed to save the plan's storage tier: %w", err)
	}
	plan.StorageTier = *tier
	return nil
}

// saveExtras writes the allowances and add-on prices that live outside the
// model's writable fields, and only when they changed, so a plan saved
// without them never touches those columns.
func (s *PlanService) saveExtras(ctx context.Context, plan *models.Plan, req *PlanRequest) error {
	prices := req.AddonPrices
	if prices == nil {
		prices = map[string]float64{}
	}
	stored := plan.AddonPrices
	if stored == nil {
		stored = map[string]float64{}
	}
	if intPtrEqual(plan.MaxExtraDisks, req.MaxExtraDisks) && intPtrEqual(plan.MaxImages, req.MaxImages) &&
		intPtrEqual(plan.MaxFloatingIPs, req.MaxFloatingIPs) && maps.Equal(stored, prices) {
		return nil
	}
	raw, err := json.Marshal(prices)
	if err != nil {
		return err
	}
	if err := s.repo.SetColumns(ctx, plan.ID, map[string]any{
		"max_extra_disks": req.MaxExtraDisks, "max_images": req.MaxImages, "max_floating_ips": req.MaxFloatingIPs,
		"addon_prices": string(raw),
	}); err != nil {
		return fmt.Errorf("failed to save the plan's allowances: %w", err)
	}
	plan.MaxExtraDisks, plan.MaxImages, plan.MaxFloatingIPs, plan.AddonPrices = req.MaxExtraDisks, req.MaxImages, req.MaxFloatingIPs, prices
	return nil
}

func intPtrEqual(a, b *int) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

// DeletePlan removes a plan.
func (s *PlanService) DeletePlan(ctx context.Context, id string) error {
	if _, err := s.GetPlan(ctx, id); err != nil {
		return err
	}
	return s.repo.Delete(ctx, id)
}
