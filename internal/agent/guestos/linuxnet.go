package guestos

import (
	"fmt"
	"net"
	"os/exec"
	"strings"

	"github.com/google/uuid"
)

// A running Linux guest is re-addressed through its guest agent with
// LinuxNetworkScript. The script finds out which program configures the
// guest's network and writes that program's own configuration, then brings
// it up:
//
//   - systemd-networkd: a unit per card in /etc/systemd/network. Every guest
//     this panel provisioned has 10-maburvm.network and stays on this path,
//     and so does netplan rendering for networkd (Ubuntu servers).
//   - netplan rendering for NetworkManager: /etc/netplan/90-maburvm.yaml;
//     files that configured the same cards are renamed *.maburvm-off so
//     netplan stops reading them.
//   - ifupdown (Debian classic): a marked block at the end of
//     /etc/network/interfaces; earlier stanzas for the cards are commented out.
//   - NetworkManager (RHEL family, Fedora, desktops): a keyfile profile per
//     card that outranks the guest's own.
//   - network-scripts (older RHEL without NetworkManager): ifcfg-<card>.
//
// Anything else is left alone: the panel says the addresses have to be set in
// the guest. Nothing here restarts the guest.

// Exit codes of LinuxNetworkScript besides 0.
const (
	// ExitUnknownStack: no network configuration the script knows.
	ExitUnknownStack = 43
	// ExitNotApplied: the configuration could not be written or brought up.
	ExitNotApplied = 44
)

// Files the script owns in the guest.
const (
	NetworkdFile     = "/etc/systemd/network/10-maburvm.network"
	AnnounceUnitFile = "/etc/systemd/system/maburvm-announce.service"
	netplanFile      = "/etc/netplan/90-maburvm.yaml"
	nmDir            = "/etc/NetworkManager/system-connections"
)

// cards are the guest's cards in order, the first carrying the default route.
func (n LinuxNetwork) cards() []GuestNIC {
	return append([]GuestNIC{{MAC: strings.ToLower(n.MAC)}}, n.NICs...)
}

func dotted(prefix int) string { return net.IP(net.CIDRMask(prefix, 32)).String() }

// devVar is the shell variable the script holds card i's device name in.
func devVar(i int) string { return fmt.Sprintf("${dev%d}", i) }

// NetplanConfig is the netplan file. Device names are left as shell
// variables, filled in by the script from the cards' MACs.
func NetplanConfig(n LinuxNetwork) string {
	var b strings.Builder
	b.WriteString("# Managed by MaburVM: this server's addresses, rewritten when they change in the panel.\n")
	b.WriteString("network:\n  version: 2\n  ethernets:\n")
	for i, c := range n.cards() {
		fmt.Fprintf(&b, "    maburvm%d:\n      match:\n        macaddress: \"%s\"\n      set-name: \"%s\"\n      dhcp4: false\n", i, strings.ToLower(c.MAC), devVar(i))
		if i > 0 {
			b.WriteString("      addresses:\n")
			for _, a := range c.Addresses {
				fmt.Fprintf(&b, "        - \"%s/%d\"\n", a.Address, a.Prefix)
			}
			continue
		}
		if n.IPv6Address != "" {
			b.WriteString("      dhcp6: false\n      accept-ra: false\n      ipv6-address-generation: eui64\n")
		}
		b.WriteString("      addresses:\n")
		fmt.Fprintf(&b, "        - \"%s/%d\"\n", n.Address, n.Prefix)
		for _, a := range n.Extra {
			fmt.Fprintf(&b, "        - \"%s/%d\"\n", a.Address, a.Prefix)
		}
		if n.IPv6Address != "" {
			fmt.Fprintf(&b, "        - \"%s\"\n", n.IPv6Address)
			for _, a := range n.IPv6Extra {
				fmt.Fprintf(&b, "        - \"%s/%d\"\n", a, n.ipv6Len())
			}
		}
		if n.Gateway != "" || n.IPv6Address != "" {
			b.WriteString("      routes:\n")
		}
		if n.Gateway != "" {
			// 0.0.0.0/0 rather than "default", which older netplan does not know.
			fmt.Fprintf(&b, "        - to: \"0.0.0.0/0\"\n          via: \"%s\"\n", n.Gateway)
		}
		if n.IPv6Address != "" {
			fmt.Fprintf(&b, "        - to: \"::/0\"\n          via: \"%s\"\n          on-link: true\n", n.ipv6Gateway())
		}
		fmt.Fprintf(&b, "      nameservers:\n        addresses: [\"%s\"]\n", strings.Join(resolvers(n.DNS, n.IPv6DNS), "\", \""))
	}
	return b.String()
}

// nmUUID is a profile's id, the same for the same card every time.
func nmUUID(mac string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("maburvm-nic-"+strings.ToLower(mac))).String()
}

// NMKeyfiles are the NetworkManager profiles, one per card, by file name.
// The high autoconnect priority makes them win over the guest's own profiles
// for the same card at the next boot.
func NMKeyfiles(n LinuxNetwork) []struct{ Name, Content string } {
	var out []struct{ Name, Content string }
	for i, c := range n.cards() {
		var b strings.Builder
		fmt.Fprintf(&b, "# Managed by MaburVM: rewritten when this server's addresses change in the panel.\n[connection]\nid=maburvm-%d\nuuid=%s\ntype=ethernet\nautoconnect=true\nautoconnect-priority=999\n\n[ethernet]\nmac-address=%s\n\n[ipv4]\nmethod=manual\n",
			i, nmUUID(c.MAC), strings.ToUpper(c.MAC))
		if i > 0 {
			for j, a := range c.Addresses {
				fmt.Fprintf(&b, "address%d=%s/%d\n", j+1, a.Address, a.Prefix)
			}
			b.WriteString("never-default=true\n\n[ipv6]\nmethod=ignore\n")
			out = append(out, struct{ Name, Content string }{fmt.Sprintf("maburvm-%d.nmconnection", i), b.String()})
			continue
		}
		if n.Gateway != "" {
			fmt.Fprintf(&b, "address1=%s/%d,%s\n", n.Address, n.Prefix, n.Gateway)
		} else {
			fmt.Fprintf(&b, "address1=%s/%d\n", n.Address, n.Prefix)
		}
		for j, a := range n.Extra {
			fmt.Fprintf(&b, "address%d=%s/%d\n", j+2, a.Address, a.Prefix)
		}
		var v4dns, v6dns []string
		for _, d := range resolvers(n.DNS, n.IPv6DNS) {
			if net.ParseIP(d).To4() != nil {
				v4dns = append(v4dns, d)
			} else {
				v6dns = append(v6dns, d)
			}
		}
		if len(v4dns) > 0 {
			fmt.Fprintf(&b, "dns=%s;\nignore-auto-dns=true\n", strings.Join(v4dns, ";"))
		}
		b.WriteString("\n[ipv6]\n")
		if n.IPv6Address == "" {
			b.WriteString("method=auto\n")
		} else {
			fmt.Fprintf(&b, "method=manual\naddr-gen-mode=eui64\naddress1=%s,%s\n", n.IPv6Address, n.ipv6Gateway())
			for j, a := range n.IPv6Extra {
				fmt.Fprintf(&b, "address%d=%s/%d\n", j+2, a, n.ipv6Len())
			}
			if len(v6dns) > 0 {
				fmt.Fprintf(&b, "dns=%s;\nignore-auto-dns=true\n", strings.Join(v6dns, ";"))
			}
		}
		out = append(out, struct{ Name, Content string }{"maburvm-0.nmconnection", b.String()})
	}
	return out
}

// IfupdownBlock is the block appended to /etc/network/interfaces.
func IfupdownBlock(n LinuxNetwork) string {
	var b strings.Builder
	b.WriteString("# BEGIN MaburVM: this server's addresses, rewritten when they change in the panel.\n")
	for i, c := range n.cards() {
		d := devVar(i)
		fmt.Fprintf(&b, "auto %s\n", d)
		if i > 0 {
			for _, a := range c.Addresses {
				fmt.Fprintf(&b, "iface %s inet static\n    address %s\n    netmask %s\n", d, a.Address, dotted(a.Prefix))
			}
			continue
		}
		fmt.Fprintf(&b, "iface %s inet static\n    address %s\n    netmask %s\n", d, n.Address, dotted(n.Prefix))
		if n.Gateway != "" {
			fmt.Fprintf(&b, "    gateway %s\n", n.Gateway)
		}
		fmt.Fprintf(&b, "    dns-nameservers %s\n", strings.Join(resolvers(n.DNS, n.IPv6DNS), " "))
		for _, a := range n.Extra {
			fmt.Fprintf(&b, "iface %s inet static\n    address %s\n    netmask %s\n", d, a.Address, dotted(a.Prefix))
		}
		if n.IPv6Address != "" {
			ip, _, _ := net.ParseCIDR(n.IPv6Address)
			fmt.Fprintf(&b, "iface %s inet6 static\n    address %s\n    netmask %d\n    gateway %s\n    accept_ra 0\n    autoconf 0\n", d, ip, n.ipv6Len(), n.ipv6Gateway())
			for _, a := range n.IPv6Extra {
				fmt.Fprintf(&b, "iface %s inet6 static\n    address %s\n    netmask %d\n", d, a, n.ipv6Len())
			}
		}
	}
	b.WriteString("# END MaburVM\n")
	return b.String()
}

// IfcfgFiles are the network-scripts files, one per card, named by the
// card's device variable.
func IfcfgFiles(n LinuxNetwork) []string {
	var out []string
	for i, c := range n.cards() {
		var b strings.Builder
		fmt.Fprintf(&b, "# Managed by MaburVM: rewritten when this server's addresses change in the panel.\nDEVICE=%s\nHWADDR=%s\nTYPE=Ethernet\nONBOOT=yes\nBOOTPROTO=none\n", devVar(i), strings.ToLower(c.MAC))
		addrs := c.Addresses
		if i == 0 {
			addrs = append([]GuestAddr{{Address: n.Address, Prefix: n.Prefix}}, n.Extra...)
		}
		for j, a := range addrs {
			suffix := ""
			if j > 0 {
				suffix = fmt.Sprint(j)
			}
			fmt.Fprintf(&b, "IPADDR%s=%s\nPREFIX%s=%d\n", suffix, a.Address, suffix, a.Prefix)
		}
		if i == 0 {
			if n.Gateway != "" {
				fmt.Fprintf(&b, "GATEWAY=%s\n", n.Gateway)
			}
			for j, d := range resolvers(n.DNS, n.IPv6DNS) {
				fmt.Fprintf(&b, "DNS%d=%s\n", j+1, d)
			}
			if n.IPv6Address != "" {
				fmt.Fprintf(&b, "IPV6INIT=yes\nIPV6_AUTOCONF=no\nIPV6ADDR=%s\n", n.IPv6Address)
				if len(n.IPv6Extra) > 0 {
					var sec []string
					for _, a := range n.IPv6Extra {
						sec = append(sec, fmt.Sprintf("%s/%d", a, n.ipv6Len()))
					}
					fmt.Fprintf(&b, "IPV6ADDR_SECONDARIES=\"%s\"\n", strings.Join(sec, " "))
				}
				fmt.Fprintf(&b, "IPV6_DEFAULTGW=%s%%%s\n", n.ipv6Gateway(), devVar(0))
			}
		} else {
			b.WriteString("DEFROUTE=no\n")
		}
		out = append(out, b.String())
	}
	return out
}

// ifupdownStrip comments out every stanza of the cards in $devs and drops an
// earlier MaburVM block, in the file named by $1, keeping the file's owner
// and mode. POSIX awk: the guest may have mawk, gawk or busybox.
const ifupdownStrip = `strip_ifupdown() {
  awk -v devs=" $devs " '
    /^# BEGIN MaburVM/ { blk = 1; next }
    /^# END MaburVM/ { blk = 0; next }
    blk { next }
    $1 == "auto" || $1 ~ /^allow-/ {
      out = $1; kept = 0
      for (i = 2; i <= NF; i++) { d = $i; sub(/:.*/, "", d); if (index(devs, " " d " ") == 0) { out = out " " $i; kept++ } }
      skip = 0
      if (kept == NF - 1) print; else { print "# maburvm: " $0; if (kept > 0) print out }
      next
    }
    $1 == "iface" || $1 == "mapping" || $1 == "source" || $1 == "source-directory" || $1 == "rename" {
      d = $2; sub(/:.*/, "", d)
      skip = ($1 == "iface" || $1 == "mapping") && index(devs, " " d " ") > 0
    }
    { if (skip) print "# maburvm: " $0; else print }
  ' "$1" > "$1.maburvm-new" && cat "$1.maburvm-new" > "$1"
  rm -f "$1.maburvm-new"
}
`

// LinuxNetworkScript is the shell script that re-addresses a running Linux
// guest. It prints "stack=<name>" first and exits 0, ExitUnknownStack or
// ExitNotApplied (with the reason on stderr). Every value in it was parsed
// as an address first, so nothing the caller sent reaches the shell as text.
func LinuxNetworkScript(n LinuxNetwork) (string, error) {
	if err := n.Validate(); err != nil {
		return "", err
	}
	cards := n.cards()
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\n")
	w("fail() { echo \"$1\" >&2; exit %d; }\n", ExitNotApplied)
	// Only a real card (one with a device behind it): a bridge, bond or VLAN
	// in the guest carries the same MAC and must not be renamed or readdressed.
	w("devof() { for d in /sys/class/net/*; do [ -e \"$d/device\" ] && [ \"$(cat \"$d/address\" 2>/dev/null)\" = \"$1\" ] && { echo \"${d##*/}\"; return 0; }; done; return 1; }\n")
	// A card plugged in a moment ago may not be in the guest yet.
	w("waitdev() { i=0; while [ $i -lt 10 ]; do devof \"$1\" && return 0; sleep 1; i=$((i+1)); done; return 1; }\n")
	var devs []string
	for i, c := range cards {
		mac := strings.ToLower(c.MAC)
		w("dev%d=$(waitdev %s) || fail 'the guest has no card with MAC %s yet; nothing was changed'\n", i, mac, mac)
		devs = append(devs, devVar(i))
	}
	w("devs=\"%s\"\n", strings.Join(devs, " "))
	var macs []string
	for _, c := range cards {
		macs = append(macs, strings.ToLower(c.MAC))
	}
	w("stack=\n")
	// A netplan guest whose netplan renders for a running networkd takes our
	// networkd unit (it sorts before netplan's 10-netplan-*), as it always
	// has, live and on disk alike; its netplan files are left untouched. The
	// netplan branch is for netplan rendering for NetworkManager.
	w("if [ -f %s ] && systemctl is-active -q systemd-networkd 2>/dev/null; then stack=networkd\n", NetworkdFile)
	// ifupdown only when it brought the card up (its state says so): a stale
	// stanza on a guest run by something else must not start a second manager.
	w("elif command -v ifup >/dev/null 2>&1 && grep -qsE \"^[[:space:]]*iface[[:space:]]+${dev0}([[:space:]]|:)\" /etc/network/interfaces /etc/network/interfaces.d/* &&\n")
	w("  { [ -e \"/run/network/ifstate.${dev0}\" ] || grep -qsE \"^${dev0}=\" /run/network/ifstate /run/ifstate /var/run/ifstate; }; then stack=ifupdown\n")
	w("elif systemctl is-active -q systemd-networkd 2>/dev/null; then stack=networkd\n")
	w("elif command -v netplan >/dev/null 2>&1 && ls /etc/netplan/*.yaml >/dev/null 2>&1; then stack=netplan\n")
	w("elif systemctl is-active -q NetworkManager 2>/dev/null; then stack=networkmanager\n")
	w("elif command -v ifup >/dev/null 2>&1 && [ -d /etc/sysconfig/network-scripts ]; then stack=ifcfg\n")
	w("fi\n")
	w("echo \"stack=$stack\"\n")
	// The announce unit makes the guest's first packets reach the gateway, so
	// upstream ARP caches learn the address at once.
	w("announce() {\n  command -v systemctl >/dev/null 2>&1 || return 0\n  cat > %s <<'MABURVM_EOF'\n%sMABURVM_EOF\n  systemctl daemon-reload >/dev/null 2>&1\n  systemctl enable maburvm-announce.service >/dev/null 2>&1\n  systemctl restart --no-block maburvm-announce.service >/dev/null 2>&1\n  return 0\n}\n",
		AnnounceUnitFile, AnnounceUnit(n.Gateway))
	w("case \"$stack\" in\n")

	w("networkd)\n  mkdir -p /etc/systemd/network || fail 'cannot write /etc/systemd/network'\n  rm -f /etc/systemd/network/11-maburvm-*.network\n")
	w("  cat > %s <<'MABURVM_EOF' || fail 'cannot write %s'\n%sMABURVM_EOF\n  chmod 0644 %s\n", NetworkdFile, NetworkdFile, NetworkdUnit(n), NetworkdFile)
	for i, c := range n.NICs {
		f := fmt.Sprintf("/etc/systemd/network/11-maburvm-%d.network", i+1)
		w("  cat > %s <<'MABURVM_EOF' || fail 'cannot write %s'\n%sMABURVM_EOF\n  chmod 0644 %s\n", f, f, NetworkdNICUnit(c), f)
	}
	w("  announce\n  systemctl restart systemd-networkd || fail 'systemd-networkd did not restart'\n  ;;\n")

	w("netplan)\n  off=\n  for f in /etc/netplan/*.yaml; do\n    [ \"$f\" = %s ] && continue\n", netplanFile)
	w("    if grep -qiE '%s' \"$f\" || grep -qE \"^[[:space:]]+($(echo $devs | tr ' ' '|')):\" \"$f\"; then mv \"$f\" \"$f.maburvm-off\" && off=\"$off $f\"; fi\n  done\n", strings.Join(macs, "|"))
	w("  ( umask 077; cat > %s <<MABURVM_EOF\n%sMABURVM_EOF\n  ) || fail 'cannot write %s'\n", netplanFile, NetplanConfig(n), netplanFile)
	w("  if ! netplan generate; then rm -f %s; for f in $off; do mv \"$f.maburvm-off\" \"$f\"; done; fail 'netplan refused the new configuration; the old one was put back'; fi\n", netplanFile)
	w("  [ -d /etc/cloud/cloud.cfg.d ] && echo 'network: {config: disabled}' > /etc/cloud/cloud.cfg.d/99-maburvm-network.cfg\n")
	w("  announce\n  netplan apply || fail 'netplan apply failed'\n  ;;\n")

	w("ifupdown)\n%s", indent(ifupdownStrip, "  "))
	w("  for f in /etc/network/interfaces /etc/network/interfaces.d/*; do [ -f \"$f\" ] && strip_ifupdown \"$f\"; done\n")
	w("  [ -d /etc/cloud/cloud.cfg.d ] && echo 'network: {config: disabled}' > /etc/cloud/cloud.cfg.d/99-maburvm-network.cfg\n")
	w("  cat >> /etc/network/interfaces <<MABURVM_EOF || fail 'cannot write /etc/network/interfaces'\n%sMABURVM_EOF\n", IfupdownBlock(n))
	// Without resolvconf the dns-nameservers line does nothing.
	w("  if [ -f /etc/resolv.conf ] && [ ! -L /etc/resolv.conf ] && ! command -v resolvconf >/dev/null 2>&1; then\n")
	w("    { grep -v '^[[:space:]]*nameserver' /etc/resolv.conf; printf 'nameserver %%s\\n' %s; } > /etc/resolv.conf.maburvm && cat /etc/resolv.conf.maburvm > /etc/resolv.conf\n    rm -f /etc/resolv.conf.maburvm\n  fi\n",
		strings.Join(resolvers(n.DNS, n.IPv6DNS), " "))
	// -f rather than --force: busybox ifdown (Alpine) knows only the short form.
	w("  announce\n  for d in $devs; do\n    ifdown -f \"$d\" >/dev/null 2>&1\n    ip -4 addr flush dev \"$d\" scope global 2>/dev/null\n    ip -6 addr flush dev \"$d\" scope global 2>/dev/null\n    ifup \"$d\" || fail \"ifup $d failed\"\n  done\n  ;;\n")

	w("networkmanager)\n  mkdir -p %s || fail 'cannot write %s'\n  rm -f %s/maburvm-*.nmconnection\n", nmDir, nmDir, nmDir)
	for _, f := range NMKeyfiles(n) {
		p := nmDir + "/" + f.Name
		w("  ( umask 077; cat > %s <<'MABURVM_EOF'\n%sMABURVM_EOF\n  ) || fail 'cannot write %s'\n  chmod 600 %s\n", p, f.Content, p, p)
	}
	w("  announce\n  nmcli connection reload || fail 'NetworkManager did not reload its profiles'\n")
	for _, c := range cards {
		// A bounded wait keeps the script well inside the agent's 90 seconds.
		w("  nmcli -w 15 connection up uuid %s >/dev/null || fail 'NetworkManager did not bring up the card %s'\n", nmUUID(c.MAC), strings.ToLower(c.MAC))
	}
	w("  ;;\n")

	w("ifcfg)\n")
	// The guest's own files for the cards (by name, device or MAC) are set
	// aside as *.bak, which network-scripts and NetworkManager both skip;
	// left in place, the old address would come up again at boot.
	w("  for f in /etc/sysconfig/network-scripts/ifcfg-*; do\n")
	w("    case \"$f\" in *.bak|*~|*.orig|*.old|*.rpmnew|*.rpmsave|*.rpmorig) continue ;; esac\n")
	w("    [ -f \"$f\" ] || continue\n    grep -qs '^# Managed by MaburVM' \"$f\" && continue\n    aside=\n")
	w("    for d in $devs; do [ \"$f\" = \"/etc/sysconfig/network-scripts/ifcfg-$d\" ] && aside=1; done\n")
	w(`    grep -qsiE "^[[:space:]]*(DEVICE=[\"']?($(echo $devs | tr ' ' '|'))[\"']?[[:space:]]*\$|HWADDR=[\"']?(%s))" "$f" && aside=1`+"\n", strings.Join(macs, "|"))
	w("    [ -n \"$aside\" ] && mv \"$f\" \"$f.maburvm.bak\"\n  done\n")
	for i, content := range IfcfgFiles(n) {
		f := "/etc/sysconfig/network-scripts/ifcfg-" + devVar(i)
		w("  cat > %s <<MABURVM_EOF || fail 'cannot write %s'\n%sMABURVM_EOF\n", f, f, content)
	}
	w("  announce\n  for d in $devs; do ifdown \"$d\" >/dev/null 2>&1; ifup \"$d\" || fail \"ifup $d failed\"; done\n  ;;\n")

	w("*)\n  echo 'the guest uses no network configuration this panel knows (systemd-networkd, netplan, ifupdown, NetworkManager, network-scripts)' >&2\n  exit %d\n  ;;\nesac\n", ExitUnknownStack)
	return b.String(), nil
}

// DiskUsesNetworkd reports whether a stopped Linux guest's network is set up
// by systemd-networkd: this panel's own unit is on its disk, or the service is
// enabled. The disk is only read.
func DiskUsesNetworkd(diskPath string) (bool, error) {
	if _, err := exec.LookPath("guestfish"); err != nil {
		return false, fmt.Errorf("guestfish not found on this node (install libguestfs-tools): %w", err)
	}
	args := append(append([]string{"--ro"}, DiskArgs(diskPath)...), "-i",
		"is-file", NetworkdFile, ":",
		"is-symlink", "/etc/systemd/system/multi-user.target.wants/systemd-networkd.service", ":",
		"is-symlink", "/etc/systemd/system/dbus-org.freedesktop.network1.service")
	out, err := guestfish(args...)
	if err != nil {
		return false, fmt.Errorf("reading the guest's disk: %w: %s", err, strings.TrimSpace(string(out)))
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "true" {
			return true, nil
		}
	}
	return false, nil
}

func indent(s, pad string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n") + "\n"
}
