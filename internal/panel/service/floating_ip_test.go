package service

import (
	"testing"

	"github.com/maburvm/panel/internal/shared/models"
)

// A VM that already owns a public address must keep that egress identity when it
// gains a floating IP (inbound-only DNAT); a VM on a private address has no
// public identity of its own, so it should egress as the floating IP.
func TestDefaultNATMode(t *testing.T) {
	cases := map[string]string{
		"10.20.0.5":     models.NATModeFull,
		"192.168.1.20":  models.NATModeFull,
		"172.16.4.9":    models.NATModeFull,
		"203.0.113.10":  models.NATModeInbound,
		"198.51.100.7": models.NATModeInbound,
		"":              models.NATModeInbound,
	}
	for ip, want := range cases {
		if got := defaultNATMode(ip); got != want {
			t.Errorf("defaultNATMode(%q) = %q, want %q", ip, got, want)
		}
	}
}

// Full 1:1 NAT is only meaningful when the node is the VM's gateway. A VM
// bridged with its own public address never sends outbound traffic through the
// node, so the egress SNAT cannot match — confirmed on a live node, where such a
// VM kept egressing under its own address while nat_mode said "full". Storing
// that mode would be a silent lie, so attach must reject it.
func TestFullNATOnlyForHostRoutedVMs(t *testing.T) {
	for ip, hostRouted := range map[string]bool{
		"10.20.0.5":      true,
		"192.168.1.20":   true,
		"172.16.4.9":     true,
		"203.0.113.163": false, // directly bridged public — full mode is unhonourable
		"198.51.100.7":  false,
		"":               false,
	} {
		if got := isHostRoutedAddress(ip); got != hostRouted {
			t.Errorf("isHostRoutedAddress(%q) = %v, want %v", ip, got, hostRouted)
		}
		// The default must never be a mode the data path cannot honour.
		if def := defaultNATMode(ip); !hostRouted && def == models.NATModeFull {
			t.Errorf("defaultNATMode(%q) = %q, but full mode cannot work for it", ip, def)
		}
	}
}
