package guestos

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func sampleNetwork() LinuxNetwork {
	return LinuxNetwork{
		MAC: "52:54:00:AB:CD:EF", Address: "192.0.2.10", Prefix: 24, Gateway: "192.0.2.1",
		Extra:       []GuestAddr{{Address: "198.51.100.7", Prefix: 25}},
		IPv6Address: "2001:db8:20:5::1/64", IPv6Gateway: "fe80::1", IPv6DNS: []string{"2606:4700:4700::1111"},
		IPv6Extra: []string{"2001:db8:20:5::25"},
		DNS:       []string{"9.9.9.9"},
		NICs:      []GuestNIC{{MAC: "52:54:00:11:22:33", Addresses: []GuestAddr{{Address: "10.0.0.5", Prefix: 24}}}},
	}
}

func TestLinuxNetworkScriptIsValidShell(t *testing.T) {
	script, err := LinuxNetworkScript(sampleNetwork())
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "net.sh")
	if err := os.WriteFile(f, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-n", f).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v: %s\n%s", err, out, script)
	}
	for _, want := range []string{
		"dev0=$(waitdev 52:54:00:ab:cd:ef)", "dev1=$(waitdev 52:54:00:11:22:33)",
		"if [ -f /etc/systemd/network/10-maburvm.network ] && systemctl is-active -q systemd-networkd 2>/dev/null; then stack=networkd",
		"/etc/systemd/network/11-maburvm-1.network",
		"netplan generate", "netplan apply",
		"cat >> /etc/network/interfaces",
		`[ -e "/run/network/ifstate.${dev0}" ]`, // ifupdown only when it runs the card
		"nmcli -w 15 connection up uuid " + nmUUID("52:54:00:ab:cd:ef"),
		"/etc/sysconfig/network-scripts/ifcfg-${dev1}",
		"exit 43",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q", want)
		}
	}
	// A guest with a running networkd keeps the networkd path before netplan's
	// files are touched, and an ifupdown stanza for the card wins over both.
	ifup := strings.Index(script, "then stack=ifupdown")
	networkd := strings.Index(script, "elif systemctl is-active -q systemd-networkd 2>/dev/null; then stack=networkd")
	netplan := strings.Index(script, "then stack=netplan")
	if ifup < 0 || networkd < 0 || netplan < 0 || !(ifup < networkd && networkd < netplan) {
		t.Errorf("stack detection order: ifupdown %d, networkd %d, netplan %d", ifup, networkd, netplan)
	}
}

func TestLinuxNetworkScriptRefusesBadValues(t *testing.T) {
	for name, mutate := range map[string]func(*LinuxNetwork){
		"mac":       func(n *LinuxNetwork) { n.MAC = "52:54:00:ab:cd:ef'; reboot; '" },
		"address":   func(n *LinuxNetwork) { n.Address = "192.0.2.10\nrm -rf /" },
		"dns":       func(n *LinuxNetwork) { n.DNS = []string{"$(reboot)"} },
		"v6 extra":  func(n *LinuxNetwork) { n.IPv6Extra = []string{"192.0.2.9"} },
		"card mac":  func(n *LinuxNetwork) { n.NICs[0].MAC = "zz" },
		"card addr": func(n *LinuxNetwork) { n.NICs[0].Addresses[0].Prefix = 40 },
	} {
		n := sampleNetwork()
		mutate(&n)
		if _, err := LinuxNetworkScript(n); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNetworkdUnitsUseResolversAndExtras(t *testing.T) {
	n := sampleNetwork()
	got := NetworkdUnit(n)
	for _, want := range []string{"Address=2001:db8:20:5::25/64\n", "DNS=9.9.9.9\nDNS=2606:4700:4700::1111\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "1.1.1.1") {
		t.Errorf("the pool's resolvers replace the defaults:\n%s", got)
	}
	card := NetworkdNICUnit(n.NICs[0])
	if !strings.Contains(card, "MACAddress=52:54:00:11:22:33") || !strings.Contains(card, "Address=10.0.0.5/24") || strings.Contains(card, "Gateway") {
		t.Errorf("card unit:\n%s", card)
	}
}

func TestNetplanConfig(t *testing.T) {
	got := NetplanConfig(sampleNetwork())
	for _, want := range []string{
		"macaddress: \"52:54:00:ab:cd:ef\"\n      set-name: \"${dev0}\"",
		"- \"192.0.2.10/24\"\n        - \"198.51.100.7/25\"\n        - \"2001:db8:20:5::1/64\"\n        - \"2001:db8:20:5::25/64\"",
		"- to: \"0.0.0.0/0\"\n          via: \"192.0.2.1\"",
		"- to: \"::/0\"\n          via: \"fe80::1\"\n          on-link: true",
		"addresses: [\"9.9.9.9\", \"2606:4700:4700::1111\"]",
		"maburvm1:\n      match:\n        macaddress: \"52:54:00:11:22:33\"",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}

func TestNMKeyfiles(t *testing.T) {
	files := NMKeyfiles(sampleNetwork())
	if len(files) != 2 || files[0].Name != "maburvm-0.nmconnection" {
		t.Fatalf("files: %+v", files)
	}
	for _, want := range []string{"mac-address=52:54:00:AB:CD:EF", "address1=192.0.2.10/24,192.0.2.1", "address2=198.51.100.7/25",
		"dns=9.9.9.9;", "addr-gen-mode=eui64", "address1=2001:db8:20:5::1/64,fe80::1", "address2=2001:db8:20:5::25/64", "autoconnect-priority=999"} {
		if !strings.Contains(files[0].Content, want) {
			t.Errorf("missing %q in\n%s", want, files[0].Content)
		}
	}
	if !strings.Contains(files[1].Content, "never-default=true") || !strings.Contains(files[1].Content, "address1=10.0.0.5/24") {
		t.Errorf("card profile:\n%s", files[1].Content)
	}
	if nmUUID("52:54:00:AB:CD:EF") != nmUUID("52:54:00:ab:cd:ef") {
		t.Error("a card's profile id must not depend on how its MAC is written")
	}
}

func TestIfupdownBlockAndIfcfg(t *testing.T) {
	block := IfupdownBlock(sampleNetwork())
	for _, want := range []string{"auto ${dev0}\niface ${dev0} inet static\n    address 192.0.2.10\n    netmask 255.255.255.0\n    gateway 192.0.2.1\n",
		"iface ${dev0} inet static\n    address 198.51.100.7\n    netmask 255.255.255.128\n",
		"iface ${dev0} inet6 static\n    address 2001:db8:20:5::1\n    netmask 64\n    gateway fe80::1\n",
		"auto ${dev1}\niface ${dev1} inet static\n    address 10.0.0.5\n"} {
		if !strings.Contains(block, want) {
			t.Errorf("missing %q in\n%s", want, block)
		}
	}
	ifcfg := IfcfgFiles(sampleNetwork())
	for _, want := range []string{"IPADDR=192.0.2.10\nPREFIX=24\nIPADDR1=198.51.100.7\nPREFIX1=25\n", "GATEWAY=192.0.2.1\n", "DNS1=9.9.9.9\n",
		"IPV6ADDR_SECONDARIES=\"2001:db8:20:5::25/64\"", "IPV6_DEFAULTGW=fe80::1%${dev0}"} {
		if !strings.Contains(ifcfg[0], want) {
			t.Errorf("missing %q in\n%s", want, ifcfg[0])
		}
	}
	if !strings.Contains(ifcfg[1], "DEFROUTE=no") {
		t.Errorf("card file:\n%s", ifcfg[1])
	}
}

// The stanzas of the cards are commented out, everything else is kept, and a
// block from an earlier change is dropped.
func TestIfupdownStrip(t *testing.T) {
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("no awk")
	}
	dir := t.TempDir()
	f := filepath.Join(dir, "interfaces")
	orig := "source /etc/network/interfaces.d/*\nauto lo eth0\niface lo inet loopback\n\nallow-hotplug eth0\niface eth0 inet dhcp\n    hostname x\niface eth0:1 inet static\n    address 10.9.9.9\nauto eth2\niface eth2 inet manual\n# BEGIN MaburVM: old\nauto eth0\n# END MaburVM\n"
	if err := os.WriteFile(f, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	script := ifupdownStrip + "devs='eth0'\nstrip_ifupdown " + f + "\n"
	if out, err := exec.Command("sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	b, _ := os.ReadFile(f)
	got := string(b)
	want := "source /etc/network/interfaces.d/*\n# maburvm: auto lo eth0\nauto lo\niface lo inet loopback\n\n# maburvm: allow-hotplug eth0\n# maburvm: iface eth0 inet dhcp\n# maburvm:     hostname x\n# maburvm: iface eth0:1 inet static\n# maburvm:     address 10.9.9.9\nauto eth2\niface eth2 inet manual\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// The guest's own ifcfg files for the cards are set aside under a name
// network-scripts skips at boot; other cards' files and ours stay.
func TestIfcfgSetsAsideOldFiles(t *testing.T) {
	script, err := LinuxNetworkScript(sampleNetwork())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, `[ -e "$d/device" ]`) {
		t.Error("devof must pick a real card, not a bridge or VLAN with the same MAC")
	}
	start := strings.Index(script, "ifcfg)\n")
	end := strings.Index(script[start:], "  cat > ")
	if start < 0 || end < 0 {
		t.Fatal("no ifcfg branch")
	}
	dir := t.TempDir()
	files := []struct{ name, body string }{
		{"ifcfg-eth0", "DEVICE=eth0\nIPADDR=203.0.113.9\n"},           // by name and device
		{"ifcfg-System_eth1", "HWADDR=52:54:00:11:22:33\n"},           // by MAC
		{"ifcfg-ens9", "DEVICE=\"ens9\"\nHWADDR=52:54:00:99:99:99\n"}, // another card
		{"ifcfg-lo", "DEVICE=lo\n"},
		{"ifcfg-eth1", "# Managed by MaburVM: earlier\nDEVICE=eth1\n"}, // ours
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	loop := strings.ReplaceAll(script[start+len("ifcfg)\n"):start+end], "/etc/sysconfig/network-scripts", dir)
	if out, err := exec.Command("sh", "-c", "devs='eth0 eth1'\n"+loop).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s\n%s", err, out, loop)
	}
	for _, c := range []struct {
		name  string
		aside bool
	}{{"ifcfg-eth0", true}, {"ifcfg-System_eth1", true}, {"ifcfg-ens9", false}, {"ifcfg-lo", false}, {"ifcfg-eth1", false}} {
		_, errKept := os.Stat(filepath.Join(dir, c.name))
		_, errAside := os.Stat(filepath.Join(dir, c.name+".maburvm.bak"))
		if c.aside != (errKept != nil && errAside == nil) {
			t.Errorf("%s: set aside %v, want %v", c.name, errKept != nil, c.aside)
		}
	}
}

func TestRouterAndWindowsUseResolvers(t *testing.T) {
	ros, err := RouterOSNetworkScript(RouterOSNetwork{Address: "192.0.2.10", Prefix: 24, DNS: []string{"9.9.9.9"}})
	if err != nil || !strings.Contains(ros, "/ip dns set servers=9.9.9.9\n") {
		t.Fatalf("router: %v\n%s", err, ros)
	}
	win, err := WindowsNetworkScript(WindowsNetwork{MAC: "52:54:00:ab:cd:ef", Address: "192.0.2.10", Prefix: 24, DNS: []string{"9.9.9.9", "149.112.112.112"}})
	if err != nil || !strings.Contains(win, "-ServerAddresses 9.9.9.9,149.112.112.112\n") {
		t.Fatalf("windows: %v\n%s", err, win)
	}
	if _, err := WindowsNetworkScript(WindowsNetwork{MAC: "52:54:00:ab:cd:ef", Address: "192.0.2.10", Prefix: 24, DNS: []string{"1.1.1.1;Restart-Computer"}}); err == nil {
		t.Fatal("a resolver that is not an address was accepted")
	}
}
