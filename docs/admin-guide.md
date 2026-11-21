# Administrator guide

How to run a MaburVM panel day to day: nodes, storage, networking, plans,
customers and staff, backups, moves, alerts, billing and security. Installing
the panel itself, TLS and the panel's own backup are in
[DEPLOYMENT.md](DEPLOYMENT.md); preparing a hypervisor is in
[node-setup.md](node-setup.md). When something breaks, see
[runbooks.md](runbooks.md). The HTTP API is described in [api.md](api.md).

Menu names below are as the panel shows them. A section that needs a staff
permission names it in `code`, as listed on the Staff roles page.

## Contents

1. [Concepts](#1-concepts)
2. [First steps](#2-first-steps)
3. [Nodes](#3-nodes)
4. [Storage pools](#4-storage-pools)
5. [Networking](#5-networking)
6. [Templates and ISOs](#6-templates-and-isos)
7. [Plans and allowances](#7-plans-and-allowances)
8. [Customers and staff](#8-customers-and-staff)
9. [Backups and snapshots](#9-backups-and-snapshots)
10. [Moving VMs and draining nodes](#10-moving-vms-and-draining-nodes)
11. [Alerts, email and notifications](#11-alerts-email-and-notifications)
12. [Billing integration](#12-billing-integration)
13. [Security settings](#13-security-settings)
14. [Importing from another panel](#14-importing-from-another-panel)
15. [Where each setting lives](#15-where-each-setting-lives)

## 1. Concepts

| Term | What it is |
|---|---|
| **Panel** | The Go API server, its PostgreSQL database and its job queue. It holds every record and decides everything. |
| **Web app** | The Next.js front end. It is the only part published to the internet; it passes `/api`, `/ws`, `/webhooks` and `/install-agent.sh` to the panel. |
| **Node** | A KVM hypervisor running the **agent**. The panel calls the agent over gRPC with TLS (port 50051 by default); the agent never calls the panel. Only the agent touches libvirt and QEMU. |
| **Region** | A named location holding one or more nodes. Customers pick a region when ordering; regions with no room are hidden. |
| **Storage pool** | Where a node keeps VM disks: a directory, an LVM volume group or an LVM thin pool. |
| **IP pool** | A range of addresses (IPv4 or IPv6) on one or more nodes, attached to a bridge. VMs take addresses from pools of their node. |
| **Template** | An operating system image VMs are installed from (Linux with cloud-init, MikroTik RouterOS, or Windows). |
| **Plan** | A size customers order: CPU, memory, disk, speed, monthly transfer, limits, price and allowances. |
| **Customer** | An account with the `client` role. It sees only its own resources, in the client area (`/client`). |
| **Staff** | An account with the `admin` role and a staff role that decides what it may do. The founding administrator is the **Owner**. |
| **Job** | Work that drives a node (create, power, backup, network change) runs in the background job queue and is retried a bounded number of times. The API answers at once; the VM's page shows progress. |
| **Alert** | Something the panel noticed and keeps open until it is resolved: a node offline, a pool filling, a failed backup. |

VMs keep running while the panel is down. Nothing is scheduled, collected,
billed or alerted in that time, and the panel cannot report its own death:
watch it from outside (see [Alerts](#11-alerts-email-and-notifications)).

## 2. First steps

A new panel shows **Setup guide** in the menu with the steps that make it
usable, each ticked when it is done:

1. **Connect a node** ([section 3](#3-nodes)).
2. **Add an IP range** ([section 5.1](#51-ip-pools)).
3. **Add an operating system** ([section 6](#6-templates-and-isos)).
4. **Create a plan** ([section 7](#7-plans-and-allowances)).
5. **Set up outgoing email** (Settings, System, Email). Password resets and
   welcome emails need it.
6. **Put your name on it** (Settings, Branding): name, logo, colour and the
   sign-in page picture.
7. **Turn on two-step sign-in** for your own account (Profile).

Then add a storage pool for each node ([section 4](#4-storage-pools)), set up
alert delivery ([section 11](#11-alerts-email-and-notifications)), choose
where VM backups go ([section 9](#9-backups-and-snapshots)) and configure the
panel's own nightly backup ([DEPLOYMENT.md §9](DEPLOYMENT.md#9-backups--restore)).

## 3. Nodes

**Nodes** (`node:read` to see; `node:create`, `node:update`, `node:delete` to
change).

### 3.1 Adding a node

Add node asks for a name and the address the panel reaches the node on, then
shows a single-use install command (valid one hour, shown once). Run it as
root on a **new** machine. The installer, its options and what it changes are
described in [node-setup.md](node-setup.md). The node's page can make a new
command while no agent has answered for the node; the old one then stops
working. The panel refuses an install command for a node an agent has ever
answered for, so a token cannot take over a running node.

### 3.2 Host checks

A node's page has **Host checks** (the agent's preflight): hardware
virtualization and `/dev/kvm`, libvirt and QEMU versions, `qemu-img`, every
bridge the node's IP pools name, every directory storage pool (exists,
writable, free space, root filesystem or not), bridge netfilter, IP
forwarding, the tools the agent shells out to, and clock sync. Each line is
pass, warning or failed with how to fix it. **Check now** asks again; it reads
the host and changes nothing.

### 3.3 The node page

- **Pause new VMs** (`creation_paused`): no new VM is placed on the node;
  existing VMs are untouched. A drain pauses the node by itself.
- **Status**: `active`, `maintenance` (no node alerts are raised) or
  `offline`. The panel sets `offline` and `active` itself from its checks.
- **vCPU ratio**: how many vCPUs the node may carry per CPU thread. Overrides
  the panel-wide ratio ([section 4.3](#43-what-a-node-and-pool-accept)).
- **NAT ports**: the public port range for NAT VMs ([section 5.6](#56-port-forwards-and-nat-vps)).
- **Agent settings**: what the agent runs with, read from the node (secrets
  only as set or not set). Change them on the node; see
  [node-setup.md §6](node-setup.md#6-agent-settings-reference).
- **Agent token**: regenerate or reveal it (`node:update`). A new token must
  be put in the node's `/etc/maburvm-agent.env` (`AGENT_AUTH_TOKEN`) and the
  agent restarted, or the node goes offline.
- **Import**: adopt libvirt guests already on the node, with their real CPU,
  memory and disk ([section 14](#14-importing-from-another-panel)).

### 3.4 Node health

The panel checks every node once a minute, each under its own time limit. A
node that misses the set number of checks in a row (three by default; Alerts
page, "Missed checks before a node is offline") is marked offline: no VM is placed on it, and its
VMs show "State unknown" to staff and owners. The first answered check marks
it active again. A node whose port answers but whose agent sends no figures
raises "Node not reporting". See [runbooks.md, Node down](runbooks.md#1-node-down).

### 3.5 Updating agents

Settings, System, Version lists each node's agent build beside the panel's and
offers **Update** for a node on another build, one node at a time. The node
verifies the binary (size, SHA-256, and the signature when
`AGENT_UPDATE_PUBKEY` is set on it), test-runs it, swaps it in and rolls back
by itself if the new agent does not stay up. Restarting the agent restarts no
VM. Details and the supervised first run: [DEPLOYMENT.md §6](DEPLOYMENT.md#6-agent-deployment-separate-privileged).

### 3.6 Removing a node

Delete asks what to do with the node's VMs, backups and networks
(`delete_vms`, `delete_backups`, `delete_networks`). Move the VMs off first
([section 10](#10-moving-vms-and-draining-nodes)). Deleting a node also removes
its migration key from every other node that is online.

## 4. Storage pools

**Storage** (`node:read` to see; `storage:create`, `storage:update`,
`storage:delete` to change).

### 4.1 Kinds of pool

| Kind | Path | Disks | Notes |
|---|---|---|---|
| Directory | a directory on the node | qcow2 files, thin | ZFS datasets, NFS exports and CephFS work by mounting them on the node and registering the mount point. |
| LVM volume group | the volume group | raw logical volumes | Each disk takes its full size from the group when it is created. |
| LVM thin pool | `vg/thinpool` | thin logical volumes | Disks take space as they are written; watch data **and metadata** use (the Storage page shows both). A full metadata area stops every thin volume. |

Whatever you name must already exist on the node, and a volume group or thin
pool must not hold the node's own system; the node reports why it cannot use a
pool on the Storage page. Capacity and free space are measured by the node
every minute, never typed in. The pool marked **primary** is where that node
creates new disks; a VM's extra disks go beside its own.

VMs on LVM cannot take snapshots, always get full backups, and cannot move to
another node. A backup or restore of a disk on a block device needs scratch
space as a file; it is kept under the agent's `TEMPLATE_CACHE_DIR`, so put that
on a data volume on such nodes.

An agent of this version refuses to create a disk in a directory pool whose
directory does not exist on the node; it never falls back to the root
filesystem.

### 4.2 Pool alerts

Each pool has an alert level (`alert_threshold`, 90% by default) that raises a
warning, and the Alerts page has a panel-wide critical level
(`storage_critical_percent`). A pool that fills **pauses every VM on it**, and
disks are thin, so a pool fills later without any new order. See
[runbooks.md, Pool full](runbooks.md#5-storage-pool-full).

### 4.3 What a node and pool accept

Settings, System, Quotas, **Node overcommit**. All three are off (0) by
default, because live nodes already carry more than any ratio you might pick
and a ratio below what a node holds refuses every new order there. Read the
Storage page and each node's page first and set each ratio above the highest
figure shown.

| Setting | Meaning |
|---|---|
| RAM overcommit ratio (`ramOvercommitRatio`) | Allocated VM memory may reach node memory times this. A host out of memory does not slow down: the kernel kills a running VM. |
| Disk overcommit ratio (`diskAllocationRatio`) | Disk promised on a pool may reach capacity times this. |
| vCPU overcommit ratio (`vcpuAllocationRatio`) | vCPUs per CPU thread. A node's own ratio wins. |

Whatever the ratios, a new or grown disk must fit in the pool's real free
space with 20 GB to spare. Disks of machines the panel does not manage are not
counted.

## 5. Networking

### 5.1 IP pools

**IP Pools** (`network:read`, `network:update`). A pool has a family, a CIDR,
gateway, bridge, an optional range and the node or nodes it serves.

- **Generate** fills a pool with address rows from its range; **Allocate**
  reserves one; **Release** frees one.
- **Orderable**: off by default, so a pool kept for infrastructure is never
  handed to customers. Turn it on for pools customers may draw floating IPs
  or extra addresses from.
- **NAT pool**: private addresses masqueraded behind the node, only for NAT
  VMs ([section 5.6](#56-port-forwards-and-nat-vps)).
- The panel ARP-probes addresses before handing them out and adopts addresses
  live VMs already hold, so imported guests are not double-assigned. Nodes
  need `arping` (package `iputils-arping`) for this to see addresses outside
  the node's own subnets.
- **Reverse DNS**: set per address, or by customers on their own addresses.
  With a nameserver connected (Settings, System, API, DNS provider: the
  nameserver's HTTP API address, server ID and key), records are pushed there; **Reverse zone** exports a zone
  file for a pool, and **Import** reads existing records back.

Changing a VM's address no longer needs a reinstall: the guest is reconfigured
live through its guest agent where it can be, otherwise at its next start from
the panel, and the VM's page says which.

### 5.2 IPv6

Two kinds of IPv6 pool:

- **Per address**: like IPv4, an address on a subnet shared with other VMs;
  the subnet's own router is the gateway.
- **Routed prefix** (`delegated_prefix_len`): each VM gets its own prefix out
  of the pool, for example a /64 out of a /48. The guest uses `<prefix>::1`
  and the gateway `fe80::1`, which the node holds on the bridge. The first
  prefix of the pool is the router-to-node link unless a **link prefix** is
  set. The upstream router must route the pool to the node. Where it treats
  the whole pool as on-link instead, the agent adds an NDP proxy entry for each
  guest's `::1`, which works only once you set
  `net.ipv6.conf.<bridge>.proxy_ndp=1` on the node.

Staff add or release a VM's IPv6 on its Network tab. A plan's
`max_extra_ipv6` caps how many a VM may hold.

### 5.3 Address history

Every assignment and release of an address or prefix is recorded by the
database itself. **Abuse**, "Who held an address", answers an abuse report:
give the address and, optionally, the moment from the report, and it lists
every VM that held it with its owner at the time. Holdings that existed before
the history began are marked as such. See
[runbooks.md, Abuse report](runbooks.md#6-abuse-report-about-an-address).

### 5.4 Private networks (VPCs) and managed networks

Customers create **private networks** in their area (`vpc:*` for staff). Each
lives on one node and so in one region; a VM joins one only on that node. Each
has its own router network namespace on the node, so two customers may both
use `10.0.0.0/24`; one customer's subnets must not overlap within a region.
How many one account may hold: Settings, System, Quotas (default 5). The agent
rebuilds VPCs after a node reboot and starts the guests on them that are set
to autostart.

**Networks** (`network:create`, `network:delete`) are staff-made networks,
including per-VM VLANs. A VLAN works only on bridges named in the agent's
`MABURVM_VLAN_BRIDGES` (`bridge:uplink` pairs); switching VLAN filtering on
changes how the whole bridge forwards, so it is never done implicitly.

### 5.5 Floating IPs

**Floating IPs** (`floating_ip:read`, `floating_ip:update`,
`floating_ip:delete`). A floating IP is a public address configured on the
host and forwarded to a VM's own address, so it moves between VMs of the same
node without touching either guest. It is kept when its VM is deleted.

- It stays within one node's address block (nodes sit on different networks),
  and its pool must have a bridge.
- **Inbound** forwards traffic to the VM, which still sends from its own
  address. **Full** also makes the VM send as the floating IP; only for a VM
  on a private address.
- Customers order them in their area, up to the per-account limit (Settings,
  System, Quotas; default 3). One attached address per VM is free; the rest are
  reported as billable in the `floating_ip.usage` event
  ([section 12](#12-billing-integration)).
- Staff allocate one for an account with `network:update`.

### 5.6 Port forwards and NAT VPS

A port forward publishes a node port on **one** node address: the address of
its default route, or `MABURVM_NAT_ADDRESS` in the agent's environment. A port
is held by one VM per node and protocol. A VM may hold 20 forwards unless
Settings, System, Quotas says otherwise.

A **NAT VPS** is a VM on a plan whose network type is NAT. It gets a private
address from a NAT pool on its node, a block of public ports on the publish
address, and its first port forwarded to SSH (Remote Desktop on Windows). Its
owner forwards more ports inside the block only, and may route website
hostnames to it. To offer it on a node: give a bridge the gateway of a private
range, create a NAT pool for it, set the node's **NAT ports** range, and create
a plan with network type NAT, ports per VM and hostnames per VM. The steps and
the hostname router are in [DEPLOYMENT.md §6.4](DEPLOYMENT.md#64-firewall-groups-and-nat-vps).
NAT VMs cannot move to another node.

### 5.7 Firewall

Each VM has inbound rules (Firewall tab) and a default policy for traffic no
rule matches (`accept` or `drop`). New VMs get built-in rules that admit SSH or
Remote Desktop, ping, 80 and 443. A VM may hold 100 rules unless Settings,
System, Quotas says otherwise.

**Firewall groups** (`firewall_group:read`, `firewall_group:update`) are rule
sets attached to many VMs, placed before a VM's own rules (which cannot
override them) or after (which can). Staff make groups for everyone; customers
make their own. A plan can name a group that VMs created on it get instead of
the built-in rules.

**Firewalls need bridge netfilter on the node.** Without `br_netfilter` and
`net.bridge.bridge-nf-call-iptables=1`, rules are installed and filter
nothing. The panel warns on the node page, on each VM's Firewall tab and in the
Security page while a node is not enforcing. See
[node-setup.md §5](node-setup.md#5-what-the-agent-changes-on-a-host).

### 5.8 Outbound mail

A plan's mail policy (`smtp_policy`: allowed or blocked) decides whether new
VMs on it may send on port 25; staff block or allow it per VM on the Firewall
tab. The block sits above every firewall rule. Existing VMs keep what they
had when a plan changes.

### 5.9 Abuse, quarantine and reputation

- **Abuse** ranks guests by their rate of new outbound connections, the
  signature of a compromised VM. **Quarantine** cuts a guest's traffic while
  leaving it running (`network:update`). The node also limits each guest's new
  outbound connections per second.
- **IP Reputation** checks your address space against public blocklists. An
  AbuseIPDB key (Settings, System, API) adds a confidence score; optional.
- Anti-spoofing (per interface, Network tab) stops a guest sending from an
  address it was not given.

### 5.10 Speed and transfer

Each interface can carry a speed limit (both directions). A plan's monthly
transfer quota and what happens past it, throttle, stop or bill the overage,
are measured always and **enforced** only when Settings, System, Quotas,
"Restrict VMs that use up their quota" is on (its starting value is
`BANDWIDTH_ENFORCE`). Owners are emailed at the warning levels set on the same
page. Before turning enforcement on, check the quotas your plans carry.

## 6. Templates and ISOs

**Templates** (`template:update` to change). A template has a system type
(Linux, RouterOS or Windows) that decides how a VM built from it is set up;
**Sync** copies it to nodes. A template can be limited to chosen plans, or
marked as needing a paid licence. RouterOS and Windows notes, and how to build
a Windows image on a node, are in [DEPLOYMENT.md §12](DEPLOYMENT.md#12-templates-that-are-not-linux).

**ISOs** lists install media staff define. Customers may also attach an ISO by
URL: the node fetches only public addresses, bounded by the agent settings
`CUSTOMER_ISO_MAX_GB`, `CUSTOMER_ISO_MIN_FREE_GB` and
`CUSTOMER_ISO_RETENTION_DAYS`.

**Rescue mode** boots a stopped VM from a rescue ISO, set under Settings,
System, Rescue (or `RESCUE_ISO_URL` in the panel's environment until one is
saved). With neither, customers are told rescue is not available.

## 7. Plans and allowances

**Plans** (`plan:update` to change; every signed-in account can list them).

| Field | Meaning |
|---|---|
| CPU, memory, disk | The VM's size. |
| Speed (`bandwidth_mbps`) | Interface speed cap; 0 is none. |
| Monthly transfer (`data_quota_gb`) and policy (`over_quota_policy`) | `throttle` (to `throttle_speed_mbps`), `suspend` (stop until the month resets) or `overage` (keep going and send `bandwidth.overage`). Enforced only as [section 5.10](#510-speed-and-transfer) says. |
| Limits | CPU cap and weight, disk IOPS and MB/s. |
| Mail policy (`smtp_policy`) | Port 25 allowed or blocked for new VMs. |
| Price (`price_monthly`, `price_hourly`, `currency`) | Shown to customers; nothing is charged by the panel. |
| Allowances (`max_snapshots`, `max_backups`, `max_extra_ipv4`, `max_extra_ipv6`) | Per VM. Empty is no limit, 0 is none. Read from the plan at each use, so a change reaches every VM on it at once. Lowering `max_backups` removes the oldest completed scheduled backups above it at the next hourly pass; manual backups are never removed, nor a backup a kept incremental one builds on. |
| Network type (`network_type`), `nat_ports`, `nat_hostnames` | Public or NAT. |
| Default firewall group | What new VMs get instead of the built-in rules. |
| Active | Inactive plans are not offered. |

Size, limits, speed, quota and mail policy reach existing VMs only through
**Apply to VMs** on the plan, which previews then applies; running VMs keep
their size until they are stopped. An account's own limits (servers, vCPU,
memory, disk) are set on its user page; new customer accounts start from
Settings, System, Quotas, "Starting quota for new customer accounts".

What customers may do without the billing system (order servers, clone, add
disks, delete billed servers) is switched under Settings, System, Quotas.

## 8. Customers and staff

### 8.1 Customer accounts

**Users** (`user:read`, `user:create`, `user:update`, `user:delete`,
`user:recover`). From a customer's page staff can:

- **Suspend** the account, optionally with its VMs: it cannot sign in, and its
  sessions and API keys stop at once. Unsuspend restores it (and, with VMs,
  lifts the suspension from every VM of the account).
- **Reset password** by email link, or set a temporary one shown once;
  **reset two-factor**; **unlock** after failed sign-ins; see and end
  **sessions**; read the **sign-in history** (kept 180 days).
- Set the account's **quota**.
- **Delete** it: an account that owns VMs needs a choice, give its VMs (and
  floating IPs) to another account or delete them with it.

A single VM is suspended or unsuspended from its page (`vm:lifecycle`);
customers cannot undo it.

### 8.2 Staff roles

Every staff account has a role, shown on **Staff roles**:

| Role | Can |
|---|---|
| Owner | Everything, and the only one who creates or deletes staff accounts. Always the founding administrator (the oldest permanent staff account). |
| Administrator | Everything except managing staff (`admin:access`). |
| Support | Sees everything except settings and billing; powers VMs, opens consoles and rescue; recovers customer accounts. |
| Billing | Customers, plans and billing deliveries. |
| Read-only | Sees everything, changes nothing; stored credentials stay hidden. |
| Custom | Any set of permissions from the catalog. |

Only the Owner, or a role holding `staff:manage`, changes roles, and nobody
hands out a permission they do not hold. A few actions need `admin:access`
because they are not in the catalog: restore a backup as a new VM, data
export, support access accounts, and ordering a floating IP as staff. A staff
API key acts with its owner's role as it is at each call; MCP calls go through
the same checks. Another staff member's account, sessions and sign-in history
are the Owner's to manage.

Staff accounts that existed before roles were introduced are Administrators.
Rolling the panel back to a version without roles gives every staff account
full access again; see [upgrading.md](upgrading.md#what-a-rollback-does-not-undo).

### 8.3 Viewing as a customer

Staff with `user:impersonate` open a customer's client area for up to an hour
from the customer's page, in their own browser session only, and can do there
what the customer can. A banner says so; every action is recorded with both
accounts; the customer's password, email, two-factor, sessions, API keys and
initial passwords stay out of reach. **End** on the banner returns to the staff
session.
A staff password change or "sign out everywhere" ends open views too.

### 8.4 Support access

Security, **Temporary support access** (Owner to open, `admin:access` to see
and close)
creates a temporary staff account for an outside helper, with the Support role
unless another is chosen. Its password is shown once and it expires by itself.

## 9. Backups and snapshots

### 9.1 Destinations

Settings, System, **Backup** (`settings:update`). VM backups go to the default
destination; each backup remembers where it was made, so changing the default
keeps older ones restorable. A destination is:

- an **S3-compatible bucket**;
- an **SFTP server**, with its host key pinned (paste the `ssh-keyscan` line or
  use **Read from server** and compare the fingerprint);
- a **directory on the nodes**: an NFS or CIFS share, or a disk of its own,
  mounted at the same path on each node. A node refuses a directory on its root
  filesystem and stops writing when the mount is gone.

An S3 bucket or SFTP server must be reachable from every node and from the
panel. **Test destination** writes, reads and deletes a small file from the
panel and each chosen node. Do not use the panel stack's own MinIO: nodes
cannot reach it, and it dies with the panel host. Secrets are stored encrypted.

The same tab sets the **backup window** (scheduled backups due outside it wait,
then run once; manual backups are never held) and how many backups run at once
on one node (default 2).

### 9.2 Schedules, incremental backups and retention

Schedules are per VM (Backups tab): how often, and how many or how long to
keep. A VM can have several. Customers set their own, within their plan's
`max_backups`.

Staff can set **Full backup every N days**: in between, a running VM on a node
with libvirt 7.2+ and QEMU 6.0+, whose disks are all qcow2 files, copies only
what changed since the previous backup. Anything else, or a broken chain, gets
a full backup and the backup says why: an older node, a disk on LVM, a VM that
was off, a lost checkpoint, disks replaced by a restore, reinstall, snapshot
revert or move, a changed destination, or a chain at its longest (40). Manual
backups are always full. Retention and allowances never delete a backup that a
kept incremental one builds on, and such a backup cannot be deleted by hand
before the newer ones.

The **Backups** page lists every VM with its last good backup, last result,
schedule and next run, failing and stale ones first.

### 9.3 Restore

- **Restore** (VM's Backups tab, `backup:update`) writes the backup over the
  VM's disks on its node; the VM must be stopped. An incremental backup is downloaded with every backup
  it builds on.
- **Restore as new VM** (`admin:access`) creates a new VM from a backup on any
  active node that passes the destination test, for when the original node is
  gone. It keeps the owner, size, limits and firewall rules and takes a new
  address. The original VM and the backup are not changed. A backup kept in a
  node's own storage (made before destinations existed) can be restored only on
  a node whose environment reaches that storage.

Prove each destination once per node with a throwaway VM: back up, restore in
place, restore as new on another node.

### 9.4 Snapshots and images

**Snapshots** are kept by libvirt next to the VM's disk and revert it in
place; they are not backups. VMs on LVM cannot take them, and a VM with
snapshots cannot move. **Images** capture a VM's disk to object storage; an
image outlives its VM and can seed a new one. Captured images use the object
storage in each node's own environment (`STORAGE_*` or `S3_*`).

## 10. Moving VMs and draining nodes

Staff with `vm:migrate` move a VM from its Settings tab; `node:drain` empties a
node from its page. Both need the current agent on both nodes. A running VM
moves live (a pause of about a second at the end), a stopped one offline. The
VM stays on its node until it runs on the new one; a failed or cancelled move
leaves it where it was. **Check** shows before anything starts why a VM cannot
move: floating IPs or a private network, snapshots, an ISO, rescue mode, a
running backup, a pool or port the destination lacks, a NAT VM, a disk on LVM,
no room, or a different CPU for a live move of a `host-passthrough` VM (move it
offline instead).

A **drain** pauses the node for new VMs, plans a destination for every VM,
moves them one to three at a time with two tries each, and reports what moved
and what stayed. Stop drain cancels the moves under way. The node stays paused
until you resume it.

The first move between two nodes sets up the **migration link** by itself:
each agent's own key, accepted on the other nodes only from that node's
address and only for the agent's migration shell, with pinned host keys. Live
moves send memory and disks **unencrypted** on TCP 49152-49215 between nodes:
keep that traffic on a private network or move VMs offline. Requirements:
[DEPLOYMENT.md §6.3](DEPLOYMENT.md#63-moving-vms-between-nodes-and-draining-a-node).
When a move fails: [runbooks.md](runbooks.md#4-failed-move-to-another-node).

## 11. Alerts, email and notifications

### 11.1 Alerts

**Alerts** (`ops:read` to see, `ops:update` to close). Until delivery is set up
there, alerts are only listed.

| Alert | Raised when |
|---|---|
| Node offline | the agent port stops answering for the set number of checks |
| Node not reporting | the port answers but the agent sends no figures |
| Storage pool | a pool passes its alert level (warning) or the critical level |
| Node not filtering guest traffic | a node reports bridge netfilter off |
| Backup failed | a backup fails with no retry left |
| Job failed | any other background job fails for good |
| Node keeps losing VM network rules | the agent had to put VM firewall, anti-spoofing or NAT rules back three times within an hour |

Delivery is by email (to the recipients listed, through the SMTP server) and by
webhook in JSON, Slack or Discord format, optionally signed. An alert is sent
when it opens and when it closes; flapping inside the settle time (ten minutes
by default) is one alert. The JSON body and signature are in
[api.md](api.md#outbound-alerts). The panel also serves Prometheus
metrics at `/metrics` with a metrics token (Settings, System, Panel, or
`METRICS_TOKEN`); rules for node, queue, backup and pool alerts are in
[DEPLOYMENT.md §14](DEPLOYMENT.md#14-alerts-and-node-monitoring).

Keep one check of the panel from outside: the panel cannot report that it is
down, and mail sent through a server on a dead node never arrives.

### 11.2 Email

Settings, System, **Email** holds the SMTP server and the **email log**: every
email to a customer is queued, retried while the mail server refuses or is
down, and listed for 90 days without its body; a failed one can be sent again.
Settings, **Emails** rewords or switches off each template: welcome, welcome
(billing order), password reset, server ready, reinstalled, rescue on and off,
transfer warning, suspended, unsuspended, backup failed, password changed,
two-factor turned off, API key created, and the invitation for imported
accounts. Nothing is sent until a mail server is set up.

### 11.3 Notifications

Customers see the same events under Notifications in their area, with a read
state per account. Staff with `audit:read` see the newest audit log rows in
their bell instead.

### 11.4 Audit log and job queue

**Audit log** (`audit:read`) records every write by staff, customers, API keys,
MCP and the billing webhook, with who, from where, the route, the outcome and
the redacted body. **Job queue** (`ops:read`, `ops:update`) shows queued,
running and failed jobs with retry and cancel, and the workers per queue
(applied at the next panel restart).

## 12. Billing integration

The panel charges nothing. A billing system drives it through the signed
**billing webhook**, and the panel sends events back.

**Inbound**: `POST /webhooks/billing`, with the billing API key in `X-API-Key`
and an HMAC-SHA256 signature of the raw body in `X-Webhook-Signature`. Set the
key and secret under Settings, System, Panel, Billing webhook (or
`BILLING_API_KEY` and `BILLING_WEBHOOK_SECRET`); the webhook answers 503 until
both are set. Each signed request is accepted once and must carry a
`timestamp` within 5 minutes of the panel's clock. Events create, resize,
suspend and remove VMs, ensure and suspend accounts, report usage and status,
list the catalog and hand out single sign-on links; the full list is in the API
reference and at `GET /webhooks/billing/docs`.

**Outbound**: Settings, System, API, **Webhook URL** receives `vm.created`,
`vm.failed`, `vm.suspended`, `vm.unsuspended`, `vm.destroyed`,
`bandwidth.overage` and `floating_ip.usage`, signed with the HMAC secret saved
there (or the billing webhook secret when none is). Each event is stored,
retried up to 10 times over about four hours, and listed with its outcome on
the same page (`billing:read`, `billing:update` to resend). A delivery that
fails for good raises an alert.

**WHMCS**: `integrations/whmcs` holds a provisioning module (orders, suspends,
resizes, removes VMs, usage, power buttons, and **Open control panel** single
sign-on through the `user.sso` event: a one-time link valid 60 seconds,
customer accounts only, the panel's own two-factor still applies) and an
optional events addon (bills floating IPs, records failed installs and
overage). Installation and a first-run checklist:
[integrations/whmcs/README.md](../integrations/whmcs/README.md).

Things to set before taking orders:

- Settings, System, Panel, **panel address** (`PANEL_PUBLIC_URL`): used in
  welcome emails and single sign-on links.
- A default region with room, for orders that name none.
- Settings, System, Quotas, what customers may do on their own, and whether
  they may delete a server billing manages (off by default).
- The quotas and over-quota policies on your plans.

## 13. Security settings

| What | Where | Notes |
|---|---|---|
| Password rules | Settings, System, Security | Minimum length (8 to 128) and required character classes, for passwords set from then on. |
| Account lockout | Same | "Failed sign-ins before an account locks" (0 never) and "Locked for (minutes)". Staff unlock from the user page; a password reset unlocks too. |
| Session length | Same | Hours a sign-in lasts (1 to 720), for sign-ins from then on. |
| Two-factor for staff | Same, "Require two-factor authentication for administrators" | A staff account without it reaches only its profile until it turns it on. Customers and API keys are not affected. Each person turns two-factor on under Profile and gets one-time backup codes. |
| Sign-in check (captcha) | Same | Cloudflare Turnstile or hCaptcha, site key and secret key, and after how many failures from one address or for one email in 15 minutes it is asked (0: always). The panel must reach the provider; while it cannot, those attempts wait. |
| Sessions | Profile; user pages | Each sign-in is a session its owner and staff can end. "Sign out everywhere else" keeps only the current one. |
| IP whitelist | User page | An account may sign in only from the listed addresses or ranges, whatever the method. |
| API keys | Settings, API Keys; Settings, System, API | Keys (`mvk_...`) act as their owner. **Read-only** keys may only read; **own resources only** makes a staff key act as a customer. "Accept API keys" switches them all off. |
| API rate limit | Settings, System, API | Requests per minute per signed-in account or key (default 1200, 0 off); past it, 429 with `Retry-After`. Sign-in, password reset, single sign-on, first-run and node enrollment are limited per address on their own. |
| Single sign-on | Settings, System, Panel | Any OpenID Connect provider (`OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_BUTTON_LABEL`). Matches an existing account by verified email; creates nobody. Two-factor and allow-lists still apply. |
| Agent allow-list | Node's environment | `AGENT_ALLOWED_PANEL_IPS`: the agent closes connections from anywhere else before TLS. |
| Agent update signing | Node's environment | `AGENT_UPDATE_PUBKEY`: the node installs only agent builds signed with the matching key, kept off the panel host. |
| Trusted proxies | Panel's environment | `TRUSTED_PROXIES`: whose `X-Forwarded-For` is believed for rate limits, allow-lists and the audit log. |

The **Security** page (`settings:read`) checks the install against these and
lists what is weak or off. Stored secrets (agent tokens, console passwords,
mail and DNS credentials, billing secrets, captcha secret, 2FA secrets) are
encrypted with the AES key in the panel's `secrets.json`; keep that file with
the database backups ([DEPLOYMENT.md §9](DEPLOYMENT.md#9-backups--restore)).

## 14. Importing from another panel

Settings, **Import** (`import:manage`) reads customers, plans, IP pools and VMs
from another panel's API, plans the import, applies the chosen items and
invites the imported customers by email. A node's page can also adopt the
libvirt guests already on it (`vm:import`), and preview and import the VMs of a
Virtualizor install on that node. Imported VMs keep running; the panel adopts
their real CPU, memory and disk on each reconcile.

The Import page walks through four steps. Add the nodes the source's VMs run on
before you start: VMs are adopted where they run, and the read also looks at
those nodes.

1. **Connect.** Add the source panel's address (its admin API, port 4085 by
   default) and its admin API key and pass. They are stored encrypted and only
   read from. For a self-signed certificate, paste the SHA-256 fingerprint that
   **Test connection** shows. Changing a source to another host or scheme needs the key and
   pass entered again.
2. **Choose what comes over.** **Read the source** plans the import without
   writing anything. Map each source server to a node (matched by address, then
   by name, or left out), map source plans to panel plans or let them be
   created, pick a template to record for systems no template name matches, and
   paste billing references (one line per VM: source VM ID, then its billing
   service; a bare number is a WHMCS service ID). **Save choices**, then include
   or leave out items. Items that are already imported, or that cannot be
   imported, say why and are not offered: for example an address another VM, a
   customer's floating IP or another source VM already holds.
3. **Import.** Applies the chosen items in parts, and **Stop after this part**
   stops between them. A chosen VM brings its owner. Customer accounts are
   created with no usable password and nobody is emailed. Running it again
   skips what is already imported.
4. **After the import.** **Download CSV** lists each imported VM's new ID and
   its owner's account ID for the billing system. **Send invitations** emails
   each account the import created, once, with a link to choose a password that
   works for 7 days (the "Account moved here" template under Settings,
   **Emails**).

## 15. Where each setting lives

Most settings are on pages and apply at once. A few must exist before the
database can be read and stay in the panel's environment (`.env.production`):
the database connection, `ALLOWED_ORIGINS`, `TRUSTED_PROXIES` and the keys in
`secrets.json`. Settings, System, **Panel** holds the panel address, billing
webhook key and secret, metrics token and single sign-on, each falling back to
its environment variable while nothing is saved there and showing where the
value in force comes from; only the Owner or a role with `staff:manage` changes
them. Agent settings live in each node's `/etc/maburvm-agent.env`
([node-setup.md §6](node-setup.md#6-agent-settings-reference)).
