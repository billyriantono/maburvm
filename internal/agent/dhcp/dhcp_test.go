package dhcp

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestConfigServesOnlyTheLeases(t *testing.T) {
	got := Config("br0", "/run/x.pid", []Host{
		{Bridge: "br0", MAC: "52:54:00:AA:BB:02", IP: "192.0.2.20", Prefix: 24, Gateway: "192.0.2.1", DNS: []string{"9.9.9.9", "2620:fe::fe"}},
		{Bridge: "br0", MAC: "52:54:00:aa:bb:01", IP: "192.0.2.10", Prefix: 24, Gateway: "192.0.2.1", DNS: []string{"9.9.9.9", "2620:fe::fe"}},
		{Bridge: "br0", MAC: "52:54:00:aa:bb:03", IP: "198.51.100.5", Prefix: 25, Gateway: "198.51.100.1"},
	})
	want := "# Written by the MaburVM agent: static leases only, from the panel's address assignments.\n" +
		"port=0\ninterface=br0\nbind-dynamic\nexcept-interface=lo\nleasefile-ro\npid-file=/run/x.pid\ndhcp-ignore=tag:!known\n" +
		"dhcp-range=set:s0,192.0.2.0,static,255.255.255.0,infinite\nshared-network=br0,192.0.2.1\n" +
		"dhcp-option=tag:s0,option:router,192.0.2.1\ndhcp-option=tag:s0,option:dns-server,9.9.9.9\n" +
		"dhcp-range=set:s1,198.51.100.0,static,255.255.255.128,infinite\nshared-network=br0,198.51.100.1\n" +
		"dhcp-option=tag:s1,option:router,198.51.100.1\n" +
		"dhcp-host=52:54:00:aa:bb:01,192.0.2.10,infinite\ndhcp-host=52:54:00:aa:bb:02,192.0.2.20,infinite\ndhcp-host=52:54:00:aa:bb:03,198.51.100.5,infinite\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, ",dynamic") || strings.Contains(got, "dhcp-range=set:s0,192.0.2.0,192.") {
		t.Fatal("a range must never hand out addresses")
	}
}

func TestManagerOneServerPerBridgeAndNoDoubleLease(t *testing.T) {
	var started []string
	m := &Manager{dir: t.TempDir(), hosts: map[string][]Host{}, procs: map[string]*proc{}}
	m.start = func(conf string) (*exec.Cmd, error) {
		started = append(started, conf)
		cmd := exec.Command("sleep", "30")
		return cmd, cmd.Start()
	}
	defer func() {
		for _, p := range m.procs {
			p.stop()
		}
	}()
	m.Set("vm-a", []Host{{Bridge: "br0", MAC: "52:54:00:00:00:01", IP: "192.0.2.10", Prefix: 24}})
	m.Set("vm-b", []Host{{Bridge: "br0", MAC: "52:54:00:00:00:02", IP: "192.0.2.10", Prefix: 24}, // taken by vm-a
		{Bridge: "br1", MAC: "52:54:00:00:00:02", IP: "10.0.0.2", Prefix: 24},
		{Bridge: "br0; reboot", MAC: "52:54:00:00:00:03", IP: "192.0.2.11", Prefix: 24}}) // invalid: dropped
	if len(m.procs) != 2 {
		t.Fatalf("servers: %d", len(m.procs))
	}
	if c := m.procs["br0"].conf; strings.Count(c, "dhcp-host=") != 1 || !strings.Contains(c, "52:54:00:00:00:01,192.0.2.10") {
		t.Fatalf("br0 conf:\n%s", c)
	}
	before := len(started)
	m.Set("vm-b", []Host{{Bridge: "br1", MAC: "52:54:00:00:00:02", IP: "10.0.0.2", Prefix: 24}})
	if len(started) != before {
		t.Fatal("an unchanged bridge must not be restarted")
	}
	m.Set("vm-a", nil)
	if _, ok := m.procs["br0"]; ok {
		t.Fatal("a bridge without leases keeps no server")
	}
	// Saved, for a restarted agent to serve again.
	if b, err := os.ReadFile(m.statePath()); err != nil || !strings.Contains(string(b), "vm-b") || strings.Contains(string(b), "vm-a") {
		t.Fatalf("saved leases: %s %v", b, err)
	}
	if st := m.Status(); st["br1"] != "" {
		t.Fatalf("status: %v", st)
	}
}
