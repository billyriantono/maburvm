package network

import (
	"strings"
	"testing"
)

// The panel accepts "80", "1000-11000" and "80,443"; iptables wants a colon for
// a range and -m multiport for anything that is not a single port. A mismatch
// here is not a cosmetic bug: rules are applied as a set, so one unparsable
// entry aborts the apply and leaves the VM with NO rules — wide open, rather
// than partially fenced.
func TestNormalizePortSpec(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		want      string
		wantMulti bool
		wantErr   bool
	}{
		{name: "single port", in: "80", want: "80", wantMulti: false},
		{
			name: "hyphenated range becomes a colon range", in: "1000-11000",
			want: "1000:11000", wantMulti: true,
		},
		{
			name: "colon range still accepted", in: "1000:11000",
			want: "1000:11000", wantMulti: true,
		},
		{name: "comma list", in: "80,443,8443", want: "80,443,8443", wantMulti: true},
		{name: "mixed list and range", in: "80,1000-2000", want: "80,1000:2000", wantMulti: true},
		{name: "full range bounds", in: "1-65535", want: "1:65535", wantMulti: true},

		{name: "port zero", in: "0", wantErr: true},
		{name: "port above 65535", in: "65536", wantErr: true},
		{name: "descending range", in: "11000-1000", wantErr: true},
		{name: "not a number", in: "http", wantErr: true},
		{name: "three bounds", in: "80:443:8443", wantErr: true},
		{
			name: "more entries than multiport allows",
			in:   "1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16", wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, multi, err := normalizePortSpec(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizePortSpec(%q) = %q, want an error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizePortSpec(%q) returned unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("normalizePortSpec(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if multi != tt.wantMulti {
				t.Errorf("normalizePortSpec(%q) multiport = %v, want %v", tt.in, multi, tt.wantMulti)
			}
		})
	}
}

func TestBuildRuleSpecRemoteAddress(t *testing.T) {
	base := FirewallRule{ID: "r", Protocol: "tcp", PortRange: "22", Action: "allow", Priority: 10}

	in := base
	in.Direction, in.SourceIP = "inbound", "198.51.100.0/24"
	got, err := buildRuleSpec("vm", "192.0.2.10", in)
	if err != nil || strings.Join(got[:6], " ") != "-p tcp -s 198.51.100.0/24 -d 192.0.2.10" {
		t.Fatalf("inbound: %v %v", got, err)
	}

	// Outbound: the CIDR is the destination; two -s flags made iptables fail.
	out := base
	out.Direction, out.SourceIP = "outbound", "198.51.100.0/24"
	got, err = buildRuleSpec("vm", "192.0.2.10", out)
	if err != nil || strings.Join(got[:6], " ") != "-p tcp -s 192.0.2.10 -d 198.51.100.0/24" {
		t.Fatalf("outbound: %v %v", got, err)
	}

	// An IPv6 remote is not an IPv4 rule, and not an error either.
	v6 := base
	v6.Direction, v6.SourceIP = "inbound", "2001:db8::/32"
	if got, err := buildRuleSpec("vm", "192.0.2.10", v6); got != nil || err != nil {
		t.Fatalf("v6 remote: %v %v", got, err)
	}
}
