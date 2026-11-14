package guestos

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestRosQuoteCannotBeEscaped(t *testing.T) {
	// Only harmless characters and \XX escapes may sit between the quotes.
	safe := regexp.MustCompile(`^"([A-Za-z0-9\-_.:@/+=,]|\\[0-9A-F]{2})*"$`)
	for _, in := range []string{
		`plain`, `with space`, `q"uote`, `back\slash`, `$var`, `$[/system reset-configuration]`,
		`"; /system reset-configuration; :put "`, "new\nline", "tab\t", "café", ``,
	} {
		if got := rosQuote(in); !safe.MatchString(got) {
			t.Errorf("rosQuote(%q) = %s, which is not a closed literal", in, got)
		}
	}
	if got := rosQuote(`a"b`); got != `"a\22b"` {
		t.Errorf(`rosQuote(a"b) = %s`, got)
	}
}

func TestRouterOSSetupScript(t *testing.T) {
	n := RouterOSNetwork{Address: "192.0.2.10", Prefix: 24, Gateway: "192.0.2.1",
		IPv6Address: "2001:db8:20:5::1/64", IPv6Gateway: "fe80::1", IPv6DNS: []string{"2606:4700:4700::1111"}}
	s, err := RouterOSSetupScript("edge-01", `Aa1 "$\[x]`, n)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`/user set [find name=admin] password="Aa1\20\22\24\5C\5Bx\5D"`,
		`/system identity set name="edge-01"`,
		`/ip address add address=192.0.2.10/24 interface=$nic comment="panel"`,
		`/ip route add gateway=192.0.2.1 comment="panel"`,
		`/ip dns set servers=1.1.1.1,8.8.8.8,2606:4700:4700::1111`,
		`[[:parse "`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("setup script lacks %q\n%s", want, s)
		}
	}
	// The password must be set before the address makes the router reachable.
	if strings.Index(s, "/user set") > strings.Index(s, "/ip address add") {
		t.Error("the password is set after the address")
	}
	// The IPv6 commands must not appear as bare lines: on version 6 without its
	// IPv6 package they would stop the whole script from parsing.
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "/ipv6") {
			t.Errorf("bare IPv6 line: %s", line)
		}
	}
	// MABURVM_PRINT_ROS=<dir> writes the scripts out, to be run on a real router.
	if dir := os.Getenv("MABURVM_PRINT_ROS"); dir != "" {
		net2 := RouterOSNetwork{Address: "198.51.100.7", Prefix: 24, Gateway: "198.51.100.1", IPv6Address: "2001:db8:20:6::1/64"}
		change, _ := RouterOSNetworkScript(net2)
		_ = os.WriteFile(dir+"/setup.scr", []byte(s), 0o600)
		_ = os.WriteFile(dir+"/change.scr", []byte(change), 0o600)
		_ = os.WriteFile(dir+"/password.scr", []byte(RouterOSPasswordScript(`N3w "$\[pw]`)), 0o600)
	}
}

func TestRouterOSScriptsRejectBadInput(t *testing.T) {
	for _, n := range []RouterOSNetwork{
		{Address: "192.0.2.10; /system reset-configuration", Prefix: 24},
		{Address: "192.0.2.10", Prefix: 0},
		{Address: "192.0.2.10", Prefix: 24, Gateway: "gw"},
		{Address: "192.0.2.10", Prefix: 24, IPv6Address: "2001:db8::1"}, // not CIDR
		{Address: "192.0.2.10", Prefix: 24, IPv6Address: "2001:db8::1/64", IPv6DNS: []string{"x y"}},
	} {
		if _, err := RouterOSSetupScript("h", "p", n); err == nil {
			t.Errorf("accepted %+v", n)
		}
	}
	if _, err := RouterOSNetworkScript(RouterOSNetwork{}); err == nil {
		t.Error("a network change with no address was accepted")
	}
	// No address at all is a valid setup: the router keeps DHCP.
	s, err := RouterOSSetupScript("h", "p", RouterOSNetwork{})
	if err != nil || strings.Contains(s, "/ip address") {
		t.Errorf("DHCP setup: err=%v script=%s", err, s)
	}
}

func TestOSTypeRoundTrip(t *testing.T) {
	xml := `<domain><metadata><maburvm:metadata xmlns:maburvm="http://maburvm.local/schema">` + MetadataElement(RouterOS) + `</maburvm:metadata></metadata></domain>`
	if got := FromDomainXML(xml); got != RouterOS {
		t.Errorf("FromDomainXML = %q", got)
	}
	if got := FromDomainXML(`<domain/>`); got != Linux {
		t.Errorf("a domain that does not say is %q, want linux", got)
	}
	if Normalize("anything") != Linux || Machine("") != "q35" || Machine(RouterOS) != "pc" {
		t.Error("defaults changed")
	}
}

func TestWindowsNetworkScript(t *testing.T) {
	s, err := WindowsNetworkScript(WindowsNetwork{MAC: "52:54:00:ab:cd:ef", Address: "192.0.2.10", Prefix: 24, Gateway: "192.0.2.1",
		IPv6Address: "2001:db8:20:5::1/64", IPv6DNS: []string{"2606:4700:4700::1111"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"$_.MacAddress -eq '52-54-00-AB-CD-EF'",
		"New-NetIPAddress -InterfaceIndex $i -IPAddress '192.0.2.10' -PrefixLength 24 -DefaultGateway '192.0.2.1'",
		"New-NetIPAddress -InterfaceIndex $i -IPAddress '2001:db8:20:5::1' -PrefixLength 64",
		"-NextHop 'fe80::1'",
		"-ServerAddresses 1.1.1.1,8.8.8.8,2606:4700:4700::1111",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("script lacks %q\n%s", want, s)
		}
	}
	// Nothing reaches PowerShell unparsed.
	for _, n := range []WindowsNetwork{
		{MAC: "52:54:00:ab:cd:ef'; Restart-Computer; '", Address: "192.0.2.10", Prefix: 24},
		{MAC: "52:54:00:ab:cd:ef", Address: "192.0.2.10'; x", Prefix: 24},
		{MAC: "52:54:00:ab:cd:ef", Address: "192.0.2.10", Prefix: 24, Gateway: "$(x)"},
		{MAC: "52:54:00:ab:cd:ef", Address: "192.0.2.10", Prefix: 24, IPv6Address: "2001:db8::1/64'; x"},
	} {
		if _, err := WindowsNetworkScript(n); err == nil {
			t.Errorf("accepted %+v", n)
		}
	}
}
