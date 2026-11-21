package guestos

import (
	"strings"
	"testing"
)

func TestNetworkdUnit(t *testing.T) {
	got := NetworkdUnit(LinuxNetwork{MAC: "52:54:00:AB:CD:EF", Address: "192.0.2.58", Prefix: 25, Gateway: "192.0.2.1"})
	for _, want := range []string{"MACAddress=52:54:00:ab:cd:ef", "Address=192.0.2.58/25", "Gateway=192.0.2.1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"IPv6", "fe80::", "[Link]"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("v4-only config must not mention %q:\n%s", unwanted, got)
		}
	}
	if strings.Contains(NetworkdUnit(LinuxNetwork{MAC: "52:54:00:ab:cd:ef", Address: "10.0.0.2", Prefix: 24}), "Gateway=") {
		t.Fatal("gateway line written without a gateway")
	}
	if !strings.Contains(AnnounceUnit("192.0.2.1"), "ping -c1 -w2 192.0.2.1") {
		t.Fatal("announce unit does not ping the gateway")
	}
}

// Adding an address must keep the ones the guest has: the primary stays first
// (and so the source of the guest's own traffic), the new one comes after it.
func TestNetworkdUnitKeepsEveryAddress(t *testing.T) {
	got := NetworkdUnit(LinuxNetwork{MAC: "52:54:00:12:34:56", Address: "192.0.2.58", Prefix: 25, Gateway: "192.0.2.1",
		Extra:       []GuestAddr{{Address: "198.51.100.7", Prefix: 24}},
		IPv6Address: "2001:db8:20:5::1/64", IPv6Gateway: "fe80::1", IPv6DNS: []string{"2606:4700:4700::1111", "2001:4860:4860::8888"}})
	for _, want := range []string{
		"Address=192.0.2.58/25\nGateway=192.0.2.1\nAddress=198.51.100.7/24\n",
		"Address=2001:db8:20:5::1/64\nGateway=fe80::1\nIPv6AcceptRA=no\n",
		"[Link]\nIPv6LinkLocalAddressGenerationMode=eui64\n",
		"DNS=1.1.1.1\nDNS=8.8.8.8\nDNS=2606:4700:4700::1111\nDNS=2001:4860:4860::8888\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

func TestExtraAddressesInRouterAndWindowsScripts(t *testing.T) {
	extra := []GuestAddr{{Address: "198.51.100.7", Prefix: 24}}
	ros, err := RouterOSNetworkScript(RouterOSNetwork{Address: "192.0.2.10", Prefix: 24, Gateway: "192.0.2.1", Extra: extra})
	if err != nil {
		t.Fatal(err)
	}
	primary := strings.Index(ros, "address=192.0.2.10/24")
	second := strings.Index(ros, `/ip address add address=198.51.100.7/24 interface=$nic comment="panel"`)
	if primary < 0 || second < primary {
		t.Fatalf("router script:\n%s", ros)
	}
	win, err := WindowsNetworkScript(WindowsNetwork{MAC: "52:54:00:ab:cd:ef", Address: "192.0.2.10", Prefix: 24, Gateway: "192.0.2.1", Extra: extra})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(win, "-IPAddress '198.51.100.7' -PrefixLength 24 -SkipAsSource $true") {
		t.Fatalf("windows script:\n%s", win)
	}
	for _, bad := range [][]GuestAddr{{{Address: "198.51.100.7'; x", Prefix: 24}}, {{Address: "198.51.100.7", Prefix: 0}}, {{Address: "2001:db8::1", Prefix: 64}}} {
		if _, err := WindowsNetworkScript(WindowsNetwork{MAC: "52:54:00:ab:cd:ef", Address: "192.0.2.10", Prefix: 24, Extra: bad}); err == nil {
			t.Errorf("windows accepted %+v", bad)
		}
		if _, err := RouterOSNetworkScript(RouterOSNetwork{Address: "192.0.2.10", Prefix: 24, Extra: bad}); err == nil {
			t.Errorf("router accepted %+v", bad)
		}
	}
}
