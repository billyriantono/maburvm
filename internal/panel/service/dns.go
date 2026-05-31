package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

var (
	// ErrDNSZoneNotFound is returned when a zone does not exist.
	ErrDNSZoneNotFound = errors.New("dns zone not found")
	// ErrDNSZoneExists is returned when a zone name is already taken.
	ErrDNSZoneExists = errors.New("dns zone already exists")
	// ErrDNSRecordNotFound is returned when a record does not exist.
	ErrDNSRecordNotFound = errors.New("dns record not found")
	// ErrInvalidDNSRecord is returned when a record fails validation.
	ErrInvalidDNSRecord = errors.New("invalid dns record")
)

// supportedDNSTypes are the record types the manager validates and renders.
var supportedDNSTypes = map[string]bool{
	"A": true, "AAAA": true, "CNAME": true, "MX": true, "TXT": true, "NS": true, "SRV": true,
}

// DNSService manages forward DNS zones and records, optionally pushing changes
// to a live nameserver via a DNSProvider.
type DNSService struct {
	repo     *repository.DNSRepository
	provider DNSProvider
	logger   *slog.Logger
}

// NewDNSService creates a DNSService with no live provider (export-only).
func NewDNSService(db *gorm.DB) *DNSService {
	return &DNSService{repo: repository.NewDNSRepository(db), provider: noopDNSProvider{}, logger: slog.Default()}
}

// NewDNSServiceWithProvider creates a DNSService that pushes zone changes to a
// live nameserver (e.g. PowerDNS) in addition to persisting them.
func NewDNSServiceWithProvider(db *gorm.DB, provider DNSProvider, logger *slog.Logger) *DNSService {
	if provider == nil {
		provider = noopDNSProvider{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &DNSService{repo: repository.NewDNSRepository(db), provider: provider, logger: logger}
}

// ProviderConfigured reports whether a live nameserver provider is wired.
func (s *DNSService) ProviderConfigured() bool { return s.provider.Configured() }

// ProviderName returns the configured provider's name (for UI/status).
func (s *DNSService) ProviderName() string { return s.provider.Name() }

// SyncZone pushes a zone's full record set to the live nameserver. Returns
// ErrDNSProviderNotConfigured when no provider is wired.
func (s *DNSService) SyncZone(ctx context.Context, zoneID string) error {
	if !s.provider.Configured() {
		return ErrDNSProviderNotConfigured
	}
	zone, err := s.GetZone(ctx, zoneID)
	if err != nil {
		return err
	}
	records, err := s.repo.ListRecords(ctx, zoneID)
	if err != nil {
		return err
	}
	return s.provider.SyncZone(ctx, zone, records)
}

// autoSync pushes a zone to the nameserver best-effort after a mutation. A push
// failure is logged but never fails the (already-persisted) DB change; the zone
// can be re-synced explicitly. The DB remains the source of truth.
func (s *DNSService) autoSync(ctx context.Context, zoneID string) {
	if !s.provider.Configured() {
		return
	}
	if err := s.SyncZone(ctx, zoneID); err != nil {
		s.logger.WarnContext(ctx, "dns auto-sync to nameserver failed", "zone_id", zoneID, "error", err)
	}
}

// ZoneRequest is the input for creating/updating a zone.
type ZoneRequest struct {
	Name        string `json:"name" validate:"required"`
	TTL         int    `json:"ttl"`
	PrimaryNS   string `json:"primary_ns"`
	AdminEmail  string `json:"admin_email"`
	Description string `json:"description"`
}

// RecordRequest is the input for creating/updating a record.
type RecordRequest struct {
	Name     string `json:"name"`
	Type     string `json:"type" validate:"required"`
	Content  string `json:"content" validate:"required"`
	TTL      int    `json:"ttl"`
	Priority int    `json:"priority"`
}

// --- Zones ---

func (s *DNSService) ListZones(ctx context.Context) ([]models.DNSZone, error) {
	return s.repo.ListZones(ctx)
}

func (s *DNSService) GetZone(ctx context.Context, id string) (*models.DNSZone, error) {
	zone, err := s.repo.GetZone(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDNSZoneNotFound
		}
		return nil, err
	}
	return zone, nil
}

func (s *DNSService) CreateZone(ctx context.Context, req *ZoneRequest) (*models.DNSZone, error) {
	name := normalizeZoneName(req.Name)
	if name == "" {
		return nil, fmt.Errorf("%w: zone name is required", ErrInvalidDNSRecord)
	}
	if !isValidRDNSHostname(name) {
		return nil, fmt.Errorf("%w: %q is not a valid domain", ErrInvalidDNSRecord, name)
	}
	exists, err := s.repo.ZoneNameExists(ctx, name)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrDNSZoneExists
	}
	zone := &models.DNSZone{
		Name:        name,
		TTL:         orDefaultTTL(req.TTL),
		PrimaryNS:   strings.TrimSpace(req.PrimaryNS),
		AdminEmail:  strings.TrimSpace(req.AdminEmail),
		Description: req.Description,
	}
	if err := s.repo.CreateZone(ctx, zone); err != nil {
		return nil, err
	}
	s.autoSync(ctx, zone.ID)
	return zone, nil
}

func (s *DNSService) UpdateZone(ctx context.Context, id string, req *ZoneRequest) (*models.DNSZone, error) {
	zone, err := s.GetZone(ctx, id)
	if err != nil {
		return nil, err
	}
	zone.TTL = orDefaultTTL(req.TTL)
	zone.PrimaryNS = strings.TrimSpace(req.PrimaryNS)
	zone.AdminEmail = strings.TrimSpace(req.AdminEmail)
	zone.Description = req.Description
	if err := s.repo.UpdateZone(ctx, zone); err != nil {
		return nil, err
	}
	s.autoSync(ctx, zone.ID)
	return zone, nil
}

func (s *DNSService) DeleteZone(ctx context.Context, id string) error {
	zone, err := s.GetZone(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteZone(ctx, id); err != nil {
		return err
	}
	// Best-effort removal from the live nameserver; the DB delete is authoritative.
	if s.provider.Configured() {
		if derr := s.provider.DeleteZone(ctx, zone.Name); derr != nil {
			s.logger.WarnContext(ctx, "dns provider delete-zone failed", "zone", zone.Name, "error", derr)
		}
	}
	return nil
}

// --- Records ---

func (s *DNSService) ListRecords(ctx context.Context, zoneID string) ([]models.DNSRecord, error) {
	if _, err := s.GetZone(ctx, zoneID); err != nil {
		return nil, err
	}
	return s.repo.ListRecords(ctx, zoneID)
}

func (s *DNSService) CreateRecord(ctx context.Context, zoneID string, req *RecordRequest) (*models.DNSRecord, error) {
	if _, err := s.GetZone(ctx, zoneID); err != nil {
		return nil, err
	}
	rec := recordFromRequest(zoneID, req)
	if err := validateDNSRecord(rec); err != nil {
		return nil, err
	}
	if err := s.repo.CreateRecord(ctx, rec); err != nil {
		return nil, err
	}
	s.autoSync(ctx, zoneID)
	return rec, nil
}

func (s *DNSService) UpdateRecord(ctx context.Context, recordID string, req *RecordRequest) (*models.DNSRecord, error) {
	rec, err := s.repo.GetRecord(ctx, recordID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrDNSRecordNotFound
		}
		return nil, err
	}
	updated := recordFromRequest(rec.ZoneID, req)
	updated.ID = rec.ID
	if err := validateDNSRecord(updated); err != nil {
		return nil, err
	}
	if err := s.repo.UpdateRecord(ctx, updated); err != nil {
		return nil, err
	}
	s.autoSync(ctx, updated.ZoneID)
	return updated, nil
}

func (s *DNSService) DeleteRecord(ctx context.Context, recordID string) error {
	rec, err := s.repo.GetRecord(ctx, recordID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrDNSRecordNotFound
		}
		return err
	}
	if err := s.repo.DeleteRecord(ctx, recordID); err != nil {
		return err
	}
	s.autoSync(ctx, rec.ZoneID)
	return nil
}

// ExportZone renders the zone's records as a BIND zone file.
func (s *DNSService) ExportZone(ctx context.Context, zoneID string) (string, error) {
	zone, err := s.GetZone(ctx, zoneID)
	if err != nil {
		return "", err
	}
	records, err := s.repo.ListRecords(ctx, zoneID)
	if err != nil {
		return "", err
	}
	// Serial is a regenerated-on-export YYYYMMDDNN-style stamp.
	serial := time.Now().UTC().Format("20060102") + "01"
	return buildZoneFile(zone, records, serial), nil
}

// --- helpers ---

func normalizeZoneName(name string) string {
	return strings.TrimSuffix(strings.TrimSpace(strings.ToLower(name)), ".")
}

func orDefaultTTL(ttl int) int {
	if ttl <= 0 {
		return 3600
	}
	return ttl
}

func recordFromRequest(zoneID string, req *RecordRequest) *models.DNSRecord {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "@"
	}
	return &models.DNSRecord{
		ZoneID:   zoneID,
		Name:     name,
		Type:     strings.ToUpper(strings.TrimSpace(req.Type)),
		Content:  strings.TrimSpace(req.Content),
		TTL:      orDefaultTTL(req.TTL),
		Priority: req.Priority,
	}
}

// validateDNSRecord checks a record's content against its type. Pure (no I/O).
func validateDNSRecord(r *models.DNSRecord) error {
	if !supportedDNSTypes[r.Type] {
		return fmt.Errorf("%w: unsupported type %q", ErrInvalidDNSRecord, r.Type)
	}
	if r.Content == "" {
		return fmt.Errorf("%w: content is required", ErrInvalidDNSRecord)
	}
	switch r.Type {
	case "A":
		ip := net.ParseIP(r.Content)
		if ip == nil || ip.To4() == nil {
			return fmt.Errorf("%w: A record needs an IPv4 address", ErrInvalidDNSRecord)
		}
	case "AAAA":
		ip := net.ParseIP(r.Content)
		if ip == nil || ip.To4() != nil {
			return fmt.Errorf("%w: AAAA record needs an IPv6 address", ErrInvalidDNSRecord)
		}
	case "CNAME", "NS":
		if !isValidRDNSHostname(r.Content) {
			return fmt.Errorf("%w: %s target must be a hostname", ErrInvalidDNSRecord, r.Type)
		}
	case "MX":
		if r.Priority < 0 {
			return fmt.Errorf("%w: MX priority must be >= 0", ErrInvalidDNSRecord)
		}
		if !isValidRDNSHostname(r.Content) {
			return fmt.Errorf("%w: MX target must be a hostname", ErrInvalidDNSRecord)
		}
	case "TXT", "SRV":
		// TXT is free-form; SRV content is validated loosely (non-empty).
	}
	return nil
}

// buildZoneFile renders a BIND-format zone file. Pure: serial is supplied.
func buildZoneFile(zone *models.DNSZone, records []models.DNSRecord, serial string) string {
	var b strings.Builder
	ttl := orDefaultTTL(zone.TTL)
	primaryNS := ensureDot(firstNonEmpty(zone.PrimaryNS, "ns1."+zone.Name))
	admin := adminToRName(firstNonEmpty(zone.AdminEmail, "hostmaster@"+zone.Name))

	fmt.Fprintf(&b, "$ORIGIN %s.\n", zone.Name)
	fmt.Fprintf(&b, "$TTL %d\n", ttl)
	fmt.Fprintf(&b, "@\tIN\tSOA\t%s %s (\n", primaryNS, admin)
	fmt.Fprintf(&b, "\t\t%s ; serial\n", serial)
	b.WriteString("\t\t3600       ; refresh\n")
	b.WriteString("\t\t900        ; retry\n")
	b.WriteString("\t\t604800     ; expire\n")
	b.WriteString("\t\t86400 )    ; minimum\n")

	if zone.PrimaryNS != "" {
		fmt.Fprintf(&b, "@\tIN\tNS\t%s\n", primaryNS)
	}

	for i := range records {
		r := records[i]
		switch r.Type {
		case "MX", "SRV":
			fmt.Fprintf(&b, "%s\t%d\tIN\t%s\t%d %s\n", r.Name, orDefaultTTL(r.TTL), r.Type, r.Priority, formatContent(r))
		case "TXT":
			fmt.Fprintf(&b, "%s\t%d\tIN\tTXT\t%s\n", r.Name, orDefaultTTL(r.TTL), quoteTXT(r.Content))
		default:
			fmt.Fprintf(&b, "%s\t%d\tIN\t%s\t%s\n", r.Name, orDefaultTTL(r.TTL), r.Type, formatContent(r))
		}
	}
	return b.String()
}

// formatContent adds a trailing dot to hostname-valued records so they are
// treated as fully-qualified.
func formatContent(r models.DNSRecord) string {
	switch r.Type {
	case "CNAME", "NS", "MX", "SRV":
		return ensureDot(r.Content)
	default:
		return r.Content
	}
}

func quoteTXT(s string) string {
	if strings.HasPrefix(s, "\"") {
		return s
	}
	return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\""
}

func ensureDot(host string) string {
	host = strings.TrimSpace(host)
	if host == "" || strings.HasSuffix(host, ".") {
		return host
	}
	return host + "."
}

func adminToRName(email string) string {
	email = strings.TrimSpace(email)
	if at := strings.Index(email, "@"); at >= 0 {
		email = email[:at] + "." + email[at+1:]
	}
	return ensureDot(email)
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
