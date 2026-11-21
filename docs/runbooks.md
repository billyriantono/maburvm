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

**When the node is gone for good:**

1. Pause the node (it is offline already, so nothing new lands there).
2. For each VM, **Restore as new VM** from its newest backup onto another node
   (VM page, Backups; `admin:access`). The new VM keeps the owner, size, limits
   and firewall rules and gets an address from the new node's pools, so tell the
   customer their address changed. Floating IPs and private networks of the
   dead node cannot move.
3. When every VM that can be restored is, delete the old VMs and the node.

Moves and drains need both nodes alive; they cannot rescue VMs from a dead
node.

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
2. **Is it still going on?** The Abuse page ranks guests by new outbound
   connections; a scanning or spamming VM is near the top. IP Reputation shows
   whether your addresses are listed.
3. **Stop it:** **Quarantine** the guest (Abuse page) to cut its traffic while
   it keeps running, so the owner can still reach it on the console;
   or suspend the VM (VM page) or the whole account (user page). For spam, block
   outbound mail on the VM (Firewall tab).
4. **Tell the owner** and keep the evidence: the history lookup, the report,
   and the audit log rows for the VM.
5. When cleaned, lift the quarantine and ask the reporter or blocklist to
   delist.

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
