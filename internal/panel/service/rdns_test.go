package service

import (
	"strings"
	"testing"

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
