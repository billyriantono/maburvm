package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// reversePTRName returns the in-addr.arpa (IPv4) or ip6.arpa (IPv6) name for an
// IP, without a trailing dot. e.g. 203.0.113.10 -> "10.113.0.203.in-addr.arpa".
func reversePTRName(ipStr string) (string, error) {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return "", fmt.Errorf("invalid IP address: %q", ipStr)
	}
	if v4 := ip.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.%d.in-addr.arpa", v4[3], v4[2], v4[1], v4[0]), nil
	}
	v6 := ip.To16()
	if v6 == nil {
		return "", fmt.Errorf("invalid IP address: %q", ipStr)
	}
	const hexdig = "0123456789abcdef"
	// 32 nibbles, least-significant first, dot-separated.
	buf := make([]byte, 0, 4*len(v6)+len("ip6.arpa"))
	for i := len(v6) - 1; i >= 0; i-- {
		b := v6[i]
		buf = append(buf, hexdig[b&0xF], '.', hexdig[b>>4], '.')
	}
	buf = append(buf, "ip6.arpa"...)
	return string(buf), nil
}

// PTRRecord is where an address's PTR lives: the zone holding it and the
// record's full name, both without a trailing dot.
type PTRRecord struct {
	Zone string
	Name string
}

// ptrLocation places an address's PTR record. With no zone given it is the
// standard one: the /24 zone for IPv4, the /64 zone for IPv6. A zone that is a
// parent of the address's name (a /16, or the IPv6 /48 delegated to us) holds
// the record under its standard name. For IPv4, a zone below the address's /24
// is an RFC 2317 classless zone ("0-127.113.0.203.in-addr.arpa", with
// "/" in place of "-" also common), which names the record after the last
// octet inside it: the upstream's /24 points at it with a CNAME.
func ptrLocation(ip, zone string) (PTRRecord, error) {
	name, err := reversePTRName(ip)
	if err != nil {
		return PTRRecord{}, err
	}
	zone = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zone)), ".")
	if zone == "" {
		std, err := reverseZoneName(ip)
		if err != nil {
			return PTRRecord{}, err
		}
		return PTRRecord{Zone: strings.TrimSuffix(std, "."), Name: name}, nil
	}
	if name == zone || strings.HasSuffix(name, "."+zone) {
		return PTRRecord{Zone: zone, Name: name}, nil
	}
	if v4 := net.ParseIP(ip).To4(); v4 != nil {
		slash24 := fmt.Sprintf("%d.%d.%d.in-addr.arpa", v4[2], v4[1], v4[0])
		if label, rest, ok := strings.Cut(zone, "."); ok && rest == slash24 && classlessLabel(label) {
			return PTRRecord{Zone: zone, Name: fmt.Sprintf("%d.%s", v4[3], zone)}, nil
		}
	}
	return PTRRecord{}, fmt.Errorf("%s does not belong in the reverse zone %s", ip, zone)
}

// classlessLabel reports whether a zone's first label names a sub-/24 block
// the RFC 2317 way: two numbers with "-" or "/" between them.
func classlessLabel(label string) bool {
	a, b, ok := strings.Cut(label, "-")
	if !ok {
		a, b, ok = strings.Cut(label, "/")
	}
	if !ok || a == "" || b == "" {
		return false
	}
	for _, r := range a + b {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// validateRDNSZone checks a pool's reverse zone: a DNS name under in-addr.arpa
// or ip6.arpa that holds the first address of the pool.
func validateRDNSZone(cidr, zone string) error {
	zone = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zone)), ".")
	if zone == "" {
		return nil
	}
	if len(zone) > 253 || !(strings.HasSuffix(zone, ".in-addr.arpa") || strings.HasSuffix(zone, ".ip6.arpa")) {
		return fmt.Errorf("the reverse zone must end in .in-addr.arpa or .ip6.arpa")
	}
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("set the pool's CIDR before its reverse zone")
	}
	if _, err := ptrLocation(ip.String(), zone); err != nil {
		return err
	}
	return nil
}

// isValidRDNSHostname reports whether name is a plausible fully-qualified
// hostname for a PTR target (RFC 1123 labels, at least two labels).
func isValidRDNSHostname(name string) bool {
	name = strings.TrimSuffix(name, ".")
	if name == "" || len(name) > 253 {
		return false
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if len(l) == 0 || len(l) > 63 {
			return false
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-':
			default:
				return false
			}
		}
	}
	return true
}

// rdnsEntry is one address→PTR mapping used to build a reverse zone.
type rdnsEntry struct {
	Address string
	RDNS    string
}

// buildReverseZone renders PTR records as a BIND-style zone fragment ready to
// load into an authoritative nameserver, placed in zone (see ptrLocation; ""
// for the standard zones). Entries with an empty RDNS are skipped. For a
// classless zone it also lists the CNAMEs the holder of the /24 must publish.
func buildReverseZone(entries []rdnsEntry, zone string) (string, error) {
	var b, cnames strings.Builder
	b.WriteString("; MaburVM reverse DNS (PTR) records\n")
	b.WriteString("; Load these into your authoritative nameserver.\n")
	b.WriteString("$TTL 3600\n")
	for _, e := range entries {
		if strings.TrimSpace(e.RDNS) == "" {
			continue
		}
		rec, err := ptrLocation(strings.SplitN(e.Address, "/", 2)[0], zone)
		if err != nil {
			return "", err
		}
		std, _ := reversePTRName(strings.SplitN(e.Address, "/", 2)[0])
		if rec.Name != std {
			fmt.Fprintf(&cnames, ";   %s. IN CNAME %s.\n", std, rec.Name)
		}
		fmt.Fprintf(&b, "%s. IN PTR %s\n", rec.Name, ensureDot(e.RDNS))
	}
	if cnames.Len() > 0 {
		b.WriteString(";\n; Classless delegation (RFC 2317): the holder of the /24 zone publishes these\n")
		b.WriteString("; CNAMEs and delegates " + zone + " to your nameserver:\n")
		b.WriteString(cnames.String())
	}
	return b.String(), nil
}

// poolRDNSZone is the reverse zone a pool's addresses are written to.
func (s *IPAMService) poolRDNSZone(ctx context.Context, poolID string) string {
	if poolID == "" {
		return ""
	}
	var zone string
	s.db.WithContext(ctx).Table("ip_pools").Select("rdns_zone").Where("id = ?", poolID).Scan(&zone)
	return zone
}

// pushPTR writes or clears one PTR on the live nameserver, when one is
// configured. Without one the record is export-only (see GenerateReverseZone).
func (s *IPAMService) pushPTR(ctx context.Context, ip, zone, ptr string) error {
	if s.dnsProvider == nil || !s.dnsProvider.Configured() {
		return nil
	}
	rec, err := ptrLocation(ip, zone)
	if err != nil {
		return err
	}
	if ptr == "" {
		if err := s.dnsProvider.ClearPTR(ctx, rec); err != nil {
			return fmt.Errorf("failed to clear PTR on nameserver: %w", err)
		}
		return nil
	}
	if err := s.dnsProvider.SetPTR(ctx, rec, ptr); err != nil {
		return fmt.Errorf("failed to set PTR on nameserver: %w", err)
	}
	return nil
}

// SetRDNS sets (or clears, when ptr is empty) the reverse-DNS hostname for an
// address after validating it.
func (s *IPAMService) SetRDNS(ctx context.Context, addressID, ptr string) (*models.IPAddress, error) {
	ptr = strings.TrimSpace(ptr)
	if ptr != "" && !isValidRDNSHostname(ptr) {
		return nil, fmt.Errorf("invalid rDNS hostname: %q", ptr)
	}
	address, err := s.repo.GetAddress(ctx, addressID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrIPAddressNotFound
		}
		return nil, err
	}

	// When a live nameserver is configured, push the PTR first so the DB only
	// reflects what's actually in DNS. Works without any forward zone (the
	// provider manages the reverse zone).
	if err := s.pushPTR(ctx, strings.SplitN(address.Address, "/", 2)[0], s.poolRDNSZone(ctx, address.PoolID), ptr); err != nil {
		return nil, err
	}

	address.RDNS = ptr
	if err := s.repo.UpdateAddress(ctx, address); err != nil {
		return nil, err
	}
	return address, nil
}

// ErrRDNSForwardMismatch means the requested PTR hostname does not resolve back
// to the address it is being set on.
var ErrRDNSForwardMismatch = errors.New("hostname does not resolve to this address")

// rdnsForwardMismatch carries the sentence shown to the customer, and still
// answers errors.Is(err, ErrRDNSForwardMismatch).
type rdnsForwardMismatch struct{ host, ip string }

func (e rdnsForwardMismatch) Error() string {
	return fmt.Sprintf("%s does not point to %s yet. Add a DNS record for it, then save again.", e.host, e.ip)
}
func (e rdnsForwardMismatch) Is(target error) bool { return target == ErrRDNSForwardMismatch }

// rdnsLookupHost resolves a hostname for the forward check. A variable so tests
// can answer without the network.
var rdnsLookupHost = func(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

// SetVMRDNS sets or clears the PTR for an address a VM holds, which is how a
// VM's owner manages their own reverse DNS without any view of the pools.
//
// With verifyForward, the hostname must already resolve to the address. That
// is what makes a self-service PTR safe to offer: without it a customer could
// label their address as somebody else's mail server. It is also what mail
// receivers check, so a PTR that fails it would not help the customer anyway.
func (s *IPAMService) SetVMRDNS(ctx context.Context, vmID, ip, ptr string, verifyForward bool) (*models.IPAddress, error) {
	want := net.ParseIP(strings.TrimSpace(ip))
	if want == nil {
		return nil, fmt.Errorf("%q is not an IP address", ip)
	}
	addrs, err := s.repo.ListAddressesByVMID(ctx, vmID)
	if err != nil {
		return nil, err
	}
	var held *models.IPAddress
	for i := range addrs {
		// An inet column may come back with a /32 suffix.
		if got := net.ParseIP(strings.SplitN(addrs[i].Address, "/", 2)[0]); got != nil && got.Equal(want) {
			held = &addrs[i]
			break
		}
	}
	var prefix *models.VMIPv6Prefix
	if held == nil {
		// Any address in the IPv6 prefix routed to the VM is the VM's to name.
		p, err := vmIPv6Prefix(ctx, s.db, vmID)
		if err != nil {
			return nil, err
		}
		if p == nil || want.To4() != nil || !prefixContains(p.Prefix, want) {
			return nil, ErrIPAddressNotFound
		}
		prefix = p
	}

	ptr = strings.TrimSuffix(strings.TrimSpace(ptr), ".")
	if ptr != "" && verifyForward {
		if !isValidRDNSHostname(ptr) {
			return nil, fmt.Errorf("invalid rDNS hostname: %q", ptr)
		}
		lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		resolved, lerr := rdnsLookupHost(lookupCtx, ptr)
		cancel()
		matches := false
		for _, r := range resolved {
			if got := net.ParseIP(r); got != nil && got.Equal(want) {
				matches = true
				break
			}
		}
		if lerr != nil || !matches {
			return nil, rdnsForwardMismatch{host: ptr, ip: want.String()}
		}
	}
	if prefix != nil {
		return s.setPrefixPTR(ctx, vmID, prefix, want.String(), ptr)
	}
	return s.SetRDNS(ctx, held.ID, ptr)
}

// prefixContains reports whether ip lies inside the prefix (which an inet or
// cidr column may return with or without its length).
func prefixContains(prefix string, ip net.IP) bool {
	_, n, err := net.ParseCIDR(strings.TrimSpace(prefix))
	return err == nil && n.Contains(ip)
}

// maxPrefixPTRs bounds how many names one VM may set inside its prefix: a
// /64 has more addresses than any zone should hold.
const maxPrefixPTRs = 64

// setPrefixPTR sets or clears the PTR of an address inside a VM's routed
// prefix. The answer is shaped like an address row for the caller's sake.
func (s *IPAMService) setPrefixPTR(ctx context.Context, vmID string, prefix *models.VMIPv6Prefix, ip, ptr string) (*models.IPAddress, error) {
	if ptr != "" && !isValidRDNSHostname(ptr) {
		return nil, fmt.Errorf("invalid rDNS hostname: %q", ptr)
	}
	if ptr != "" {
		var n int64
		s.db.WithContext(ctx).Model(&models.VMPrefixPTR{}).Where("vm_id = ? AND address <> ?", vmID, ip).Count(&n)
		if n >= maxPrefixPTRs {
			return nil, fmt.Errorf("at most %d reverse DNS names can be set inside one prefix", maxPrefixPTRs)
		}
	}
	if err := s.pushPTR(ctx, ip, s.poolRDNSZone(ctx, prefix.PoolID), ptr); err != nil {
		return nil, err
	}
	db := s.db.WithContext(ctx)
	var err error
	if ptr == "" {
		err = db.Where("vm_id = ? AND address = ?", vmID, ip).Delete(&models.VMPrefixPTR{}).Error
	} else {
		now := time.Now()
		err = db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "address"}},
			DoUpdates: clause.Assignments(map[string]any{"rdns": ptr, "vm_id": vmID, "updated_at": now}),
		}).Create(&models.VMPrefixPTR{ID: uuid.NewString(), VMID: vmID, Address: ip, RDNS: ptr, CreatedAt: now, UpdatedAt: now}).Error
	}
	if err != nil {
		return nil, err
	}
	return &models.IPAddress{Address: ip, RDNS: ptr}, nil
}

// ListVMPrefixPTRs are the names set inside a VM's routed prefix.
func (s *IPAMService) ListVMPrefixPTRs(ctx context.Context, vmID string) ([]models.VMPrefixPTR, error) {
	out := []models.VMPrefixPTR{}
	err := s.db.WithContext(ctx).Where("vm_id = ?", vmID).Order("address").Find(&out).Error
	for i := range out {
		out[i].Address = strings.SplitN(out[i].Address, "/", 2)[0]
	}
	return out, err
}

// clearPrefixPTRs drops the names set inside a VM's prefix when the prefix
// goes back to its pool, so the next holder does not inherit them. The
// nameserver is cleared after the transaction, best-effort.
func (s *IPAMService) clearPrefixPTRs(ctx context.Context, tx *gorm.DB, vmID string) []models.VMPrefixPTR {
	var rows []models.VMPrefixPTR
	if err := tx.WithContext(ctx).Where("vm_id = ?", vmID).Find(&rows).Error; err != nil || len(rows) == 0 {
		return nil
	}
	if err := tx.WithContext(ctx).Where("vm_id = ?", vmID).Delete(&models.VMPrefixPTR{}).Error; err != nil {
		return nil
	}
	return rows
}

// ImportRDNS pulls existing PTR records from the live nameserver into MaburVM's
// view for a pool's addresses (read-only: it never pushes back). This lets you
// adopt rDNS that was set manually before migrating, so the UI reflects reality
// and you don't accidentally overwrite untouched PTRs. Returns the count updated.
func (s *IPAMService) ImportRDNS(ctx context.Context, poolID string) (int, error) {
	if s.dnsProvider == nil || !s.dnsProvider.Configured() {
		return 0, ErrDNSProviderNotConfigured
	}
	addrs, err := s.ListAddresses(ctx, poolID, 0, 0)
	if err != nil {
		return 0, err
	}

	poolZone := s.poolRDNSZone(ctx, poolID)
	zoneCache := make(map[string]map[string]string) // reverseZone -> {ptrFQDN -> host}
	imported := 0
	for i := range addrs {
		a := &addrs[i]
		rec, zerr := ptrLocation(strings.SplitN(a.Address, "/", 2)[0], poolZone)
		if zerr != nil {
			continue
		}
		zone := rec.Zone + "."
		ptrs, ok := zoneCache[zone]
		if !ok {
			ptrs, err = s.dnsProvider.ListPTRs(ctx, zone)
			if err != nil {
				return imported, err
			}
			zoneCache[zone] = ptrs
		}
		host := strings.TrimSuffix(ptrs[rec.Name+"."], ".")
		if host != "" && host != a.RDNS {
			a.RDNS = host
			if uerr := s.repo.UpdateAddress(ctx, a); uerr != nil {
				return imported, uerr
			}
			imported++
		}
	}
	return imported, nil
}

// GenerateReverseZone renders PTR records for every address in a pool that has
// an rDNS hostname set.
func (s *IPAMService) GenerateReverseZone(ctx context.Context, poolID string) (string, error) {
	addrs, err := s.ListAddresses(ctx, poolID, 0, 0)
	if err != nil {
		return "", err
	}
	entries := make([]rdnsEntry, 0, len(addrs))
	for i := range addrs {
		if addrs[i].RDNS != "" {
			entries = append(entries, rdnsEntry{Address: addrs[i].Address, RDNS: addrs[i].RDNS})
		}
	}
	return buildReverseZone(entries, s.poolRDNSZone(ctx, poolID))
}
