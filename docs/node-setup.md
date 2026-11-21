# Node setup guide

How to turn a new machine into a MaburVM node: what it needs, what the
installer does, how the bridge is created without locking you out, what the
agent changes on the host, and every setting the agent reads. Day-to-day node
management is in the [administrator guide](admin-guide.md#3-nodes).

> **Never run the installer on a machine that already carries VMs.** It is
> for new machines only and refuses such a host by itself (exit code 3) unless
> forced. Update an existing node's agent from the panel instead
> ([section 7](#7-updating-an-agent)).

## 1. Requirements

**Hardware.** A 64-bit x86 or ARM CPU with hardware virtualization (Intel VT-x
or AMD-V) switched on in the firmware, so `/dev/kvm` exists. Memory and disk
for the VMs you plan, plus room on the root filesystem: the template cache,
customer ISOs, clone copies and backup exports land there unless you move them
([section 6](#6-agent-settings-reference)), and a full root filesystem takes
libvirt and the agent down.

**Operating system.** Debian 12 or 13, or Ubuntu 22.04 or 24.04. The RHEL 9
family is best effort (`--any-os` to try anything else).

**Network.**

| From | To | Port | What for |
|---|---|---|---|
| The panel host | The node | TCP 50051 (`AGENT_GRPC_PORT`) | The panel drives the agent over gRPC with TLS. Allow only the panel's outgoing address. The agent never connects to the panel. |
| The node | The panel's public address | TCP 443 | Only during installation: the script, the agent binary and enrollment. |
| Other nodes | The node | TCP 22 | Moving VMs between nodes: root logins with a key, which the agents set up themselves. |
| Other nodes | The node | TCP 49152-49215 | Live moves: QEMU sends memory and disk **unencrypted**. Keep this on a private network or a VPN. |
| Customers | The node's publish address | The NAT port range, 80 and 443 | Only on nodes that offer NAT VPS and website hostnames. |

VM consoles do not need any port: each console listens on the node's loopback
and the panel reaches it through the agent.

**Time.** A synchronised clock (the installer installs `chrony`).

## 2. Before you start

In the panel, **Nodes, Add node**: give the node a name and the address the
panel reaches it on. The panel shows a single-use install command, valid for
one hour and shown once:

```bash
curl -fsSL https://panel.example.com/install-agent.sh | sudo bash -s -- --token mbe_...
```

Decide first:

- where VM disks go (`--pool-dir`; default `/var/lib/libvirt/images`, which is
  on the root filesystem on most machines);
- which bridge VMs attach to (`--bridge`), and whether it exists yet;
- the panel's outgoing address (`--allowed-panel-ips`), which can differ from
  the address its hostname resolves to;
- whether you sign agent builds (`--update-pubkey`; see
  [DEPLOYMENT.md §6](DEPLOYMENT.md#6-agent-deployment-separate-privileged)).

## 3. Running the installer

Add `--dry-run` first: it prints every step and file without changing anything
(root is not needed for that). Then, as root, in order the installer:

1. Stops with exit code 3 if the host already has libvirt domains or a MaburVM
   agent. `--force` goes on, but then leaves the network and bridge netfilter
   as they are.
2. Asks the panel whether the token is good and the node still has no agent,
   and whether the panel has an agent build for this CPU architecture.
3. Installs QEMU/KVM, libvirt, `qemu-utils`, `iputils-arping`, `ebtables`,
   `iptables`, `bridge-utils`, `libguestfs-tools`, `genisoimage`, `chrony` and
   `dnsmasq-base`, and starts libvirt and chrony.
4. Creates the storage pool directory, warning when it is on the root
   filesystem.
5. Loads `br_netfilter` and writes the module and sysctl files listed in
   [section 5](#5-what-the-agent-changes-on-a-host).
6. Creates the bridge, only with `--bridge` and only when it does not exist
   ([section 4](#4-the-bridge-and-its-rollback)).
7. Downloads the agent bundled with the panel, using the token; checks its
   SHA-256 against the digest in the script (and its signature with
   `--update-pubkey`); checks that it runs on this host.
8. Enrolls: the token is used up and the panel hands back the node's agent
   credential, written to `/etc/maburvm-agent.env` (mode 0600).
9. Installs the agent as `/usr/local/bin/maburvm-agent` with the systemd unit
   `maburvm-agent`, starts it and prints the host checks.

| Option | Meaning |
|---|---|
| `--token TOKEN` | The install token from the panel. Required. |
| `--dry-run` | Print every step, change nothing. |
| `--pool-dir DIR` | Where VM disks go. Default `/var/lib/libvirt/images`. |
| `--bridge NAME` | The bridge VMs attach to. An existing bridge is used as it is; otherwise it is created. |
| `--bridge-iface IFACE` | The uplink to put in the new bridge. Default: the interface of the IPv4 default route. |
| `--confirm-timeout SECS` | Time to confirm the new network, 30 to 3600. Default 120. |
| `--allowed-panel-ips LIST` | Written as `AGENT_ALLOWED_PANEL_IPS`. |
| `--update-pubkey KEY` | Written as `AGENT_UPDATE_PUBKEY`; the downloaded agent must carry a matching signature. |
| `--bind ADDRESS` | Written as `AGENT_BIND_ADDRESS`. Default `0.0.0.0`. |
| `--force` | Install on a host that already runs VMs or an agent. |
| `--any-os` | Try on a system that is not supported. |

The token works once and for one hour. The script and
`/api/v1/nodes/agent-binary/checksum` are public and hold nothing secret; the
binary download and the enrollment need the token in an `Authorization:
Bearer` header, never in a URL. All four are rate limited per address, and
each enrollment is in the audit log.

## 4. The bridge and its rollback

With `--bridge br0` and no such bridge, the installer moves the uplink's
addresses, routes and MAC address onto a new bridge. It does so only where
netplan or ifupdown configures the network and that configuration names
nothing but the uplink. With NetworkManager, a bond or VLAN, another
interface, DHCP under ifupdown, extra routes on the uplink, or anything it does
not recognise, it stops and says so: create the bridge yourself and run the
command again with `--bridge` naming it.

The change is built to undo itself:

1. The current configuration is copied to
   `/var/backups/maburvm-network-<time>/` with a `rollback.sh`.
2. A systemd timer, `maburvm-net-rollback-<time>`, is armed to run it after
   the confirmation time plus 30 seconds.
3. The change runs as its own systemd unit, so a dropped SSH session cannot
   stop it halfway.
4. If the panel is not reachable over the new network, the old one is put back
   at once.
5. Otherwise the installer asks you to open a **new** SSH session and type
   `keep`. Without that, or with no terminal, the old network comes back. It
   stops asking 15 seconds before the timer fires.

From another session, `systemctl stop maburvm-net-rollback-<time>.timer` keeps
the new network too. If your first session was cut, run the install command
again: the token is still unused and the new bridge is used as it is. A
cloud-init host also gets
`/etc/cloud/cloud.cfg.d/99-maburvm-disable-network-config.cfg`, so cloud-init
does not rewrite the network at the next boot. **Do not reboot during the
confirmation window**: the timer does not survive a reboot.

## 5. What the agent changes on a host

Installed by the installer:

| Path | What |
|---|---|
| `/usr/local/bin/maburvm-agent` | The agent. |
| `/etc/systemd/system/maburvm-agent.service` | Its unit: `Restart=always`, `KillMode=mixed` (an agent restart does not kill the disk copies it started). |
| `/etc/maburvm-agent.env` | Its settings and credential (mode 0600). |
| `/etc/modules-load.d/maburvm.conf` | Loads `br_netfilter` at boot. |
| `/etc/sysctl.d/99-maburvm-bridge.conf` | `net.bridge.bridge-nf-call-iptables` and `-ip6tables` = 1. |
| `/etc/sysctl.d/99-maburvm.conf` | IP forwarding. |

Kept by the agent while it runs:

| What | Where |
|---|---|
| Its TLS certificate and key (self-signed; the panel pins it on first contact) | `MABURVM_DATA_DIR` (`/var/lib/maburvm`): `agent-cert.pem`, `agent-key.pem` |
| Its migration key, pinned host keys of the other nodes, operation records, VLAN assignments | `/var/lib/maburvm/migration/`, `/var/lib/maburvm/operations/`, `/var/lib/maburvm/vlan-assignments.json` |
| A managed block in root's `authorized_keys` accepting the other nodes' migration keys, each only from that node's address and only for the agent's migration shell | `/root/.ssh/authorized_keys` |
| The SSH wrapper moves use, with strict host key checking | `/usr/bin/maburvm-migration-ssh` |
| iptables and ip6tables chains | `MABURVM-FIREWALL` (VM firewalls), `MABURVM-NAT` (port forwards and NAT VPS), `MABURVM-FIP`, `MABURVM-FIP-FWD`, `MABURVM-FIP-POST` (floating IPs), `MABURVM-VPC-POST`, `MABURVM-ANTISPOOF`, `MABURVM-SCANGUARD`, `MABURVM-SCANCOUNT` (outbound connection limit), `MABURVM-CONSOLE` (closes console ports of VMs started before consoles moved to loopback) |
| libvirt network filters | `maburvm-clean-traffic*`, `maburvm-antispoof-*` |
| A network namespace per private network (VPC) | `maburvm-vpc-*` |
| Speed limits on VM interfaces | tc and libvirt interface bandwidth |
| IPv6 routes and neighbour entries for routed prefixes, and NDP proxy entries | on the bridge |
| Quarantined guests | `/etc/maburvm/quarantine` |
| Downloaded templates, clone copies, scratch space for disks on LVM, customer ISOs | `TEMPLATE_CACHE_DIR` (`/var/lib/libvirt/templates`), customer ISOs in its `customer-iso` directory |
| Agent backups kept by updates from the panel (newest five) | `/root/maburvm-agent.bak-<time>` |
| Transient units for an update | `maburvm-agent-update-<time>` |

The agent checks every five minutes that each VM's firewall, NAT, port
forwards and anti-spoofing rules are still installed and puts back what is
missing; a node that needs this three times in an hour raises an alert. After a
reboot it restores VM network rules and rebuilds VPCs.

**Bridge netfilter.** VM firewalls and the outbound connection limit filter
nothing unless `br_netfilter` is loaded and the two sysctls are 1. Where it is
on, the agent keeps it across reboots. Where it is off (a node set up by hand),
the agent leaves it off and the panel warns: turning it on starts enforcing
rules that never applied to the running VMs. Set
`MABURVM_BRIDGE_NETFILTER=enforce` to have the agent turn it on at start; the
installer does so on a new host. Even then the agent does not turn it on while
the node's `FORWARD` policy is `DROP`.

**Not touched:** the node's own firewall rules outside the chains above, its
SSH configuration, and images you place on it by hand (a Windows template, an
ISO), wherever they are.

## 6. Agent settings reference

The agent reads these from `/etc/maburvm-agent.env` (the unit's
`EnvironmentFile`). Change one with `systemctl edit maburvm-agent` (adding
`Environment=NAME=value`) or by editing the file, then
`systemctl restart maburvm-agent`. Restarting the agent restarts no VM. The
node's page in the panel lists what the agent runs with.

**Connection and security**

| Variable | Default | Meaning |
|---|---|---|
| `AGENT_AUTH_TOKEN` | none | The node's credential from enrollment (or the node's page). Without it every call is refused. |
| `AGENT_BIND_ADDRESS` | `127.0.0.1` (the installer writes `0.0.0.0`) | Address the gRPC server listens on. |
| `AGENT_GRPC_PORT` | `50051` | Its port. Must match the node's address in the panel. |
| `AGENT_ALLOWED_PANEL_IPS` | none | The panel's outgoing addresses, IPs or CIDRs, comma separated. Connections from anywhere else (except the node itself) are closed before TLS. An invalid entry stops the agent from starting. The agent warns at start while it is unset. |
| `AGENT_UPDATE_PUBKEY` | none | An ed25519 public key. The node then installs only agent builds signed with its private key. |
| `AGENT_TLS_CERT_FILE`, `AGENT_TLS_KEY_FILE` | none | Your own certificate instead of the generated one. Changing the certificate changes what the panel pinned (see [runbooks.md](runbooks.md#1-node-down)). |
| `ENVIRONMENT` | `development` | `production` makes the agent refuse to start without `AGENT_AUTH_TOKEN` and its own certificate files. |
| `AGENT_CONSOLE_SHIELD` | on | `off` removes the console port rules. Only for a panel older than the agent. |
| `MABURVM_DATA_DIR` | `/var/lib/maburvm` | Where the agent keeps its certificate and state. |
| `LIBVIRT_URI` | `qemu:///system` | The libvirt connection. |

**Networking**

| Variable | Default | Meaning |
|---|---|---|
| `MABURVM_NAT_ADDRESS` | the default route's source address | The IPv4 address port forwards and NAT VPS ports are published on. Must be an address the node holds and not a floating IP. |
| `MABURVM_BRIDGE_NETFILTER` | unset | `enforce` turns bridge netfilter on at start ([section 5](#5-what-the-agent-changes-on-a-host)). |
| `MABURVM_VLAN_BRIDGES` | none | Bridges that may carry per-VM VLANs, each with its uplink: `br1:bond0,br2:eth3`. A VLAN on any other bridge is refused. |

**Disks, templates and ISOs**

| Variable | Default | Meaning |
|---|---|---|
| `TEMPLATE_CACHE_DIR` | `/var/lib/libvirt/templates` | Where downloaded templates, clone copies and disk scratch space go. Absolute path; put it on a data volume. |
| `TEMPLATE_CACHE_MAX_GB` | no limit | Keeps downloads under this size, least recently used first. A download a VM uses, or used in the last six hours, is never removed. |
| `CUSTOMER_ISO_MAX_GB` | `8` | Largest ISO a customer may attach by URL. |
| `CUSTOMER_ISO_MIN_FREE_GB` | `10` | A customer ISO download is refused, or stopped, rather than leave less than this free. |
| `CUSTOMER_ISO_RETENTION_DAYS` | `7` | A customer ISO no VM uses is removed after this long. |

**Object storage for images** (and for backups made before backup
destinations were set in the panel). Each name is read as `STORAGE_<name>`
first, then `S3_<name>`.

| Variable | Meaning |
|---|---|
| `STORAGE_ENDPOINT` / `S3_ENDPOINT` | Host and port, without a scheme. |
| `STORAGE_BUCKET` / `S3_BUCKET` | Bucket. |
| `STORAGE_REGION` / `S3_REGION` | Region. |
| `STORAGE_ACCESS_KEY` / `S3_ACCESS_KEY`, `STORAGE_SECRET_KEY` / `S3_SECRET_KEY` | Credentials. |
| `STORAGE_FORCE_HTTP` / `S3_FORCE_HTTP` | `true` for plain HTTP. |
| `STORAGE_USE_PATH_STYLE` / `S3_USE_PATH_STYLE` | `true` for path-style addressing (MinIO). |

Backups of VMs go to the destination set in the panel, which it sends with
every backup; they do not need these. The node page also lists
`MABURVM_MANAGED_DIR_ROOTS`, `MABURVM_MANAGED_LVM_VGS` and
`MABURVM_MANAGED_ZFS_DATASETS`; they have no effect in this release.

The agent does not need the panel's database or object storage settings
(`DB_*`) to start.

## 7. Updating an agent

From the panel: Settings, System, Version, **Update** next to the node
([admin guide §3.5](admin-guide.md#35-updating-agents)). By hand, with the
agent built into the panel image:

```bash
# On the panel host
docker compose -f docker-compose.production.yml --env-file .env.production \
  cp panel:/app/bin/linux/agent-amd64 ./agent-amd64
sha256sum agent-amd64

# Copy it to the node, then on the node
/tmp/agent-amd64 --version          # must print the panel's commit
cp /usr/local/bin/maburvm-agent /root/maburvm-agent.bak-$(date +%Y%m%d%H%M%S)
install -m 0755 /tmp/agent-amd64 /usr/local/bin/maburvm-agent
systemctl restart maburvm-agent
journalctl -u maburvm-agent -n 50
```

To go back, `install -m 0755` the backup over the binary and restart again.

## 8. After installing

1. In the panel, add a **storage pool** for the pool directory (Storage, Add
   pool) and mark it primary.
2. Add an **IP pool** for the node with its bridge and gateway (IP Pools).
3. Open the node's page and read the **host checks**. The same checks run on
   the node with `maburvm-agent --preflight [--pool DIR] [--bridge NAME]
   [--json]`, which exits 1 when one fails.
4. Create a throwaway VM, open its console, check its address answers, delete
   it.
5. Before putting customers on it: back up the throwaway VM and restore it, and
   if you have several nodes, move it to another node and back.

Optional, on the node:

- `net.ipv6.conf.<bridge>.proxy_ndp=1` when the upstream router treats your
  IPv6 pool as on-link instead of routing it to the node.
- `AGENT_ALLOWED_PANEL_IPS` and `AGENT_UPDATE_PUBKEY`, if you did not pass them
  to the installer.
