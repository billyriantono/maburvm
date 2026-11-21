package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
)

// The address of a card on a private network is not an extra IPv4 address,
// for the plan's allowance or for the bill; the first address never is.
func TestVMIPv4Counts(t *testing.T) {
	_, db := newPortForwardTestService(t)
	ctx := context.Background()
	for _, r := range [][4]string{
		{"n1", "vm-pub", "203.0.113.10", "2026-01-01 00:00:00"},
		{"n2", "vm-pub", "10.20.0.5", "2026-01-02 00:00:00"}, // a VPC card
		{"n3", "vm-pub", "203.0.113.11/32", "2026-01-03 00:00:00"},
		{"n4", "vm-nat", "10.0.0.5", "2026-01-01 00:00:00"}, // a NAT VPS
		{"n5", "vm-nat", "203.0.113.12", "2026-01-02 00:00:00"},
	} {
		require.NoError(t, db.Exec(`INSERT INTO networks (id, vm_id, ip_address, created_at) VALUES (?, ?, ?, ?)`, r[0], r[1], r[2], r[3]).Error)
	}
	for _, c := range []struct {
		vm           string
		total, extra int64
	}{{"vm-pub", 3, 1}, {"vm-nat", 2, 1}, {"vm-none", 0, 0}} {
		total, extra, err := vmIPv4Counts(ctx, db, c.vm)
		require.NoError(t, err)
		assert.Equal(t, [2]int64{c.total, c.extra}, [2]int64{total, extra}, c.vm)
	}
}

// An over-quota throttle holds in every network job, as an abuse limit does,
// so an unrelated change (a card, a firewall rule) cannot lift it.
func TestNetworkConfigKeepsTheThrottle(t *testing.T) {
	_, db := newGroupTestService(t)
	nets := NewNetworkService(db, repository.NewNetworkRepository(db), repository.NewFirewallRepository(db), repository.NewVMRepository(db), repository.NewNodeRepository(db), nil)
	vm := &models.VM{ID: pfVMA}
	for _, c := range []struct {
		limit     int64
		throttled bool
		speed     int
		want      int64
	}{{100, false, 5, 100}, {100, true, 5, 5}, {0, true, 0, defaultThrottleMbps}, {3, true, 5, 3}} {
		raw, err := nets.networkConfigParams(context.Background(), vm,
			&models.Network{VMID: pfVMA, IPAddress: "10.0.0.5", BandwidthLimit: c.limit, Throttled: c.throttled, ThrottleSpeedMbps: c.speed}, nil)
		require.NoError(t, err)
		var p NetworkConfigParams
		require.NoError(t, json.Unmarshal(raw, &p))
		assert.Equal(t, c.want, p.BandwidthLimit, "%+v", c)
	}
}

func TestHostOnlyIP(t *testing.T) {
	cases := map[string]string{
		"203.0.113.10":    "203.0.113.10",
		"203.0.113.10/24": "203.0.113.10",
		"203.0.113.10/32": "203.0.113.10",
		"2001:db8::1":     "2001:db8::1",
		"2001:db8::1/64":  "2001:db8::1",
		"":                "",
	}
	for in, want := range cases {
		if got := hostOnlyIP(in); got != want {
			t.Errorf("hostOnlyIP(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNetmaskFromCIDR(t *testing.T) {
	cases := map[string]string{
		"203.0.113.0/24": "255.255.255.0",
		"10.0.0.0/8":     "255.0.0.0",
		"192.168.1.0/30": "255.255.255.252",
		"2001:db8::/64":  "/64",
		"":               "",
		"not-a-cidr":     "",
	}
	for in, want := range cases {
		if got := netmaskFromCIDR(in); got != want {
			t.Errorf("netmaskFromCIDR(%q) = %q, want %q", in, got, want)
		}
	}
}
