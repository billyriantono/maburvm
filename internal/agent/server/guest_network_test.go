//go:build linux

package server

import (
	"strings"
	"testing"
)

func TestGuestNetworkFile(t *testing.T) {
	got := guestNetworkFile("52:54:00:AB:CD:EF", "192.0.2.58", 25, "192.0.2.1", guestIPv6{})
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
	if strings.Contains(guestNetworkFile("52:54:00:ab:cd:ef", "10.0.0.2", 24, "", guestIPv6{}), "Gateway=") {
		t.Fatal("gateway line written without a gateway")
	}
	if !strings.Contains(guestAnnounceUnit("192.0.2.1"), "ping -c1 -w2 192.0.2.1") {
		t.Fatal("announce unit does not ping the gateway")
	}
}

func TestGuestNetworkFileIPv6(t *testing.T) {
	v6 := guestIPv6From("2001:db8:20:5::1/64", "", nil)
	got := guestNetworkFile("52:54:00:12:34:56", "192.0.2.58", 25, "192.0.2.1", v6)
	for _, want := range []string{
		"Address=192.0.2.58/25\nGateway=192.0.2.1\n",
		"Address=2001:db8:20:5::1/64\nGateway=fe80::1\nIPv6AcceptRA=no\n",
		"[Link]\nIPv6LinkLocalAddressGenerationMode=eui64\n",
		"DNS=1.1.1.1\nDNS=8.8.8.8\nDNS=2606:4700:4700::1111\nDNS=2001:4860:4860::8888\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}
