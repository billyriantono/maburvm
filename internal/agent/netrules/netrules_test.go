package netrules

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const testVM = "0b6f3c1e-5d0a-4c57-9a55-1f1e6f3f8a10"

// A port forward must name the one address it answers on. Matching "any local
// address" is what let one customer's forward take the port on every floating
// IP bound on the node.
func TestPortForwardRuleMatchesOnlyThePublishAddress(t *testing.T) {
	rule := strings.Join(PortForwardRule("203.0.113.2", testVM, 3306, "10.0.0.5", 3306, "tcp", ""), " ")
	want := "-d 203.0.113.2/32 -p tcp --dport 3306 -j DNAT --to-destination 10.0.0.5:3306 -m comment --comment maburvm-vm-" + testVM + "-port-3306"
	if rule != want {
		t.Fatalf("rule = %q\nwant   %q", rule, want)
	}
	if strings.Contains(rule, "addrtype") || strings.Contains(rule, "LOCAL") {
		t.Fatalf("rule still matches every local address: %q", rule)
	}

	restricted := PortForwardRule("203.0.113.2", testVM, 8080, "10.0.0.5", 80, "udp", "198.51.100.0/24")
	if got := strings.Join(restricted[:4], " "); got != "-s 198.51.100.0/24 -d 203.0.113.2/32" {
		t.Fatalf("source restriction lost: %q", got)
	}
	if any := PortForwardRule("203.0.113.2", testVM, 8080, "10.0.0.5", 80, "tcp", "0.0.0.0/0"); any[0] != "-d" {
		t.Fatalf("0.0.0.0/0 must mean any source, got %v", any)
	}
}

func TestRescopedPortForward(t *testing.T) {
	const addr = "203.0.113.2"
	tag := "maburvm-vm-" + testVM + "-port-8080"
	scoped := []string{"-d", "203.0.113.2/32", "-p", "tcp", "-m", "tcp", "--dport", "8080", "-m", "comment", "--comment", tag, "-j", "DNAT", "--to-destination", "10.0.0.5:80"}

	cases := []struct {
		name    string
		line    string
		want    []string
		changed bool
	}{
		{
			name:    "every local address",
			line:    "-A MABURVM-NAT -p tcp -m addrtype --dst-type LOCAL -m tcp --dport 8080 -m comment --comment " + tag + " -j DNAT --to-destination 10.0.0.5:80",
			want:    scoped,
			changed: true,
		},
		{
			name:    "every destination, the oldest form",
			line:    "-A MABURVM-NAT -p tcp -m tcp --dport 8080 -m comment --comment " + tag + " -j DNAT --to-destination 10.0.0.5:80",
			want:    scoped,
			changed: true,
		},
		{
			name:    "source restriction is kept",
			line:    "-A MABURVM-NAT -s 198.51.100.0/24 -p tcp -m addrtype --dst-type LOCAL -m tcp --dport 8080 -m comment --comment " + tag + " -j DNAT --to-destination 10.0.0.5:80",
			want:    append([]string{"-d", "203.0.113.2/32", "-s", "198.51.100.0/24"}, scoped[2:]...),
			changed: true,
		},
		{
			name:    "already scoped",
			line:    "-A MABURVM-NAT -d 203.0.113.2/32 -p tcp -m tcp --dport 8080 -m comment --comment " + tag + " -j DNAT --to-destination 10.0.0.5:80",
			want:    scoped,
			changed: false,
		},
		{
			name:    "scoped to an address the node no longer publishes on",
			line:    "-A MABURVM-NAT -d 203.0.113.99/32 -p tcp -m tcp --dport 8080 -m comment --comment " + tag + " -j DNAT --to-destination 10.0.0.5:80",
			want:    scoped,
			changed: true,
		},
		{
			name:    "quoted comment",
			line:    `-A MABURVM-NAT -p tcp -m addrtype --dst-type LOCAL -m tcp --dport 8080 -m comment --comment "` + tag + `" -j DNAT --to-destination 10.0.0.5:80`,
			want:    scoped,
			changed: true,
		},
	}
	for _, c := range cases {
		got, changed := RescopedPortForward(c.line, addr)
		if changed != c.changed || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got  %v (changed=%v)\n want %v (changed=%v)", c.name, got, changed, c.want, c.changed)
		}
	}

	for _, line := range []string{
		"-N MABURVM-NAT",
		"-A MABURVM-NAT -s 10.0.0.5/32 -j MASQUERADE -m comment --comment maburvm-vm-" + testVM,
	} {
		if spec, changed := RescopedPortForward(line, addr); changed || spec != nil {
			t.Errorf("%q is not a port forward and must be left alone, got %v", line, spec)
		}
	}
}

func TestResolvePublishAddress(t *testing.T) {
	floating := RuleDestinations([]string{
		"-N MABURVM-FIP",
		`-A MABURVM-FIP -d 203.0.113.50/32 -m comment --comment "maburvm-fip-203.0.113.50" -j DNAT --to-destination 10.0.0.9`,
	})
	if !floating["203.0.113.50"] || len(floating) != 1 {
		t.Fatalf("floating addresses = %v", floating)
	}

	ok := []struct{ configured, detected, want string }{
		{"", "203.0.113.2", "203.0.113.2"},
		{" 203.0.113.7 ", "203.0.113.2", "203.0.113.7"}, // the operator's choice wins
	}
	for _, c := range ok {
		got, err := ResolvePublishAddress(c.configured, c.detected, floating)
		if err != nil || got != c.want {
			t.Errorf("ResolvePublishAddress(%q, %q) = %q, %v; want %q", c.configured, c.detected, got, err, c.want)
		}
	}

	refused := []struct{ configured, detected string }{
		{"", ""},                       // nothing detected
		{"", "127.0.0.1"},              // not an address anyone can reach
		{"", "0.0.0.0"},                // would mean every address again
		{"2001:db8::1", "203.0.113.2"}, // forwards are IPv4
		{"not-an-address", "203.0.113.2"},
		{"", "203.0.113.50"},            // a customer's floating IP
		{"203.0.113.50", "203.0.113.2"}, // even when configured
	}
	for _, c := range refused {
		if got, err := ResolvePublishAddress(c.configured, c.detected, floating); err == nil {
			t.Errorf("ResolvePublishAddress(%q, %q) = %q, want an error", c.configured, c.detected, got)
		}
	}
}

func TestBridgeNFState(t *testing.T) {
	dir := t.TempDir()
	if got := BridgeNFState(dir); got != FirewallOff {
		t.Fatalf("module not loaded: got %q, want %q", got, FirewallOff)
	}
	write := func(key, v string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, key), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("bridge-nf-call-iptables", "1")
	write("bridge-nf-call-ip6tables", "0")
	if got := BridgeNFState(dir); got != FirewallOff {
		t.Fatalf("IPv6 rules not applied: got %q, want %q", got, FirewallOff)
	}
	write("bridge-nf-call-ip6tables", "1")
	if got := BridgeNFState(dir); got != FirewallEnforced {
		t.Fatalf("got %q, want %q", got, FirewallEnforced)
	}
}

func TestPersistBridgeNetfilter(t *testing.T) {
	etc := t.TempDir()
	if err := PersistBridgeNetfilter(etc); err != nil {
		t.Fatal(err)
	}
	mod, _ := os.ReadFile(filepath.Join(etc, "modules-load.d", "maburvm.conf"))
	if string(mod) != "br_netfilter\n" {
		t.Fatalf("modules-load.d = %q", mod)
	}
	sysctlPath := filepath.Join(etc, "sysctl.d", "99-maburvm-bridge.conf")
	sysctl, _ := os.ReadFile(sysctlPath)
	want := "net.bridge.bridge-nf-call-iptables = 1\nnet.bridge.bridge-nf-call-ip6tables = 1\n"
	if string(sysctl) != want {
		t.Fatalf("sysctl.d = %q, want %q", sysctl, want)
	}

	// Unchanged files are not rewritten.
	before, _ := os.Stat(sysctlPath)
	if err := PersistBridgeNetfilter(etc); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(sysctlPath)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("an unchanged file was rewritten")
	}
}
