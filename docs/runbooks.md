# Runbooks

What to do when something goes wrong. Each runbook says how the problem shows,
what to check, how to fix it and what to check afterwards. Commands on the
panel host run in the repository directory with:

```bash
DC="docker compose -f docker-compose.production.yml --env-file .env.production"
```

and database queries with `$DC exec postgres psql -U maburvm -d maburvm`
(use your `POSTGRES_USER` and `POSTGRES_DB`). Read before you write: run a
`SELECT` with the same `WHERE` first.

1. [Node down](#1-node-down)
2. [Restoring the panel from a backup](#2-restoring-the-panel-from-a-backup)
3. [Failed backups and broken backup chains](#3-failed-backups-and-broken-backup-chains)
4. [Failed move to another node](#4-failed-move-to-another-node)
5. [Storage pool full](#5-storage-pool-full)
6. [Abuse report about an address](#6-abuse-report-about-an-address)
7. [Lost access to the Owner account](#7-lost-access-to-the-owner-account)
8. [A VM that keeps crashing, or a guest agent that does not answer](#8-a-vm-that-keeps-crashing-or-a-guest-agent-that-does-not-answer)
9. [Shared pool not usable on a node](#9-shared-pool-not-usable-on-a-node)

## 1. Node down

**Shows as:** a "Node offline" or "Node not reporting" alert; the node offline
on the Nodes page; its VMs in "State unknown"; `maburvm_node_up` 0. Customers'
VMs may well still be running: the panel only knows it cannot reach the agent.

**Check, in order:**

1. **Is the host up?** Ping it and SSH to it from outside. If not, use your
   provider's console or IPMI. VMs the panel created are set to start with the
   host, and the agent restores their network rules and private networks after
   a reboot.
2. **Is the agent running?** On the node: `systemctl status maburvm-agent` and
   `journalctl -u maburvm-agent -n 200`. Also `systemctl status libvirtd` (or
   `virtqemud`) and `df -h /` (a full root filesystem stops libvirt and the
   agent).
3. **Can the panel reach it?** From the panel host: `nc -vz <node address>
   50051`. A firewall change on the node or in between, or a new outgoing
   address for the panel, breaks this.
4. **Does the agent accept the panel?** In the agent's log:
   - connections refused before TLS: the panel's outgoing address is not in
     `AGENT_ALLOWED_PANEL_IPS`. Add it in `/etc/maburvm-agent.env` and restart
     the agent;
   - unauthenticated calls: the token differs from the panel's (after
     **Regenerate token**, say). Put the token from the node's page in
     `AGENT_AUTH_TOKEN` and restart the agent;
   - in the panel's log (`$DC logs panel | grep -i 'fingerprint mismatch'`), a
     certificate that does not match the pinned one: the agent's
     `/var/lib/maburvm/agent-cert.pem` was lost or replaced. **Only if you know
     why** (the node was reinstalled, the directory was wiped), compare the
     node's fingerprint with the one the panel holds and clear the pin, so the
     panel trusts the new certificate on its next connection:

     ```bash
     # On the node
     openssl x509 -in /var/lib/maburvm/agent-cert.pem -outform der | sha256sum
     # On the panel host
     $DC exec postgres psql -U maburvm -d maburvm -c \
       "SELECT name, cert_fingerprint FROM nodes WHERE name = '<node>'"
     $DC exec postgres psql -U maburvm -d maburvm -c \
       "UPDATE nodes SET cert_fingerprint = '' WHERE name = '<node>'"
     ```

     A certificate that changed without a reason you know of is someone else
     answering on the node's address: do not clear the pin.
5. **Is the agent too old?** "Node not reporting" with the port open can be an
   agent older than the panel. Update it ([node-setup.md §7](node-setup.md#7-updating-an-agent)).

**When it is back:** the node turns active on the first answered check and the
alert closes after the settle time. Open a VM console, and look at the node's
host checks.

**When the node is gone, or will be for too long:**

Moves and drains need both nodes alive. Use **Recover VMs on other nodes** on
the node's page (`node:recover`;
[admin-guide.md §10.1](admin-guide.md#101-recovering-the-vms-of-a-node-that-went-down)).

1. **Make sure the node is off.** Power it off at the provider's console or
   IPMI, or cut it off from its storage and its VM network. A node that is only
   unreachable from the panel may still run its VMs; recovering one then runs
   it twice, which destroys a disk on shared storage and breaks the address of
   any VM. If you cannot make sure, do not recover.
2. For each VM choose a node. A VM on shared storage is defined on the other
   node on its own disks, with nothing lost. Any other VM is restored from its
   newest backup; what it wrote since is on the dead node only. A VM without
   either is listed with the reason.
3. A node in another site does not carry the VM's address pool: tick **Give it
   a new address**, and tell the customer. A VM in a private network or with
   several network cards is not recovered this way: use **Restore as new VM**
   from its backup (VM page, Backups).
4. Type the node's name and start. Each VM is a job on the card; a failed one
   says why and changed nothing unless it says the records already moved.

**"is open in a running VM":** the node is not down, or another node already
runs the VM. Stop and find out where it runs before anything else. One
exception: on an NFSv3 export the server keeps a dead client's locks until
that client comes back. If the node is certainly powered off, release its
locks on the NFS server (on Linux, `echo <node address> >
/proc/fs/nfsd/unlock_ip`), then recover again. With NFSv4 they expire by
themselves after the lease time, about 90 seconds.

**"a backup of the VM was under way ... and left <file>":** the node died in
the middle of a backup, while the VM wrote to a temporary overlay beside its
disk. On a member node, compare the two files' modification times. If the
overlay is the newer one it holds the VM's latest writes: commit it into the
disk with `qemu-img commit <overlay>`, then remove it. If the disk is newer
the backup had already finished: remove the overlay. Then recover again.

**"is built on ..., which is not in the shared pool":** the VM's disk is an
overlay on a backing image outside the pool, usually an older node's own
template copy. Another node would open a different file at that path. Recover
the VM from its backup onto a node that is not a member of the pool.

**"already has a definition of this VM, left from an earlier move":** the
chosen node still has the VM defined from a time it lived there. On that node,
`virsh undefine <vm id>` (never with `--remove-all-storage`), then recover
again.

**When a recovered-from node comes back:**

1. If you can reach its console first, keep the old copies from starting:
   unplug its VM network, or delete the recovered VMs' files from
   `/etc/libvirt/qemu/autostart/` before libvirt starts.
2. The panel tells the agent which VMs live elsewhere at the first answered
   check; from then on the node will not start them. An alert **"runs on
   <node> although it was recovered onto <node>"** means the old copy started
   before that. Decide which copy to keep; normally the recovered one. Power
   the old copy off from **Recovery leftovers** on the node's page (Remove,
   which offers to power it off), or with `virsh destroy <vm id>` on the node.
3. For a VM restored from a backup, copy what you still need off the old disk
   (the leftovers list shows its path), then **Remove** the old copy. For a VM
   on shared storage, Remove deletes only the old definition. Until the old
   copy is removed, the VM cannot be moved or recovered back onto that node.
4. An alert that the node **"may run VMs that were recovered elsewhere"** is
   an agent too old to be told: update it now
   ([node-setup.md §7](node-setup.md#7-updating-an-agent)), and check with
   `virsh list` that none of the VMs on the card runs there.

Do not define and start a VM on another node by hand while its old node might
still be running it.

## 2. Restoring the panel from a backup

**When:** the panel host is lost, its database is damaged, or you are
rehearsing (do it at least once a quarter, on a scratch machine).

You need a host with Docker and the Compose plugin, this repository at the
version that took the backup **or newer**, the backup file and the age private
key. The full procedure is [DEPLOYMENT.md §9.3](DEPLOYMENT.md#93-restore-onto-a-fresh-host);
in short:

```bash
rclone copy offsite:my-bucket/maburvm-panel/maburvm-panel-<time>.tar.age .
scripts/panel-restore.sh maburvm-panel-<time>.tar.age maburvm-backup.key
```

Then, before letting anyone in:

1. **Switch the old host off for good** if it may come back: two panels would
   drive the same nodes. Delete the age key from the new host.
2. **Move DNS and the TLS proxy** to the new host
   ([DEPLOYMENT.md §7](DEPLOYMENT.md#7-tls--reverse-proxy)).
3. **Let the new panel reach the nodes:** if its outgoing address changed, add
   it to `AGENT_ALLOWED_PANEL_IPS` on every node and restart each agent, and
   allow it on port 50051 in any firewall.
4. **Sign in with an account that has two-factor.** An accepted code proves
   `secrets.json` matches the database.
5. **Check every node comes online.** ISOs uploaded to the panel are not in
   its backups (the nodes keep their copies): upload any you will add to new
   nodes again from the ISOs page.
6. **Reconcile with the nodes.** The panel knows the fleet as it was at backup
   time, up to a day earlier. On each node compare `virsh list --all` with the
   panel's VMs: a VM created after the backup still runs but is unknown to the
   panel (and its address looks free; adopt it with the node's **Import**); a
   VM deleted since is listed but gone; changes since are back at their old
   values; jobs queued at backup time run again.
7. **Billing:** if the panel's address changed, update it in the billing system
   (for WHMCS, the server's hostname), and check Settings, System, API, Billing
   deliveries for events that failed while the panel was down.

**If `secrets.json` is lost** and only a database dump survives, the database
restores but nothing encrypted in it can be read. Then: every node needs a new
token (node page, **Regenerate token**, then `AGENT_AUTH_TOKEN` on the node);
every account with two-factor needs it reset (user page; your own with the
tool in [runbook 7](#7-lost-access-to-the-owner-account)); re-enter the mail
password, DNS provider key, billing secrets, captcha secret, backup destination
credentials and alert webhook; kept initial passwords are gone; everyone signs
in again.

## 3. Failed backups and broken backup chains

**Shows as:** a "Backup failed" alert and email to the owner; the VM failing or
stale on the **Backups** page; a restore that refuses with "an earlier backup of
its chain is failed" (or deleted).

**Check:**

1. The reason on the failed backup (VM page, Backups). A backup that will fail
   the same way again (no destination, refused credentials, missing VM or disk)
   is not retried.
2. **Test destination** (Settings, System, Backup) from the panel and the VM's
   node. A changed SFTP host key is refused on purpose: check with the provider
   before pasting the new one.
3. Room on the node. An export is written beside the source disk and needs the
   disk's size plus 20 GiB free; an incremental copy needs the disks' size plus
   the largest one. On the node: `df -h <pool path>` and
   `journalctl -u maburvm-agent | grep -i backup`.
4. For a chain, the note on each backup says why it was full or incremental.
   The panel never builds on a chain it cannot use: a deleted or failed parent,
   a changed destination, a move, replaced disks, a lost checkpoint (a VM that
   was powered off rather than shut down) or a chain at its longest all start a
   new one with a full backup.

**Fix:**

- Fix the cause (credentials, space, the mount on the node), then **take a
  backup by hand**: manual backups are always full, so this gives a restore
  point that depends on nothing else.
- A chain whose parent failed or whose file is missing in the destination
  cannot restore its later backups. Restore from a full backup or a newer chain
  instead, then delete the broken chain's backups, **newest first** (the panel
  refuses to delete a backup a kept one builds on).
- A delete that reports the file was not removed (the destination was
  unreachable) can be repeated once the destination answers.

**Afterwards:** restore the new full backup as a new VM on a test node, check it
boots, then delete the test VM.

## 4. Failed move to another node

**Shows as:** the move failed on the VM's Settings tab (status, phase, error),
a "Job failed" alert, or a drain report with VMs left behind and why.

**What a failed move leaves:** the VM on its original node, as it was; the
files it prepared on the destination removed. The VM changes node in the panel
only once it has been seen running on the destination.

**Check:**

1. The error on the move. "Needs an agent update" means one of the two agents
   is older: update it (VMs on logical volumes, or moving onto an LVM pool,
   need the current agent on both nodes). A refusal listed by **Check**
   (snapshots, an ISO, floating IPs, a NAT VM, no room, a CPU mismatch for a
   live move) needs that cause removed, or an offline move.
2. The job queue: a move interrupted by a panel restart is picked up by the
   next try and follows what the nodes are doing; let it finish.
3. On both nodes, `virsh list --all | grep <vm id>` and `virsh domstate
   <domain>`. Expected after a failure: running (or shut off) on the original
   node only.
4. Live moves need TCP 49152-49215 open between the nodes and SSH between them
   (`journalctl -u maburvm-agent` on both nodes names the connection that
   failed).

**Fix:**

- Retry the move once the cause is gone, or move it offline.
- **"<VM> moved, but its old copy on <node> was left"**: the VM runs on the
  destination and the panel shows it there; only the clean-up failed. On the
  old node, check the domain is shut off and not running anywhere else
  (`virsh domstate <domain>`), then `virsh undefine <domain>`; its old disk
  files and volumes then show under Storage, Orphaned disks, where they are
  deleted after a check. Never start the old copy: two copies
  of one VM with the same address corrupt each other's view of the network and
  the customer's data diverges.
  **If the VM's disks are in a shared pool, the files on the old node are the
  disks the VM is running on now.** Undefine only: never
  `virsh undefine --remove-all-storage`, and never delete the files by hand.
  The orphaned-disk list does not offer them.
- A VM defined on both nodes after an interrupted move: leave the running copy
  alone, `virsh autostart --disable <domain>` on the other, and let the move's
  job finish before removing anything.

## 5. Storage pool full

**Shows as:** a "Storage pool" alert (warning at the pool's level, critical at
the panel-wide level); VMs on the pool **paused** (a guest that cannot write is
paused by QEMU); new orders refused on that node; backups failing for lack of
room.

**Stop it getting worse:**

1. On the node's page, **Pause new VMs**.
2. Set the disk overcommit ratio (Settings, System, Quotas) above what the
   Storage page shows as allocated on the other pools but below this pool's
   figure, so no new disk lands here.

**Free space:**

- Storage, the pool, **Volumes**: volumes a deleted VM left behind are listed
  there; delete them.
- Storage, **Orphaned disks**: files and volumes in the node's pools that no VM
  uses (left by failed creates, restores, moves and removed VMs), deleted after
  a check.
- Move a disk to another pool of the node from its VM's page (Disks, Move),
  while the VM runs.
- When the agent's `TEMPLATE_CACHE_DIR` is on the same filesystem: set
  `TEMPLATE_CACHE_MAX_GB` in the agent's settings, or remove unused downloads
  under it by hand. Images you placed there yourself are yours to remove.
- Old snapshots of the VMs on the pool (each holds its changed blocks in the
  disk file).
- Move VMs to another pool or node ([admin guide §10](admin-guide.md#10-moving-vms-and-draining-nodes)).
- For an **LVM thin pool**, the metadata area fills independently of the data:
  the Storage page shows both. Extend the thin pool or its metadata with
  `lvextend` on the node.

**Resume the VMs:** on the node, `virsh list --all` shows them as `paused`;
`virsh resume <domain>` each once there is room, then check them from the panel.
Expect the guests to log disk errors; ask their owners to check their file
systems.

**Afterwards:** set each pool's alert level so the warning comes with time to
act, set the disk overcommit ratio, and keep an eye on the Storage page's
allocated figure.

A disk move that failed or was cancelled leaves the disk where it was; a copy
it had made shows under Orphaned disks. A VM whose offline disk copy is
running cannot be started until it ends: wait, or cancel the move on its page.

## 6. Abuse report about an address

**Shows as:** a report from an upstream provider, a blocklist, or IP
Reputation, naming an address and usually a time.

1. **Who held it then:** Abuse, **Who held an address**: the address and the
   time from the report (your local time). The answer lists every VM that held
   it then with its owner, newest first, including floating IPs and IPv6
   prefixes. The same lookup is `GET /api/v1/ip-history?address=...&at=...`.
2. **Is it still going on?** The Abuse page ranks guests by new connections
   and packets per second; a scanning or flooding VM is near the top, and its
   episodes on the timeline carry the readings. IP Reputation shows whether
   your addresses are listed.
3. **Stop it:** **Quarantine** the guest (Abuse page) to cut its traffic while
   it keeps running, so the owner can still reach it on the console;
   or suspend the VM (VM page) or the whole account (user page). For spam, block
   outbound mail on the VM (Firewall tab).
4. **Tell the owner** and keep the evidence: the history lookup, the report,
   and the audit log rows for the VM.
5. When cleaned, lift the quarantine (**Restore**, or **Release** on its
   timeline episode) and ask the reporter or blocklist to delist.

**A guest is under attack** (packets in far above the others, other guests on
the node slow): **Null-route** the attacked address from the Abuse page for a
while; the node drops traffic to it and lifts the null-route by itself when the
time is up. That protects the node, not the link into it: if the link is full,
ask your upstream to blackhole the address. To have the panel do this on its
own, set the inbound packets reading to Null-route in the Abuse policy. The
null-routes in force on a node are listed in `/etc/maburvm/nullroutes`.

**The address is a node's own address:** NAT VMs and port forwards share it.
For inbound abuse, the reported port identifies the VM: on the panel host,

```bash
$DC exec postgres psql -U maburvm -d maburvm -c "
  SELECT v.hostname, u.email, b.start_port, b.start_port + b.port_count - 1 AS end_port
  FROM nat_port_blocks b JOIN vms v ON v.id = b.vm_id JOIN users u ON u.id = v.user_id
  JOIN nodes n ON n.id = b.node_id
  WHERE n.name = '<node>' AND <port> BETWEEN b.start_port AND b.start_port + b.port_count - 1"
```

Outbound traffic from NAT VMs leaves from the node's address with any source
port; use the Abuse page's connection ranking for that node to find the VM.

## 7. Lost access to the Owner account

The Owner is the founding administrator. Other staff, Administrators included,
cannot change the Owner's password or two-factor from the panel, by design.

1. **Lost the authenticator only:** sign in with one of the backup codes shown
   when two-factor was turned on; each works once. Then turn two-factor off and
   on again under Profile to get a new device and new codes.
2. **Forgot the password:** **Forgot password** on the sign-in page, if
   outgoing mail works.
3. **Locked out after failed sign-ins:** wait the lockout time, or reset the
   password as above.
4. **None of that works:** reset the account on the panel host with
   `cmd/reset-admin-password`, which sets a new password, can turn two-factor
   off and lifts the sign-in lock. It is not in the panel image; build it from
   the repository at the panel's version and run it beside the database:

   ```bash
   docker run --rm -v "$PWD":/src -w /src -e CGO_ENABLED=0 golang:1.25 \
     go build -o reset-admin-password ./cmd/reset-admin-password
   NET=$(docker network ls --format '{{.Name}}' | grep '_data$')
   docker run --rm -it --network "$NET" -v "$PWD":/src:ro -w /src \
     -e DB_HOST=postgres -e DB_USER=maburvm -e DB_NAME=maburvm -e DB_SSL_MODE=disable \
     -e DB_PASSWORD_FILE=/src/deploy/secrets/postgres_password \
     -e MABURVM_DATA_DIR=/tmp/scratch \
     alpine:3.24 ./reset-admin-password --email owner@example.com --clear-2fa
   rm reset-admin-password
   ```

   It asks for the new password twice (at least 12 characters with upper and
   lower case, a digit and a symbol). `MABURVM_DATA_DIR` points at a throwaway
   directory so the tool never touches the panel's `secrets.json`.
5. **Locked out by the account's IP whitelist:** clear it in the database:

   ```bash
   $DC exec postgres psql -U maburvm -d maburvm -c \
     "UPDATE users SET ip_whitelist = '[]'::jsonb WHERE email = 'owner@example.com'"
   ```

**Afterwards:** sign in, turn two-factor on again and store the backup codes
away from the authenticator, look at the account's sign-in history and the
audit log for anything you did not do, and end sessions you do not recognise.
If you suspect the account was taken over rather than forgotten, change the
billing webhook key and secret, the metrics token and each node's agent token
too.

## 8. A VM that keeps crashing, or a guest agent that does not answer

**How it shows.** The alert *A VM keeps crashing*: a VM set to restart on crash
crashed twice within an hour (warning), or four times, after which its node
left it off (critical). Separately, a VM whose guest agent stops answering shows
its addresses from the node's DHCP leases and ARP table, and the agent's log
says nothing alarming: its status calls give the guest agent two seconds and
then leave it alone for one to fifteen minutes, so the panel's status, graphs
and transfer counters keep coming.

**Check.**

1. The VM's console: a kernel panic, a boot loop, out of memory.
2. On the node, `journalctl -u maburvm-agent | grep <vm id>` shows each
   restart ("crashed and was started again") and when it gave up.
3. `virsh domstate --reason <vm id>`: `shut off (crashed)` is a crash the
   agent sees; `shut off (destroyed)` is a stop, which it never restarts.
4. For a guest agent: `virsh qemu-agent-command <vm id> '{"execute":"guest-ping"}' --timeout 5`.

**Fix.** Repair the guest (rescue mode if it does not boot), then start it from
the panel: a VM the node gave up on stays off until someone starts it, and the
alert closes an hour after its last crash. To stop the restarts while you work
on it, clear **Restart on crash** under its Hardware (it takes effect at the
next power off and on; the node forgets the crashes at once). A hung guest
agent is the guest's: restart `qemu-guest-agent` inside it. A password reset
while it does not answer works with the VM stopped: it is then written onto the
disk.

**Afterwards.** The VM page shows it running, and the alert resolves.

## 9. Shared pool not usable on a node

**Shows as:** a critical "Shared pool <name> is not usable on <node>" alert;
the node marked **Not usable** on the pool's card on the Storage page, with the
reason; a move refused with "does not have the shared storage" or "not looking
at the same storage".

**What it means:** the node's path for the pool is not the shared storage
right now. Usually the mount is gone, and the path is an empty directory on
the node's own disk. The node writes nothing there while this lasts: new disks,
moves, backup restores, reinstalls and rescue onto the pool are refused on that
node. VMs already running there
keep their open disks only if the storage itself is still reachable; a VM
whose storage is gone is paused or fails I/O.

**Check, on the node:**

1. `findmnt <path>` (for an NFS pool the path is
   `/var/lib/maburvm/pools/<pool id>`, shown on the card). Nothing printed
   means it is not mounted.
2. For an NFS pool: `systemctl status 'var-lib-maburvm-pools-*.mount'` and
   `journalctl -u '<that unit>'` name the mount error; `showmount -e <server>`
   and a ping tell whether the server is reachable.
3. `ls -la <path>/.maburvm-shared-pool`: the marker file must be there and
   hold the pool's id. If the storage is mounted but the marker is missing,
   someone removed it; `cat` it on a healthy member and write the same content
   back.

**Fix:** bring the mount back (`systemctl start '<unit>'` for an NFS pool, your
own mount otherwise). The node reports the pool usable again within a minute
and the alert closes; **Check now** on the card confirms every node sees the
same storage. If files were written into the empty directory while it was
unmounted (by hand; the agent refuses to), move them away before mounting, or
the mount hides them.

**Do not** remove and re-add the node to "reset" it while VMs on it have disks
in the pool: removal is refused for that reason.
