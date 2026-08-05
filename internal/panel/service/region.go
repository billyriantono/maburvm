package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

var (
	// ErrRegionNotFound is returned for a missing, disabled or soft-deleted region.
	ErrRegionNotFound = errors.New("region not found")
	// ErrRegionRequired is returned when a customer orders without choosing one.
	ErrRegionRequired = errors.New("please choose a region")
	// ErrRegionNoCapacity is returned when a region exists but has no node able
	// to take the order.
	ErrRegionNoCapacity = errors.New("that region has no capacity right now")
	// ErrRegionInUse blocks deleting a region that still has nodes attached.
	ErrRegionInUse = errors.New("region still has nodes assigned")
)

var regionSlugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$`)

// SettingsKeyDefaultRegion names the region used when a caller that is not a
// customer omits one — principally the WHMCS webhook, whose payload has no
// region field. Without this, making the field required would break billing-
// driven provisioning the moment it shipped.
const SettingsKeyDefaultRegion = "defaultRegionSlug"

// RegionService manages the locations customers choose between.
type RegionService struct {
	db *gorm.DB
}

func NewRegionService(db *gorm.DB) *RegionService { return &RegionService{db: db} }

type CreateRegionRequest struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Country string `json:"country"`
	Enabled *bool  `json:"enabled,omitempty"`
}

// List returns regions. availableOnly restricts to regions a customer may
// actually order into: enabled, and holding at least one active node. A region
// with no capacity is worse than a missing one — the customer picks it and the
// order fails afterwards.
func (s *RegionService) List(ctx context.Context, availableOnly bool) ([]models.Region, error) {
	var regions []models.Region
	q := s.db.WithContext(ctx)
	if availableOnly {
		q = q.Where("enabled = ?", true)
	}
	if err := q.Order("name ASC").Find(&regions).Error; err != nil {
		return nil, err
	}

	counts, err := s.activeNodeCounts(ctx)
	if err != nil {
		return nil, err
	}
	out := regions[:0]
	for i := range regions {
		regions[i].NodeCount = counts[regions[i].ID]
		if availableOnly && regions[i].NodeCount == 0 {
			continue
		}
		out = append(out, regions[i])
	}
	return out, nil
}

// activeNodeCounts maps region id → number of active nodes.
func (s *RegionService) activeNodeCounts(ctx context.Context) (map[string]int, error) {
	var rows []struct {
		RegionID string
		N        int
	}
	if err := s.db.WithContext(ctx).Model(&models.Node{}).
		Select("region_id, COUNT(*) AS n").
		Where("status = ? AND region_id IS NOT NULL", models.NodeStatusActive).
		Group("region_id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(rows))
	for _, r := range rows {
		counts[r.RegionID] = r.N
	}
	return counts, nil
}

// Get resolves a region by id or slug, so callers and customers can use whichever
// they hold without a lookup round-trip.
func (s *RegionService) Get(ctx context.Context, idOrSlug string) (*models.Region, error) {
	idOrSlug = strings.TrimSpace(idOrSlug)
	if idOrSlug == "" {
		return nil, ErrRegionNotFound
	}
	var region models.Region
	if err := s.db.WithContext(ctx).
		Where("id::text = ? OR slug = ?", idOrSlug, strings.ToLower(idOrSlug)).
		First(&region).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRegionNotFound
		}
		return nil, err
	}
	return &region, nil
}

func (s *RegionService) Create(ctx context.Context, req *CreateRegionRequest) (*models.Region, error) {
	slug := strings.ToLower(strings.TrimSpace(req.Slug))
	if !regionSlugPattern.MatchString(slug) {
		return nil, fmt.Errorf("slug must be lowercase letters, digits and dashes, e.g. \"jakarta\"")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	country := strings.ToUpper(strings.TrimSpace(req.Country))
	if len(country) != 2 {
		return nil, fmt.Errorf("country must be a 2-letter ISO code, e.g. \"ID\"")
	}
	region := &models.Region{Slug: slug, Name: name, Country: country, Enabled: true}
	if req.Enabled != nil {
		region.Enabled = *req.Enabled
	}
	if err := s.db.WithContext(ctx).Create(region).Error; err != nil {
		return nil, err
	}
	return region, nil
}

func (s *RegionService) Update(ctx context.Context, id string, req *CreateRegionRequest) (*models.Region, error) {
	region, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if v := strings.TrimSpace(req.Name); v != "" {
		region.Name = v
	}
	if v := strings.ToUpper(strings.TrimSpace(req.Country)); len(v) == 2 {
		region.Country = v
	}
	if req.Enabled != nil {
		region.Enabled = *req.Enabled
	}
	if err := s.db.WithContext(ctx).Save(region).Error; err != nil {
		return nil, err
	}
	return region, nil
}

// Delete refuses while nodes are still assigned, which would otherwise leave
// them region-less and invisible to every customer order.
func (s *RegionService) Delete(ctx context.Context, id string) error {
	region, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	var attached int64
	if err := s.db.WithContext(ctx).Model(&models.Node{}).
		Where("region_id = ?", region.ID).Count(&attached).Error; err != nil {
		return err
	}
	if attached > 0 {
		return fmt.Errorf("%w (%d)", ErrRegionInUse, attached)
	}
	return s.db.WithContext(ctx).Delete(region).Error
}

// AssignNode places a node in a region.
func (s *RegionService) AssignNode(ctx context.Context, nodeID, regionIDOrSlug string) error {
	region, err := s.Get(ctx, regionIDOrSlug)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(&models.Node{}).
		Where("id = ?", nodeID).Update("region_id", region.ID).Error
}

// ResolveOrderRegion decides which region an order lands in.
//
// A customer must choose: they are picking a physical location with real latency
// consequences, and silently placing them somewhere is a decision the platform
// should not make on their behalf. Callers that are not customers — chiefly the
// WHMCS webhook, whose payload carries no region — fall back to the configured
// default so billing-driven provisioning keeps working.
func (s *RegionService) ResolveOrderRegion(ctx context.Context, requested string, mustChoose bool) (*models.Region, error) {
	if strings.TrimSpace(requested) != "" {
		region, err := s.Get(ctx, requested)
		if err != nil {
			return nil, err
		}
		if !region.Enabled {
			return nil, ErrRegionNotFound
		}
		return region, nil
	}
	if mustChoose {
		return nil, ErrRegionRequired
	}
	if slug := s.defaultRegionSlug(ctx); slug != "" {
		if region, err := s.Get(ctx, slug); err == nil && region.Enabled {
			return region, nil
		}
	}
	// No default configured: keep the pre-region behaviour rather than failing an
	// integration that never sent a region in the first place.
	return nil, nil
}

// defaultRegionSlug reads the admin-managed fallback from system settings.
func (s *RegionService) defaultRegionSlug(ctx context.Context) string {
	var raw string
	if err := s.db.WithContext(ctx).
		Raw("SELECT data ->> ? FROM system_settings WHERE section = 'general'", SettingsKeyDefaultRegion).
		Scan(&raw).Error; err != nil {
		return ""
	}
	return strings.TrimSpace(raw)
}

// CountryFlag turns an ISO 3166-1 alpha-2 code into its flag.
//
// Deliberately Unicode regional indicators rather than SVG or an icon package:
// they are real flag glyphs rendered by the system font, so there is no asset to
// ship, nothing to keep in sync when a region is added, and no dependency.
//
// Caveat worth knowing: Windows has no flag glyphs in its emoji font and shows
// the two letters instead ("ID"). That degrades to something still meaningful
// rather than to a broken image.
func CountryFlag(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 2 {
		return ""
	}
	const regionalIndicatorA = 0x1F1E6
	out := make([]rune, 0, 2)
	for _, c := range code {
		if c < 'A' || c > 'Z' {
			return ""
		}
		out = append(out, rune(regionalIndicatorA+(c-'A')))
	}
	return string(out)
}
