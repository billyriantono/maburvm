package guestos

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// RouterOS has no cloud-init. What it does have, and what was confirmed on
// 6.49.22 and 7.24.6 before any of this was written:
//
//   - A script at rw/autorun.scr on its system partition is run once at the
//     next boot with full rights and then removed. This is the only way to set
//     a user's password from outside.
//   - Its guest agent runs scripts sent with guest-exec, but as a restricted
//     user: on version 7 it may change addresses and routes and may not touch
//     users. So a running router can be re-addressed without a restart, while
//     a password reset on version 7 needs one.

// RouterOSNetwork is the addressing a router is given on its first interface.
// A zero value leaves the image's DHCP client in place.
type RouterOSNetwork struct {
	Address     string // IPv4, no prefix
	Prefix      int
	Gateway     string
	IPv6Address string // CIDR form
	IPv6Gateway string
	IPv6DNS     []string
}

// rosPanelComment marks the address and route this panel added, so a later
// change replaces exactly those and leaves the owner's own entries alone.
const rosPanelComment = "panel"

// rosQuote renders s as a RouterOS string literal. Everything outside a small
// set of harmless characters is written as a \XX byte escape, so no value
// (a password above all) can close the string or start a substitution.
func rosQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == ':', c == '@', c == '/', c == '+', c == '=', c == ',':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, `\%02X`, c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func (n RouterOSNetwork) validate() error {
	if n.Address != "" {
		if ip := net.ParseIP(n.Address); ip == nil || ip.To4() == nil {
			return fmt.Errorf("invalid IPv4 address %q", n.Address)
		}
		if n.Prefix < 1 || n.Prefix > 32 {
			return fmt.Errorf("invalid prefix length %d", n.Prefix)
		}
		if n.Gateway != "" {
			if ip := net.ParseIP(n.Gateway); ip == nil || ip.To4() == nil {
				return fmt.Errorf("invalid gateway %q", n.Gateway)
			}
		}
	}
	if n.IPv6Address != "" {
		if ip, _, err := net.ParseCIDR(n.IPv6Address); err != nil || ip.To4() != nil {
			return fmt.Errorf("invalid IPv6 address %q", n.IPv6Address)
		}
		if n.IPv6Gateway != "" && net.ParseIP(n.IPv6Gateway) == nil {
			return fmt.Errorf("invalid IPv6 gateway %q", n.IPv6Gateway)
		}
		for _, d := range n.IPv6DNS {
			if net.ParseIP(d) == nil {
				return fmt.Errorf("invalid DNS server %q", d)
			}
		}
	}
	return nil
}

// rosFirstInterface is how the scripts name the router's first network card.
// By its factory name, not "ether1": owners rename interfaces.
const rosFirstInterface = `[/interface ethernet get [find default-name=ether1] name]`

// ipv4Lines replaces the panel's IPv4 address and default route. Call only
// with a validated network that has an address.
func (n RouterOSNetwork) ipv4Lines() []string {
	c := rosQuote(rosPanelComment)
	lines := []string{
		":local nic " + rosFirstInterface,
		// The image ships a DHCP client on the first interface. Left in place it
		// would add a second default route from any DHCP server on the segment.
		"/ip dhcp-client remove [find interface=$nic]",
		"/ip address remove [find comment=" + c + "]",
		"/ip route remove [find comment=" + c + "]",
		fmt.Sprintf("/ip address add address=%s/%d interface=$nic comment=%s", n.Address, n.Prefix, c),
	}
	if n.Gateway != "" {
		lines = append(lines, fmt.Sprintf("/ip route add gateway=%s comment=%s", n.Gateway, c))
	}
	dns := append([]string{"1.1.1.1", "8.8.8.8"}, n.IPv6DNS...)
	return append(lines, "/ip dns set servers="+strings.Join(dns, ","))
}

// ipv6Block replaces the panel's IPv6 address and default route.
//
// The commands are handed to the router as text to parse when it reaches them.
// RouterOS 6 ships its IPv6 package switched off, and there the /ipv6 menu does
// not exist: written inline, one such line would make the whole script fail to
// parse and none of it would run.
func (n RouterOSNetwork) ipv6Block() string {
	c := rosQuote(rosPanelComment)
	gw := n.IPv6Gateway
	if gw == "" {
		gw = "fe80::1"
	}
	gwExpr := gw
	if ip := net.ParseIP(gw); ip != nil && ip.IsLinkLocalUnicast() {
		gwExpr = `("` + gw + `%" . $nic)` // a link-local next hop needs its interface
	}
	inner := strings.Join([]string{
		":local nic " + rosFirstInterface,
		"/ipv6 address remove [find comment=" + c + "]",
		"/ipv6 route remove [find comment=" + c + "]",
		fmt.Sprintf("/ipv6 address add address=%s interface=$nic advertise=no comment=%s", n.IPv6Address, c),
		fmt.Sprintf("/ipv6 route add dst-address=::/0 gateway=%s comment=%s", gwExpr, c),
	}, "\n")
	return ":if ([:len [/system package find name=ipv6 disabled=yes]] = 0) do={ [[:parse " + rosQuote(inner) + "]] }"
}

// RouterOSSetupScript is the script a new or reinstalled router runs on its
// first boot. Each concern is its own guarded block: a script stops at its
// first error, and a router that could not take its address must still get
// its password.
func RouterOSSetupScript(hostname, password string, n RouterOSNetwork) (string, error) {
	if err := n.validate(); err != nil {
		return "", err
	}
	guard := func(lines ...string) string {
		return ":do {\n" + strings.Join(lines, "\n") + "\n} on-error={}"
	}
	// The network card can appear a few seconds after the script starts.
	parts := []string{
		":local n 0",
		":while (([:len [/interface ethernet find]] = 0) && ($n < 60)) do={ :delay 1s; :set n ($n + 1) }",
	}
	if password != "" {
		// First, so the router is never reachable on its new address with the
		// image's empty password.
		parts = append(parts, guard(RouterOSPasswordScript(password)))
	}
	if hostname != "" {
		parts = append(parts, guard("/system identity set name="+rosQuote(hostname)))
	}
	if n.Address != "" {
		parts = append(parts, guard(n.ipv4Lines()...))
	}
	if n.IPv6Address != "" {
		parts = append(parts, guard(n.ipv6Block()))
	}
	return strings.Join(parts, "\n") + "\n", nil
}

// RouterOSNetworkScript re-addresses a router. Unlike the setup script it is
// not guarded: sent through the guest agent, its failure must be reported.
func RouterOSNetworkScript(n RouterOSNetwork) (string, error) {
	if err := n.validate(); err != nil {
		return "", err
	}
	if n.Address == "" {
		return "", fmt.Errorf("an IPv4 address is required")
	}
	lines := n.ipv4Lines()
	if n.IPv6Address != "" {
		lines = append(lines, n.ipv6Block())
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// RouterOSPasswordScript sets the password of the router's admin user.
func RouterOSPasswordScript(password string) string {
	return "/user set [find name=admin] password=" + rosQuote(password)
}

// guestfish runs one libguestfs command set against a disk image. The direct
// backend starts the helper appliance itself rather than through libvirt,
// which is what works for a root service on a hypervisor.
func guestfish(args ...string) ([]byte, error) {
	cmd := exec.Command("guestfish", args...)
	cmd.Env = append(os.Environ(), "LIBGUESTFS_BACKEND=direct")
	return cmd.CombinedOutput()
}

// WriteRouterOSAutorun puts script on the router's disk as rw/autorun.scr, to
// be run at its next boot. The VM must be off.
//
// The disk is edited inside libguestfs's own small VM, never mounted by the
// host kernel: after first boot the filesystem is the customer's.
func WriteRouterOSAutorun(diskPath, script string) error {
	if _, err := exec.LookPath("guestfish"); err != nil {
		return fmt.Errorf("guestfish not found on this node (install libguestfs-tools): %w", err)
	}
	f, err := os.CreateTemp("", "maburvm-autorun-*.scr")
	if err != nil {
		return fmt.Errorf("failed to stage the start-up script: %w", err)
	}
	defer os.Remove(f.Name()) // it can hold a password
	if _, err := f.WriteString(script); err != nil {
		f.Close()
		return fmt.Errorf("failed to stage the start-up script: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("failed to stage the start-up script: %w", err)
	}
	// Version 7 keeps rw/ on its second partition and has a boot partition
	// first; version 6 has a single partition.
	var last string
	for _, part := range []string{"/dev/sda2", "/dev/sda1"} {
		out, err := guestfish("--rw", "-a", diskPath, "-m", part, "upload", f.Name(), "/rw/autorun.scr")
		if err == nil {
			return nil
		}
		last = strings.TrimSpace(string(out))
	}
	return fmt.Errorf("could not write the start-up script to %s (is this a RouterOS disk?): %s", diskPath, last)
}

// RouterOSExec runs script inside a running router through its guest agent
// and returns what it printed. A script that fails is an error carrying the
// router's own message.
func RouterOSExec(domainName, script string, timeout time.Duration) (string, error) {
	agent := func(req map[string]any) ([]byte, error) {
		body, _ := json.Marshal(req)
		out, err := exec.Command("virsh", "qemu-agent-command", domainName, string(body)).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
		}
		return out, nil
	}
	out, err := agent(map[string]any{
		"execute": "guest-exec",
		"arguments": map[string]any{
			"input-data":     base64.StdEncoding.EncodeToString([]byte(script)),
			"capture-output": true,
		},
	})
	if err != nil {
		return "", fmt.Errorf("guest-exec: %w", err)
	}
	var started struct {
		Return struct {
			PID int `json:"pid"`
		} `json:"return"`
	}
	if err := json.Unmarshal(out, &started); err != nil || started.Return.PID == 0 {
		return "", fmt.Errorf("guest-exec: unexpected reply %s", strings.TrimSpace(string(out)))
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		out, err := agent(map[string]any{
			"execute": "guest-exec-status", "arguments": map[string]int{"pid": started.Return.PID},
		})
		if err != nil {
			return "", fmt.Errorf("guest-exec-status: %w", err)
		}
		var st struct {
			Return struct {
				Exited   bool   `json:"exited"`
				ExitCode int    `json:"exitcode"`
				OutData  string `json:"out-data"`
			} `json:"return"`
		}
		if err := json.Unmarshal(out, &st); err != nil {
			return "", fmt.Errorf("guest-exec-status: unexpected reply %s", strings.TrimSpace(string(out)))
		}
		if !st.Return.Exited {
			continue
		}
		printed, _ := base64.StdEncoding.DecodeString(st.Return.OutData)
		text := strings.TrimSpace(string(printed))
		if st.Return.ExitCode != 0 {
			return text, fmt.Errorf("the router refused the script: %s", text)
		}
		return text, nil
	}
	return "", fmt.Errorf("the router did not finish the script within %s", timeout)
}
