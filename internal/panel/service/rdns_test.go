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
	})
	require.NoError(t, err)
	require.Contains(t, zone, "10.113.0.203.in-addr.arpa. IN PTR vm1.example.com.")
	require.Contains(t, zone, "11.113.0.203.in-addr.arpa. IN PTR vm2.example.com.")
	require.NotContains(t, zone, "12.113.0.203", "empty rDNS entries are skipped")
	// No double trailing dot on the already-FQDN entry.
	require.NotContains(t, zone, "vm2.example.com..")

	// An invalid address surfaces an error.
	_, err = buildReverseZone([]rdnsEntry{{Address: "bogus", RDNS: "x.example.com"}})
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
