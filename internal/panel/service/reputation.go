package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DefaultBlocklists are the DNS blocklists queried when the operator has not
// chosen their own.
//
// Kept short deliberately. Every public blocklist has usage terms, and several
// refuse queries from hosting IPs or above a daily volume — checking our own
// space from our own panel is exactly the case they refuse most often. A long
// default list would multiply that problem while adding little: an address that
// is genuinely burned appears on these.
var DefaultBlocklists = []string{
	"zen.spamhaus.org",
	"bl.spamcop.net",
}

// SettingsKeyAbuseIPDBToken names the optional AbuseIPDB key in the settings
// 'api' section. Without it the score is simply not collected — the blocklist
// checks need no credentials and carry the feature on their own.
const SettingsKeyAbuseIPDBToken = "abuseipdbToken"

// ReputationService answers "what does the internet currently think of our
// addresses?".
//
// It exists because that question had no answer inside the product, and the
// consequence was concrete: an address used to scan the internet at 90k
// packets/sec was reallocated to a paying customer days later, complete with
// whatever listings that earned. The customer sees mail rejections and endless
// CAPTCHAs; the panel shows a healthy VM.
type ReputationService struct {
	db         *gorm.DB
	resolver   *net.Resolver
	httpClient *http.Client
	logger     *slog.Logger
}

func NewReputationService(db *gorm.DB, logger *slog.Logger) *ReputationService {
	if logger == nil {
		logger = slog.Default()
	}
	return &ReputationService{
		db: db,
		// The system resolver, deliberately. Blocklists refuse queries arriving
		// via large public resolvers, so a hardcoded 8.8.8.8 would return
		// "refused" for every address and look like a clean fleet.
		resolver:   net.DefaultResolver,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		logger:     logger,
	}
}

// reverseIPv4 renders an address in the in-addr form blocklists expect:
// 1.2.3.4 becomes 4.3.2.1.
func reverseIPv4(ip net.IP) (string, bool) {
	v4 := ip.To4()
	if v4 == nil {
		return "", false
	}
	return fmt.Sprintf("%d.%d.%d.%d", v4[3], v4[2], v4[1], v4[0]), true
}

// checkBlocklist reports whether an address is listed on one blocklist.
//
// The third return value distinguishes "answered: not listed" from "could not
// ask". Blocklists signal a refused or over-quota query by returning an address
// in 127.255.255.0/24 rather than by failing, so a naive check reads a refusal
// as a clean result — the single most misleading outcome this code could
// produce, because it tells a provider their space is fine precisely when they
// have lost the ability to find out.
func (s *ReputationService) checkBlocklist(ctx context.Context, reversed, zone string) (listed bool, err error) {
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	addrs, lookupErr := s.resolver.LookupHost(lookupCtx, reversed+"."+zone)
	if lookupErr != nil {
		var dnsErr *net.DNSError
		if ok := asDNSError(lookupErr, &dnsErr); ok && dnsErr.IsNotFound {
			// NXDOMAIN is the blocklist's way of saying "not listed".
			return false, nil
		}
		return false, fmt.Errorf("%s: %w", zone, lookupErr)
	}

	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil {
			continue
		}
		refusal := &net.IPNet{IP: net.IPv4(127, 255, 255, 0), Mask: net.CIDRMask(24, 32)}
		if refusal.Contains(ip) {
			return false, fmt.Errorf("%s refused the query (%s) — a key or datafeed is needed to check from this host", zone, a)
		}
	}
	return len(addrs) > 0, nil
}

func asDNSError(err error, target **net.DNSError) bool {
	for err != nil {
		if d, ok := err.(*net.DNSError); ok {
			*target = d
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// abuseIPDBResult is the subset of AbuseIPDB's response we record.
type abuseIPDBResult struct {
	Score        int
	TotalReports int
	LastReported *time.Time
}

// checkAbuseIPDB fetches a confidence score, when a key is configured.
func (s *ReputationService) checkAbuseIPDB(ctx context.Context, token, address string) (*abuseIPDBResult, error) {
	if token == "" {
		return nil, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.abuseipdb.com/api/v2/check?maxAgeInDays=90&ipAddress="+address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Key", token)
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// 429 is the common one — the free tier is 1,000 checks a day, and a
		// fleet larger than that needs a paid plan or a longer interval. Say so
		// rather than record a zero score.
		return nil, fmt.Errorf("abuseipdb returned %s", resp.Status)
	}

	var body struct {
		Data struct {
			AbuseConfidenceScore int    `json:"abuseConfidenceScore"`
			TotalReports         int    `json:"totalReports"`
			LastReportedAt       string `json:"lastReportedAt"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}

	out := &abuseIPDBResult{
		Score:        body.Data.AbuseConfidenceScore,
		TotalReports: body.Data.TotalReports,
	}
	if body.Data.LastReportedAt != "" {
		if t, perr := time.Parse(time.RFC3339, body.Data.LastReportedAt); perr == nil {
			out.LastReported = &t
		}
	}
	return out, nil
}

// abuseIPDBToken reads the optional key from admin-managed settings.
func (s *ReputationService) abuseIPDBToken(ctx context.Context) string {
	var raw string
	if err := s.db.WithContext(ctx).
		Raw("SELECT data ->> ? FROM system_settings WHERE section = 'api'", SettingsKeyAbuseIPDBToken).
		Scan(&raw).Error; err != nil {
		return ""
	}
	return strings.TrimSpace(raw)
}

// CheckAddress evaluates one address and stores the result.
func (s *ReputationService) CheckAddress(ctx context.Context, address, poolID, token string, zones []string) error {
	ip := net.ParseIP(address)
	if ip == nil {
		return fmt.Errorf("%q is not an address", address)
	}
	reversed, ok := reverseIPv4(ip)
	if !ok {
		// IPv6 blocklist coverage is thin and the reversal differs; skip rather
		// than record a misleading clean result.
		return nil
	}

	record := models.IPReputation{
		Address:    address,
		PoolID:     poolID,
		Listings:   []string{},
		AbuseScore: models.AbuseScoreNotChecked,
		CheckedAt:  time.Now(),
	}

	var problems []string
	for _, zone := range zones {
		listed, err := s.checkBlocklist(ctx, reversed, zone)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if listed {
			record.Listings = append(record.Listings, zone)
		}
	}

	if res, err := s.checkAbuseIPDB(ctx, token, address); err != nil {
		problems = append(problems, err.Error())
	} else if res != nil {
		record.AbuseScore = res.Score
		record.TotalReports = res.TotalReports
		record.LastReportedAt = res.LastReported
	}
	record.CheckError = strings.Join(problems, "; ")

	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "address"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"pool_id", "listings", "abuse_score", "total_reports",
			"last_reported_at", "check_error", "checked_at",
		}),
	}).Create(&record).Error
}

// CheckDueAddresses evaluates public addresses that have not been checked
// recently, newest-stale first, up to a limit.
//
// Limited per run because both the blocklists and AbuseIPDB's free tier have
// daily quotas, and burning them in one sweep would leave the rest of the fleet
// unchecked while reporting nothing wrong. Addresses currently assigned to a VM
// are checked first: a listing on an address nobody is using costs nothing
// today, while one on a customer's address is costing them right now.
func (s *ReputationService) CheckDueAddresses(ctx context.Context, staleAfter time.Duration, limit int) (checked int) {
	token := s.abuseIPDBToken(ctx)
	zones := DefaultBlocklists

	type row struct {
		Address string
		PoolID  string
	}
	var rows []row
	cutoff := time.Now().Add(-staleAfter)

	if err := s.db.WithContext(ctx).Raw(`
		SELECT a.address::text AS address, a.pool_id::text AS pool_id
		FROM ip_addresses a
		JOIN ip_pools p ON p.id = a.pool_id
		LEFT JOIN ip_reputation r ON r.address = a.address
		WHERE family(a.address) = 4
		  AND NOT (a.address << '10.0.0.0/8'
		        OR a.address << '172.16.0.0/12'
		        OR a.address << '192.168.0.0/16'
		        OR a.address << '100.64.0.0/10')
		  AND (r.checked_at IS NULL OR r.checked_at < ?)
		ORDER BY (a.status = 'assigned') DESC, r.checked_at ASC NULLS FIRST
		LIMIT ?`, cutoff, limit).Scan(&rows).Error; err != nil {
		s.logger.Error("reputation: could not list addresses due for a check", "error", err)
		return 0
	}

	for _, r := range rows {
		if ctx.Err() != nil {
			break
		}
		if err := s.CheckAddress(ctx, r.Address, r.PoolID, token, zones); err != nil {
			s.logger.Warn("reputation: check failed", "address", r.Address, "error", err)
			continue
		}
		checked++
	}
	return checked
}

// List returns stored reputation, worst first. flaggedOnly narrows it to the
// addresses that are actually listed or scored, which is the view an operator
// wants — a fleet of clean addresses buries the handful that are not.
func (s *ReputationService) List(ctx context.Context, flaggedOnly bool) ([]models.IPReputation, error) {
	q := s.db.WithContext(ctx).
		Table("ip_reputation AS r").
		Select(`r.*, p.name AS pool_name,
		        COALESCE(v.hostname, '') AS vm_hostname,
		        (a.status = 'assigned') AS assigned`).
		Joins("LEFT JOIN ip_pools p ON p.id = r.pool_id").
		Joins("LEFT JOIN ip_addresses a ON a.address = r.address").
		Joins("LEFT JOIN vms v ON v.id::text = a.vm_id").
		Order("(jsonb_array_length(r.listings) > 0) DESC, r.abuse_score DESC, r.address")

	if flaggedOnly {
		q = q.Where("jsonb_array_length(r.listings) > 0 OR r.abuse_score > 0")
	}

	var out []models.IPReputation
	if err := q.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}
