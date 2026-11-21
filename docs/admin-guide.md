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
16. [Day-to-day tools](#16-day-to-day-tools)

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
the host and changes nothing. Three lines are optional hardware and always
pass, saying what is missing: UEFI firmware (OVMF, with or without Secure Boot
firmware and keys), the TPM emulator (swtpm) and reserved huge pages. A VM that
needs one is placed only on a node that has it, and refused with that reason
otherwise. Three more are optional and always pass too, for nodes that will
use a Ceph cluster (section 4.8): the Ceph client (`ceph` and `rbd`), QEMU's
RBD driver, and libvirt's secret store.

### 3.3 The node page

- **Pause new VMs** (`creation_paused`): no new VM is placed on the node;
  existing VMs are untouched. A drain pauses the node by itself.
- **Status**: `active`, `maintenance` (no node alerts are raised) or
  `offline`. The panel sets `offline` and `active` itself from its checks.
- **vCPU ratio**: how many vCPUs the node may carry per CPU thread. Overrides
  the panel-wide ratio ([section 4.6](#46-what-a-node-and-pool-accept)).
- **NAT ports**: the public port range for NAT VMs ([section 5.6](#56-port-forwards-and-nat-vps)).
- **Dedicated cores**: the host cores (a CPU list such as `4-15`) that VMs
  with dedicated CPUs take one each per vCPU ([section 7.1](#71-vm-hardware)).
  Cores a VM holds must stay in the pool. The node's other VMs still run on
  any core; isolate the pool on the host (the kernel's `isolcpus`) if
  dedicated VMs must have their cores alone.
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
| Directory | a directory on the node | qcow2 files, thin | ZFS datasets, NFS exports and CephFS work by mounting them on the node and registering the mount point. Storage that several nodes mount is better added as a shared pool (section 4.7). |
| LVM volume group | the volume group | raw logical volumes | Each disk takes its full size from the group when it is created. |
| LVM thin pool | `vg/thinpool` | thin logical volumes | Disks take space as they are written; watch data **and metadata** use (the Storage page shows both). A full metadata area stops every thin volume. |
| RBD pool (shared) | `pool` or `pool/namespace` of a registered Ceph cluster | raw RBD images, thin | Added as a shared pool, not per node (section 4.8). |

Whatever you name must already exist on the node, and a volume group or thin
pool must not hold the node's own system; the node reports why it cannot use a
pool on the Storage page. Capacity and free space are measured by the node
every minute, never typed in. The pool marked **primary** is where that node
creates new disks; a VM's extra disks go beside its own.

VMs on LVM cannot take snapshots and always get full backups. They move to
other nodes like any VM (section 10). A backup or restore of a disk on a block
device, and an offline move of a qcow2 disk onto a logical volume, need scratch
space as a file; it is kept under the agent's `TEMPLATE_CACHE_DIR`, so put that
on a data volume on such nodes.

An agent of this version refuses to create a disk in a directory pool whose
directory does not exist on the node; it never falls back to the root
filesystem.

### 4.2 Storage tiers

A pool can carry a **tier**, a short label such as `nvme` or `hdd` (the
**Tier** button on its card; `storage:update`). A plan naming a tier
(`storage_tier`) puts its VMs' disks only in pools of that tier: placement
picks among nodes that have an online pool of the tier with room, and the
order is refused when none has. Without a tier a VM goes in its node's primary
pool, as before. Staff creating a VM may also name the pool itself, which
names its node too. A VM moved to another node lands in a pool of the same
tier and kind when the destination has one. A VM whose plan changes keeps its
disk where it is: move the disk to a pool of the new tier from its page
(section 4.3).

### 4.3 Moving a disk to another pool

On a VM's page (Disks, **Move**; `vm:migrate`) a disk moves to another pool of
the same node: between directories, onto or off a volume group or thin pool.
A running VM keeps running: the disk is block-copied and the VM switches to
the copy when every block is there. A stopped VM is copied while off and
cannot be started until the copy is done. The destination is admitted like a
new disk, a VM with snapshots cannot move its disks, and the old disk is
removed afterwards, except a volume MaburVM did not make (an imported VM's),
which is kept and listed under orphaned disks. A move can be cancelled; the
disk then stays where it was. **Cancel** also makes the panel ask the node
again about a move that seems stuck (for example after the node was
unreachable for a long time), and records how it ended. The next backup is a
full one. A move blocks node migration, snapshots, rebuilds and disk changes
of that VM until it ends.

### 4.4 Orphaned disks

**Storage, Orphaned disks** asks a node for the disk files and logical volumes
in its registered pools that no VM on the node uses and no panel record names:
what failed creates, restores, moves and removed VMs left. Each shows what it
looks like, the VM its name points to and whether MaburVM made it. Deleting
one (`storage:delete`) needs its path typed out, and one MaburVM did not make
needs a second confirmation. Both the panel and the node list it again first
and refuse if it is in use, no longer in a registered pool, or has changed size
or time since it was listed. Files written in the last six hours, and the
disks of VMs being created or moved, are never listed. Neither are the disk
files and volumes named for a VM the panel still has on that node (a create
may be about to use them); once the VM is deleted or recorded on another node,
what it left there is listed.

### 4.5 Pool alerts

Each pool has an alert level (`alert_threshold`, 90% by default) that raises a
warning, and the Alerts page has a panel-wide critical level
(`storage_critical_percent`). A pool that fills **pauses every VM on it**, and
disks are thin, so a pool fills later without any new order. See
[runbooks.md, Pool full](runbooks.md#5-storage-pool-full).

### 4.6 What a node and pool accept

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
counted. A disk on a volume group (thick LVM) that grows has its new space
zeroed before the VM sees it, which takes as long as writing that much on
slow disks.

### 4.7 Shared pools

A **shared pool** is storage that several nodes reach at the same path: an NFS
export, or a CephFS, GlusterFS or other cluster file system mount. A VM whose
disks are in one moves between the pool's nodes **without its disks being
copied** (section 10). Shared pools are optional. Nothing changes on any node,
and nothing is mounted, until you add that node to a pool; nodes with only
local storage are not affected at all.

**Storage, Add shared pool** (`storage:create`) records the pool:

| Kind | You give | What happens on a member node |
|---|---|---|
| NFS export, mounted by the nodes | server, export, optional mount options | The node's agent mounts it at `/var/lib/maburvm/pools/<pool id>` with a systemd mount unit, so it is mounted again after a reboot, before libvirt starts VMs. The node needs the NFS client (`nfs-common`; `nfs-utils` on the RHEL family). |
| A directory I mount myself | the path, the same on every node | Nothing is mounted for you. Mount it on each node first and make it mount at boot. |

NFS pools are mounted `hard`. The options `nolock`, `local_lock=all`,
`local_lock=posix`, `soft`, `softerr` and `ro` are refused: without locks the
other nodes see, two nodes could write one disk image at once, and a soft
mount can silently lose a VM's writes.

Then add nodes on the pool's card (**Add node**; `storage:update`). A node is
**checked before it joins**: a node that is already a member writes a file with
a random token into the pool, the new node must read it back, and the new node
writes one that a member must read back. A node that does not see the same
storage (a wrong export, a path that is not mounted, a read-only mount) is
taken out again and told why. The first node of a pool has nobody to be
checked against; it writes the pool's marker file (`.maburvm-shared-pool`,
holding the pool's id), which every later node must find.

Each member node gets an ordinary pool of its own for the shared storage, so
everything that works on a pool works here: **Use for new VMs** on the pool's
card sends that node's new VMs to it, staff creating a VM can name it, tiers
and the overcommit ratio apply. The Storage page shows the shared pool once,
with its nodes; capacity and the disk allocated count the storage once, not
once per node. A directory pool you had already registered at the same path on
a node becomes that node's part of the shared pool when you add the node.

What keeps disks safe on shared storage:

- **An unmounted pool takes no writes.** When the mount is gone, the path is
  an empty directory on the node's own disk. The node sees that the marker
  file is missing, reports the pool **Not usable** (and raises a critical
  storage alert), and refuses to create, move, restore or reinstall a disk
  there, or put a rescue disk beside one, instead of filling its own disk. It is usable again by itself once the storage is back.
  **Check now** on the card runs the token check on every member at once.
- **Disks bypass the host cache.** Disks created in a shared pool are defined
  with `cache=none`, which a live move without a copy needs. QEMU's image
  locking is left on: a second hypervisor that tries to open a disk in use is
  refused by QEMU.
- **One VM's files are never another's.** Disk files are named after their VM,
  so VMs on different nodes cannot collide. Deleting a VM or a disk removes its
  own files once, from the node the VM is on. The orphaned-disk list (4.4) does
  not offer a file in a shared pool unless its name points to a VM the panel
  has deleted: a VM on another member may be using a file this node cannot see
  in use. For the same reason the clean-ups a node runs by itself (left-over
  backup files, files of a move that did not finish) leave shared pools alone,
  and a create that finds its VM's disk file already in a shared pool is
  refused instead of removing the file as left over from a failed try.

Templates are cached per node in `TEMPLATE_CACHE_DIR`, not in the pool, so a
shared pool holds VM disks only.

**Remove** on a node's row takes the node out; it is refused while a VM on
that node has a disk in the pool. An NFS pool is then unmounted there and its
unit removed; a directory you mount yourself is left alone. A shared pool with
no nodes left can be deleted; files on the storage are never removed.

For live moves without a copy libvirt must recognise the file system as
shared. It knows NFS, CephFS, GlusterFS, GFS2, OCFS2 and a few more; for any
other, list its mount point under `shared_filesystems` in
`/etc/libvirt/qemu.conf` on each node.

### 4.8 Ceph clusters and RBD pools

MaburVM can be a **client of a Ceph cluster you already run**. It does not
install, grow, repair or upgrade a cluster, and it does nothing with Ceph on
any node until you enable a cluster there. Installs without Ceph are not
affected. What the cluster and the nodes need, with the commands, is in
[runbooks.md, Attaching an existing Ceph cluster](runbooks.md#10-attaching-an-existing-ceph-cluster).

**Storage, Add Ceph cluster** (`storage:create`) registers a cluster: a name,
the monitor addresses, the cephx client id (without `client.`) and its key,
and optionally the cluster's fsid. The key is stored encrypted with the
panel's AES key, is never shown again, is not in any API answer and is
redacted in the audit log. Use a client with the `rbd` profile on the pools
you will use, not the admin key.

**Enable** on the cluster's card (`storage:update`) gives one node access:

1. The node's agent writes a minimal `ceph.conf` and a keyring (mode 0600)
   under `/var/lib/maburvm/ceph/<cluster id>/`. `/etc/ceph` is never touched.
2. It defines a libvirt secret (usage type `ceph`) holding the key. Its UUID
   is the cluster's id, the same on every node.
3. It proves the access by reading the cluster's fsid and health with that
   id and key.

The first node's fsid is recorded (unless you gave one); **every later node
must report the same fsid**. A node whose monitors lead to another cluster is
taken out again and refused with both fsids, as is a node that cannot reach
the cluster at all.

**RBD pools.** **Add shared pool**, kind **RBD pool of a Ceph cluster**: the
cluster, the RBD pool's name, and optionally a namespace. The pool must
already exist on the cluster. It is a shared pool (section 4.7) whose members
are simply the nodes the cluster is enabled on: you do not add or remove
nodes on its card. Each node proves it can use the pool by listing its
images, and a pool no answering node can list is refused with what `rbd`
said (a mistyped name, missing caps). The pool is shown once, with its
capacity counted once, whatever the number of nodes.

**VM disks on an RBD pool.** A VM's system disk and data disks can be images
in an RBD pool. The pool is used only where you choose it:

- **Use for new VMs** on a node's row of the pool's card makes it that node's
  pool for new VMs;
- a storage tier on the pool (**Tier** on its card; section 4.2) sends a
  plan's VMs to it. The tier is set on every node that uses the pool at that
  moment: set it again after enabling the cluster on another node;
- staff can name it in the **Storage pool** list when creating a VM.

It is never picked just because it has the most free space. Room is checked
as for any pool: the disk must fit in what the Ceph pool can still store, plus
the headroom, and the allocation ratio applies (section 4.6). Capacity is the
Ceph pool's own figure, so space held by snapshots counts as used.

Each disk is a raw image named `maburvm-<vm id>-<disk>`, opened by QEMU
through libvirt with the cluster's monitors and the libvirt secret (the key
is in no VM definition), without the host's cache, and giving freed blocks
back to the pool. A VM's data disks go in the same pool as its system disk: a
VM never has disks on both Ceph and a node's own storage.

| On a VM whose disks are on RBD | What happens |
|---|---|
| Create, reinstall | The image is prepared as a file on the node (in the template cache directory), then imported. A reinstall writes a second image and swaps it in, so a failure leaves the disk as it was. Its snapshots go with the old system. |
| Delete | The images and their snapshots are removed, unless another client still has an image open (the VM still runs on another node): that image is kept and the delete's record says so. |
| Start, start with the node | Refused while another client has one of the VM's images open, naming it: the VM runs on another node, and two copies would corrupt the disk. libvirt does not start these VMs at boot; the agent starts those set to start with the node, after the same check. |
| Grow a disk | Works running or stopped. Disks never shrink. |
| Snapshots | RBD snapshots of every disk. They hold the disks, not the memory: for a running VM choose **Disks only** (with **Freeze the filesystems first** to quiesce the guest). Schedules take disk-only snapshots by themselves. Names use letters, digits, dots, dashes and underscores. Restoring needs the VM stopped, as for every snapshot. |
| Backups | Full copies, read from a temporary RBD snapshot taken with the guest's file systems frozen when its agent answers. A schedule set to incremental makes full backups and says why. Restore writes a second image and swaps it in. |
| Move to another node, drain | No disk is copied: the destination must have the cluster enabled and the pool online. A node without it is refused. Snapshots go with the VM and do not stop the move. |
| Recover from a failed node (10.1) | On its own disks, on any node with the pool. The new node first breaks the failed node's locks on the images and puts it on the cluster's blocklist, so a VM still alive there cannot write. Snapshots are kept. |
| Rescue, console, CPU and disk limits, hardware, ISO, firewall, network | As for any VM. The rescue system's own disk is a file on the node. |
| Add, remove a data disk | In the same RBD pool. |
| Move a disk to another pool | Refused, to and from RBD. Back the VM up and restore it as a new VM on the other storage. |
| Clone | Refused for a source on RBD. A VM on a node's own storage can be cloned onto an RBD pool. |
| Reset a password while stopped | Refused: the node cannot edit an image from outside. Start the VM; the guest agent sets it. An address change made while the VM is off is applied at its next start the same way. |
| A router (RouterOS) | Created and reinstalled like any VM. A change it does not take through its guest agent cannot be written for its next boot. |
| Volumes that belong to no VM | Not made in an RBD pool. |

**Health and capacity** are read on the nodes about once a minute, each read
bounded to seconds so a slow cluster never holds up a node's other work. The
card shows:

- the cluster's health (`HEALTH_OK`, `HEALTH_WARN`, `HEALTH_ERR`) with the
  checks behind a warning or an error;
- raw capacity, and per RBD pool what is stored and what can still be stored
  (from `ceph df`; shown as not reported when the client may not read it);
- each node: reaches the cluster, cannot reach it, reaches another cluster,
  or needs attention (libvirt lost the secret), with the reason.

A critical alert (kind Storage pool) is raised when the cluster reports
`HEALTH_ERR`, when a node cannot use the cluster, and when a node cannot use
an RBD pool. A warning is shown on the card only. **Check now** asks every
node at once and sends the cluster's settings again first, which puts back a
keyring or a libvirt secret a node lost.

**Edit** changes the name, monitors, client id or key (an empty key keeps the
stored one). New access settings are sent to the cluster's nodes, and each
proves them before replacing the ones it has; when the first node to answer
refuses them, nothing is changed anywhere.

**Disable** on a node's row removes the conf, keyring and libvirt secret from
that node and takes it out of the cluster's RBD pools. It is refused while a
VM on that node has a disk on the cluster, by the panel's records and by the
node's own look at its VMs. **Deleting an RBD pool** removes only its record
and is refused while a VM or volume is in it. **Unregistering a cluster** is
refused while it has an RBD pool or is enabled on a node. None of these
removes anything on the cluster.

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
- **Reverse DNS**: set per address, or by customers on their own addresses
  and on any address inside the IPv6 prefix routed to their VM (up to 64 names
  per prefix; released with the prefix). With a nameserver connected
  (Settings, System, DNS provider) records are pushed there: a **PowerDNS**
  server (its HTTP API address, server ID and key) or **Cloudflare** (an API
  token with Zone, DNS, Edit rights; every zone written to, reverse zones
  included, must already be on the account, and the panel never creates or
  deletes a Cloudflare zone). **Reverse zone** exports a zone file for a pool,
  and **Import** reads existing records back.
- **Reverse DNS zone** (pool edit): where the pool's PTRs go when it is not
  the standard /24 (IPv4) or /64 (IPv6) zone. For a block smaller than a /24
  delegated the RFC 2317 way, give the classless zone your upstream delegated
  (`0-127.113.0.203.in-addr.arpa`, or `0/25...`); records are named after the
  last octet inside it, and the pool's zone file lists the CNAMEs the holder
  of the /24 publishes. A parent zone (a /16, the IPv6 /48 delegated to you)
  works too. It applies to PTRs saved from then on.
- **Nameservers**: the resolvers the pool's guests are given (up to four, IPv4
  or IPv6); blank keeps 1.1.1.1 and 8.8.8.8 (and Cloudflare and Google over
  IPv6). They reach a guest through cloud-init when it is created or
  reinstalled, and with every address change; running guests keep theirs until
  then. A routed IPv6 pool's nameservers are its guests' IPv6 resolvers.
- **DHCP**: the node answers DHCP for the pool's assigned addresses, for guests
  installed without cloud-init (from an ISO, an imported machine set to DHCP).
  Each address is leased only to its own VM's card on the pool's bridge, for
  good; a machine the panel did not assign gets no answer, and no address of
  the range is ever handed out. IPv4 pools with a gateway only. It needs
  `dnsmasq` on the node ([node-setup.md](node-setup.md#1-requirements)); the
  node's host checks show each bridge served. Switching it reaches the nodes at
  once and touches no guest. A guest that took its address by DHCP sees a
  change of address at its next lease request, so reboot it after one.

Changing a VM's address no longer needs a reinstall: the guest is reconfigured
live through its guest agent where it can be, otherwise at its next start from
the panel, and the VM's page says which. A running Linux guest is changed
through whatever configures its network: systemd-networkd (every VM the panel
installed, and Ubuntu's netplan when it renders for networkd: the panel's
unit outranks netplan's and netplan's files are left alone), ifupdown
(`/etc/network/interfaces`, Debian, when ifupdown brought the card up), netplan rendering for NetworkManager,
NetworkManager (the RHEL family, Fedora, desktops) or network-scripts (older
RHEL without NetworkManager). In the netplan case, netplan files that
configured the same card are renamed `*.maburvm-off` and cloud-init is told
not to rewrite them; earlier `/etc/network/interfaces` stanzas for the card
are commented out (`# maburvm:`); the guest's own network-scripts files for
the card are renamed `*.maburvm.bak`, a name it skips at boot. A change that
starts in the guest but does not finish is reported and not run again. A
stopped Linux guest is changed on its disk
only when it uses systemd-networkd; any other is changed when it next starts
and its guest agent answers; if it then cannot take the change, it is not
tried again until its next start from the panel. A guest with none of these
keeps its addresses and the VM's page says to set them inside it. Nothing is
ever restarted for an address change.

### 5.1a Network cards and MAC addresses

A VM's **Network cards** card lists each card with its MAC, network and
addresses. A VM has one card from its creation; staff add another on a pool's
bridge (a second public network, a private VLAN bridge), and customers add one
on their own private network in the VM's location, up to four. Each added card
takes one address from that network's pool and is pinned to it by the node's
anti-spoofing; one card per network. It is added while the VM runs where its
machine has a free slot (a q35 machine created before has none, and a router's
machine has), otherwise when the VM is next started from the panel; the
guest gets the card's address the way an address change does (Linux guests;
on a router or Windows the page says which address to set on which card).
Removing a card gives its addresses back. A private network's address does not
count toward the plan's extra IPv4 addresses, nor as an extra IP in the
billing usage sync. The VM's speed limit, an over-quota throttle and an abuse
rate limit apply to its first card only; abuse readings and quarantines are
per card (by MAC). Every card's traffic counts toward the VM's transfer,
private-network traffic included.

Each card's MAC is kept by the panel, so it stays the same through reinstalls
and moves, and no MAC is given to two cards. A new VM's first card gets a MAC
worked out from the VM's id, or the one staff typed when creating it. Staff
change a card's MAC with the VM stopped (**Change MAC**): it must be unicast
and unused; the guest's network settings are rewritten for it (on its disk
for systemd-networkd, otherwise at its next start). A VM created before cards
were recorded, or imported, has its cards read from its node the first time
its page shows them.

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
  address the guest holds in its prefix, which works only once you set
  `net.ipv6.conf.<bridge>.proxy_ndp=1` on the node. Prefix lengths other than
  /64 (a /56, a /80) work the same way: the guest's address is still the
  prefix's `::1` with that length, and its anti-spoofing filter lets it send
  from exactly its prefix.

Which mode a node needs:

| The upstream router | Pool | Node |
|---|---|---|
| Routes the pool to the node's address | Routed prefix | Nothing more |
| Treats the pool as on-link on the node's segment | Routed prefix | `proxy_ndp=1` on the bridge |
| Gives each machine an address on a shared subnet | Per address | Nothing more |

A guest holds more than its `::1`: staff and the VM's owner add up to 16 further
addresses from its routed prefix on the Network tab (**More addresses in**).
They are written into the guest like an address change and proxied for the
node's neighbours where the node proxies. A per-address pool gives each VM
one address.

Staff add or release a VM's IPv6 on its Network tab. A customer turns IPv6 on
through an address request (granted at once or by staff, see
[section 8.1](#81-customer-accounts)) and off from the Network tab. A plan's
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

A VPC's **Nameservers** (`vpc:update`) are the resolvers its VMs are given,
kept on its address pool; they reach a VM like a pool's do. A VM joins a
private network when it is ordered into it, or later as a second card
([section 5.1a](#51a-network-cards-and-mac-addresses)).

**Networks** (`network:create`, `network:delete`) are staff-made networks.

**VLAN trunking** (node page, `node:update`) enables per-VM VLANs on some of a
node's bridges, each with the uplink port that carries the VLANs tagged. It is
opt-in: a VLAN set on a VM whose bridge is not listed is refused, and nothing
on the node changes until a VM on a listed bridge gets a VLAN. Then the
bridge's VLAN filtering is switched on, which changes how the whole bridge
forwards: untagged traffic carries on, tagged frames that already pass the
bridge need their VLAN added, and the switch port must carry the VLANs tagged.
The list is saved once the node has it (the node keeps it across restarts), and
each bridge comes back with what the node sees: a missing bridge, an uplink
that is not its port. The host checks report each listed bridge. Until a list
is saved here, the agent's `MABURVM_VLAN_BRIDGES` decides, and **Use the
agent's environment** goes back to it. Removing a bridge from the list leaves
its filtering as it is, and is refused while a VM there has a VLAN: take the
VM's VLAN off first, or at its next start it would land on the untagged
network.

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
A NAT VM moves to another node where its NAT pool is offered too; its ports
and forwards go into a free block there ([section 10](#10-moving-vms-and-draining-nodes)).

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

- **Abuse** shows every guest's new connections and packets per second, out
  and in, read from the node once a minute, including guests the panel does not
  manage. **Quarantine** cuts a guest's traffic while leaving it running;
  **Null-route** drops all traffic to one address on its node for a set time
  (at most a week), after which the node lifts it by itself (`network:update`).
  It does not stop traffic that reaches a VM through a floating IP, which the
  node lets through before the null-route sees it; detach the floating IP for
  that.
  The node also limits how fast each guest opens new outbound connections;
  connections arriving for a guest, such as a busy site behind a CDN, are never
  limited.
- **Timeline**: each time a guest goes over a threshold is an episode, kept
  with up to 30 readings, the peak, and what was done. Staff actions
  (quarantine, null-route) are episodes too. **Release** lifts an episode's
  quarantine, rate limit or null-route; the episode is then left alone until
  the guest has been quiet.
- **Policy** (Abuse page): a threshold per reading and what happens once a
  guest stays over it for a number of readings in a row (2 by default). A guest
  sending too much can be **rate limited** (its speed held down) or
  **quarantined**; a guest receiving a flood can be **null-routed**, which
  protects the node and its other guests but not the link into the node (ask
  your upstream to blackhole the address for that). Every action is "alert
  staff only" until you choose one. Staff are told under Alerts (kind "A guest
  is attacking or under attack"); the owner is emailed when an action is taken
  on their server and when it is lifted, unless switched off in the policy
  (template: Network action on a server). Inbound readings and null-routes need
  agents of this version; older agents report outbound connections only. New
  inbound connections are counted only on nodes with bridge netfilter on (see
  the firewall note above); elsewhere that reading stays at zero.
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

- **Reset day**: a plan's allowance starts again on a day of the month (1 to
  28, default 1); an account can have its own day (user page, Transfer), for
  example its billing day. Changing it starts a new period for the VMs it
  covers.
- **What counts**: a plan counts traffic in and out (the default), out only
  (what the VM sends, as clouds bill) or in only. Both directions are still
  recorded and shown.
- **Pooled transfer** (user page, Transfer): the account's VMs share the sum
  of their allowances and go over together; owners get one warning for the
  pool. VMs without an allowance stay outside it.
- The last counters read from each VM are kept, so a panel restart loses no
  traffic. A VM's counters restart with the VM; traffic between its last
  reading and a stop is not counted.

## 6. Templates and ISOs

**Templates** (`template:update` to change). A template has a system type
(Linux, RouterOS or Windows) that decides how a VM built from it is set up;
**Sync** copies it to nodes. A template can be limited to chosen plans, or
marked as needing a paid licence. RouterOS and Windows notes, and how to build
a Windows image on a node, are in [DEPLOYMENT.md §12](DEPLOYMENT.md#12-templates-that-are-not-linux).

**Checksums.** A template fetched over http(s) needs its sha256, or the address
of a checksum list (a `SHA256SUMS` or `CHECKSUM` file naming the image), when
it is added; only a publisher that gives none may be confirmed as such. Every
node checks its download against it and refuses one that does not match, and
keeps the copy under the checksum. Plain http is refused unless allowed for
the template. Templates added before this keep working without one; add it on
the template's page.

**Refresh.** A template's page shows each node's copy. **Check for a new
image** reads the checksum list again; when the image changed the panel
records the new checksum and every active node fetches it. **Refresh on
nodes** fetches it in any case. With **Refresh every day** the panel checks the
list daily by itself. VMs built from the old image are copies of it and are
not touched; a node removes its old copy once nothing uses it.

**Boot firmware.** A template is BIOS or UEFI, and may ask for **Secure Boot**
(UEFI only) and a **virtual TPM 2.0**, as Windows 11 needs. A VM from a UEFI
template is created only on a node with UEFI firmware (the `ovmf` package),
with Secure Boot only where the Secure Boot firmware and its keys are
installed, and with a TPM only where `swtpm` is; the node's host checks show
which. A VM keeps the firmware it was created with: it is reinstalled only from
a template for the same firmware, and a reinstall onto the other is refused
with that reason (the disk is installed for one or the other). A copy of a VM
keeps its firmware. `scripts/build-windows-template.sh --uefi` builds UEFI
Windows images.

**ISOs** lists install media: added from an address (with a checksum, as
templates) or uploaded from your computer (**Upload a file**, sent to the panel
in pieces, kept under `MABURVM_DATA_DIR/images` and copied to every node, which
checks it). An ISO is offered to customers like a template, by plan. **Drivers
disc** marks the ISO put in a Windows install's second drive (the virtio
drivers, for example `virtio-win.iso`).

**Installing from an ISO.** Choosing an ISO as the system when creating a VM
gives it an empty disk and that ISO in its first CD drive, which boots first;
whoever installs it does so from the console, and no password is generated.
For a Windows ISO the drivers disc can go in the second drive. A VM has two CD
drives: drive 1 boots first while it holds a disc, drive 2 is for drivers and
files. A running VM takes a disc at once in a drive it has (VMs installed from
an ISO have both; others gain drive 1 the first time a disc goes in while they
are stopped).

**Settings, Customer area** holds
the list of ISOs customers pick from when they mount one (a name, an http(s)
address the node downloads from, a description); mounting one of those counts
as staff-supplied. Customers may also attach an ISO by their own URL unless
"Customers may also mount an ISO from an address of their own" is cleared: the
node fetches only public addresses, bounded by the agent settings
`CUSTOMER_ISO_MAX_GB`, `CUSTOMER_ISO_MIN_FREE_GB` and
`CUSTOMER_ISO_RETENTION_DAYS`.

**Rescue mode** boots a stopped VM into a rescue system, set under Settings,
System, Rescue:

- The **built-in rescue system** is a small cloud disk image you choose once (a
  qcow2 URL with its sha256, or a path on every node; a minimal Debian or Ubuntu
  cloud image works, as long as no template is built from the same image). Each
  rescue copies it for the VM, gives it the VM's own address, a one-time root
  password (shown once to whoever turned rescue on, never emailed) and their
  chosen saved SSH keys, and attaches it as the first boot disk with the VM's
  own disks behind it for repair. Starting the VM boots it; SSH needs the VM's
  firewall to let port 22 in. **Exit rescue** (VM stopped) removes the copy.
  Backups, snapshots and reinstalls wait until the VM is out of rescue mode.
- The **rescue ISO** (or `RESCUE_ISO_URL` in the panel's environment until one
  is saved) is used where no built-in system is set or the VM's node needs an
  agent update for it; the API's `mode: iso` asks for it explicitly.

With neither, customers are told rescue is not available.

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
| Allowances (`max_snapshots`, `max_backups`, `max_extra_ipv4`, `max_extra_ipv6`, `max_extra_disks`) | Per VM. Empty is no limit, 0 is none. Read from the plan at each use, so a change reaches every VM on it at once. Lowering `max_backups` removes the oldest completed scheduled backups above it at the next hourly pass; manual backups are never removed, nor a backup a kept incremental one builds on. |
| Account allowances (`max_images`, `max_floating_ips`) | Images and floating IPs belong to the account: each of its VMs adds its plan's figure. An account with a VM on no plan or on a plan without the limit, or with no VMs at all, is not limited by plans (the floating-IP limit under Settings, System, Quotas still applies). |
| Prices of extras (`addon_prices`) | Monthly display prices, in the plan's currency, for `snapshot`, `backup`, `image`, `extra_disk_gb`, `floating_ip`, `extra_ipv4` and `extra_ipv6`; shown on the order page. |
| Network type (`network_type`), `nat_ports`, `nat_hostnames` | Public or NAT. |
| Default firewall group | What new VMs get instead of the built-in rules. |
| Hardware (`hardware`) | CPU model, sockets and threads, dedicated cores, NUMA placement, huge pages, Secure Boot and TPM (for images that boot UEFI), watchdog and restart on crash, for new VMs; see [section 7.1](#71-vm-hardware). Existing VMs keep theirs. |
| Hypervisor group (`node_group`) | New VMs on the plan are placed only on nodes in this group (set on each node's page, such as `nvme`), inside the region the customer chose; with no such node that can take the VM, the order is refused rather than placed elsewhere. A drain moves such a VM only within the group. A node staff choose by hand is taken as given. |
| Active | Inactive plans are not offered. |

Size, limits, speed, quota and mail policy reach existing VMs only through
**Apply to VMs** on the plan, which previews then applies; running VMs keep
their size until they are stopped. An account's own limits (servers, vCPU,
memory, disk) are set on its user page; new customer accounts start from
Settings, System, Quotas, "Starting quota for new customer accounts".

What customers may do without the billing system (order servers, clone, add
disks, delete billed servers) is switched under Settings, System, Quotas.

**Settings, Customer area** also sets the links shown in the customer menu
and on the order page (billing, service status; support is under Branding),
and how many days of hourly graph averages are kept (default 90, see
[section 11.5](#115-graph-history)).

### 7.1 VM hardware

A VM's hardware comes from its plan, from its image (UEFI, Secure Boot, TPM)
and, for a VM staff create, from what they set on the create form. On the VM's
page, **Hardware, Change** (`vm:update`, staff only) changes the CPU model,
topology, dedicated cores, NUMA placement, huge pages, watchdog and restart on
crash. The node saves the change in the VM's definition and the VM uses it from
its next power off and on; nothing is restarted, and a restart from inside the
guest is not enough. Firmware, Secure Boot and the TPM are fixed when a VM is
created.

| Option | What it does |
|---|---|
| CPU model | Empty is the node's own CPU (host-passthrough): fastest, and the VM moves live only to a node with the same CPU. `host-model`, or a named model all the nodes offer (such as `Haswell-noTSX`), lets VMs move live between different CPUs. |
| Sockets, threads | How the vCPUs are laid out; the cores per socket follow from the vCPU count. A count that does not divide is laid out as one socket of single cores, so a resize never fails on it. |
| Dedicated cores | Each vCPU gets a host core of its own from the node's pool (the node page), and the emulator runs on the same cores. A VM is placed only on a node with enough free cores; a resize takes or gives back cores; turning it off frees them. A VM with dedicated cores must have them turned off before it moves. |
| NUMA placement | With dedicated cores: the memory is kept on the NUMA nodes of those cores (preferred, never strict, so a full node does not kill the guest). |
| Huge pages | The memory is backed by the node's reserved huge pages (`vm.nr_hugepages`). The node refuses the VM, or the change, when too few are free, since the VM would not start. |
| Watchdog | Resets a guest that stops answering once its system uses the watchdog. VMs on the q35 machine (Linux, Windows) have the chipset's watchdog anyway; RouterOS VMs get an i6300esb. |
| Restart on crash | The node starts the VM again when it crashes (its QEMU process dies, or the guest kernel panics, which a panic device reports): at once the first time, then after a minute, then five. After a fourth crash within an hour it leaves the VM off and raises the *A VM keeps crashing* alert, until someone starts it. |
| Secure Boot, TPM | Set at creation from the plan or the image; UEFI images only for Secure Boot. A TPM's state stays on its node, so such a VM does not move to another node yet. |

Only the options changed are sent, so whatever else an imported VM's
definition has is left alone. A change the node refuses is not kept: the VM
keeps the hardware it had and its page says why. One that may not have reached
the node (it was offline, say) is kept and marked as not in force; saving again
sends the options the VM has set, never the ones left at their defaults.

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

**Teams.** A customer can let others into their account (Team in the customer
area): Admin, Technical, Billing or Read-only, reaching every VM, chosen VMs
or chosen projects. Every VM-scoped check (the VM handlers, backups,
snapshots, networking, console, SSH console, metrics, images) goes through one
rule, `service.CheckVMAccess`: the owner, or a member whose membership covers
the VM, with changes and the console only for Admin and Technical. The VMs
stay the account's, so quotas, plans, billing and notifications are the
owner's. A suspended or deleted account's members reach nothing. Invitations
go out by email (template "Team invitation") and expire after 7 days; every
invite, change, removal, acceptance and project change is in the audit log.
An account made by accepting an invitation is *member-only*: it cannot order
VMs or floating IPs of its own until you click **Let it order** in the Team
card on its user page (`PUT /api/v1/users/{id}/member-only`).

**Address requests** (`network:read` to see, `network:update` to decide).
Customers ask for another IPv4 address or IPv6 on a VM within its plan's
allowance. Settings, Customer area chooses whether staff decide each one
(the default), they are granted at once, or customers cannot ask. "Granted
at once" applies only within a number the plan sets: an IPv4 request on a VM
whose plan sets no number of extra IPv4 addresses waits for staff, so no
customer can empty a pool a request at a time. Granting
assigns from a pool on the VM's node exactly as **Assign IP address** does,
and never restarts the VM; one that cannot be assigned waits in the queue
with the reason.

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

**Encryption.** **Encrypt backups** on a destination makes each node encrypt
a backup before it uploads it (AES-256-GCM in 64 KiB chunks, a key derived per
file from the destination's key), so the store and anyone holding its
credential see only ciphertext. It applies to backups made from then on;
existing ones stay as they are and still restore. The destination's key is
kept in the panel database sealed with the panel's AES key and is sent to a
node only with a backup or restore job. **New key** encrypts new backups with
a fresh key and keeps the old ones, so every backup stays restorable; nothing
is re-encrypted. **Stop encrypting** keeps every key for the same reason.

- **Export keys** (Administrator role) shows the keys. Keep them away from the
  panel, in a password manager or on paper: if the panel and its database are
  lost, `go run ./cmd/backup-decrypt -keys keys.json -in backup.qcow2 -out
  disk.qcow2` turns a backup file back into its disk image with them, and
  without them nobody can. Export again after **New key**.
- A node whose agent is older than the panel cannot encrypt: its backups are
  still made, in the clear, and say so to staff on the VM's Backups tab. An
  encrypted backup is restored only on a node whose agent can decrypt it; an
  older one is refused before anything changes.
- Retention and deleting a backup work as before: removing a file needs no key.
- Encryption protects backups from whoever holds the destination's credential
  or the store itself, not from a compromised node: a node running a backup or
  restore job is sent every key of the destination.
- The keys are sealed with the panel's AES key and are not re-sealed when it
  changes: keep a retired panel key in `aes_key_previous` (`AES_KEY_PREVIOUS`)
  while any backup made with these keys is kept, or export the keys first.
- A backup is checked against its checksum after it is decrypted on restore,
  so a damaged or truncated file is refused, never written to a disk.

**Least-privilege credentials.** One credential per destination travels with
every backup job to every node, so treat it as exposed to any node that is
compromised. Encryption keeps the credential alone from reading backups; keep
it from destroying them too:

- **S3:** a bucket for backups only, with **versioning** and, where the
  provider has it, **object lock** in governance mode. Give the credential
  `s3:PutObject`, `s3:GetObject`, `s3:DeleteObject`, `s3:ListBucket`,
  `s3:AbortMultipartUpload` on that bucket and nothing else: no
  `s3:DeleteObjectVersion`, `s3:PutBucket*`, `s3:PutLifecycleConfiguration` or
  `s3:BypassGovernanceRetention`. The panel's retention then still deletes
  (on a versioned bucket a delete leaves a delete marker), while earlier
  versions stay recoverable by the bucket owner until a lifecycle rule you set
  expires noncurrent versions, for example after 30 days. A credential that
  cannot delete at all also works for writing and restoring, but then
  retention cannot remove old backups and the bucket grows until a lifecycle
  rule removes them.
- **SFTP:** a user of its own, chrooted to the backup directory, on a server
  that takes snapshots of it (ZFS, Btrfs) the user cannot remove.
- **Directory on the nodes:** an export of its own, snapshotted on the storage
  side; nodes need read and write, nothing else.

**Downloads.** Whoever holds `backup:download` (customers do) downloads a full
backup from the VM's Backups tab. A plain backup in S3 is a 15-minute link to
the bucket itself, so customers must be able to reach the S3 endpoint, and the
link shows its address and the bucket. An encrypted backup, or one on an SFTP
server, is streamed through the panel and decrypted on the way, on the panel's
bandwidth. Backups in a node's own storage or a directory on the nodes cannot
be downloaded.

### 9.2 Schedules, incremental backups and retention

Schedules are per VM (Backups tab): how often, and how many or how long to
keep. A VM can have several. Customers set their own, within their plan's
`max_backups`.

Staff can set **Full backup every N days**: in between, a running VM on a node
with libvirt 7.2+ and QEMU 6.0+, whose disks are all qcow2 files, copies only
what changed since the previous backup. Anything else, or a broken chain, gets
a full backup and the backup says why: an older node, a disk on LVM or on Ceph
RBD, a VM that was off, a lost checkpoint, disks replaced by a restore, reinstall, snapshot
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
place; they are not backups. A VM on Ceph RBD takes RBD snapshots instead:
its disks without its memory (section 4.8). VMs on LVM cannot take them, and a VM with
snapshots cannot move.

- A snapshot of a running VM saves its memory too, and pauses it while the
  memory is written (minutes for a large VM). **Disks only** saves no memory
  and does not pause it: QEMU snapshots each qcow2 disk inside the image at
  once. **Freeze the filesystems first** asks the guest agent to quiesce them
  for that, and the snapshot fails when it cannot. A stopped VM's snapshot
  holds its disks only either way. Disk-only snapshots need the current agent.
- Reverting leaves the VM as it was when the snapshot was taken: running from
  its saved memory, or shut off; a disk-only snapshot of a running VM boots the
  VM from the snapshot's disks. The panel records the state the node reports.
- A **snapshot schedule** (one per VM, on its Snapshots tab, for staff and
  owners with `snapshot:create`) takes a snapshot every 6 hours to every week
  and keeps the newest 1 to 10 it took, removing the oldest first. Snapshots
  taken by hand are never removed by it, and count against the plan's
  `max_snapshots`, which the schedule must fit in. A run that cannot take its
  snapshot (no room, rescue mode, LVM disks, the VM being created or deleted)
  removes none of the schedule's snapshots and is skipped with the reason on
  the card; one while the VM moves or another snapshot is still being taken
  waits. A failed scheduled snapshot is removed first to make room. Removing the
  schedule keeps its snapshots. **Images** capture a VM's disk to object storage; an
image outlives its VM and can seed a new one. Captured images use the object
storage in each node's own environment (`STORAGE_*` or `S3_*`).

## 10. Moving VMs and draining nodes

Staff with `vm:migrate` move a VM from its Settings tab; `node:drain` empties a
node from its page. Both need the current agent on both nodes. A running VM
moves live (a pause of about a second at the end), a stopped one offline. The
VM stays on its node until it runs on the new one; a failed or cancelled move
leaves it where it was. **Check** shows before anything starts why a VM cannot
move: floating IPs or a private network, snapshots, an ISO, rescue mode, a
running backup, a pool or port the destination lacks, no room, dedicated cores
or a virtual TPM (both belong to the node), huge pages or UEFI firmware the
destination lacks, or a different CPU for a live move of a `host-passthrough`
VM (move it offline instead). A UEFI VM's firmware settings start fresh on the
new node, so it boots its disk's default boot loader. An offline move points
the VM at the destination's own OVMF files when they live elsewhere there; a
live move needs them at the same path on both nodes, and fails cleanly (the VM
stays where it was) when they are not.

A **NAT VM** moves when its NAT pool is offered on the destination too (the
same private network there; otherwise the check names the pool) and the
destination has a NAT port range. It keeps its public ports when they are free
there, else takes the lowest free block of the same size, and its port
forwards move into it. Its public address becomes the destination's, and its
verified hostnames are routed there (the destination's agent must route
hostnames, and its ports 80 and 443 must not be forwarded to a VM). The check
says which ports it gets; once moved, its owner is emailed the new address and
ports (template *NAT server moved*).

Disks on logical volumes move too, and a VM can move onto an LVM pool: the
destination makes the volumes first (named and tagged as its own), a live move
copies into them, and an offline move writes a raw disk into its volume
directly or a qcow2 one through scratch space beside it. A disk on a volume
moving to a directory pool becomes a qcow2 file (live) or a raw file
(offline). Both nodes need the current agent. The source's volumes MaburVM
made are removed after the move; an imported VM's are kept and listed under
orphaned disks.

**On shared storage no disk is copied.** When a VM's disks are in a shared
pool (section 4.7) that the destination is also a usable member of, the move
leaves them where they are: a live move copies only the memory, and an offline
move defines the VM on the destination and removes its definition from the
source. The files are the same on both nodes, so the source never deletes
them. Right before such a move the panel runs the token check between the two
nodes again and refuses the move when the destination does not see the same
storage. Other refusals the check may name: a node whose agent is too old, a
running VM whose disk does not use `cache=none` (stop it and move it offline
once; it is defined with `cache=none` on arrival), a disk built on a backing
image outside the shared pool (the other node has no such image at that
path), and an offline move of a VM that is not shut off (two hypervisors must
never open one image). A VM
with some disks in a shared pool and some local keeps the shared ones in
place and copies only the others, into the destination pool you name or the
panel picks. A disk on shared storage that the destination also uses always
stays; to take a VM off shared storage, move its disk to another pool of its
node first (section 4.3). When no node is named, a destination that needs no
copy is preferred, in single moves and in drains.

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

### 10.1 Recovering the VMs of a node that went down

Moves and drains need both nodes alive. For a node that is down and not coming
back soon, staff with `node:recover` use **Recover VMs on other nodes** on the
node's page. The panel never does this by itself: there is no automatic
failover.

The card lists the node's VMs and, for each, the nodes it could go to and how:

| The VM's disks | What recovery does |
|---|---|
| All in a shared pool (section 4.7) that the other node is a healthy member of | Defines the VM there from the panel's record, on the same disk files. Nothing is created or copied. It is started if **Start it** is ticked, which it is for a VM that was running. For disks on Ceph RBD (section 4.8) the other node first breaks the failed node's locks on the images and puts it on the cluster's blocklist, the VM's snapshots stay, and its cloud-init disc is written again. |
| On the failed node's own storage | Restores its newest finished backup onto the other node and starts it. What was written after that backup is not in it. |
| On the node's own storage, no finished backup | Listed as not recoverable, with the reason. |

Either way it stays **the same VM**: same record, owner, plan, firewall rules,
limits, port forwards and backups. A NAT VM gets a port block on the new node,
as in a move. Not carried over, and said in the plan: dedicated CPU cores
(turn them on again afterwards), snapshots (kept only for disks on Ceph RBD), a
virtual TPM's keys, UEFI settings, rescue mode, and a disc in the drive.

**Addresses.** A VM is only recovered with its address onto a node its address
pool is available on (section 5.1), floating IPs included. Nodes in different
sites usually share no pool, so those are refused with the reason. You can then
tick **Give it a new address**: the VM's card keeps its record and gets an
address from the other node's pool, the old address is released, a floating IP
from an unreachable pool is detached (it stays the customer's), IPv6 from one
is released, and the guest is given the new address the way an address change
does (section 5.1). Tell the customer. A VM with more than one network card,
or in a private network (which exists on one node only), is not recovered this
way: restore its backup as a new VM (section 9.3).

**The node must really be off.** A node the panel cannot reach may still be
running its VMs. Recover one of them and two copies run: on shared storage
they write the same disk and destroy it; from a backup they answer on the same
address. So:

- recovery is refused unless the panel has marked the node offline by its own
  checks, has not heard from its agent for as long as that takes (section 3.4),
  and cannot reach the agent's port at that moment. The job checks again right
  before it defines the VM;
- you type the node's name to confirm it is **powered off, or cut off from its
  storage and its VM network** (API: `node_is_down: true`). Who confirmed is in
  the audit log, and each VM gets its own entry in its activity;
- on shared storage the new node refuses to define a VM whose disk is open in
  another QEMU (the image lock), or that has a backup's temporary overlay lying
  beside its disk. It also refuses a disk built on a backing image outside the
  pool (older disks sit on a node's own template copy): that node would open a
  different file at that path. Recover such a VM from its backup onto a node
  that is not a member of the pool.

Each VM is its own background job, shown on the card with its progress.
Nothing else can change a VM while it is being recovered. A recovery that
fails before the VM is defined on the other node changes nothing, and can be
run again. It fails, and leaves things as they are, when the other node
already has a definition of the VM left from an earlier move: undefine that
one there (keep its disks) and recover again. A backup is never restored into
the shared pool the VM's own disks are in, which would replace them; pick a
node that restores it elsewhere.

**When the node comes back** the panel tells its agent which VMs now live
elsewhere, at the first check the node answers. The agent turns their
autostart off, removes their network rules from the host, and refuses to start
them from then on; the list survives a reboot. It does **not** stop a copy it
finds running: the panel raises a critical alert and you decide. The VM's
record is never moved back by itself.

The first minute is the gap: libvirt starts its autostart VMs at boot, before
the panel can reach the agent. For a VM on shared storage the image lock stops
the second copy. A VM on Ceph RBD is not started by libvirt at all: the agent
starts it after boot only when no other node has its images open. For a VM
restored from a backup, the old copy boots with the
same address. If you can, bring the node back with its VM network unplugged,
or remove the recovered VMs' files from `/etc/libvirt/qemu/autostart/` (named
after the VM ids listed on the card) before libvirt starts.

**Recovery leftovers** on the returned node's page asks the node what it still
holds of those VMs. **Remove** deletes the old definition. For a VM restored
from a backup it also deletes the old disks, which hold what the VM wrote
after that backup: copy off what you need first. For a VM on shared storage
only the definition goes; the disks are the ones it runs on now. A disk file
some running VM holds open is kept too (storage the nodes share without a
shared pool saying so). A VM cannot be recovered or moved back onto a node
that still holds its old copy: that node's agent refuses to run it.

Needs the current agent on the node a VM is recovered onto (either way) and on
the node that comes back; an older returning agent cannot be told and raises a
critical alert until it is updated.

## 11. Alerts, email and notifications

### 11.1 Alerts

**Alerts** (`ops:read` to see, `ops:update` to close). Until delivery is set up
there, alerts are only listed.

| Alert | Raised when |
|---|---|
| Node offline | the agent port stops answering for the set number of checks |
| Node not reporting | the port answers but the agent sends no figures |
| Storage pool | a pool passes its alert level (warning) or the critical level; a shared pool is not usable on a member node; a Ceph cluster reports `HEALTH_ERR`, or a node cannot use it (section 4.8) |
| Node not filtering guest traffic | a node reports bridge netfilter off |
| Backup failed | a backup fails with no retry left |
| Job failed | any other background job fails for good |
| Node keeps losing VM network rules | the agent had to put VM firewall, anti-spoofing or NAT rules back three times within an hour |
| A VM keeps crashing | a VM set to restart on crash crashed twice within an hour (warning), or four times, after which its node leaves it off (critical) |

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
transfer warning, suspended, unsuspended, backup failed, NAT server moved,
password changed, two-factor turned off, API key created, and the invitation
for imported accounts. Nothing is sent until a mail server is set up.

### 11.3 Notifications

Customers see the same events under Notifications in their area, with a read
state per account. Staff with `audit:read` see the newest audit log rows in
their bell instead.

### 11.4 Audit log and job queue

**Audit log** (`audit:read`, in the sidebar) records every write by staff,
customers, API keys, MCP and the billing webhook, with who, from where, the
route, the outcome and the redacted body; filter it by action, kind of
resource and dates. **Job queue** (`ops:read`, `ops:update`) lists jobs 50 a
page, filtered by state and kind and searched by job number, VM or node id,
operation or error; a job opens with its arguments (passwords, keys and
scripts shown as `[REDACTED]`) and the error of every failed try. Retry,
cancel, and the workers per queue (applied at the next panel restart) are
there too.

**Logs** (in the sidebar) gathers three logs, each behind its own permission:
**API requests** (`audit:read`: the last 10,000 requests to the API and the
billing webhook with method, path, answer, time taken, account or API key and
address; kept in memory, so a restart empties it, and query strings are never
recorded. It is the memory of the one panel process that answered: run more
than one panel behind a load balancer and each lists only the requests it
served, so use the load balancer's access log for the whole picture. Changes
are recorded durably in the audit log either way), **Addresses** (`network:read`: every assignment and release of an
address, from the address history) and **Emails** (`settings:read`: the email
log, searchable by recipient or subject).

### 11.5 Graph history

The metrics collector keeps each VM's raw readings for 7 days and, once an
hour, averages each finished hour into `vm_metrics_hourly`, kept for the days
set under Settings, Customer area (8 to 400, default 90). Once a day it
averages each finished day of those into `vm_metrics_daily`, kept two years.
VM graphs offer 30 and 90 days, 1 and 2 years. The first run after an upgrade
averages what the raw readings and hourly averages still hold, so nothing
recorded before the upgrade is lost. Node graphs stay at 7 days.

The collector checks every node at once, each under its own deadline, on its
own clock, so a node going offline is noticed on time however long the rest
takes. The per-node work (status reconcile, storage, address checks) and the
VM readings run up to eight nodes side by side, each node's VMs one after
another. A node whose agent answers slowly holds up only its own VMs; when its
VM pass runs past three minutes, the VMs left are read on the next tick, from
a different starting point each time.

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

**Outbound webhooks of your own**: the same page, **Outbound webhooks**, adds
any number of further receivers (a chat bridge, a monitoring system, your own
automation). Each has its own URL, signing secret (stored encrypted; **Make
one** fills in a random one to give the receiver), the events it wants (VM
lifecycle, backups, alerts, billing events, accounts, or every event) and its
own delivery log with **Send again**. **Send test** posts a `webhook.test`
event at once and says whether it arrived. Seeing them needs `webhook:read`;
adding, changing, testing and resending need `webhook:update`. Events and their
fields: [api.md](api.md#outbound-webhooks-an-operator-adds).

**WHMCS**: `integrations/whmcs` holds a provisioning module (orders, suspends,
resizes, removes VMs, usage, power buttons, and **Open control panel** single
sign-on through the `user.sso` event: a one-time link valid 60 seconds,
customer accounts only, the panel's own two-factor still applies) and an
optional events addon (bills floating IPs, records failed installs and
overage). Installation and a first-run checklist:
[integrations/whmcs/README.md](../integrations/whmcs/README.md). The module's
client area also gives customers their VM's state, power buttons, the console
(through a one-time sign-in link), a new root password, a reinstall with the
systems their plan offers and the last day's graphs; its daily usage update
writes disk, snapshot, backup, address and uptime figures beside traffic.

**Paymenter and FOSSBilling**: `integrations/paymenter` and
`integrations/fossbilling` hold provisioning modules with the same lifecycle
(create, suspend, unsuspend, terminate, change package, single sign-on). They
are tested against stand-ins and the real webhook only, not yet inside those
systems; each README has a first-run checklist.

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
| API keys | Settings, API Keys (staff and customers); Settings, System, API | Keys (`mvk_...`) act as their owner and never more. Each has a scope (**Full access**; **Read only**; **Power only**: list VMs and start, shut down or restart them; **Billing**, staff only: billing, plans, customer accounts and usage, never staff accounts or roles), an optional list of addresses it works from, and an optional expiry. **Own resources only** makes a staff key act as a customer. "Accept API keys" switches them all off. |
| API rate limit | Settings, System, API | Requests per minute per signed-in account or key (default 1200, 0 off); past it, 429 with `Retry-After`. Sign-in, password reset, single sign-on, first-run and node enrollment are limited per address on their own. |
| Single sign-on | Settings, System, Panel | Any OpenID Connect provider (`OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OIDC_BUTTON_LABEL`). Matches an existing account by verified email; creates nobody. Two-factor and allow-lists still apply. |
| Agent allow-list | Node's environment | `AGENT_ALLOWED_PANEL_IPS`: the agent closes connections from anywhere else before TLS. |
| Agent update signing | Node's environment | `AGENT_UPDATE_PUBKEY`: the node installs only agent builds signed with the matching key, kept off the panel host. |
| Trusted proxies | Panel's environment | `TRUSTED_PROXIES`: whose `X-Forwarded-For` is believed for rate limits, allow-lists and the audit log. |

The **Security** page (`settings:read`) checks the install against these and
lists what is weak or off. Stored secrets (agent tokens, console passwords,
mail and DNS credentials, billing secrets, captcha secret, 2FA secrets, backup
destination secrets and encryption keys) are
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

## 16. Day-to-day tools

### 16.1 Acting on several VMs

On **Virtual Machines**, tick VMs (or every VM on the page) and choose an
action: start, shut down, restart (`vm:power`), suspend, unsuspend
(`vm:lifecycle`), lock or unlock against deletion (`vm:update`), move to a plan
(`vm:update` and `billing:update`), add a firewall group (`vm:update` and
`firewall_group:read`), or move to another node (`vm:migrate`). **Check what
happens** shows what the action does to each VM, and which ones it skips and
why, before anything changes; the result lists what happened to each. Up to
100 VMs at once. Moving to a plan works as Apply to VMs does: a running VM
keeps its size until it is stopped, and a VM the billing system bills keeps its
package there until it is changed in the billing system. Moves are planned
together, so two VMs are never both sent to room only one fits in, and run a
few at a time on the long-jobs queue, like a drain.

### 16.2 Tags, delete locks and staff notes

A VM's Settings tab holds its **tags** (up to 10 short labels, filterable on
both VM lists and through the API's `tag` parameter) and its **lock against
deletion**. A locked VM cannot be deleted or reinstalled until it is unlocked.
A customer may lock and unlock their own VM; their lock does not stop the
billing system terminating it, so it cannot be used to keep a server that is
no longer paid for. A staff lock stops every removal, the billing system
included, and only staff lift it. Deleting an account together with its VMs
is refused while any of them is locked; nothing changes until they are unlocked.

**Staff notes** sit on a VM's Settings tab and on a customer's page: staff
only, never shown to the customer, each with its author and time, and every
change recorded in the audit log (`vm:read` or `user:read` to read,
`vm:update` or `user:update` to write).

### 16.3 Announcements

**Announcements** (`announcement:read` to see, `announcement:update` to write) appear at
the top of the customer dashboard while they run, to every customer or only to
the owners of VMs on chosen nodes, regions, plans or VMs and the members of
their teams: the way to warn the customers of one node about its maintenance.
**Preview** shows how many
customers an email would reach and the message. **Email it** queues one email
per customer through the email log, spread at the rate chosen (60 a minute by
default); sending again reaches only those who have not had it, and is refused
while the previous send is still being queued. A **service
notice** reaches everyone in the audience. A **news or offer** skips customers
who unsubscribed and carries an unsubscribe link (it needs the panel's public
address); customers also switch news off on their profile.

### 16.4 Reports

**Reports** (`report:read`) shows capacity by node, storage pool or region over
up to two years (allocated vCPU, memory and disk against what there is, disk
used and the day's average use, recorded hourly into one row a day), and what
each customer holds and transferred in a month. Both download as CSV.

### 16.5 Maintenance mode and the update notice

Settings, **Maintenance**. Maintenance mode (the Owner switches it) lets
customers and the billing system read but not change anything: changes answer
503 with the message shown in their banner, and the billing module reports the
refusal and tries again on its next run. Staff carry on. **Hold jobs** stops new
jobs from starting while running ones finish. `scripts/upgrade-panel.sh` uses
it around every upgrade ([upgrading.md](upgrading.md#2-upgrading-the-panel)).

The **update notice** is off until the Owner turns it on with the GitHub
repository the panel is released from. The panel then reads that repository's
release list once a day (the request carries nothing about the panel) and
shows the Owner a notice when a newer release is out, flagged when a newer
release mentions a security fix, with its notes and a link to the upgrade
instructions.
