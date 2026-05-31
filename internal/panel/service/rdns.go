package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
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
// load into an authoritative nameserver. Entries with an empty RDNS are skipped.
func buildReverseZone(entries []rdnsEntry) (string, error) {
	var b strings.Builder
	b.WriteString("; MaburVM reverse DNS (PTR) records\n")
	b.WriteString("; Load these into your authoritative nameserver.\n")
	b.WriteString("$TTL 3600\n")
	for _, e := range entries {
		if strings.TrimSpace(e.RDNS) == "" {
			continue
		}
		ptr, err := reversePTRName(e.Address)
		if err != nil {
			return "", err
		}
		host := e.RDNS
		if !strings.HasSuffix(host, ".") {
			host += "."
		}
		fmt.Fprintf(&b, "%s. IN PTR %s\n", ptr, host)
	}
	return b.String(), nil
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
	// provider manages the reverse zone). When not configured, this is skipped
	// and the record is export-only (see GenerateReverseZone).
	if s.dnsProvider != nil && s.dnsProvider.Configured() {
		if ptr == "" {
			if perr := s.dnsProvider.ClearPTR(ctx, address.Address); perr != nil {
				return nil, fmt.Errorf("failed to clear PTR on nameserver: %w", perr)
			}
		} else {
			if perr := s.dnsProvider.SetPTR(ctx, address.Address, ptr); perr != nil {
				return nil, fmt.Errorf("failed to set PTR on nameserver: %w", perr)
			}
		}
	}

	address.RDNS = ptr
	if err := s.repo.UpdateAddress(ctx, address); err != nil {
		return nil, err
	}
	return address, nil
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

	zoneCache := make(map[string]map[string]string) // reverseZone -> {ptrFQDN -> host}
	imported := 0
	for i := range addrs {
		a := &addrs[i]
		zone, zerr := reverseZoneName(a.Address)
		if zerr != nil {
			continue
		}
		ptrs, ok := zoneCache[zone]
		if !ok {
			ptrs, err = s.dnsProvider.ListPTRs(ctx, zone)
			if err != nil {
				return imported, err
			}
			zoneCache[zone] = ptrs
		}
		ptrName, perr := reversePTRName(a.Address)
		if perr != nil {
			continue
		}
		host := strings.TrimSuffix(ptrs[ptrName+"."], ".")
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
	return buildReverseZone(entries)
}
