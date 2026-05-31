package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
)

// ErrDNSProviderNotConfigured is returned when a live push is attempted but no
// nameserver provider is configured.
var ErrDNSProviderNotConfigured = errors.New("no DNS provider configured")

// DNSProvider pushes zone state to a live authoritative nameserver.
type DNSProvider interface {
	// Configured reports whether a real provider is wired (vs. the no-op).
	Configured() bool
	// Name identifies the provider (for UI/status).
	Name() string
	// SyncZone makes the nameserver's copy of the zone match the given records.
	SyncZone(ctx context.Context, zone *models.DNSZone, records []models.DNSRecord) error
	// DeleteZone removes the zone from the nameserver.
	DeleteZone(ctx context.Context, zoneName string) error
	// SetPTR ensures the reverse zone exists and sets the PTR record for ip to
	// hostname. Works independently of any forward zone.
	SetPTR(ctx context.Context, ip, hostname string) error
	// ClearPTR removes the PTR record for ip from its reverse zone.
	ClearPTR(ctx context.Context, ip string) error
	// ListPTRs returns existing PTR records in a reverse zone as a map of
	// fully-qualified PTR name -> hostname (used to import pre-existing rDNS).
	// Returns an empty map (not an error) when the reverse zone doesn't exist.
	ListPTRs(ctx context.Context, reverseZone string) (map[string]string, error)
}

// noopDNSProvider is used when no nameserver is configured: zone-file export
// still works, but there is no live push.
type noopDNSProvider struct{}

func (noopDNSProvider) Configured() bool { return false }
func (noopDNSProvider) Name() string     { return "none (export only)" }
func (noopDNSProvider) SyncZone(context.Context, *models.DNSZone, []models.DNSRecord) error {
	return ErrDNSProviderNotConfigured
}
func (noopDNSProvider) DeleteZone(context.Context, string) error {
	return ErrDNSProviderNotConfigured
}
func (noopDNSProvider) SetPTR(context.Context, string, string) error {
	return ErrDNSProviderNotConfigured
}
func (noopDNSProvider) ClearPTR(context.Context, string) error {
	return ErrDNSProviderNotConfigured
}
func (noopDNSProvider) ListPTRs(context.Context, string) (map[string]string, error) {
	return nil, ErrDNSProviderNotConfigured
}

// NewDNSProviderFromEnv returns a PowerDNS provider when PDNS_API_URL and
// PDNS_API_KEY are set, otherwise a no-op (export-only) provider.
func NewDNSProviderFromEnv() DNSProvider {
	url := strings.TrimSpace(os.Getenv("PDNS_API_URL"))
	key := strings.TrimSpace(os.Getenv("PDNS_API_KEY"))
	if url == "" || key == "" {
		return noopDNSProvider{}
	}
	serverID := strings.TrimSpace(os.Getenv("PDNS_SERVER_ID"))
	if serverID == "" {
		serverID = "localhost"
	}
	return &pdnsProvider{
		baseURL:  strings.TrimSuffix(url, "/"),
		apiKey:   key,
		serverID: serverID,
		http:     &http.Client{Timeout: 10 * time.Second},
	}
}

// pdnsProvider talks to the PowerDNS Authoritative Server REST API.
type pdnsProvider struct {
	baseURL  string
	apiKey   string
	serverID string
	http     *http.Client
}

func (p *pdnsProvider) Configured() bool { return true }
func (p *pdnsProvider) Name() string     { return "PowerDNS" }

// pdnsRecord / pdnsRRset model the PowerDNS API payloads.
type pdnsRecord struct {
	Content  string `json:"content"`
	Disabled bool   `json:"disabled"`
}

type pdnsRRset struct {
	Name       string       `json:"name"`
	Type       string       `json:"type"`
	TTL        int          `json:"ttl,omitempty"`
	ChangeType string       `json:"changetype,omitempty"`
	Records    []pdnsRecord `json:"records,omitempty"`
}

type pdnsZone struct {
	Name        string      `json:"name"`
	Kind        string      `json:"kind,omitempty"`
	Nameservers []string    `json:"nameservers,omitempty"`
	RRsets      []pdnsRRset `json:"rrsets,omitempty"`
}

// canonicalZoneName returns the zone name with a single trailing dot.
func canonicalZoneName(name string) string {
	return strings.TrimSuffix(strings.TrimSpace(strings.ToLower(name)), ".") + "."
}

// recordFQDN resolves a record's relative name to a fully-qualified name with a
// trailing dot, within the given zone.
func recordFQDN(zoneName, name string) string {
	zone := strings.TrimSuffix(strings.TrimSpace(strings.ToLower(zoneName)), ".")
	name = strings.TrimSpace(name)
	if name == "" || name == "@" {
		return zone + "."
	}
	lower := strings.ToLower(name)
	if strings.HasSuffix(name, ".") {
		return name // already absolute
	}
	if lower == zone || strings.HasSuffix(lower, "."+zone) {
		return name + "."
	}
	return name + "." + zone + "."
}

// pdnsContent renders a record's content the way PowerDNS expects (priority
// prefix for MX/SRV, trailing dots for hostnames, quotes for TXT).
func pdnsContent(r models.DNSRecord) string {
	switch r.Type {
	case "MX", "SRV":
		return fmt.Sprintf("%d %s", r.Priority, ensureDot(r.Content))
	case "CNAME", "NS":
		return ensureDot(r.Content)
	case "TXT":
		return quoteTXT(r.Content)
	default:
		return r.Content
	}
}

// buildRRsets groups records by (fqdn, type) into REPLACE rrsets, preserving
// first-seen order for deterministic output.
func buildRRsets(zone *models.DNSZone, records []models.DNSRecord) []pdnsRRset {
	type key struct{ name, typ string }
	grouped := map[key][]pdnsRecord{}
	ttls := map[key]int{}
	var order []key

	for i := range records {
		r := records[i]
		k := key{recordFQDN(zone.Name, r.Name), strings.ToUpper(r.Type)}
		if _, ok := grouped[k]; !ok {
			order = append(order, k)
			ttls[k] = orDefaultTTL(r.TTL)
		}
		grouped[k] = append(grouped[k], pdnsRecord{Content: pdnsContent(r), Disabled: false})
	}

	out := make([]pdnsRRset, 0, len(order))
	for _, k := range order {
		out = append(out, pdnsRRset{Name: k.name, Type: k.typ, TTL: ttls[k], ChangeType: "REPLACE", Records: grouped[k]})
	}
	return out
}

// SyncZone ensures the zone exists in PowerDNS and that its records match ours,
// deleting any stale rrsets (except the SOA and the apex NS managed by PowerDNS).
func (p *pdnsProvider) SyncZone(ctx context.Context, zone *models.DNSZone, records []models.DNSRecord) error {
	zoneID := canonicalZoneName(zone.Name)

	current, exists, err := p.getZone(ctx, zoneID)
	if err != nil {
		return err
	}
	if !exists {
		if err := p.createZone(ctx, zone); err != nil {
			return err
		}
		current = nil
	}

	desired := buildRRsets(zone, records)
	desiredKeys := make(map[string]bool, len(desired))
	for _, rr := range desired {
		desiredKeys[rr.Name+"|"+rr.Type] = true
	}

	changes := make([]pdnsRRset, 0, len(desired)+len(current))
	changes = append(changes, desired...)
	for _, rr := range current {
		if rr.Type == "SOA" {
			continue
		}
		if rr.Type == "NS" && rr.Name == zoneID {
			continue // leave the apex NS managed by PowerDNS unless we replace it above
		}
		if !desiredKeys[rr.Name+"|"+rr.Type] {
			changes = append(changes, pdnsRRset{Name: rr.Name, Type: rr.Type, ChangeType: "DELETE"})
		}
	}

	if len(changes) == 0 {
		return nil
	}
	return p.patchZone(ctx, zoneID, changes)
}

// DeleteZone removes the zone from PowerDNS (idempotent: a 404 is success).
func (p *pdnsProvider) DeleteZone(ctx context.Context, zoneName string) error {
	zoneID := canonicalZoneName(zoneName)
	req, err := p.newRequest(ctx, http.MethodDelete, "/zones/"+zoneID, nil)
	if err != nil {
		return err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return expectStatus(resp, http.StatusNoContent, http.StatusOK)
}

func (p *pdnsProvider) getZone(ctx context.Context, zoneID string) ([]pdnsRRset, bool, error) {
	req, err := p.newRequest(ctx, http.MethodGet, "/zones/"+zoneID, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, apiError("get zone", resp)
	}
	var z pdnsZone
	if err := json.NewDecoder(resp.Body).Decode(&z); err != nil {
		return nil, false, fmt.Errorf("decode zone: %w", err)
	}
	return z.RRsets, true, nil
}

func (p *pdnsProvider) createZone(ctx context.Context, zone *models.DNSZone) error {
	ns := zone.PrimaryNS
	if strings.TrimSpace(ns) == "" {
		ns = "ns1." + strings.TrimSuffix(zone.Name, ".")
	}
	body := pdnsZone{
		Name:        canonicalZoneName(zone.Name),
		Kind:        "Native",
		Nameservers: []string{ensureDot(ns)},
	}
	req, err := p.newRequest(ctx, http.MethodPost, "/zones", body)
	if err != nil {
		return err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return expectStatus(resp, http.StatusCreated, http.StatusOK)
}

func (p *pdnsProvider) patchZone(ctx context.Context, zoneID string, rrsets []pdnsRRset) error {
	req, err := p.newRequest(ctx, http.MethodPatch, "/zones/"+zoneID, pdnsZone{RRsets: rrsets})
	if err != nil {
		return err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return expectStatus(resp, http.StatusNoContent, http.StatusOK)
}

func (p *pdnsProvider) newRequest(ctx context.Context, method, path string, body interface{}) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(buf)
	}
	url := fmt.Sprintf("%s/api/v1/servers/%s%s", p.baseURL, p.serverID, path)
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", p.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func expectStatus(resp *http.Response, allowed ...int) error {
	for _, code := range allowed {
		if resp.StatusCode == code {
			return nil
		}
	}
	return apiError("request", resp)
}

func apiError(op string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	msg := strings.TrimSpace(string(b))
	if msg == "" {
		msg = resp.Status
	}
	return fmt.Errorf("powerdns %s failed (%d): %s", op, resp.StatusCode, msg)
}

// reverseZoneName returns the reverse DNS zone that an IP's PTR lives in: the
// /24 in-addr.arpa zone for IPv4, or the /64 ip6.arpa zone for IPv6 (trailing dot).
func reverseZoneName(ipStr string) (string, error) {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return "", fmt.Errorf("invalid IP address: %q", ipStr)
	}
	if v4 := ip.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.in-addr.arpa.", v4[2], v4[1], v4[0]), nil
	}
	v6 := ip.To16()
	if v6 == nil {
		return "", fmt.Errorf("invalid IP address: %q", ipStr)
	}
	const hexdig = "0123456789abcdef"
	// /64: nibbles of the first 8 bytes (network prefix), least-significant first.
	buf := make([]byte, 0, 8*4+len("ip6.arpa."))
	for i := 7; i >= 0; i-- {
		b := v6[i]
		buf = append(buf, hexdig[b&0xF], '.', hexdig[b>>4], '.')
	}
	buf = append(buf, "ip6.arpa."...)
	return string(buf), nil
}

// SetPTR ensures the reverse zone exists in PowerDNS and sets the PTR for ip.
// It works independently of any forward zone.
func (p *pdnsProvider) SetPTR(ctx context.Context, ip, hostname string) error {
	zone, err := reverseZoneName(ip)
	if err != nil {
		return err
	}
	ptrName, err := reversePTRName(ip)
	if err != nil {
		return err
	}
	ptrName += "." // fully-qualified

	if _, exists, err := p.getZone(ctx, zone); err != nil {
		return err
	} else if !exists {
		if err := p.createZone(ctx, &models.DNSZone{Name: zone}); err != nil {
			return err
		}
	}

	host := hostname
	if !strings.HasSuffix(host, ".") {
		host += "."
	}
	return p.patchZone(ctx, zone, []pdnsRRset{{
		Name:       ptrName,
		Type:       "PTR",
		TTL:        3600,
		ChangeType: "REPLACE",
		Records:    []pdnsRecord{{Content: host, Disabled: false}},
	}})
}

// ClearPTR removes the PTR record for ip (no-op if the reverse zone is absent).
func (p *pdnsProvider) ClearPTR(ctx context.Context, ip string) error {
	zone, err := reverseZoneName(ip)
	if err != nil {
		return err
	}
	ptrName, err := reversePTRName(ip)
	if err != nil {
		return err
	}
	ptrName += "."

	if _, exists, err := p.getZone(ctx, zone); err != nil {
		return err
	} else if !exists {
		return nil
	}
	return p.patchZone(ctx, zone, []pdnsRRset{{Name: ptrName, Type: "PTR", ChangeType: "DELETE"}})
}

// ListPTRs returns the PTR records in a reverse zone as fqdn->hostname.
func (p *pdnsProvider) ListPTRs(ctx context.Context, reverseZone string) (map[string]string, error) {
	rrsets, exists, err := p.getZone(ctx, reverseZone)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if !exists {
		return out, nil
	}
	for _, rr := range rrsets {
		if rr.Type != "PTR" || len(rr.Records) == 0 {
			continue
		}
		out[rr.Name] = rr.Records[0].Content
	}
	return out, nil
}
