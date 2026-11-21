package service

import (
	"context"
	"strings"
	"testing"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/stretchr/testify/require"
)

func TestReversePTRName(t *testing.T) {
	tests := []struct {
		ip      string
		want    string
		wantErr bool
	}{
		{ip: "203.0.113.10", want: "10.113.0.203.in-addr.arpa"},
		{ip: "8.8.8.8", want: "8.8.8.8.in-addr.arpa"},
		{ip: "2001:db8::1", want: "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa"},
		{ip: "not-an-ip", wantErr: true},
	}
	for _, tc := range tests {
		got, err := reversePTRName(tc.ip)
		if tc.wantErr {
			require.Error(t, err, tc.ip)
			continue
		}
		require.NoError(t, err, tc.ip)
		require.Equal(t, tc.want, got)
	}
}

func TestIsValidRDNSHostname(t *testing.T) {
	valid := []string{"host.example.com", "vm-01.nodes.example.io", "a.bc", "host.example.com."}
	for _, v := range valid {
		require.True(t, isValidRDNSHostname(v), v)
	}
	invalid := []string{"", "nodot", "-bad.example.com", "bad-.example.com", "a..b", strings.Repeat("x", 64) + ".com"}
	for _, v := range invalid {
		require.False(t, isValidRDNSHostname(v), v)
	}
}

func TestBuildReverseZone(t *testing.T) {
	zone, err := buildReverseZone([]rdnsEntry{
		{Address: "203.0.113.10", RDNS: "vm1.example.com"},
		{Address: "203.0.113.11", RDNS: "vm2.example.com."}, // already FQDN
		{Address: "203.0.113.12", RDNS: ""},                 // skipped
	}, "")
	require.NoError(t, err)
	require.Contains(t, zone, "10.113.0.203.in-addr.arpa. IN PTR vm1.example.com.")
	require.Contains(t, zone, "11.113.0.203.in-addr.arpa. IN PTR vm2.example.com.")
	require.NotContains(t, zone, "12.113.0.203", "empty rDNS entries are skipped")
	// No double trailing dot on the already-FQDN entry.
	require.NotContains(t, zone, "vm2.example.com..")

	// An invalid address surfaces an error.
	_, err = buildReverseZone([]rdnsEntry{{Address: "bogus", RDNS: "x.example.com"}}, "")
	require.Error(t, err)
}

// A VM's owner sets their own PTR. Two things must hold: they can only touch an
// address their VM holds, and the hostname must already point back at it.
func TestSetVMRDNS(t *testing.T) {
	db := setupIPAMServiceTestDB(t)
	svc := NewIPAMService(db, repository.NewIPAMRepository(db))
	ctx := context.Background()

	const vmID, otherVM = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	require.NoError(t, db.Exec(`INSERT INTO ip_pools (id, name) VALUES ('pool-1', 'p')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO ip_addresses (id, pool_id, address, status, vm_id) VALUES
		('addr-mine', 'pool-1', '203.0.113.10/32', 'assigned', ?),
		('addr-theirs', 'pool-1', '203.0.113.20', 'assigned', ?)`, vmID, otherVM).Error)

	orig := rdnsLookupHost
	t.Cleanup(func() { rdnsLookupHost = orig })
	rdnsLookupHost = func(_ context.Context, host string) ([]string, error) {
		if host == "mail.example.com" {
			return []string{"203.0.113.10"}, nil
		}
		return []string{"198.51.100.1"}, nil
	}

	// Somebody else's address: not found, never "forbidden", so it cannot be probed.
	_, err := svc.SetVMRDNS(ctx, vmID, "203.0.113.20", "mail.example.com", true)
	require.ErrorIs(t, err, ErrIPAddressNotFound)

	// A hostname that points elsewhere is refused.
	_, err = svc.SetVMRDNS(ctx, vmID, "203.0.113.10", "mail.google.com", true)
	require.ErrorIs(t, err, ErrRDNSForwardMismatch)

	// One that points back is stored (trailing dot tolerated).
	a, err := svc.SetVMRDNS(ctx, vmID, "203.0.113.10", "mail.example.com.", true)
	require.NoError(t, err)
	require.Equal(t, "mail.example.com", a.RDNS)

	// An administrator skips the check.
	a, err = svc.SetVMRDNS(ctx, vmID, "203.0.113.10", "anything.example.net", false)
	require.NoError(t, err)
	require.Equal(t, "anything.example.net", a.RDNS)

	// Clearing needs no lookup.
	a, err = svc.SetVMRDNS(ctx, vmID, "203.0.113.10", "", true)
	require.NoError(t, err)
	require.Equal(t, "", a.RDNS)
}

func TestPTRLocation(t *testing.T) {
	for _, tc := range []struct {
		ip, zone       string
		wantZone, name string
		wantErr        bool
	}{
		{ip: "203.0.113.10", wantZone: "113.0.203.in-addr.arpa", name: "10.113.0.203.in-addr.arpa"},
		// A parent zone holds the record under its standard name.
		{ip: "203.0.113.10", zone: "0.203.in-addr.arpa.", wantZone: "0.203.in-addr.arpa", name: "10.113.0.203.in-addr.arpa"},
		// RFC 2317, both spellings.
		{ip: "203.0.113.10", zone: "0-127.113.0.203.in-addr.arpa", wantZone: "0-127.113.0.203.in-addr.arpa", name: "10.0-127.113.0.203.in-addr.arpa"},
		{ip: "203.0.113.130", zone: "128/25.113.0.203.in-addr.arpa", wantZone: "128/25.113.0.203.in-addr.arpa", name: "130.128/25.113.0.203.in-addr.arpa"},
		// A classless zone of another /24, or a zone that is not one.
		{ip: "203.0.114.10", zone: "0-127.113.0.203.in-addr.arpa", wantErr: true},
		{ip: "203.0.113.10", zone: "mail.113.0.203.in-addr.arpa", wantErr: true},
		// IPv6 inside a delegated /48 zone.
		{ip: "2001:db8:5::25", zone: "5.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa", wantZone: "5.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa",
			name: "5.2.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.5.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa"},
		{ip: "2001:db8:6::25", zone: "5.0.0.0.8.b.d.0.1.0.0.2.ip6.arpa", wantErr: true},
	} {
		got, err := ptrLocation(tc.ip, tc.zone)
		if tc.wantErr {
			require.Error(t, err, "%s in %s", tc.ip, tc.zone)
			continue
		}
		require.NoError(t, err, "%s in %s", tc.ip, tc.zone)
		require.Equal(t, PTRRecord{Zone: tc.wantZone, Name: tc.name}, got)
	}
	require.NoError(t, validateRDNSZone("203.0.113.0/25", "0-127.113.0.203.in-addr.arpa"))
	require.Error(t, validateRDNSZone("203.0.113.0/25", "example.com"))
	require.Error(t, validateRDNSZone("", "0-127.113.0.203.in-addr.arpa"))
	require.NoError(t, validateRDNSZone("203.0.113.0/25", ""))
}

// A classless zone file carries the CNAMEs the /24's holder must publish.
func TestBuildReverseZoneClassless(t *testing.T) {
	zone, err := buildReverseZone([]rdnsEntry{{Address: "203.0.113.10/32", RDNS: "mail.example.com"}}, "0-127.113.0.203.in-addr.arpa")
	require.NoError(t, err)
	require.Contains(t, zone, "10.0-127.113.0.203.in-addr.arpa. IN PTR mail.example.com.")
	require.Contains(t, zone, ";   10.113.0.203.in-addr.arpa. IN CNAME 10.0-127.113.0.203.in-addr.arpa.")
}

// Inside the IPv6 prefix routed to it, a VM's owner may name any address; a
// prefix the VM does not hold stays out of reach.
func TestSetVMRDNSInsidePrefix(t *testing.T) {
	db := setupIPAMServiceTestDB(t)
	svc := NewIPAMService(db, repository.NewIPAMRepository(db))
	ctx := context.Background()
	const vmID = "11111111-1111-1111-1111-111111111111"
	require.NoError(t, db.Exec(`INSERT INTO ip_pools (id, name, family, cidr, rdns_zone) VALUES ('pool-6', 'v6', 'ipv6', '2001:db8:5::/48', '')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO vm_ipv6_prefixes (id, pool_id, vm_id, prefix, idx) VALUES ('p1', 'pool-6', ?, '2001:db8:5:1::/64', 1)`, vmID).Error)

	a, err := svc.SetVMRDNS(ctx, vmID, "2001:db8:5:1::25", "mail.example.com", false)
	require.NoError(t, err)
	require.Equal(t, "mail.example.com", a.RDNS)
	_, err = svc.SetVMRDNS(ctx, vmID, "2001:db8:5:1::25", "mx.example.com", false)
	require.NoError(t, err, "setting it again replaces the name")

	got, err := svc.ListVMPrefixPTRs(ctx, vmID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "mx.example.com", got[0].RDNS)

	_, err = svc.SetVMRDNS(ctx, vmID, "2001:db8:5:2::25", "mail.example.com", false)
	require.ErrorIs(t, err, ErrIPAddressNotFound, "the neighbouring /64 is someone else's")

	_, err = svc.SetVMRDNS(ctx, vmID, "2001:db8:5:1::25", "", false)
	require.NoError(t, err)
	got, err = svc.ListVMPrefixPTRs(ctx, vmID)
	require.NoError(t, err)
	require.Empty(t, got)
}
