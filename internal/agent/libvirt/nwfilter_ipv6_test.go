package libvirt

import (
	"encoding/xml"
	"strings"
	"testing"

	"libvirt.org/go/libvirtxml"
)

func TestInterfaceFilterRef(t *testing.T) {
	if InterfaceFilterRef(false, "203.0.113.10", "52:54:00:12:34:56", "2001:db8:20:5::/64") != nil {
		t.Fatal("anti-spoofing off must bind no filter")
	}

	v4 := InterfaceFilterRef(true, "203.0.113.10", "52:54:00:12:34:56", "")
	if v4.Filter != "clean-traffic" || len(v4.Parameters) != 2 {
		t.Fatalf("v4-only interface must keep stock clean-traffic with IP+MAC, got %+v", v4)
	}

	v6 := InterfaceFilterRef(true, "203.0.113.10", "52:54:00:12:34:56", "2001:db8:20:5::/64")
	if v6.Filter != IPv6CleanTrafficFilter {
		t.Fatalf("filter = %q", v6.Filter)
	}
	params := map[string]string{}
	for _, p := range v6.Parameters {
		params[p.Name] = p.Value
	}
	if params["IP"] != "203.0.113.10" || params["MAC"] != "52:54:00:12:34:56" || params["IPV6_PREFIX"] != "2001:db8:20:5::" {
		t.Fatalf("params = %v", params)
	}
	if !filterRefEqual(v6, InterfaceFilterRef(true, "203.0.113.10", "52:54:00:12:34:56", "2001:db8:20:5::/64")) {
		t.Fatal("identical refs must compare equal")
	}
	if filterRefEqual(v4, v6) || filterRefEqual(nil, v4) {
		t.Fatal("different refs must not compare equal")
	}
}

func TestDesiredFilterRef(t *testing.T) {
	mac := "52:54:00:12:34:56"
	v4 := InterfaceFilterRef(true, "203.0.113.10", mac, "")
	v6 := InterfaceFilterRef(true, "203.0.113.10", mac, "2001:db8:20:5::/64")

	// Untouched VMs (no filter, foreign filter) stay untouched unless a prefix arrives.
	if desiredFilterRef(nil, "203.0.113.10", mac, "") != nil {
		t.Error("unfiltered v4-only interface must be left alone")
	}
	other := &libvirtxml.DomainInterfaceFilterRef{Filter: "allow-arp"}
	if desiredFilterRef(other, "203.0.113.10", mac, "") != nil {
		t.Error("foreign filter must be left alone")
	}
	if got := desiredFilterRef(nil, "203.0.113.10", mac, "2001:db8:20:5::/64"); !filterRefEqual(got, v6) {
		t.Errorf("prefix on unfiltered interface: got %+v", got)
	}
	// Prefix released: back to stock clean-traffic.
	if got := desiredFilterRef(v6, "203.0.113.10", mac, ""); !filterRefEqual(got, v4) {
		t.Errorf("release: got %+v", got)
	}
	// Address changed on clean-traffic: IP parameter follows.
	got := desiredFilterRef(v4, "203.0.113.20", mac, "")
	if got == nil || got.Filter != "clean-traffic" || got.Parameters[0].Value != "203.0.113.20" {
		t.Errorf("ip change: got %+v", got)
	}
	// Unchanged: caller sees equality and does nothing.
	if !filterRefEqual(desiredFilterRef(v4, "203.0.113.10", mac, ""), v4) {
		t.Error("unchanged clean-traffic must compare equal")
	}
}

func TestIPv6PrefixNetwork(t *testing.T) {
	for in, want := range map[string]string{
		"2001:db8:20:5::/64":  "2001:db8:20:5::",
		"2001:db8:20:5::1/64": "2001:db8:20:5::",
		"203.0.113.0/24":       "",
		"":                     "",
		"garbage":              "",
	} {
		if got := IPv6PrefixNetwork(in); got != want {
			t.Errorf("IPv6PrefixNetwork(%q) = %q, want %q", in, got, want)
		}
	}
}

// The filter XML is hand-written; make sure it is well-formed and references
// the variable the interface binding supplies, with ebtables-level matches.
func TestIPv6FilterXMLWellFormed(t *testing.T) {
	for _, def := range []string{ipv6ChainXML, ipv6CleanTrafficXML} {
		var f libvirtxml.NWFilter
		if err := xml.Unmarshal([]byte(def), &f); err != nil {
			t.Fatalf("filter XML does not parse: %v\n%s", err, def)
		}
	}
	// ebtables-level <ipv6> matches only: libvirt 6.0 (on the nodes) accepts
	// protocol='icmpv6'/'udp' with type/port there; icmpv6/udp-ipv6 elements are
	// ip6tables-level and would need br_netfilter.
	for _, want := range []string{"chain='ipv6'", "$IPV6_PREFIX", "protocol='icmpv6' type='134'", "protocol='icmpv6' type='136'", "protocol='udp' srcportstart='547'"} {
		if !strings.Contains(ipv6ChainXML, want) {
			t.Errorf("ipv6 chain missing %q", want)
		}
	}
	if !strings.Contains(ipv6CleanTrafficXML, "filter='"+ipv6ChainFilter+"'") {
		t.Error("root filter does not reference the ipv6 chain")
	}
}

func TestWithFilterUUID(t *testing.T) {
	got := withFilterUUID(ipv6ChainXML, "8f2b4c90-253d-49e5-94a8-9e5b8cbd1db3")
	var f libvirtxml.NWFilter
	if err := xml.Unmarshal([]byte(got), &f); err != nil {
		t.Fatalf("does not parse: %v\n%s", err, got)
	}
	if f.UUID != "8f2b4c90-253d-49e5-94a8-9e5b8cbd1db3" || f.Name != ipv6ChainFilter {
		t.Fatalf("uuid %q name %q", f.UUID, f.Name)
	}
}
