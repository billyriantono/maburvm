//go:build linux

package server

import (
	"strings"
	"testing"
)

func TestGuestNetworkFile(t *testing.T) {
	got := guestNetworkFile("52:54:00:AB:CD:EF", "192.0.2.58", 25, "192.0.2.1")
	for _, want := range []string{"MACAddress=52:54:00:ab:cd:ef", "Address=192.0.2.58/25", "Gateway=192.0.2.1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(guestNetworkFile("52:54:00:ab:cd:ef", "10.0.0.2", 24, ""), "Gateway=") {
		t.Fatal("gateway line written without a gateway")
	}
	if !strings.Contains(guestAnnounceUnit("192.0.2.1"), "ping -c1 -w2 192.0.2.1") {
		t.Fatal("announce unit does not ping the gateway")
	}
}
