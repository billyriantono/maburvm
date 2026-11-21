#!/usr/bin/env bash
# MaburVM node installer. For NEW machines only.
#
# Copy the command from the node's page in the panel (Add node):
#   curl -fsSL https://<panel>/install-agent.sh | sudo bash -s -- --token <install token>
# The install token in it is single-use and expires an hour after it was made.
#
# Supported: Debian 12 and 13, Ubuntu 22.04 and 24.04 (RHEL 9 family, best effort).
# It refuses a host that already has libvirt domains or a MaburVM agent unless
# --force is given, and changes the network only when --bridge is given.
# --dry-run prints every action and changes nothing. --help lists the options.
#
# The panel templates in the SHA-256 of each architecture's agent; the
# downloaded agent is checked against it before it is made executable.
set -euo pipefail

PANEL_URL="${PANEL_URL:-__PANEL_URL__}"
AGENT_SHA256_AMD64="${AGENT_SHA256_AMD64:-__AGENT_SHA256_AMD64__}"
AGENT_SHA256_ARM64="${AGENT_SHA256_ARM64:-__AGENT_SHA256_ARM64__}"

# A scratch root for exercising the installer in tests; empty on a real host.
ROOT="${MABURVM_INSTALL_ROOT:-}"

AGENT_BIN=/usr/local/bin/maburvm-agent
AGENT_ENV=/etc/maburvm-agent.env
AGENT_UNIT=/etc/systemd/system/maburvm-agent.service
GRPC_PORT=50051 # the panel dials every agent on this port

TOKEN="${MABURVM_INSTALL_TOKEN:-}"
DRY_RUN=0
FORCE=0
ANY_OS=0
POOL_DIR=/var/lib/libvirt/images
BRIDGE=""
BRIDGE_IFACE=""
CONFIRM_TIMEOUT=120
ALLOWED_PANEL_IPS=""
UPDATE_PUBKEY=""
BIND=0.0.0.0
HOST_IN_USE=0
NODE_NAME=""
AGENT_TOKEN=""
AGENT_DIGEST=""
AGENT_TMP=""
ROLLBACK_AT=0

say() { printf '==> %s\n' "$*"; }
warn() { printf 'WARNING: %s\n' "$*" >&2; }
die() {
	printf 'ERROR: %s\n' "$*" >&2
	exit 1
}
# refuse stops on a host that is not a new machine.
refuse() {
	printf 'REFUSED: %s\n' "$*" >&2
	exit 3
}

usage() {
	cat <<'USAGE'
MaburVM node installer, for new machines only.

  curl -fsSL https://<panel>/install-agent.sh | sudo bash -s -- --token <install token> [options]

  --token TOKEN            the install token from the node's page in the panel
  --dry-run                print every action, change nothing (root not needed)
  --pool-dir DIR           where VM disks go (default /var/lib/libvirt/images)
  --bridge NAME            the bridge VMs attach to. An existing bridge is used as
                           it is; otherwise it is created on the uplink, with the
                           old network put back unless you confirm it works
  --bridge-iface IFACE     the uplink to put in the bridge (default: the
                           interface of the IPv4 default route)
  --confirm-timeout SECS   seconds to confirm the new network (default 120)
  --allowed-panel-ips LIST the panel's outgoing addresses; nobody else may
                           connect to the agent
  --update-pubkey KEY      accept only agent builds signed with this ed25519
                           key; the downloaded agent's signature is checked too
  --bind ADDRESS           address the agent listens on (default 0.0.0.0)
  --force                  install although this host already runs VMs or an
                           agent (the network and firewall are then left alone)
  --any-os                 try on a system that is not supported
USAGE
}

# run executes a command, or prints it under --dry-run.
run() {
	if [ "$DRY_RUN" = 1 ]; then
		printf '[dry-run] %s\n' "$*"
		return 0
	fi
	"$@"
}

# write_file PATH MODE writes stdin to PATH, or prints it under --dry-run.
write_file() {
	local path="$ROOT$1" mode="$2" content tmp
	content="$(cat)"
	if [ "$DRY_RUN" = 1 ]; then
		printf '[dry-run] write %s (mode %s):\n' "$1" "$mode"
		printf '%s\n' "$content" | sed 's/^/    | /'
		return 0
	fi
	install -d -m 0755 "$(dirname "$path")"
	tmp="$(mktemp "$path.XXXXXX")"
	printf '%s\n' "$content" >"$tmp"
	chmod "$mode" "$tmp"
	mv -f "$tmp" "$path"
}

json_field() { sed -n "s/.*\"$1\":\"\\([^\"]*\\)\".*/\\1/p" | head -n 1; }

need_value() {
	if [ $# -lt 2 ] || [ -z "$2" ] || [[ "$2" == --* ]]; then
		die "$1 needs a value (see --help)"
	fi
}

parse_args() {
	while [ $# -gt 0 ]; do
		case "$1" in
		--token | --pool-dir | --bridge | --bridge-iface | --confirm-timeout | --allowed-panel-ips | --update-pubkey | --bind)
			need_value "$@"
			case "$1" in
			--token) TOKEN="$2" ;;
			--pool-dir) POOL_DIR="$2" ;;
			--bridge) BRIDGE="$2" ;;
			--bridge-iface) BRIDGE_IFACE="$2" ;;
			--confirm-timeout) CONFIRM_TIMEOUT="$2" ;;
			--allowed-panel-ips) ALLOWED_PANEL_IPS="$2" ;;
			--update-pubkey) UPDATE_PUBKEY="$2" ;;
			--bind) BIND="$2" ;;
			esac
			shift 2
			;;
		--dry-run) DRY_RUN=1 && shift ;;
		--force) FORCE=1 && shift ;;
		--any-os) ANY_OS=1 && shift ;;
		-h | --help)
			usage
			exit 0
			;;
		*) die "unknown option $1 (see --help)" ;;
		esac
	done

	ALLOWED_PANEL_IPS="$(printf '%s' "$ALLOWED_PANEL_IPS" | tr -s ' ' ',')"
	if [ -n "$TOKEN" ] && ! [[ "$TOKEN" =~ ^mbe_[0-9a-f]{48}$ ]]; then
		die "that is not an install token. Copy the install command from the node's page in the panel."
	fi
	if [ -z "$TOKEN" ] && [ "$DRY_RUN" != 1 ]; then
		die "--token is required. Copy the install command from the node's page in the panel."
	fi
	[[ "$POOL_DIR" =~ ^/[A-Za-z0-9._/-]*$ ]] || die "--pool-dir must be an absolute path (letters, digits, . _ - /)"
	if [ -n "$BRIDGE" ] && ! [[ "$BRIDGE" =~ ^[A-Za-z0-9_.-]{1,15}$ ]]; then die "--bridge must be an interface name of up to 15 characters"; fi
	if [ -n "$BRIDGE_IFACE" ] && ! [[ "$BRIDGE_IFACE" =~ ^[A-Za-z0-9_.@-]{1,15}$ ]]; then die "--bridge-iface must be an interface name"; fi
	if ! [[ "$CONFIRM_TIMEOUT" =~ ^[0-9]+$ ]] || [ "$CONFIRM_TIMEOUT" -lt 30 ] || [ "$CONFIRM_TIMEOUT" -gt 3600 ]; then
		die "--confirm-timeout must be between 30 and 3600 seconds"
	fi
	if [ -n "$ALLOWED_PANEL_IPS" ] && ! [[ "$ALLOWED_PANEL_IPS" =~ ^[0-9A-Fa-f:.,/]+$ ]]; then die "--allowed-panel-ips takes IPs or CIDRs separated by commas"; fi
	if [ -n "$UPDATE_PUBKEY" ] && ! [[ "$UPDATE_PUBKEY" =~ ^[A-Za-z0-9+/]{43}=$ ]]; then die "--update-pubkey must be a base64 ed25519 public key, as agent-sign keygen prints it"; fi
	[[ "$BIND" =~ ^[0-9A-Fa-f:.]+$ ]] || die "--bind must be an IP address"
}

detect_os() {
	local f="$ROOT/etc/os-release" id version
	[ -r "$f" ] || die "cannot read /etc/os-release. Supported: Debian 12 and 13, Ubuntu 22.04 and 24.04."
	# shellcheck disable=SC1090
	id="$(. "$f" && printf '%s' "${ID:-}")"
	# shellcheck disable=SC1090
	version="$(. "$f" && printf '%s' "${VERSION_ID:-}")"
	case "$id:$version" in
	debian:12 | debian:13 | ubuntu:22.04 | ubuntu:24.04) PKG=apt ;;
	rhel:9* | rocky:9* | almalinux:9* | centos:9*) PKG=dnf ;;
	*)
		[ "$ANY_OS" = 1 ] || die "unsupported system ($id $version). Supported: Debian 12 and 13, Ubuntu 22.04 and 24.04 (RHEL 9 family, best effort). --any-os tries anyway."
		if command -v apt-get >/dev/null 2>&1; then PKG=apt; elif command -v dnf >/dev/null 2>&1; then PKG=dnf; else die "no apt-get or dnf on this system"; fi
		warn "unsupported system ($id $version); carrying on because of --any-os"
		;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) GOARCH=amd64 ;;
	aarch64 | arm64) GOARCH=arm64 ;;
	*) die "unsupported architecture $(uname -m)" ;;
	esac
	say "System: $id $version ($GOARCH)"
}

# existing_host prints why this host is not a new machine, one reason a line.
existing_host() {
	local f doms
	for f in "$AGENT_UNIT" /lib/systemd/system/maburvm-agent.service /usr/lib/systemd/system/maburvm-agent.service \
		"$AGENT_BIN" "$AGENT_ENV" /opt/maburvm/agent; do
		if [ -e "$ROOT$f" ]; then echo "a MaburVM agent is installed ($f)"; fi
	done
	# Definitions on disk count even while libvirt is stopped.
	for f in "$ROOT"/etc/libvirt/qemu/*.xml "$ROOT"/run/libvirt/qemu/*.xml; do
		if [ -e "$f" ]; then
			echo "libvirt has domains (${f#"$ROOT"})"
			break
		fi
	done
	if command -v virsh >/dev/null 2>&1; then
		doms="$(virsh -c qemu:///system list --all --name 2>/dev/null | sed '/^[[:space:]]*$/d' | head -n 5 | tr '\n' ' ' || true)"
		if [ -n "$doms" ]; then echo "libvirt runs domains: $doms"; fi
	fi
}

check_host() {
	local findings
	findings="$(existing_host)"
	if [ -n "$findings" ]; then
		printf '%s\n' "$findings" | sed 's/^/  - /' >&2
		if [ "$FORCE" != 1 ]; then
			refuse "this is not a new machine. The installer only sets up new nodes and does not touch a host that already runs VMs or an agent. Update an existing node's agent from the panel (Settings, System). Use --force only if you are sure."
		fi
		warn "--force: carrying on although this is not a new machine. The network and bridge netfilter are left as they are."
		HOST_IN_USE=1
	fi
	if [ "$GOARCH" = amd64 ] && [ ! -e "$ROOT/dev/kvm" ] && ! grep -qwE 'vmx|svm' "$ROOT/proc/cpuinfo" 2>/dev/null; then
		warn "this CPU offers no hardware virtualization (no vmx or svm flag): VMs here would not get KVM. Turn on VT-x or AMD-V in the firmware."
	fi
}

check_token() {
	local body code msg
	if [ -z "$TOKEN" ]; then
		warn "no --token: the panel is not asked (dry run)"
		return 0
	fi
	body="$(mktemp)"
	code="$(curl -sS -o "$body" -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "$PANEL_URL/api/v1/nodes/enroll" 2>/dev/null || true)"
	if [ "$code" != 200 ]; then
		msg="$(json_field message <"$body")"
		rm -f "$body"
		die "the panel at $PANEL_URL did not accept the install token (HTTP ${code:-none}): ${msg:-no answer}"
	fi
	NODE_NAME="$(json_field node_name <"$body")"
	rm -f "$body"
	say "Installing node \"$NODE_NAME\" for $PANEL_URL"
}

# check_digest stops before anything is installed when the panel has no agent
# for this architecture.
check_digest() {
	case "$GOARCH" in
	amd64) AGENT_DIGEST="$AGENT_SHA256_AMD64" ;;
	arm64) AGENT_DIGEST="$AGENT_SHA256_ARM64" ;;
	esac
	AGENT_DIGEST="${AGENT_DIGEST,,}"
	if ! [[ "$AGENT_DIGEST" =~ ^[0-9a-f]{64}$ ]]; then
		die "no SHA-256 published for $GOARCH: the panel has no agent-$GOARCH bundled. Pin a digest you checked yourself with AGENT_SHA256_${GOARCH^^}=<digest>."
	fi
}

apt_has() { apt-cache show "$1" >/dev/null 2>&1; }

install_packages() {
	local pkgs
	say "Installing packages"
	case "$PKG" in
	apt)
		pkgs=(libvirt-daemon-system libvirt-clients qemu-utils iputils-arping ebtables iptables bridge-utils
			libguestfs-tools genisoimage chrony curl ca-certificates openssl iproute2 dnsmasq-base)
		if [ "$GOARCH" = amd64 ]; then pkgs+=(qemu-system-x86); else pkgs+=(qemu-system-arm qemu-efi-aarch64); fi
		run env DEBIAN_FRONTEND=noninteractive apt-get update -y
		# The QEMU driver is its own package on newer releases, where libvirt
		# only recommends it.
		if apt_has libvirt-daemon-driver-qemu; then pkgs+=(libvirt-daemon-driver-qemu); fi
		# NEEDRESTART_MODE: Ubuntu's needrestart otherwise stops apt to ask on the terminal.
		run env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a apt-get install -y --no-install-recommends "${pkgs[@]}"
		;;
	dnf)
		pkgs=(qemu-kvm qemu-img libvirt-daemon-kvm libvirt-client iputils ebtables-legacy iptables-nft
			guestfs-tools libguestfs-tools-c xorriso chrony curl ca-certificates openssl iproute dnsmasq)
		run dnf install -y --setopt=install_weak_deps=False --setopt=strict=0 "${pkgs[@]}"
		;;
	esac
	run systemctl enable --now libvirtd || run systemctl enable --now virtqemud.socket || warn "could not start libvirt"
	run systemctl enable --now chrony || run systemctl enable --now chronyd || warn "could not start chrony"
}

setup_pool() {
	say "Storage pool directory $POOL_DIR"
	run install -d -m 0711 "$ROOT$POOL_DIR"
	if [ "$DRY_RUN" != 1 ] && [ "$(stat -c %d "$ROOT$POOL_DIR")" = "$(stat -c %d "$ROOT/")" ]; then
		warn "$POOL_DIR is on the root filesystem: when VM disks fill it, the system fills with it. Mount a separate volume there, or use --pool-dir."
	fi
}

# setup_netfilter writes exactly what the agent keeps (netrules.PersistBridgeNetfilter).
setup_netfilter() {
	if [ "$HOST_IN_USE" = 1 ]; then
		warn "bridge netfilter left as it is: turning it on here would start filtering VMs that are already running"
		return 0
	fi
	say "Bridge netfilter and IP forwarding"
	run modprobe br_netfilter || warn "could not load br_netfilter"
	printf 'br_netfilter\n' | write_file /etc/modules-load.d/maburvm.conf 0644
	printf 'net.bridge.bridge-nf-call-iptables = 1\nnet.bridge.bridge-nf-call-ip6tables = 1\n' | write_file /etc/sysctl.d/99-maburvm-bridge.conf 0644
	printf 'net.ipv4.ip_forward = 1\n' | write_file /etc/sysctl.d/99-maburvm.conf 0644
	run sysctl --system >/dev/null || warn "sysctl --system reported errors"
}

# ---------------------------------------------------------------------------
# Bridge (opt-in). The new network is applied by a separate systemd unit, and a
# timer puts the old one back unless the operator confirms within the timeout,
# so a cut SSH session cannot leave the host unreachable.
# ---------------------------------------------------------------------------

read_uplink() {
	local fam addr dyn route4 route6
	IF_MAC="$(cat "$ROOT/sys/class/net/$BRIDGE_IFACE/address")"
	IF_MTU="$(cat "$ROOT/sys/class/net/$BRIDGE_IFACE/mtu")"
	V4_ADDRS=()
	V6_ADDRS=()
	V4_DHCP=0
	V6_RA=0
	while read -r fam addr dyn; do
		case "$fam:$dyn" in
		inet:1) V4_DHCP=1 ;;
		inet:0) V4_ADDRS+=("$addr") ;;
		inet6:1) V6_RA=1 ;;
		inet6:0) V6_ADDRS+=("$addr") ;;
		esac
	done < <(ip -o addr show dev "$BRIDGE_IFACE" scope global | awk '{d=0; for(i=5;i<=NF;i++) if($i=="dynamic") d=1; print $3, $4, d}')
	route4="$(ip -4 route show default dev "$BRIDGE_IFACE")"
	route6="$(ip -6 route show default dev "$BRIDGE_IFACE")"
	GW4="$(printf '%s\n' "$route4" | awk '{for(i=1;i<NF;i++) if($i=="via"){print $(i+1); exit}}')"
	GW6="$(printf '%s\n' "$route6" | awk '{for(i=1;i<NF;i++) if($i=="via"){print $(i+1); exit}}')"
	ONLINK4=0
	if [[ " $route4 " == *" onlink "* ]]; then ONLINK4=1; fi
	if [[ " $route6 " == *" proto ra "* ]]; then V6_RA=1 && GW6=""; fi
	DNS="$(resolvectl dns "$BRIDGE_IFACE" 2>/dev/null | sed -n 's/^[^:]*: *//p' | head -n 1 || true)"
	if [ -z "$DNS" ]; then
		DNS="$(awk '$1=="nameserver" && $2!~/^127\./ {print $2}' "$ROOT/etc/resolv.conf" 2>/dev/null | tr '\n' ' ' || true)"
	fi
	DNS="$(printf '%s' "$DNS" | tr -s '[:space:]' ' ' | sed 's/^ //; s/ $//')"
	if [ "$V4_DHCP" = 0 ] && [ "${#V4_ADDRS[@]}" = 0 ]; then
		die "$BRIDGE_IFACE has no IPv4 address to move onto $BRIDGE"
	fi
	if [ "$V4_DHCP" = 0 ] && [ -z "$GW4" ]; then
		die "no IPv4 default route through $BRIDGE_IFACE; name the uplink with --bridge-iface"
	fi
}

# network_renderer says what configures the uplink.
network_renderer() {
	local nm=""
	if command -v nmcli >/dev/null 2>&1; then nm="$(nmcli -t -f DEVICE,STATE device status 2>/dev/null || true)"; fi
	if printf '%s\n' "$nm" | grep -qx "$BRIDGE_IFACE:connected"; then
		echo networkmanager
	elif command -v netplan >/dev/null 2>&1 && ls "$ROOT"/etc/netplan/*.yaml >/dev/null 2>&1; then
		echo netplan
	elif grep -qsE "^[[:space:]]*iface[[:space:]]+${BRIDGE_IFACE}[[:space:]]" "$ROOT/etc/network/interfaces" "$ROOT"/etc/network/interfaces.d/*; then
		echo ifupdown
	else
		echo unknown
	fi
}

# The installer replaces the network configuration whole, so it may configure
# nothing but the uplink: another interface's settings would be lost.
netplan_only_uplink() {
	local key out names
	for key in bonds bridges vlans wifis tunnels vrfs modems; do
		out="$(netplan get "$key" 2>/dev/null || true)"
		case "$(printf '%s' "$out" | tr -d '[:space:]')" in "" | null) ;; *) return 1 ;; esac
	done
	out="$(netplan get ethernets 2>/dev/null)" || return 1
	names="$(printf '%s\n' "$out" | sed -n 's/^\([A-Za-z0-9_.@-]\{1,\}\):.*$/\1/p' | tr '\n' ' ')"
	[ "$names" = "$BRIDGE_IFACE " ]
}

# extra_routes prints the uplink's routes the new configuration would not carry
# over: anything but the default routes and those the kernel, DHCP or RA add.
extra_routes() {
	{
		ip -4 route show dev "$BRIDGE_IFACE" || true
		ip -6 route show dev "$BRIDGE_IFACE" || true
	} 2>/dev/null | awk '$1 != "default" && $1 != "multicast" && $1 !~ /^fe80::/ && $0 !~ / proto (kernel|dhcp|ra)( |$)/'
}

ifupdown_only_uplink() {
	local names
	names="$(grep -hsE '^[[:space:]]*iface[[:space:]]' "$ROOT/etc/network/interfaces" "$ROOT"/etc/network/interfaces.d/* |
		awk '{print $2}' | sort -u | grep -vxE "lo|$BRIDGE_IFACE" || true)"
	[ -z "$names" ]
}

render_netplan() {
	local a
	echo "# Written by the MaburVM installer. The files it replaced are in $BACKUP_DIR."
	echo "network:"
	echo "  version: 2"
	echo "  renderer: networkd"
	echo "  ethernets:"
	echo "    $BRIDGE_IFACE:"
	echo "      dhcp4: false"
	echo "      dhcp6: false"
	echo "      accept-ra: false"
	echo "      mtu: $IF_MTU"
	echo "  bridges:"
	echo "    $BRIDGE:"
	echo "      interfaces: [$BRIDGE_IFACE]"
	echo "      macaddress: \"$IF_MAC\""
	echo "      mtu: $IF_MTU"
	echo "      parameters:"
	echo "        stp: false"
	echo "        forward-delay: 0"
	if [ "$V4_DHCP" = 1 ]; then echo "      dhcp4: true"; else echo "      dhcp4: false"; fi
	echo "      dhcp6: false"
	if [ "$V6_RA" = 1 ]; then echo "      accept-ra: true"; else echo "      accept-ra: false"; fi
	if [ "${#V4_ADDRS[@]}" -gt 0 ] || [ "${#V6_ADDRS[@]}" -gt 0 ]; then
		echo "      addresses:"
		for a in "${V4_ADDRS[@]}" "${V6_ADDRS[@]}"; do echo "        - \"$a\""; done
	fi
	if { [ "$V4_DHCP" = 0 ] && [ -n "$GW4" ]; } || [ -n "$GW6" ]; then
		echo "      routes:"
		if [ "$V4_DHCP" = 0 ] && [ -n "$GW4" ]; then
			echo "        - to: default"
			echo "          via: $GW4"
			if [ "$ONLINK4" = 1 ]; then echo "          on-link: true"; fi
		fi
		if [ -n "$GW6" ]; then
			echo "        - to: default"
			echo "          via: \"$GW6\""
		fi
	fi
	if [ "$V4_DHCP" = 0 ] && [ -n "$DNS" ]; then
		echo "      nameservers:"
		echo "        addresses: [$(printf '%s' "$DNS" | sed 's/ /, /g')]"
	fi
}

render_ifupdown() {
	local a first=1
	echo "# Written by the MaburVM installer. The files it replaced are in $BACKUP_DIR."
	echo "auto lo"
	echo "iface lo inet loopback"
	echo
	echo "auto $BRIDGE_IFACE"
	echo "iface $BRIDGE_IFACE inet manual"
	echo "    mtu $IF_MTU"
	echo
	echo "auto $BRIDGE"
	echo "iface $BRIDGE inet static"
	for a in "${V4_ADDRS[@]}"; do
		if [ "$first" = 1 ]; then
			echo "    address $a"
			first=0
		else
			echo "    up ip addr add $a dev $BRIDGE"
		fi
	done
	echo "    bridge_ports $BRIDGE_IFACE"
	echo "    bridge_stp off"
	echo "    bridge_fd 0"
	echo "    bridge_maxwait 0"
	echo "    hwaddress ether $IF_MAC"
	echo "    mtu $IF_MTU"
	if [ "$ONLINK4" = 1 ]; then
		echo "    up ip route replace default via $GW4 dev $BRIDGE onlink"
	else
		echo "    gateway $GW4"
	fi
	if [ -n "$DNS" ]; then echo "    dns-nameservers $DNS"; fi
	first=1
	for a in "${V6_ADDRS[@]}"; do
		if [ "$first" = 1 ]; then
			echo
			echo "iface $BRIDGE inet6 static"
			echo "    address $a"
			if [ -n "$GW6" ]; then echo "    gateway $GW6"; fi
			first=0
		else
			echo "    up ip -6 addr add $a dev $BRIDGE"
		fi
	done
	if [ "$first" = 1 ] && [ "$V6_RA" = 1 ]; then
		echo
		echo "iface $BRIDGE inet6 auto"
	fi
}

# render_restore prints the commands that put the uplink's addresses and
# routes back on it, for the rollback.
render_restore() {
	local a onlink=""
	if [ "$ONLINK4" = 1 ]; then onlink=" onlink"; fi
	echo "ip link set dev $BRIDGE_IFACE nomaster 2>/dev/null || true"
	echo "ip link del dev $BRIDGE 2>/dev/null || true"
	echo "ip link set dev $BRIDGE_IFACE up || true"
	for a in "${V4_ADDRS[@]}" "${V6_ADDRS[@]}"; do echo "ip addr replace $a dev $BRIDGE_IFACE || true"; done
	if [ -n "$GW4" ] && [ "$V4_DHCP" = 0 ]; then echo "ip route replace default via $GW4 dev $BRIDGE_IFACE$onlink || true"; fi
	if [ -n "$GW6" ]; then echo "ip -6 route replace default via $GW6 dev $BRIDGE_IFACE || true"; fi
}

# render_live_move prints the commands that move the uplink onto the bridge
# now (ifupdown applies the new file only at the next boot).
render_live_move() {
	local a onlink=""
	if [ "$ONLINK4" = 1 ]; then onlink=" onlink"; fi
	echo "ip link add name $BRIDGE type bridge stp_state 0 forward_delay 0"
	echo "ip link set dev $BRIDGE address $IF_MAC mtu $IF_MTU"
	echo "ip link set dev $BRIDGE up"
	echo "ip link set dev $BRIDGE_IFACE master $BRIDGE"
	for a in "${V4_ADDRS[@]}" "${V6_ADDRS[@]}"; do
		echo "ip addr del $a dev $BRIDGE_IFACE || true"
		echo "ip addr replace $a dev $BRIDGE"
	done
	echo "ip route replace default via $GW4 dev $BRIDGE$onlink"
	if [ -n "$GW6" ]; then echo "ip -6 route replace default via $GW6 dev $BRIDGE || true"; fi
}

setup_bridge() {
	local renderer ts unit_apply unit_rollback config_file cloud_off routes
	[ -n "$BRIDGE" ] || return 0
	if [ -d "$ROOT/sys/class/net/$BRIDGE/bridge" ]; then
		say "Using the existing bridge $BRIDGE; the network is not changed"
		return 0
	fi
	[ ! -e "$ROOT/sys/class/net/$BRIDGE" ] || die "$BRIDGE exists but is not a bridge"
	if [ "$HOST_IN_USE" = 1 ]; then
		refuse "not creating $BRIDGE: this host already runs VMs, and moving its network onto a bridge would cut them off. Set the bridge up yourself, then re-run with --bridge $BRIDGE to use it."
	fi
	if [ -z "$BRIDGE_IFACE" ]; then
		BRIDGE_IFACE="$(ip -4 route show default | awk '{for(i=1;i<NF;i++) if($i=="dev"){print $(i+1); exit}}')"
	fi
	[ -n "$BRIDGE_IFACE" ] || die "no IPv4 default route to find the uplink by; name it with --bridge-iface"
	[ -e "$ROOT/sys/class/net/$BRIDGE_IFACE" ] || die "there is no interface $BRIDGE_IFACE"
	[ ! -d "$ROOT/sys/class/net/$BRIDGE_IFACE/bridge" ] || die "$BRIDGE_IFACE is a bridge already; use --bridge $BRIDGE_IFACE"
	[ ! -e "$ROOT/sys/class/net/$BRIDGE_IFACE/master" ] || die "$BRIDGE_IFACE already belongs to a bridge or bond; set the bridge up yourself"

	renderer="$(network_renderer)"
	case "$renderer" in
	networkmanager)
		die "NetworkManager manages $BRIDGE_IFACE, and the installer sets up bridges only with netplan or ifupdown. Create $BRIDGE with nmcli yourself, then re-run with --bridge $BRIDGE."
		;;
	netplan)
		netplan_only_uplink || die "this host's netplan configures more than $BRIDGE_IFACE (or netplan get is unavailable), and the installer would lose those settings. Set up $BRIDGE yourself, then re-run with --bridge $BRIDGE."
		;;
	ifupdown)
		ifupdown_only_uplink || die "/etc/network/interfaces configures more than $BRIDGE_IFACE, and the installer would lose those settings. Set up $BRIDGE yourself, then re-run with --bridge $BRIDGE."
		;;
	*)
		die "could not tell how this host's network is configured (not netplan, ifupdown or NetworkManager). Set up $BRIDGE yourself, then re-run with --bridge $BRIDGE."
		;;
	esac
	read_uplink
	routes="$(extra_routes)"
	if [ -n "$routes" ]; then
		printf '%s\n' "$routes" | sed 's/^/  - /' >&2
		die "$BRIDGE_IFACE has routes besides the default one (above), and the new configuration would lose them. Set up $BRIDGE yourself, then re-run with --bridge $BRIDGE."
	fi
	if [ "$renderer" = ifupdown ] && [ "$V4_DHCP" = 1 ]; then
		die "$BRIDGE_IFACE gets its address by DHCP, and with ifupdown the installer moves only static addresses. Set up $BRIDGE yourself, then re-run with --bridge $BRIDGE."
	fi
	command -v systemd-run >/dev/null 2>&1 || die "systemd-run is needed to apply the network safely"

	ts="$(date +%Y%m%d-%H%M%S)"
	BACKUP_DIR="/var/backups/maburvm-network-$ts"
	unit_apply="maburvm-net-apply-$ts"
	unit_rollback="maburvm-net-rollback-$ts"
	say "Moving $BRIDGE_IFACE onto a new bridge $BRIDGE ($renderer); the old configuration is kept in $BACKUP_DIR"

	cloud_off=""
	if [ "$renderer" = netplan ]; then
		config_file=/etc/netplan/01-maburvm-bridge.yaml
		# cloud-init would write its own network file back at the next boot.
		if [ -d "$ROOT/etc/cloud/cloud.cfg.d" ]; then cloud_off=/etc/cloud/cloud.cfg.d/99-maburvm-disable-network-config.cfg; fi
		run install -d -m 0700 "$ROOT$BACKUP_DIR/netplan"
		run cp -a "$ROOT"/etc/netplan/. "$ROOT$BACKUP_DIR/netplan/"
		render_netplan | write_file "$BACKUP_DIR/new.yaml" 0600
		{
			echo "#!/bin/bash"
			echo "set -e"
			echo "find /etc/netplan -maxdepth 1 -name '*.yaml' -delete"
			echo "install -m 0600 $BACKUP_DIR/new.yaml $config_file"
			if [ -n "$cloud_off" ]; then echo "echo 'network: {config: disabled}' > $cloud_off"; fi
			echo "netplan apply"
		} | write_file "$BACKUP_DIR/apply.sh" 0700
		{
			echo "#!/bin/bash"
			echo "# Puts the network back as it was before the MaburVM installer changed it."
			echo "exec >>$BACKUP_DIR/rollback.log 2>&1"
			echo "echo \"rollback \$(date)\""
			echo "rm -f $config_file${cloud_off:+ $cloud_off}"
			echo "cp -a $BACKUP_DIR/netplan/. /etc/netplan/"
			render_restore
			echo "netplan apply || true"
		} | write_file "$BACKUP_DIR/rollback.sh" 0700
	else
		config_file=/etc/network/interfaces
		run install -d -m 0700 "$ROOT$BACKUP_DIR/network"
		run cp -a "$ROOT"/etc/network/. "$ROOT$BACKUP_DIR/network/"
		render_ifupdown | write_file "$BACKUP_DIR/interfaces" 0600
		{
			echo "#!/bin/bash"
			echo "set -e"
			echo "find /etc/network/interfaces.d -maxdepth 1 -type f -delete 2>/dev/null || true"
			echo "install -m 0644 $BACKUP_DIR/interfaces $config_file"
			render_live_move
		} | write_file "$BACKUP_DIR/apply.sh" 0700
		{
			echo "#!/bin/bash"
			echo "# Puts the network back as it was before the MaburVM installer changed it."
			echo "exec >>$BACKUP_DIR/rollback.log 2>&1"
			echo "echo \"rollback \$(date)\""
			echo "cp -a $BACKUP_DIR/network/. /etc/network/"
			render_restore
		} | write_file "$BACKUP_DIR/rollback.sh" 0700
	fi

	# Armed before anything changes; outlives this shell and a cut SSH session.
	say "If this session drops: the old network comes back in $((CONFIRM_TIMEOUT + 30)) seconds. To keep the new one, run systemctl stop $unit_rollback.timer from a new session, then run the install command again."
	ROLLBACK_AT=$(($(date +%s) + CONFIRM_TIMEOUT + 30))
	run systemd-run --quiet --unit="$unit_rollback" --on-active="$((CONFIRM_TIMEOUT + 30))s" \
		--timer-property=AccuracySec=1s /bin/bash "$BACKUP_DIR/rollback.sh"
	if ! run systemd-run --quiet --wait --collect --unit="$unit_apply" /bin/bash "$BACKUP_DIR/apply.sh"; then
		rollback_now "$unit_rollback" "applying the new network failed"
	fi
	if [ "$DRY_RUN" = 1 ]; then
		say "[dry-run] check that the panel is still reachable, then ask for \"keep\" within $CONFIRM_TIMEOUT seconds; otherwise $unit_rollback puts the old network back"
		return 0
	fi
	if ! panel_reachable; then
		rollback_now "$unit_rollback" "the panel is not reachable over the new network"
	fi
	if ! confirm_network "$unit_rollback"; then
		# Stopped from another session: the operator kept the network there.
		if systemctl is-active --quiet "$unit_rollback.timer" || [ -e "$BACKUP_DIR/rollback.log" ]; then
			rollback_now "$unit_rollback" "the new network was not confirmed"
		fi
	fi
	systemctl stop "$unit_rollback.timer" 2>/dev/null || true
	# rollback.sh writes its log first thing, so a log means it already ran.
	if [ -e "$BACKUP_DIR/rollback.log" ]; then
		die "the old network was already put back (log: $BACKUP_DIR/rollback.log). Packages are installed; the agent was not. Re-run to try again."
	fi
	say "Kept the new network on $BRIDGE. The old configuration stays in $BACKUP_DIR."
}

panel_reachable() {
	local i code
	for i in 1 2 3 4 5 6 7 8 9 10; do
		code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$PANEL_URL/api/v1/nodes/agent-binary/checksum?arch=$GOARCH" || true)"
		if [ -n "$code" ] && [ "$code" != 000 ]; then return 0; fi
		[ "$i" = 10 ] || sleep 3
	done
	return 1
}

confirm_network() {
	local answer="" left
	if ! { : </dev/tty; } 2>/dev/null; then
		warn "no terminal to confirm the new network on"
		return 1
	fi
	# Stop asking well before the timer fires: an answer after it would be a lie.
	left=$((ROLLBACK_AT - $(date +%s) - 15))
	if [ "$left" -lt 15 ]; then
		warn "too little time left to confirm before the timer puts the old network back"
		return 1
	fi
	{
		printf '\nThe network now runs on %s and the panel is reachable.\n' "$BRIDGE"
		printf 'Open a NEW SSH session to this host now to be sure you can still get in.\n'
		printf 'Type keep within %s seconds to keep it. Anything else, or nothing, puts the old network back.\n' "$left"
		printf '(From another session: systemctl stop %s.timer keeps it too.)\n> ' "$1"
	} >/dev/tty
	read -r -t "$left" answer </dev/tty || true
	[ "$answer" = keep ]
}

rollback_now() {
	warn "$2: putting the old network back"
	systemctl stop "$1.timer" 2>/dev/null || true
	systemctl start "$1.service" || bash "$BACKUP_DIR/rollback.sh"
	die "the network was put back as it was (log: $BACKUP_DIR/rollback.log). Packages are installed; the agent was not. Nothing else changed."
}

# ---------------------------------------------------------------------------
# Agent
# ---------------------------------------------------------------------------

verify_signature() {
	local digest="$1" headers="$2" sig dir ok=1
	sig="$(tr -d '\r' <"$headers" | sed -n 's/^[Xx]-[Aa]gent-[Ss]ignature: *\([A-Za-z0-9+/=]*\)$/\1/p' | head -n 1)"
	if [ -z "$sig" ]; then
		warn "the panel sent no signature for this build, and --update-pubkey asks for one: sign it (agent-sign) and put agent-$GOARCH.sig beside the panel's bundled agent"
		return 1
	fi
	command -v openssl >/dev/null 2>&1 || {
		warn "openssl is needed to check the signature"
		return 1
	}
	dir="$(mktemp -d)"
	# An ed25519 SubjectPublicKeyInfo is this fixed prefix and the raw key.
	{
		printf '\x30\x2a\x30\x05\x06\x03\x2b\x65\x70\x03\x21\x00'
		printf '%s' "$UPDATE_PUBKEY" | base64 -d
	} >"$dir/pub.der"
	printf '%s' "$sig" | base64 -d >"$dir/sig"
	printf 'maburvm-agent-update:v1:%s' "$digest" >"$dir/msg"
	if openssl pkeyutl -verify -pubin -inkey "$dir/pub.der" -keyform DER -rawin -in "$dir/msg" -sigfile "$dir/sig" >/dev/null 2>&1; then ok=0; fi
	rm -rf "$dir"
	return "$ok"
}

# download_agent fetches and checks the agent into AGENT_TMP. It becomes
# $AGENT_BIN only after enrolling, so a failed enrollment leaves no agent
# behind to make the next run refuse the host.
download_agent() {
	local actual headers
	if [ "$DRY_RUN" = 1 ]; then
		say "[dry-run] download the $GOARCH agent from $PANEL_URL with the install token, check its SHA-256 ($AGENT_DIGEST)${UPDATE_PUBKEY:+ and signature}, check it runs here"
		return 0
	fi
	say "Downloading the agent"
	install -d -m 0755 "$ROOT$(dirname "$AGENT_BIN")"
	# Beside its destination, not in /tmp: /tmp may be mounted noexec.
	AGENT_TMP="$(mktemp "$ROOT$AGENT_BIN.new.XXXXXX")"
	trap 'rm -f "$AGENT_TMP"' EXIT
	headers="$(mktemp)"
	if ! curl -fsS -D "$headers" -H "Authorization: Bearer $TOKEN" "$PANEL_URL/api/v1/nodes/agent-binary?arch=$GOARCH" -o "$AGENT_TMP"; then
		rm -f "$headers"
		die "could not download the agent from $PANEL_URL"
	fi
	actual="$(sha256sum "$AGENT_TMP" | awk '{print $1}')"
	if [ "$actual" != "$AGENT_DIGEST" ]; then
		rm -f "$headers"
		die "agent binary checksum MISMATCH, refusing to install it. Expected $AGENT_DIGEST, got $actual."
	fi
	say "Checksum OK ($actual)"
	if [ -n "$UPDATE_PUBKEY" ] && ! verify_signature "$actual" "$headers"; then
		rm -f "$headers"
		die "the agent's signature does not match --update-pubkey, refusing to install it"
	fi
	rm -f "$headers"
	chmod 0755 "$AGENT_TMP"
	if ! "$AGENT_TMP" --version >/dev/null 2>&1; then
		die "the downloaded agent does not run on this host (glibc or libvirt too old?)"
	fi
}

place_agent() {
	if [ "$DRY_RUN" = 1 ]; then
		say "[dry-run] install the checked agent as $AGENT_BIN"
		return 0
	fi
	mv -f "$AGENT_TMP" "$ROOT$AGENT_BIN"
}

enroll() {
	local body code msg host
	if [ "$DRY_RUN" = 1 ]; then
		say "[dry-run] enroll with the panel: the install token is used up and the panel hands out this node's agent credential"
		AGENT_TOKEN="<from the panel>"
		return 0
	fi
	host="$(hostname 2>/dev/null | tr -cd 'A-Za-z0-9.-' | cut -c1-200)"
	body="$(mktemp)"
	code="$(curl -sS -o "$body" -w '%{http_code}' -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
		--data "{\"hostname\":\"$host\",\"arch\":\"$GOARCH\"}" "$PANEL_URL/api/v1/nodes/enroll" 2>/dev/null || true)"
	AGENT_TOKEN="$(sed -n 's/.*"agent_token":"\([0-9a-f]\{64\}\)".*/\1/p' "$body")"
	msg="$(json_field message <"$body")"
	rm -f "$body"
	if [ "$code" != 200 ] || [ -z "$AGENT_TOKEN" ]; then
		die "the panel refused to enroll this node (HTTP ${code:-none}): ${msg:-no answer}"
	fi
	say "Enrolled with the panel"
}

write_agent_config() {
	say "Agent configuration"
	{
		echo "# Written by the MaburVM installer. Root only: AGENT_AUTH_TOKEN is this node's credential."
		echo "AGENT_AUTH_TOKEN=$AGENT_TOKEN"
		echo "AGENT_BIND_ADDRESS=$BIND"
		echo "AGENT_GRPC_PORT=$GRPC_PORT"
		if [ -n "$ALLOWED_PANEL_IPS" ]; then echo "AGENT_ALLOWED_PANEL_IPS=$ALLOWED_PANEL_IPS"; fi
		if [ -n "$UPDATE_PUBKEY" ]; then echo "AGENT_UPDATE_PUBKEY=$UPDATE_PUBKEY"; fi
		# Its TLS key and secrets file, beside the agent's other state.
		echo "MABURVM_DATA_DIR=/var/lib/maburvm"
		# Keeps bridge netfilter on across agent restarts; never on a host that had VMs.
		if [ "$HOST_IN_USE" = 0 ]; then echo "MABURVM_BRIDGE_NETFILTER=enforce"; fi
	} | write_file "$AGENT_ENV" 0600
	write_file "$AGENT_UNIT" 0644 <<UNIT
[Unit]
Description=MaburVM node agent
After=network-online.target libvirtd.service virtqemud.service
Wants=network-online.target

[Service]
EnvironmentFile=$AGENT_ENV
ExecStart=$AGENT_BIN
Restart=always
RestartSec=5
LimitNOFILE=65536
# Only the agent gets the stop signal, not the qemu-img jobs it started: a
# backup or image capture runs for hours and an agent restart must not kill it.
KillMode=mixed

[Install]
WantedBy=multi-user.target
UNIT
}

start_agent() {
	run systemctl daemon-reload
	run systemctl enable --now maburvm-agent
	if [ "$DRY_RUN" = 1 ]; then return 0; fi
	sleep 3
	systemctl is-active --quiet maburvm-agent || die "the agent did not start: journalctl -u maburvm-agent"
}

report() {
	local pf=(--pool "$POOL_DIR")
	if [ -n "$BRIDGE" ]; then pf+=(--bridge "$BRIDGE"); fi
	echo
	if [ "$DRY_RUN" = 1 ]; then
		say "[dry-run] check the host: $AGENT_BIN --preflight ${pf[*]}"
		say "Dry run finished; nothing was changed."
		return 0
	fi
	say "Host checks"
	if ! "$ROOT$AGENT_BIN" --preflight "${pf[@]}"; then
		warn "fix the failed checks above. The node's page in the panel shows the same report once the node is online."
	fi
	echo
	say "MaburVM agent installed and running${NODE_NAME:+ for node \"$NODE_NAME\"}."
	echo "    It listens on $BIND:$GRPC_PORT; the panel must reach this host there."
	if [ -z "$ALLOWED_PANEL_IPS" ]; then
		echo "    Any host holding the credential may connect. To accept only the panel, set AGENT_ALLOWED_PANEL_IPS"
		echo "    in $AGENT_ENV (or firewall $GRPC_PORT/tcp to the panel) and restart the agent."
	fi
	if command -v ufw >/dev/null 2>&1 && [[ "$(ufw status 2>/dev/null || true)" == *"Status: active"* ]]; then
		warn "ufw is active: allow $GRPC_PORT/tcp from the panel (ufw allow from <panel address> to any port $GRPC_PORT proto tcp)"
	fi
	echo "    Add a storage pool for $POOL_DIR and an IP pool${BRIDGE:+ on bridge $BRIDGE} for this node in the panel."
	echo "    Status: systemctl status maburvm-agent    Logs: journalctl -u maburvm-agent -f"
}

main() {
	parse_args "$@"
	if [ "$DRY_RUN" != 1 ] && [ "$(id -u)" != 0 ]; then die "run as root (sudo)"; fi
	if [ "$DRY_RUN" = 1 ]; then say "Dry run: printing every action, changing nothing"; fi
	detect_os
	check_host
	check_token
	check_digest
	install_packages
	setup_pool
	setup_netfilter
	setup_bridge
	download_agent
	enroll
	write_agent_config
	place_agent
	start_agent
	report
}

main "$@"
