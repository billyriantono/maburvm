# Upgrading and rolling back

How to move a panel and its nodes to a new version, how to go back, and which
changes going back does not undo. The scripts and every option are described in
[DEPLOYMENT.md §16](DEPLOYMENT.md#16-releases-upgrades-and-rollback); this page
is the order of work and the cautions.

## 1. Before you upgrade

1. **Read the release notes.** The draft release lists the database migrations
   the version adds. A migration the notes call long may need a higher
   `MIGRATION_TIMEOUT` (default `30m`).
2. **Make sure the panel's off-host backup works** (`panel-backup check`;
   [DEPLOYMENT.md §9](DEPLOYMENT.md#9-backups--restore)). The upgrade script
   takes one more before it starts and stops if it fails.
3. **Pick a quiet time.** A panel restart cancels the background jobs that are
   running; they are tried again when the panel is back, and a long one (a
   backup, an image capture) may start over. The upgrade script therefore turns
   **maintenance mode** on first: customers and the billing system can read but
   not change anything, new jobs are held, and the running ones get
   `--drain-timeout` seconds (default 900) to finish. If some are still running
   then, it switches maintenance off again and stops without changing anything;
   check the Job queue and the Backups page and run it again later, with a longer
   `--drain-timeout`, or with `--no-drain` to restart anyway. VMs keep running
   throughout; customers lose only the panel pages and API while it restarts.
4. **Check disk space on the panel host** for the database dump the script
   writes to `backups/` (the newest five are kept).

## 2. Upgrading the panel

```bash
scripts/upgrade-panel.sh --version v1.3.0   # published images
scripts/upgrade-panel.sh --build            # built from this directory
```

In order, the script tags the running images for rollback, takes the off-host
backup, pulls or builds the new images while the old panel keeps serving,
turns maintenance mode on and waits for running jobs, stops the panel, dumps
the database and checks the dump reads back, then starts the new panel, which
applies pending migrations as it boots, and waits for the health checks. Once
the new panel and web app are healthy it turns maintenance mode off and the
held jobs start. If either does not become healthy, it puts the previous images
back, switches maintenance off in the database and offers to restore the dump.
A panel from before maintenance mode is upgraded without it (the script says
so): its running jobs are cancelled by the restart and retried after it.

Maintenance mode can also be switched by hand: Settings, **Maintenance** (the
Owner), or on the host:

```bash
DC="docker compose -f docker-compose.production.yml --env-file .env.production"
$DC exec -T panel /app/panel maintenance on --hold-jobs --wait 900   # exits 3 if jobs still run
$DC exec -T panel /app/panel maintenance status
$DC exec -T panel /app/panel maintenance off
```

The pre-upgrade dump is taken in maintenance mode; a panel restored from it by
hand starts in maintenance mode with jobs held until it is switched off.

With `MIGRATION_REQUIRE_BACKUP_WITHIN` set (the installer sets `6h`), a panel
refuses to migrate a database with accounts in it unless such a dump was
recorded within that time, so a plain `docker compose up` with a new image
cannot migrate without a way back. To see what is applied and pending:

```bash
docker compose -f docker-compose.production.yml --env-file .env.production \
  run --rm --no-deps panel migrate status
```

After the upgrade:

- Settings, System, **Version** shows the new build.
- Sign in, open a VM's console, and check every node is online.
- Settings, System, **Panel** shows where each setting comes from; nothing in
  `.env.production` needs to change for an upgrade.

## 3. Upgrading the agents

**Upgrade the panel first, then the agents**, one node at a time. A newer panel
keeps working with older agents (features that need the new agent say "needs
an agent update"); an older panel cannot open the console of a VM started
under a newer agent.

Settings, System, Version, **Update** next to each node. Wait until a node
shows "Same as the panel" before the next one. The update restarts no VM. A
node whose agent predates updates from the panel is updated once by hand
([node-setup.md §7](node-setup.md#7-updating-an-agent)). If you sign agent
builds, sign the new panel image's agent before updating
([DEPLOYMENT.md §6](DEPLOYMENT.md#6-agent-deployment-separate-privileged)).

Read the release notes for agent changes that need a step on the node, such as
a new package or a setting in `/etc/maburvm-agent.env`.

## 4. Rolling back

**Prefer restoring the dump taken just before the upgrade.** It returns the
database to exactly what the previous version knew; it loses everything done
since the upgrade. The upgrade script offers this when the new panel fails to
start; to do it later by hand, follow
[DEPLOYMENT.md §16.3](DEPLOYMENT.md#163-rolling-back-by-hand).

Without a usable dump, the new version can undo its own migrations, newest
first. It lists them and changes nothing until `--yes`; run it with the panel
stopped and only after a backup:

```bash
DC="docker compose -f docker-compose.production.yml --env-file .env.production"
$DC stop panel web
$DC run --rm --no-deps panel migrate down 2
$DC run --rm --no-deps panel migrate down 2 --yes
```

Then start the previous images. If agents were updated too and you go back past
the console change, put the previous agents back as well (each node keeps its
last five agents in `/root/maburvm-agent.bak-*`), or consoles of VMs started
under the newer agent stay unreachable.

### What a rollback does not undo

Going back to an older panel, whether by down migrations or by starting old
images, has effects the older version cannot see. Read these before rolling
back across them.

| Change | Going back past it |
|---|---|
| **Credentials encrypted at boot** (agent tokens, console and mail passwords, other stored secrets) | A panel from before encryption at rest cannot read them: it sends nodes tokens they refuse, and consoles and mail stop. Restore the pre-upgrade dump instead; never start such an image on the upgraded database. |
| **Staff roles** (migration 077) | The role column, custom roles, sessions and sign-in history are dropped, and an older panel treats **every staff account as a full administrator**: Support, Billing, Read-only and temporary support accounts included. Before rolling back, suspend or delete staff accounts that must not have full access, and close temporary support access. |
| **Account suspension and billing links** (070) | Suspended customer accounts can sign in again, and VMs lose their billing reference, so customers can delete VMs their billing service still charges for. Re-suspend accounts by hand afterwards. |
| **Billing deliveries** (071) | The record of events sent to the billing system, and the link between billing clients and panel accounts, are dropped. |
| **Address history** (073) | Who held which address when is dropped; abuse reports can no longer be traced back. Export it first if you may need it. |
| **Port forwards per node** (065) | The down migration removes the per-node column but does not put back the fleet-wide unique index: forwards made since may share a port across nodes, and the index would not build. |
| **Moves and drains** (080) | Move and drain records are dropped. The migration keys the agents installed in root's `authorized_keys` on each node stay until removed by hand. |
| **Backup destinations and incremental backups** (082) | An older panel knows only the S3 store in each node's environment: backups kept on an SFTP server or a node directory become unreachable from the panel, and an incremental backup looks like a full one. **Restoring an incremental backup on an older panel produces a broken disk.** Take a full backup of every VM you care about after rolling back. |
| **Firewall groups and NAT VPS** (083) | Groups, NAT port blocks and hostnames are dropped. A VM that relied on a group's rules loses them at its next network change; NAT VMs lose their public ports and hostnames. Do not roll back past it while either is in use. |
| **API key limits and outbound webhooks** (086) | Keys lose their scope and address list, so the down migration switches off every **Power only** or **Billing** key and every key limited to some addresses rather than let it act as a full key from anywhere. Make replacements after rolling back. The outbound webhooks and their logs are dropped. Error answers go back to `error` as a string. |
| **Admin console extras** (085) | Tags, delete locks, hypervisor groups, staff notes, announcements, marketing opt-outs and the daily capacity history are dropped. A locked VM can be deleted again; plans place VMs on any node. |
| **Panel settings saved on Settings, System, Panel** | Read only by this version and later. On an older panel the environment variables apply again, so keep them in `.env.production` until you no longer expect to go back. |
| **Data a newer version wrote** | Any down migration that drops a table or column drops what was stored there. |

## 5. After a rollback

- Check every node is online and a console opens.
- Compare each node's `virsh list --all` with the panel's VM list when you
  restored a dump: VMs created after the dump still run but the panel does not
  know them.
- Re-check staff accounts and suspended customers (above).
- Tell the people who use the API: answers may have lost fields that the newer
  version added (`meta` on lists, the `error` object on errors).
