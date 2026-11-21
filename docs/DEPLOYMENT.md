# MaburVM Production Deployment Guide

> **Status: hardening guidance, NOT a certification.** This document describes a
> safer, explicit production entrypoint (`docker-compose.production.yml`). It does
> **not** claim the system is production-certified. You are responsible for
> reviewing every image tag, supplying real secrets, terminating TLS, backing up
> data, and deploying the KVM agent on each hypervisor host.

Other guides: [node setup](node-setup.md), [administrator guide](admin-guide.md),
[user guide](user-guide.md), [upgrading](upgrading.md), [runbooks](runbooks.md)
and the [API](api.md) (the full reference is served at `/docs/api` on every
panel).

## 1. Scope & files

| File | Purpose |
|------|---------|
| `docker-compose.production.yml` | The only production composition. **Not** auto-loaded. |
| `.env.production.example` | Template env (empty guarded values; no working creds). |
| `Dockerfile.panel` | Panel image (targeted `COPY`, no `COPY . .`); entrypoint starts as root, drops to non-root `maburvm`. |
| `Dockerfile.web` | Web image; production build enforces non-localhost `API_BASE_URL`. |
| `.dockerignore` | Keeps secrets/host artifacts out of build contexts. |
| `docker/panel-entrypoint.sh` | Chowns `/data`, resolves `*_FILE` secrets, then `su-exec maburvm`. |
| `docker/minio-init.sh` | One-shot `mc` initializer: bucket + immutable versioned policy (`maburvm-bucket-rw-v1`) + restricted user (`maburvm-app-v1`) + access proof. |
| `docker/panel-backup/` | Image and script behind the `panel-backup` service: nightly encrypted off-host backup of the panel (§9). |
| `scripts/panel-restore.sh` | Rebuilds a panel from a backup on a fresh host, or as a rehearsal (§9). |
| `scripts/install-panel.sh` | One-command install on a new host: checks it, writes `.env.production` and the secret files, starts the stack, prints the first-administrator link (§3). |
| `scripts/upgrade-panel.sh` | Upgrade with a backup first and an automatic rollback (§16). |
| `.github/workflows/release.yml` | On a version tag: publishes the panel and web images and the agent binaries, drafts the release (§16). |
| `docker-compose.rehearsal.yml` | Overlay used only by a restore rehearsal: cuts the restored panel off from nodes and the internet. |
| `docs/DEPLOYMENT.md` | This guide. The other guides are listed in `docs/README.md`. |
| `deploy/secrets/` | Host secret files (created by you, never committed). |

This composition does **not**:
- publish PostgreSQL or MinIO host ports (they are on the `data` `internal: true` network),
- publish the panel API (it stays internal behind `web`),
- give the panel MinIO root credentials (it gets a restricted S3 user),
- include Mailpit,
- start the privileged local libvirt agent (deploy it separately — see §6).

It **does** publish only the `web` UI, and only on loopback (`127.0.0.1:WEB_PORT`)
by default; an external TLS reverse proxy must target `web`.

## 2. Why a separate, explicit file

`docker compose up` (with no `-f`) auto-merges `docker-compose.yml` **and**
`docker-compose.override.yml`. The README previously referenced
`docker compose --profile production`, which does not exist in this repo and
would silently fall back to the default dev composition. The production file is
therefore invoked **only** with an explicit `-f` and `--env-file`:

```bash
docker compose -f docker-compose.production.yml --env-file .env.production up -d
```

No `version:` key is used (the Compose Specification deprecated it; recent
Docker Compose warns when it is present).

## 3. Environment setup

### 3.0 One-command install

On a new host with Docker and the compose plugin, as root, from the source
archive of a release tag (or a checkout):

```bash
scripts/install-panel.sh --url https://panel.example.com \
  --version v1.2.0 --image-prefix ghcr.io/OWNER/maburvm
# or, to build the images on this host from the directory instead:
scripts/install-panel.sh --url https://panel.example.com
```

It refuses to run where it should not: on a host that runs virtual machines
(unless `--force`), with less than 2 GiB of memory or 10 GiB of free disk, with
port 3000 or 8081 taken, or where an install already exists. It writes
`.env.production` and the two secret files below with random values, starts the
stack, checks the panel generated its key file (`secrets.json`, §9) and prints a
link that creates the first administrator:

```
https://panel.example.com/first-run#token=<one-time token>
```

The token lives in `first-run-token` on the panel data volume only while the
panel has no administrator; the link works once. Run the script again and it
changes nothing (or finishes an install that stopped half way). It sets
`MIGRATION_REQUIRE_BACKUP_WITHIN=6h` (§16). Then put your TLS proxy in front
(§7) and configure the nightly backup (§9).

### 3.1 By hand

```bash
cp .env.production.example .env.production
# edit .env.production — replace every EMPTY mandatory value (MINIO_ROOT_USER,
# MINIO_ROOT_PASSWORD, PANEL_PUBLIC_URL, ALLOWED_ORIGINS) with a real one, and
# create the two host secret files below.
```

The template ships **empty guarded values** (no `CHANGE_ME*` placeholders). The
production compose uses `${VAR:?...}` guards for mandatory values
(`MINIO_ROOT_USER`, `MINIO_ROOT_PASSWORD`, `PANEL_PUBLIC_URL`, `ALLOWED_ORIGINS`).
A blank value aborts `up` instead of deploying with a placeholder.

## 4. Secret files (host-side bind mounts — NOT a managed store)

Two secrets use Compose `secrets:` file mounts:
`/run/secrets/postgres_password` (DB password) and
`/run/secrets/s3_restricted_password` (the RESTRICTED S3 user `maburvm-app-v1`'s
password, created by `minio-init`). DB identity (`POSTGRES_USER`/`POSTGRES_DB`)
and MinIO **root** creds stay as ordinary env (root creds are confined to
`minio`/`minio-init` and are never handed to the panel).

Compose `secrets:` are **simple file mounts** — they do NOT enforce uid/gid/mode
and are NOT a substitute for a managed secret store. The source files stay on the
host.

Practical source-file instruction (secure, simple):

```bash
mkdir -p deploy/secrets
install -m 0400 -o root /path/to/password.txt deploy/secrets/postgres_password
install -m 0400 -o root /path/to/s3pass.txt   deploy/secrets/s3_restricted_password
```

- Mode `0400`, owned by root (or the user running `docker compose`).
- The container's **root** entrypoint reads it before dropping to the app UID, so
  it does NOT need to be world-readable. Do NOT use a `0444` world-readable
  workaround.
- Prefer writing the secret precisely. The official postgres entrypoint reads the
  file via command substitution, which strips a terminal newline, and our panel
  app resolver also trims surrounding whitespace — so a trailing newline is **NOT**
  preserved as part of the secret. The cleanest approach is still to write exactly
  the secret you intend (use `printf '%s'` if you generate it inline).
- Never commit `deploy/secrets/`.

> **Local-Compose mitigation, not isolation.** Source secret files remain host-side
> bind mounts. Phase 1 / operational secret-manager integration (Vault, Docker
> secrets swarm mode, cloud KMS, etc.) is tracked separately and is not claimed
> here.

> **Secret model is honest, not "all file-backed".** In this self-contained model
> the PostgreSQL password and the panel's restricted S3 password are file mounts.
> MinIO **root** creds and `PANEL_PUBLIC_URL` are direct env because MinIO does not
> reliably support `*_FILE` indirection for root creds across versions — and we do
> NOT use unverified secret-file behavior for MinIO root configuration just to
> optimize it.
>
> **Remote DB / external S3 are NOT env-only switches in this file.** This model
> hardcodes `DB_HOST=postgres` and a scheme-less panel `S3_ENDPOINT=minio:9000`
> with `S3_FORCE_HTTP=true` and `S3_USE_PATH_STYLE=true` (the shared
> backup-transport consumers — the agent constructor in
> internal/agent/server/node_agent.go and the River worker in
> internal/shared/queue/workers.go, which read STORAGE_*/S3_* — expect a
> scheme-less endpoint and path-style HTTP for MinIO). Using a managed
> Postgres or an external S3 bucket requires a *separately authored Compose model*
> (different `DB_HOST`/`S3_ENDPOINT`/creds/flags, the `postgres`/`minio` services
> omitted or replaced) and its own validation — not a flag toggled here.

### 4.1 MinIO restricted access, rotation, and revocation

`minio-init` (fresh Phase 0 model) bootstraps a dedicated, versioned identity:

- **Restricted user:** `maburvm-app-v1` (never the MinIO root). The init aborts
  before any mutation if `MINIO_S3_USER` equals the root identity.
- **Immutable policy:** `maburvm-bucket-rw-v1`, bucket-scoped, granting exactly
   the operations the backup-transport clients use: `s3:ListBucket`,
  `s3:GetBucketLocation`, `s3:GetObject`, `s3:PutObject`, `s3:DeleteObject`,
   `s3:AbortMultipartUpload`, `s3:ListMultipartUploadParts`. Creation is
   fail-closed: `mc admin policy info` exits nonzero for BOTH "policy absent" and
   auth/transport/server failures, so testing its exit code would be fail-**open**.
   The init therefore runs a successful `mc admin policy list` first (aborting
   explicitly if the list itself fails) and **exactly scans** the listed names for
   `maburvm-bucket-rw-v1`. Only after a successful list proves the name is absent
   is the policy created **unmasked** (real errors abort via `set -e`). If the
   policy is already present it is **retained, never overwritten**.
- **User reconcile (upsert):** `mc admin user add` runs on **every** init. The
  authoritative `user add` is a PUT/upsert in this `mc` generation, so a rotated
  `s3_restricted_password` is applied deterministically — i.e. rotating the file
  secret and re-running init reconciles the same identity's credentials before
  the panel starts.
- **Access proof:** after setup, `minio-init` configures a separate non-root alias
  with the restricted creds and verifies real access (`mc ls`, plus a write/delete
  probe with cleanup). It never prints secret values.

**Rotation / revocation (operator-managed, not auto):** this Compose model
guarantees the *current* dedicated identity/policy bootstrap, not arbitrary prior
IAM cleanup. We deliberately do **not** `user remove` (non-idempotent) or
`user disable` (disable of an already-disabled user is unverified and masking that
failure is unacceptable). For a clean rotation:

1. **Credential rotation (same identity):** replace the contents of
   `deploy/secrets/s3_restricted_password` and re-run `minio-init` — `user add`
   upserts the new secret for `maburvm-app-v1`.
2. **Policy evolution:** create a *new* immutable versioned policy
   (`maburvm-bucket-rw-v2`, …) and a staged operator-controlled cutover
   (attach new policy, verify, then detach old). Never delete/recreate the live
   `v1` policy in place.
3. **Retiring a legacy/experimental identity:** schedule an operator-reviewed
   maintenance window. Disable/remove only after confirming no panel instance or
   job still references it. Additive `policy attach` cannot safely prove away
   unknown extra manual policy associations from a prior deployment — those
   require explicit operator review.

> **Runtime proof debt (mandatory, not done here):** a real Docker run is required
> to validate the `minio-init` entrypoint bypass, actual `mc` behavior, current +
> rotated credential access, and that no MinIO/Postgres host port is exposed. None
> of that is executed in authoring (Docker unavailable).

## 5. Panel runtime hardening

- `Dockerfile.panel` creates an unprivileged `maburvm` user (UID 10001) but has
  **no `USER` directive**, so the entrypoint starts as **root**.
- `docker/panel-entrypoint.sh` runs as root, then:
  1. ensures `/data` is owned by `maburvm` (restricted to `/data` only — an
     unexpected `MABURVM_DATA_DIR` is rejected rather than recursively chowned),
   2. resolves explicitly supported `*_FILE` secret inputs
      (`DB_PASSWORD`, `S3_SECRET_KEY`, `JWT_SECRET_KEY`, `AES_KEY`) before
      dropping privileges, then
   3. `exec su-exec maburvm:maburvm ./panel` so the **panel process runs
      non-root**.
- The resolver semantics: an ordinary non-empty env wins; otherwise the file must
  be readable and non-empty; secret contents are never echoed; the app resolver
  still trims surrounding whitespace/newlines as it does today.
- No privileged mode, no host mounts, no `network_mode: host` for the panel.

Crypto secrets: the provided single-panel behavior is persistent `/data`
secrets. The panel generates strong random JWT/AES keys on first boot and persists
them under `/data` (survives restarts) — this is the supported behavior in this
compose model. App-side `_FILE` secret mounting already works (the entrypoint
resolves `DB_PASSWORD`, `S3_ACCESS_KEY`, `S3_SECRET_KEY`, `JWT_SECRET_KEY`,
`AES_KEY` when their `*_FILE` vars are set), but multi-instance pinning or custom
secret mounting requires a separately designed topology, not this file.

### Sessions, headers, audit log and secrets at rest

- **Sessions** live in an `HttpOnly`, `SameSite=Lax` cookie the panel sets at
  sign-in; no script can read it. It is marked `Secure` whenever the browser
  reached the panel over https, which the panel learns from the proxy's
  `X-Forwarded-Proto` (Caddy sends it). Its lifetime is the session length under
  Settings, System, Security. Sessions started before this release keep working
  until they expire. A state-changing request carrying the cookie that the
  browser marks `Sec-Fetch-Site: cross-site` or `same-site` (another subdomain,
  such as a VM hostname under the panel's domain) is refused. API keys and
  `Bearer` tokens are unaffected.
- **Headers.** Pages carry a Content-Security-Policy (scripts, styles, fonts and
  images from the panel itself; API calls and console websockets to the same
  host; no framing), HSTS, `X-Frame-Options: DENY`, `nosniff` and a referrer
  policy. API answers carry a deny-all policy of their own.
- **Client address.** Rate limits, per-user IP allow-lists and audit rows use the
  address from `X-Forwarded-For`, believing only hops in `TRUSTED_PROXIES`
  (default: loopback and private ranges, which fits Caddy on the host in front
  of this compose network).
- **API rate limit.** Every signed-in person and every API key gets "Requests
  per minute" (Settings, System, API; default 1200, 0 = off). Past it the panel
  answers 429 with `Retry-After`. The billing webhook is not counted. The count
  is kept in the panel's memory.
- **Audit log.** Every POST, PUT, PATCH and DELETE, by staff, customers, API
  keys, MCP and the billing webhook, is recorded with who, from where, the route
  and the outcome, plus the JSON body with passwords, tokens, keys, codes,
  webhook URLs and cloud-init redacted. Failed sign-ins are recorded with the
  email tried. Not recorded: other calls refused for having no valid session,
  and billing status and usage polls.
- **Secrets at rest.** Node agent tokens, VNC passwords, the mail password, the
  billing HMAC secret, the AbuseIPDB key and the passwords inside queued jobs are
  encrypted with the AES key in `secrets.json`. On every boot the panel
  encrypts any that are still plaintext and moves values on a retired key
  (`AES_KEY_PREVIOUS`) to the current one; the log says how many. Keep
  `secrets.json` with your database backups (section 9). A panel image from
  before this release cannot read the encrypted values: rolled back to one, it
  sends nodes an encrypted token they refuse and consoles and mail stop. Roll
  back by restoring the database dump taken just before the upgrade, not by
  starting the old image on the upgraded database.

### Staff roles, sessions and the sign-in check

- **Staff roles.** Every staff account has a role: Owner (always the founding
  administrator), Administrator, Support, Billing, Read-only, or a custom role
  made on the Staff roles page from the panel's permissions. Staff accounts
  that existed before roles are Administrators, with the access they had. Only
  the Owner, or staff whose role includes "Assign staff roles", changes a role,
  and nobody hands out a permission they do not hold. Creating and deleting
  staff accounts stays with the Owner. A staff API key does what its owner's
  role allows at the time of each call; MCP calls go through the same checks.
  Support access accounts get the Support role unless another is chosen.
- **Viewing as a customer.** Staff with "View and act as a customer" open a
  customer's client area for up to an hour from the customer's user page, and
  can do there whatever the customer can. A staff password change or "sign
  out everywhere" ends the staff member's open views too. A
  banner says so; every action is recorded with both accounts; the customer's
  password, email, two-factor, sessions, API keys and initial passwords stay
  out of reach. Ending the view returns to the staff session.
- **Sessions and sign-in history.** Each sign-in is a session that its owner
  (Profile) and staff (the user page) can see and sign out; signing out ends
  only that session. Every sign-in attempt and password reset request is kept
  for 180 days. Sessions from before this release are not listed; they end
  when they expire, or with "Sign out everywhere else".
- **Sign-in check (captcha).** Off by default. Under Settings, System,
  Security, choose Cloudflare Turnstile or hCaptcha and enter the provider's
  site key and secret key (stored encrypted). After the set number of failed
  attempts from one address or for one email within 15 minutes, the sign-in
  and password reset pages ask for the check. The panel must reach the
  provider's verification address over https; while it cannot, those attempts
  are refused. The pages' Content-Security-Policy already allows both providers
  on the sign-in and reset pages only.

### Optional: Sign in with SSO

The login page can offer a "Sign in with SSO" button for any OpenID Connect
provider. Register the panel there as a confidential web client with the
redirect URI `<panel address>/api/v1/auth/oidc/callback`, then enter the issuer,
client ID and secret under Settings, System, Panel (the secret is stored
encrypted; the page shows the redirect URI). They apply at once. Or set them in
`.env.production`, where a value saved on the page takes over:

| Variable | |
|---|---|
| `OIDC_ISSUER` | the provider's issuer URL (its `/.well-known/openid-configuration` must be reachable from the panel) |
| `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET` | the client's credentials (`OIDC_CLIENT_SECRET_FILE` also works) |
| `OIDC_BUTTON_LABEL` | optional, defaults to "Sign in with SSO" |

SSO stays off unless all three of issuer, client id and secret are set. The
panel uses the authorization code flow with PKCE and reads the email from the
provider's userinfo endpoint; it must be verified. That email is matched to an
existing panel user, and nobody is created automatically. A user's IP whitelist
and panel two-factor still apply to SSO sign-ins.

### Optional: billing system and transfer quotas

A billing system drives the panel through the signed webhook
`POST <panel address>/webhooks/billing`. Its API key and signing secret are
entered under Settings, System, Panel, Billing webhook (stored encrypted, applied
at once), or passed from `.env.production`; a value saved on the page takes
over from the variable:

| Variable | |
|---|---|
| `BILLING_API_KEY`, `BILLING_WEBHOOK_SECRET` | the shared key and signing secret. The webhook is off (it answers 503) until both are set. Use `openssl rand -hex 32` for each and enter the same values in the billing system (for WHMCS: `integrations/whmcs/README.md`). |
| `BANDWIDTH_ENFORCE` | the starting value of System settings, Quotas, "Restrict VMs that use up their quota". Once that is saved on the page, the page decides and this is ignored. `true` makes a plan's monthly transfer quota act: a VM that goes over is throttled or stopped, as its plan says. Any other value only measures. Default `false`. |

After changing a variable, recreate the panel container
(`docker compose ... up -d panel`); a restart alone keeps the old environment.
Values saved on the page need neither. The Security page reports whether the
webhook is on.

The same page holds the panel address (`PANEL_PUBLIC_URL`) and the metrics
token (`METRICS_TOKEN`), and shows for each setting the value in force and
whether it comes from the page, the environment or a default. What must exist
before the database can be read stays in the environment: the database
connection, `ALLOWED_ORIGINS`, `TRUSTED_PROXIES` and the keys in `secrets.json`.
Staff who can read settings see the page; only the Owner, or staff whose role
may assign staff roles, changes it, because the SSO issuer decides who can sign
in as whom and the billing key acts on VMs.

Every request must carry a `timestamp` in its signed body within 5 minutes of
the panel's clock, and each signed request is accepted once, so keep the
billing server's clock synchronised. `GET /webhooks/billing/docs` lists the
events and their fields.

With enforcement on, a VM is throttled or stopped at the moment it crosses its
quota. A VM that already crossed this month while enforcement was off is left
alone until the next month. Check the quotas your plans carry before turning
it on: from then on they are what customers get. Owners are emailed at 80% and
100% of their quota (once per level each month) unless other levels are saved
on the same page; usage already past a level when this version was installed
counts as warned, so the upgrade sends no burst of mail.

What customers may do past the billing system is set under System settings,
Quotas: ordering, cloning and adding disks on their own, and whether they may
delete a VM that a billing service pays for (off unless you turn it on).

An order that names no region lands in the default region when one is set
(System settings), so check that region has a node with room before taking
billing orders. A template can be limited to chosen plans or marked as needing
a paid licence (Templates, a template, Edit); an order for it on another plan
is refused.

The panel also sends events back (a server created, suspended or deleted, a
failed installation, floating IP counts, transfer overage) to the webhook URL
saved under System settings, API, signed with the HMAC secret saved there, or
with `BILLING_WEBHOOK_SECRET` when none is. Each event is stored, retried up to
10 times over about four hours, and listed with its outcome on the same page.
Nothing is sent or stored while no URL is saved. The WHMCS receiver is the
events addon in `integrations/whmcs`. The "Welcome (billing order)" email to a
new customer needs SMTP and `PANEL_PUBLIC_URL`.

Single sign-on from the billing system (event `user.sso`, WHMCS's "Open control
panel") hands out links that last 60 seconds and work once. They point at
`PANEL_PUBLIC_URL` when it is set, else at the hostname the billing system
uses to reach the panel, so set it when that hostname is not the one customers
open. Customer accounts only; the panel's own two-factor step still applies.

Plans carry a display price and per-VM allowances (snapshots, backups, extra
IPv4, IPv6 added later). Existing plans read as "no price, no limit" after the
upgrade, so nothing changes until you set them. Lowering the backup allowance
removes the oldest completed scheduled backups above it at the next hourly
pass; manual backups are never removed. Plans, a plan, Apply to VMs
previews and applies an edited plan's size, limits, speed, quota and mail block
to the VMs on it; running VMs keep their size until stopped.

## 6. Agent deployment (separate, privileged)

Architecture: the panel **initiates outbound gRPC** to a **separately deployed**
hypervisor agent (from the panel's `agent-egress` network); the agent does not
connect back to the panel. A deployed agent only needs to be reachable from the
panel host on its gRPC port — it needs no inbound route to the panel. Restrict
agent ingress to the panel host at the firewall. `AGENT_PANEL_ADDRESS` is **not**
used by the current agent (omitted to avoid a misleading flag); only
`AGENT_AUTH_TOKEN` is passed.

The agent is **privileged** (it drives libvirt/QEMU on a hypervisor) and must not
run as a sidecar on the panel host. `docker-compose.production.yml` deliberately
omits it. The example below is **one manual way to run it on a hypervisor for
testing** — it is **not** a supported/production agent deployment path:

```bash
docker run --privileged --network host \
  -v /var/run/libvirt:/var/run/libvirt \
  -v /var/lib/libvirt/images:/var/lib/libvirt/images \
  -e AGENT_AUTH_TOKEN=<node-token> \
  maburvm-agent:local
```

**Who may talk to the agent.** Two settings in the agent's environment
(systemd unit, or passed to `install-agent.sh`) narrow what a stolen token or
a compromised panel can do. Both are off unless set, and the agent logs a
warning at start for each one that is off.

| Variable | Meaning |
|---|---|
| `AGENT_ALLOWED_PANEL_IPS` | The panel's outgoing address(es), as IPs or CIDRs. Connections from anywhere else (except the node itself) are closed before TLS. Use the address the panel's traffic leaves from, which may differ from the address of its hostname. An invalid entry stops the agent from starting. |
| `AGENT_UPDATE_PUBKEY` | An ed25519 public key. The node then installs only agent builds signed with its private key, which you keep off the panel host. An unsigned or wrongly signed update is refused and the running agent is untouched. |

To add them on a node set up earlier, `systemctl edit maburvm-agent`, add for
example `Environment=AGENT_ALLOWED_PANEL_IPS=192.0.2.10`, and restart the agent.
Check `journalctl -u maburvm-agent` for "accepts connections only from" and
confirm the node still shows online in the panel before moving to the next one.

Signing agent builds, offline:

```bash
# Once, on the signing machine (never the panel host; the key stays there):
go run ./cmd/agent-sign keygen agent-signing.key   # prints AGENT_UPDATE_PUBKEY=...

# For each panel image. On the panel host, take the bundled agent out:
docker compose -f docker-compose.production.yml --env-file .env.production \
  cp panel:/app/bin/linux/agent-amd64 ./agent-amd64
sha256sum agent-amd64
# Copy agent-amd64 to the signing machine. Check its sha256 against the agent
# you built there from the same commit (a panel that was tampered with would
# hand you its own build to sign), then sign it:
go run ./cmd/agent-sign sign agent-signing.key agent-amd64   # writes agent-amd64.sig
go run ./cmd/agent-sign verify "$AGENT_UPDATE_PUBKEY" agent-amd64
# Copy only agent-amd64.sig back to the panel host and put it beside the binary:
docker compose -f docker-compose.production.yml --env-file .env.production \
  cp ./agent-amd64.sig panel:/app/bin/linux/agent-amd64.sig
```

A release can be signed by the release workflow instead (§16): with the
repository secret `AGENT_SIGNING_KEY` set to the key file's contents, the
published agents come with `.sig` files and the release's panel image already
carries them beside the binaries. The key then lives in the repository's
secrets rather than only on your signing machine; leave it unset to keep signing
offline as above.

The panel sends `agent-<arch>.sig` with the update when it is there. A rebuilt
panel image has a new agent build, so sign again after every rebuild (the copied
signature does not survive a container being recreated: bind-mount it, or
repeat the `cp`). Nodes without `AGENT_UPDATE_PUBKEY` ignore the signature, and
older panels keep working with them.

**What an agent runs with.** A node's page lists its agent's settings, read
from the node when the page opens: the variables above and the rest the agent
reads (bind address, storage, VLAN bridges, bridge netfilter, and so on), with
secrets shown only as set. Change them on the node (`systemctl edit
maburvm-agent`, then restart the agent). An agent older than this panel reports
none.

**Updating an agent from the panel.** Settings, System lists each node's agent
build beside the panel's and offers "Update to <commit>" for a node on another
build, one node at a time. The panel sends the agent bundled in its image. The
node refuses it, with the running agent untouched, if the size or SHA-256 does
not match, the signature does not (when `AGENT_UPDATE_PUBKEY` is set), or the
binary does not start there and print the expected commit with `--version` (a
glibc or libvirt the build does not fit fails here). Otherwise a transient unit
(`maburvm-agent-update-<time>`) backs the old binary up to
`/root/maburvm-agent.bak-<time>` (the newest five are kept), swaps in the new
one and restarts `maburvm-agent`. If the agent is not still up 30 seconds later
with no extra restart, it puts the old binary back and restarts again. Only one
swap runs at a time on a node. Restarting the agent does not restart or pause
any VM. The page follows the update; the panel also checks the node three
minutes later, page open or not, and raises a "Job failed" alert if the node is
not on the new build. A node whose agent predates updates from the panel says
so: update it once with `scripts/deploy-agent.sh`.

The image bundles `agent-amd64` only. For arm64 nodes, build the panel image
with `--build-arg AGENT_ARM64=1`, which cross-compiles `agent-arm64` against
Debian's arm64 libvirt.

The update button has not yet been used on a live node. Make the first run a
supervised one, on the node with the fewest customer VMs:

1. Have `scripts/deploy-agent.sh` ready for that node as the fallback, and check
   that no backup or image export is running on it (Backups page, Jobs).
2. On the node, note the running build and watch the agent:
   `/usr/local/bin/maburvm-agent --version` (or the path in the unit's
   `ExecStart`), `virsh list --all | wc -l`, then `journalctl -fu maburvm-agent`
   in one shell and `journalctl -f -u 'maburvm-agent-update-*'` in another.
3. Optional dry run of the binary on the node, without swapping: copy it out of
   the panel (`docker compose ... cp panel:/app/bin/linux/agent-amd64 .`), copy
   it to the node's `/tmp`, and run `/tmp/agent-amd64 --version`. It must print
   the panel's commit. Remove it afterwards.
4. Click Update for that node. Expect, in order: "update scheduled" on the page;
   in the update unit's log "agent update OK; backup at /root/maburvm-agent.bak-…"
   about 35 seconds later; the node listed as "Same as the panel".
5. Check nothing else moved: `virsh list --all` shows the same domains in the
   same states, the node is online on the Nodes page, a VM's console opens, and
   `iptables -S | wc -l` is about what it was (the agent re-checks VM network
   rules at start).
6. If the page says the update did not take, read the update unit's log. A
   rollback leaves the old agent running; to retry by hand, use
   `scripts/deploy-agent.sh`. To undo a successful update, `install -m0755` the
   newest `/root/maburvm-agent.bak-*` over the binary and restart the agent.

Then update the remaining nodes one at a time, each only after the previous one
shows "Same as the panel".

**Template cache.** Template images downloaded from a URL or object storage,
and disks pulled for a cross-node clone, are cached on the node so later VMs
reuse them. Images you place on the node by hand (a Windows template built
there, say) are never removed by the agent, wherever they are.

| Variable | Default | Meaning |
|---|---|---|
| `TEMPLATE_CACHE_DIR` | `/var/lib/libvirt/templates` | Where downloads are cached, for example on a data volume instead of the root filesystem. Must be an absolute path. Customer ISOs go in its `customer-iso` directory. Images already cached in the old place are downloaded again when next used. |
| `TEMPLATE_CACHE_MAX_GB` | unset (no limit) | Keeps the downloads in the cache under this size: the least recently used are removed first, after each download and once an hour. A download that any VM has attached, or that was used in the last six hours, is never removed, so the cache can stay above the limit. |

**Node packages the agent shells out to:** install `iputils-arping` on every
hypervisor. The panel's IP reconciler asks the agent to ARP-probe pool addresses
before allocating them; without `arping` the agent falls back to `ping` + the
neighbour cache, which is blind for any pool subnet the node itself has no
address in (the ping is routed upstream instead of ARPed on the bridge), so
addresses already in use by imported guests look free.

**ISOs customers attach by URL.** A customer may attach an install or rescue ISO
from a URL they type. The node fetches such a URL only from public addresses
outside its own networks (never loopback, private, carrier-NAT or link-local
space, the node itself, or a neighbour on a network the node is attached to),
checks every redirect the same way, and keeps the download in
`/var/lib/libvirt/templates/customer-iso`, apart from the images you define.
A file there is removed as soon as no VM has it attached. ISOs and templates an
administrator defines are not restricted and are never removed. Three settings
in the agent's environment bound what customers can put on the node:

| Variable | Default | Meaning |
|---|---|---|
| `CUSTOMER_ISO_MAX_GB` | `8` | Largest ISO a customer may attach by URL. |
| `CUSTOMER_ISO_MIN_FREE_GB` | `10` | A download is refused, or stopped, rather than leave less than this free on the filesystem holding the cache. |
| `CUSTOMER_ISO_RETENTION_DAYS` | `7` | A customer ISO that no VM uses and that was missed when it was detached (an agent restart, a VM removed outside the panel) is removed after this long. |

Customers attach ISOs by URL only on nodes whose agent has these checks; on an
older agent the attach fails with a message asking for the agent to be updated.

**Port forwards are published on one node address.** A customer's port forward
answers on a single address of the node and nowhere else: not on floating IPs
bound on the node, and not on the node's other addresses. The agent uses the
address its default route sends from and logs it at start
(`port forwards are published on ...`); the node's page in the panel shows it
too. Forwards installed by an older agent are rewritten to that address when the
agent starts. Before updating a node, check that this is the address customers
use (`ip -4 route get 192.0.2.1` prints it after `src`), and set the variable
below if it is not. A port is held by one VM per node and protocol, and a VM may
hold a limited number of forwards (Settings, System, Quotas; 20 by default).

**VM firewalls need bridge netfilter.** Traffic between ports of one bridge
reaches iptables only while the `br_netfilter` module is loaded and
`net.bridge.bridge-nf-call-iptables` and `-ip6tables` are 1. Without that, VM
firewall rules, the outbound scan guard and null-routes of bridged guests are
installed and filter nothing. The Abuse page's inbound connection counts are
not kept at all: their iptables match would load `br_netfilter` by itself, so
the agent adds them only once it is on (packet rates still read, from the
guests' taps). The
installer turns it on for a new node. The agent reports the state to the panel,
which warns on the node page, on each VM's firewall tab and in the security
centre while a node is not enforcing, or has not said. Where it is on, the agent
makes it survive a reboot (`/etc/modules-load.d/maburvm.conf`,
`/etc/sysctl.d/99-maburvm-bridge.conf`). Where it is off, the agent leaves it
off: turning it on starts enforcing rules that never applied to the running VMs
(every VM is created with default rules that admit SSH or RDP, ping, 80 and 443
only) and starts connection-tracking bridged traffic, so it is yours to
schedule. To turn it on, add the variable below to the agent's unit
(`Environment=` in `/etc/systemd/system/maburvm-agent.service`), then
`systemctl daemon-reload` and restart the agent. Even then the agent does not
turn it on while the node's `FORWARD` policy is `DROP`, because guests without
their own accept rule would lose their network.

**VM network rules: update the panel, then every agent.** Nothing changes for
running VMs at the upgrade. VMs that had no firewall rules keep accepting
inbound traffic that no rule matches when a rule is added (the VM's Firewall tab
shows the policy and asks before switching it to drop); VMs with rules keep
dropping it. Outbound mail on port 25 stays allowed for every existing VM and
plan; a plan set to block it applies to VMs created on it afterwards, and an
administrator can block or allow it per VM. An agent older than the panel
ignores the mail block, the accept policy, and the family, ICMP type and log
fields of a rule, so update the agents before using them. An updated agent
checks every five minutes that each VM's firewall, NAT, port forwards and
anti-spoofing rules are still installed and puts back what is missing (the first
check after it starts only brings older rules in line); a node that needs this
three times in an hour raises a "keeps losing VM network rules" alert. It also
rebuilds tenant VPCs after a reboot and starts the guests on them that are set
to autostart. Adding or releasing an address never restarts a VM on an updated
agent: a guest that cannot take the change live gets it when its guest agent
answers or when it is next started from the panel, and its page says so.

**VM consoles stay on the node.** A VM's console socket listens on the node's
loopback address with a password, and the panel reaches it through the agent's
gRPC channel, so no console port needs to be open to the network. Update the
panel before the agents: a panel older than the agent cannot open the console of
any VM started under the new agent. A newer panel with an older agent keeps
connecting to the console port directly, as before.

A VM that was already running when its node's agent was updated keeps the socket
it started with, which listens on every address and, unless it was started with
a password, has none: the hypervisor cannot add one to a running VM. Until such
a VM is powered off and on (a reboot from inside the guest or the panel's
restart is not enough), the agent closes its console port to everything but the
node itself, with one rule per port in the `MABURVM-CONSOLE` iptables and
ip6tables chain, checked every five minutes. The agent's log names these VMs at
start (`journalctl -u maburvm-agent | grep '\[Console\]'`), and
`iptables -S MABURVM-CONSOLE` lists the ports. When the log says every running
VM has its console on loopback, the chain is empty.

| Variable | Default | Meaning |
|---|---|---|
| `MABURVM_NAT_ADDRESS` | the default route's source address | The node's IPv4 address port forwards are published on. It must be an address the node holds and not a floating IP; otherwise the agent logs a warning, installs no forwards and leaves existing ones as they were. |
| `MABURVM_BRIDGE_NETFILTER` | unset | Unset, the agent reports the state and only persists it when it is already on. Set to `enforce` to have the agent turn bridge netfilter on at start. |
| `AGENT_CONSOLE_SHIELD` | on | Set to `off` to remove the console port rules. Only for running a panel older than the agent, which still connects to console ports directly; the consoles of VMs that have not been powered off and on are then reachable from the network again. |

> **Exception / future work:** Privileged-agent minimization is an explicit
> exception that requires a dedicated minimization test before any change. Do not
> attempt to remove `--privileged` without that test.

### 6.1 Adding a new node with the installer

The installer is for **new machines only**: Debian 12 or 13, or Ubuntu 22.04 or
24.04 (the RHEL 9 family is best effort), with no VMs on it. Never run it on a
node that already carries customer VMs; update an existing node's agent from
Settings, System (or `scripts/deploy-agent.sh`). The panel will not give out an
install command for a node an agent has ever answered for, or that has VMs, and
the installer refuses such a host by itself as well.

1. **Nodes, Add node.** Give the name and the address the panel reaches the
   node on (port 50051). The panel shows a single-use install command:

   ```bash
   curl -fsSL https://panel.example.com/install-agent.sh | sudo bash -s -- --token mbe_...
   ```

   The token works for one enrollment and for one hour, and is shown once. The
   node's page can create a new command while no agent has answered for the
   node; that stops the old one working. Only a SHA-256 of the token is stored.
2. **On the new machine, as root,** run the command. Add `--dry-run` first to
   print every step without changing anything (root is not needed for that). In
   order, the installer:
   - stops with exit code 3 if the host already has libvirt domains (running, or
     defined under `/etc/libvirt/qemu`) or a MaburVM agent (unit, binary or
     `/etc/maburvm-agent.env`). `--force` goes on anyway, but then leaves the
     network and bridge netfilter as they are;
   - asks the panel whether the token is good and the node still has no agent,
     and checks the panel has an agent for this architecture, before
     installing anything;
   - installs QEMU/KVM, libvirt, `qemu-utils`, `iputils-arping`, `ebtables`,
     `iptables`, `bridge-utils`, `libguestfs-tools`, `genisoimage`, `chrony` and
     `dnsmasq-base`, and starts libvirt and chrony;
   - creates the storage pool directory (`--pool-dir`, default
     `/var/lib/libvirt/images`), warning when it is on the root filesystem;
   - loads `br_netfilter` and writes `/etc/modules-load.d/maburvm.conf`,
     `/etc/sysctl.d/99-maburvm-bridge.conf` and `/etc/sysctl.d/99-maburvm.conf`
     (IP forwarding), the files the agent itself keeps;
   - sets up a bridge, only with `--bridge` (below);
   - downloads the agent bundled with the panel with the token, checks its
     SHA-256 against the digest in the script (and, with `--update-pubkey`, its
     signature, using `openssl`), and checks that it runs on the host;
   - enrolls: the token is used up, and the panel hands out a new agent
     credential, written with the other settings to `/etc/maburvm-agent.env`
     (mode 0600; the agent keeps its TLS key and secrets in `/var/lib/maburvm`).
     Only then is the agent put in place. The layout is the same as for nodes
     set up by hand: binary `/usr/local/bin/maburvm-agent`, unit
     `maburvm-agent`, so updates from the panel and `scripts/deploy-agent.sh`
     work on it;
   - starts the agent and prints the host checks (`maburvm-agent --preflight`).
3. **In the panel,** add a storage pool for the pool directory and an IP pool
   for the node (with the bridge), then look at the node's host checks.

| Option | Meaning |
|---|---|
| `--dry-run` | Print every step, change nothing. |
| `--pool-dir DIR` | Where VM disks go. Default `/var/lib/libvirt/images`. |
| `--bridge NAME` | The bridge VMs attach to. An existing bridge is used as it is; otherwise it is created (below). |
| `--bridge-iface IFACE` | The uplink to put in the new bridge. Default: the interface of the IPv4 default route. |
| `--confirm-timeout SECS` | Time to confirm the new network, 30 to 3600. Default 120. |
| `--allowed-panel-ips LIST` | Written as `AGENT_ALLOWED_PANEL_IPS`. |
| `--update-pubkey KEY` | Written as `AGENT_UPDATE_PUBKEY`; the downloaded agent must carry a matching signature. |
| `--bind ADDRESS` | `AGENT_BIND_ADDRESS`, default `0.0.0.0`. |
| `--force` | Install on a host that already runs VMs or an agent. |
| `--any-os` | Try on a system that is not supported. |

**Creating a bridge.** With `--bridge br0` and no such bridge, the installer
moves the uplink's addresses, routes and MAC address onto a new bridge. It does
so only where the network is configured by netplan or ifupdown and that
configuration names nothing but the uplink; with NetworkManager, another
interface, a bond or VLAN, DHCP under ifupdown, routes on the uplink besides
the default one, or anything it does not recognise, it stops and says so, and
you create the bridge yourself and re-run with `--bridge` naming it. Before changing anything it copies the configuration
to `/var/backups/maburvm-network-<time>/` with a `rollback.sh`, and arms a
systemd timer (`maburvm-net-rollback-<time>`) that runs it after the
confirmation time plus 30 seconds. The change itself runs as its own systemd
unit, so a dropped SSH session cannot stop it halfway. If the panel is not
reachable over the new network, the old one is put back at once. Otherwise the
installer asks you to open a new SSH session and type `keep`; without that, or
with no terminal, the old network comes back. It stops asking 15 seconds
before the timer fires, so a late `keep` is never taken for one that worked.
From another session, `systemctl stop maburvm-net-rollback-<time>.timer` keeps
it too; if the first session was cut, run the install command again then (the
token is still unused, and the new bridge is used as it is). A cloud-init
host also gets `/etc/cloud/cloud.cfg.d/99-maburvm-disable-network-config.cfg`,
so cloud-init does not rewrite the network at the next boot. Do not reboot
during the confirmation window: the timer does not survive a reboot.

**The endpoints.** `/install-agent.sh` and
`/api/v1/nodes/agent-binary/checksum` are public and hold nothing secret. The
agent binary (`/api/v1/nodes/agent-binary`) and the enrollment
(`/api/v1/nodes/enroll`) need the token in an `Authorization: Bearer` header,
never in a URL. All four are rate limited per address. Each enrollment is in the
audit log (action `ENROLL`, the node), and a used, expired or wrong token is
refused the same way. A token issued for a node is refused once an agent has
answered for that node, so it cannot take over a node that is running.

### 6.2 Host checks

A node's page has **Host checks**: whether the node is set up the way VMs need.
The agent checks hardware virtualization and `/dev/kvm`; that the libvirt daemon
answers, and its and QEMU's version; `qemu-img` and qcow2 support; every bridge
the node's IP pools name (exists, is a bridge, is up); every directory storage
pool of the node (exists, writable, free space, and whether it is on the root
filesystem), or the default image directory when the node has no pool; bridge
netfilter; IP forwarding; `iptables`, an ISO tool, `arping`, `ebtables` and
libguestfs; and whether the clock is synchronised. Each line is pass, warning
or failed, with how to fix it. Check now asks the node again (it reads the host
and changes nothing); the panel keeps the last answer and when it was given. A
node whose agent is older says so: update its agent to see the checks. The
same checks run on the node with `maburvm-agent --preflight [--pool DIR]
[--bridge NAME] [--json]`, which exits 1 when one fails.

**Disks are never put in the wrong place.** An agent of this version refuses to
create a VM disk, or attach an extra disk, in a storage pool whose directory
does not exist on the node, and says so. Older agents silently put the disk in
`/var/lib/libvirt/images` on the root filesystem instead. Before updating a
node's agent, check that the path of each of its pools on the Storage page
exists on the node (`ls -ld <path>`); a missing one shows as failed in the host
checks once the agent is updated.

The agent no longer needs the panel's database or object storage settings
(`DB_*`, `S3_*`) in its environment to start; nodes that have them keep working.

### 6.3 Moving VMs between nodes, and draining a node

Staff with **Move VMs to another node** (`vm:migrate`) move a VM from its
Settings tab; staff with **Drain nodes** (`node:drain`) empty a node from its
page. Both need the agent of this version on both nodes; an older agent is
refused with "needs an agent update". A move runs as a background job: a
running VM moves live (memory and disks copied while it runs, then a pause of
about a second), a stopped VM moves offline (its disks copied). The VM stays on
its node until it is seen running on the new one; only then are its network
rules, console and limits applied there and its old definition and disk files
removed. A failed or cancelled move leaves the VM where it was and removes only
the files it prepared on the destination.

What is refused, with the reason shown before anything starts: floating IPs or
a private network attached (they live on one node), snapshots, an ISO in the
drive, rescue mode, a backup copying the disks, an address pool not available
on the destination, a port forward whose port the destination already uses
(another VM's forward, a NAT VM's ports, or 80/443 when it routes hostnames), a
NAT VM whose NAT pool or port range the destination lacks, dedicated cores or a
virtual TPM, huge pages or UEFI firmware the destination lacks, a disk on LVM
or another block device, storage that is not a directory pool, no room under
the destination's limits, and, for a live move of a VM with the host's own CPU
(the default `host-passthrough`), a destination with a different CPU; move such
a VM offline instead. Without a chosen pool a VM lands in the destination's
best directory pool, even when its primary pool is LVM. A port the destination
takes while the VM is copying is dropped from the VM's forwards at the
switch-over and named in the move's note. A moved VM's next scheduled backup is
a full one.

**Shared storage.** A VM whose disks are in a shared pool (storage several
nodes mount at the same path, added on the Storage page) moves to another
member of that pool without its disks being copied: a live move sends only the
memory, an offline move only the definition, and the source never deletes the
files. The panel checks right before the move that both nodes really see the
same storage. Shared pools are optional and change nothing on nodes that are
not added to one. See the admin guide, section 4.7.

**The migration link.** The panel sets this up the first time a VM moves to or
from a node; nothing is configured by hand. Each agent generates its own key
(`/var/lib/maburvm/migration/id_ed25519`). Every other node accepts it in
root's `authorized_keys`, in a block the agent manages, only from that node's
address, with no shell, pty or forwarding, and only to run the agent's
`migration-shell` (a libvirt connection, or a stopped VM's disk for an offline
move). Each node pins the other nodes' SSH host keys, one file per node under
`/var/lib/maburvm/migration/known_hosts.d/`, and connects with strict host key
checking through `/usr/bin/maburvm-migration-ssh`. Deleting a node removes its
key and host keys from every node that is online. Requirements on every node:

- sshd accepts root logins with a key (`PermitRootLogin prohibit-password`, the
  default) from the other nodes' addresses.
- One of `virt-ssh-helper` (libvirt 6.8 and later), `socat` or
  `netcat-openbsd`, which joins an incoming connection to libvirt.
- For live moves, TCP ports 49152-49215 open between the nodes: QEMU sends the
  memory and disk copy there directly, **not encrypted**. Keep that traffic on a
  private network or a VPN between sites, or move VMs offline (the offline copy
  runs inside the SSH connection).

**Draining** stops new VMs being placed on the node (the same as pausing it),
plans a destination for every VM (or takes the operator's), moves them one to
three at a time, gives each VM two tries, and reports what moved and what
stayed and why. VMs that cannot move keep running on the node. Stop drain
cancels the moves under way; nothing is lost. The node stays paused for new
VMs afterwards until it is resumed.

**A node that went down.** Moves and drains need both nodes alive. Staff with
**Recover a failed node's VMs** (`node:recover`) bring the VMs of a node the
panel has marked offline back on other nodes, one job per VM: on the same
disks for a VM in a shared pool, from its newest backup otherwise. Nothing is
recovered automatically, and staff must confirm the node is powered off or cut
off first. It needs the agent of this version on the node a VM goes to (either
way), and on the failed node once it is back, so it can be told not to run
those VMs again. See the admin guide, section 10.1.

### 6.4 Firewall groups and NAT VPS

**Firewall groups** (Firewall groups in the menu; `firewall_group:read` and
`firewall_group:update`) are rule sets attached to many VMs. Staff make groups
for everyone; customers make their own and attach them to their own VMs. A group
sits before a VM's own rules (they cannot override it) or after them (they can).
Its rules count toward the per-VM rule limit, and the outbound mail block stays
above every rule. A plan can name a group for everyone that VMs created on it
get instead of the built-in rules; VMs that exist are not changed. Editing a
group queues a network push to every VM it is on. Nothing new is needed on the
nodes: group rules reach them as ordinary rules.

**NAT VPS.** A VM on a NAT plan gets a private address from a NAT pool on its
node, a block of public ports on the node's publish address (`MABURVM_NAT_ADDRESS`,
see the table above), and its first port forwarded to SSH (Remote Desktop on
Windows). Its owner forwards more ports inside the block only, and may route
website hostnames to it. To offer NAT on a node:

1. Give a bridge on the node the gateway address of a private range (for
   example `10.20.0.1/24` on `natbr0`), kept across reboots by the node's own
   network configuration. Use a plain bridge, or a libvirt network in `route`
   or `open` mode: a libvirt `nat` network rejects incoming connections, so
   forwards and hostnames would never reach the VMs. The agent masquerades the
   private addresses itself.
2. Under IP Pools, create an IPv4 pool for that range on that node with the
   bridge and gateway, and tick **NAT pool**. NAT pools are never used for VMs
   that should get a public address.
3. On the node's page, set the **NAT ports** range (none until you do; for
   example 20000-29999). Ports already forwarded and the agent, libvirt and
   console ports are skipped; keep the range clear of anything else the node
   listens on, such as SSH on a non-standard port, because a forward on the
   publish address takes that port's traffic. Nodes without a range are not
   picked for NAT VMs.
4. Create a plan with **NAT** as its address, the ports per VM and the number of
   hostnames.

Hostnames: the agent listens on the publish address's ports 80 and 443 only
while at least one verified hostname is routed on the node, so those ports must
be free there (no web server on the node) and open in any firewall in front of
it. HTTP is routed by its Host header with `X-Forwarded-For` added; HTTPS is
passed through by its SNI, so certificates stay on the VMs. A customer proves a
hostname with a TXT record `_maburvm-verify.<hostname>` or by pointing it at the
publish address; one hostname is routed to one VM. This needs the agent of this
version: on an older one the panel refuses hostnames with "needs agent update".
A NAT VM moves to another node only where its NAT pool is offered too (the
same private range on a bridge there, with the pool assigned to that node) and
the node has a NAT port range: it keeps its public ports when they are free
there, else gets a free block of the same size with its forwards shifted into
it, and its owner is emailed the new address and ports.

## 7. TLS & reverse proxy

The production composition publishes **only** the `web` UI on loopback
(`127.0.0.1:WEB_PORT`, default `3000`). The panel API is internal only. Put a
reverse proxy (Caddy / nginx / Traefik) in front of `web` to terminate TLS and
expose it publicly; do **not** expose the panel API, PostgreSQL, MinIO, or the
agent. Set `ALLOWED_ORIGINS` to the exact UI origin(s) (no `*`).

Network layout (honest, least-privilege split):
- `webpanel` (`internal: true`): only `web` <-> `panel`.
- `data` (`internal: true`): `panel` <-> `postgres`/`minio`/`minio-init`. Web has
  **no** access to Postgres/MinIO.
- `agent-egress` (non-internal): `panel` only, to initiate outbound gRPC to agents.
- `edge` (non-internal, published): only `web` attaches; the TLS reverse proxy
  targets it.

A Docker network is **not** a complete host/firewall policy — enforce agent
ingress at the firewall and treat `edge`/`agent-egress` as ordinary bridges.

### 7.1 Reference production setup (Caddy + Google Trust Services)

The live reference deployment terminates TLS with **Caddy**, obtaining
certificates from **Google Trust Services (GTS)** via ACME with External Account
Binding (EAB), and reverse-proxies to the loopback-published `web` container:

```caddyfile
panel.example.com {
    tls {
        # ACME with Google Trust Services (get eab_key_id + eab_mac_key from
        # Google Cloud → Public CA → external account key).
        acme_ca https://dv.acme-v02.api.pki.goog/directory
        acme_eab {
            key_id  <EAB_KEY_ID>
            mac_key <EAB_MAC_KEY>
        }
    }
    # web publishes on 127.0.0.1:3000 (WEB_PORT); the panel API is never exposed.
    reverse_proxy 127.0.0.1:3000
}
```

Let's Encrypt (Caddy's default) works with zero extra config if you don't need
GTS — just drop the `tls` block. Either way, set `PANEL_PUBLIC_URL` and
`ALLOWED_ORIGINS` to the exact `https://` origin.

### 7.2 Building & shipping on the host

The reference host keeps the repo source under `/root/maburvm` and builds the
images **on the host** (no registry), then restarts only the changed services.

`scripts/deploy-panel.sh root@HOST` does all of the below **and stamps the build**
(`git describe --tags` version + commit). The host has no `.git`, so a build run
there by hand reports itself as unstamped unless you export `GIT_COMMIT`,
`APP_VERSION` and `BUILD_TIME` yourself. The panel image also bundles the node
agent built from the same commit; *Settings → System → Update* pushes it to a
node (verified, test-run, auto-rollback). Nodes must already run an agent with
the `UpdateAgent` RPC — the first upgrade goes through `deploy-agent.sh`.

On the host, `scripts/upgrade-panel.sh --build` (§16) builds from the synced
source with a dump first and a rollback if the new panel does not come up.

Manual equivalent:

```bash
# From your workstation: sync source to the host (per-tree; NEVER lump multiple
# source dirs into the repo root with --delete — it will delete top-level files
# like docker-compose*.yml and .env.production). Exclude secrets + env + build junk.
rsync -az --delete internal/ root@HOST:/root/maburvm/internal/
rsync -az --delete web/src/ root@HOST:/root/maburvm/web/src/

# On the host: rebuild + roll out. The panel auto-runs SQL + River migrations on
# boot (see internal/panel/server startup), so schema changes apply on restart.
cd /root/maburvm
docker compose -f docker-compose.production.yml --env-file .env.production build panel web
docker compose -f docker-compose.production.yml --env-file .env.production up -d panel web
```

`deploy-panel.sh` ships source only (and `docker/agent-sigs/`, which
`Dockerfile.panel` copies). `docker-compose.production.yml`,
`docker-compose.rehearsal.yml`, `docker/` and `scripts/panel-restore.sh` change
rarely and are copied by hand: compare the host's copy with the repo's first
(`diff`), then copy each file to its own path. The `panel-backup` service (§9)
needs all of them.

`.env.production` is **not** in git. Every panel backup carries a copy (§9). If
it is lost and there is no backup, reconstruct it from the
running containers' resolved env (`docker inspect maburvm-panel` /
`maburvm-minio` → `.Config.Env`) — the required keys are `POSTGRES_USER`,
`POSTGRES_DB`, `MINIO_ROOT_USER`, `MINIO_ROOT_PASSWORD`, `PANEL_PUBLIC_URL`,
`ALLOWED_ORIGINS`. Validate before deploy with
`docker compose -f docker-compose.production.yml --env-file .env.production config`.

Object storage differs by role. Backups of customers' VMs go to the backup
destination set in the panel (§9.6), which the panel sends to each node with
every backup and restore. Captured images still use the store in each node's
own environment file (`STORAGE_*`/`S3_*`). The local MinIO in the **panel**
stack is on the panel's internal network and cannot be reached by nodes, so it
is not a backup destination.

## 8. Image / tag review

Before each deploy, confirm every tag in `docker-compose.production.yml` is the
exact pinned version you intend:
- `postgres:16.14` (also the base of `maburvm-panel-backup:local`, through the
  `POSTGRES_IMAGE` build argument: change both together so `pg_dump` matches the
  server)
- `minio/minio:RELEASE.2025-09-07T16-13-09Z` (same image used for `minio-init`)
- `node:22.23.1-alpine3.24` (Dockerfile.web build/runtime)
- `alpine:3.24` (Dockerfile.panel / Dockerfile.agent runtime)
- `maburvm-panel:local` / `maburvm-web:local` (built from the local Dockerfiles;
  `web` builds from the **repo-root** `Dockerfile.web`, not `./web`), or
  `${MABURVM_IMAGE_PREFIX}-panel:${MABURVM_VERSION}` and `-web:` when the install
  runs published releases (§16)

No `latest`, and **no invented digests** — only tags that exist upstream. Image
tag review, SBOM, and CVE scanning — plus actual Docker image build/start and
runtime tests — remain **release gates** (not executed as part of authoring).

For the **self-contained internal Postgres** in THIS file, `DB_SSL_MODE` defaults
to `disable` (the stock `postgres` image has SSL OFF). Pointing at a REMOTE or
MANAGED Postgres is NOT a supported switch in this file — it needs a separately
authored Compose model with real server certs + client config and its own
validation; set `DB_SSL_MODE` there accordingly and verify the cert chain.

## 9. Backups & restore

The panel host has to be rebuildable from nothing. Four things make a panel, and
all four must survive the loss of the host:

| What | Where it lives | Without it |
|------|----------------|------------|
| Panel database | `postgres_data` volume | Every VM, user, IP and setting record is gone. |
| `secrets.json` | `panel_data` volume (`/data` in the panel container) | The database restores but **cannot be decrypted**: 2FA secrets, node agent tokens (every node then needs a new token), the mail password, the DNS provider key, kept initial passwords and every other stored secret are lost, and all sessions are invalid. |
| `.env.production` and `deploy/secrets/` | The repo directory on the host | The stack has to be reconfigured by hand. |
| The age private key | **Not on the host.** With you. | No backup can be read by anyone. |

`secrets.json` holds the AES key the panel generated on first boot. A database
restored next to a *new* `secrets.json` is a database full of ciphertext nobody
can read. The two are only useful together, so they are backed up together.

The `panel-backup` service in `docker-compose.production.yml` takes that backup
every night and sends it off the host. **It does nothing until you configure
it**, and its log says so (`NOT CONFIGURED ... NO PANEL BACKUPS ARE BEING TAKEN`).

This backup rebuilds the **panel**. VM disks live on the nodes and are covered by
VM backups, not by this.

### 9.1 What a backup is

One file per night, `maburvm-panel-<UTC time>.tar.age`, holding:

- `db.dump`, a `pg_dump` of the panel database (custom format),
- `panel-data.tar`, the whole panel data volume including `secrets.json`,
- `host-config/`, a copy of `.env.production` and `deploy/secrets/`,
- `MANIFEST`, when it was taken and the last migration it contains.

The file is encrypted with [age](https://age-encryption.org) to a **public** key.
The panel host only ever holds that public key, so nothing on the host, and
nobody who takes the host or the storage, can read a backup. Only the private key
can, and it never goes near either.

### 9.2 Set up the nightly backup

**1. Make the key pair on your own machine, not on the panel host.**

```bash
age-keygen -o maburvm-backup.key
# Public key: age1...        <- this line goes to the panel host
```

Put `maburvm-backup.key` in your password manager and in one more place that
does not depend on the panel host or the backup storage. If you lose it, every
backup is unreadable. If you keep a copy on the panel host, the backups protect
nothing from whoever takes the host.

**2. Choose a destination that is not the panel host.** Any storage
[rclone](https://rclone.org) can write to: S3-compatible object storage, SFTP,
and many others. It must be different hardware, ideally a different provider and
account. The stack's own MinIO does not count: it dies with the host.

Describe it in `deploy/secrets/rclone.conf` on the panel host (mode `0600`):

```ini
# S3-compatible object storage
[offsite]
type = s3
provider = Other
endpoint = https://s3.example.com
access_key_id = <access key>
secret_access_key = <secret key>
```

```ini
# or an SFTP server
[offsite]
type = sftp
host = backup.example.com
user = maburvm
key_file = /host/deploy/secrets/backup_ssh_key
known_hosts_file = /host/deploy/secrets/backup_known_hosts
```

The repo directory is mounted read-only at `/host` in the container, so files in
`deploy/secrets/` are reachable as `/host/deploy/secrets/...`.

These credentials stay on the panel host, so whoever takes the host can use
them. Limit what they can do:

- Give them access to that one bucket or directory and nothing else. An S3 key
  that may not create buckets usually needs `no_check_bucket = true` in its
  section of `rclone.conf`: without it rclone tries to create the bucket before
  each upload and is refused. `panel-backup check` (step 4) shows whether it
  works.
- Turn on object versioning or object lock if the storage has it. That is what
  keeps older backups when someone deletes or overwrites them with these
  credentials.
- If the credentials cannot delete, set `BACKUP_RETENTION_DAYS=0` and let the
  storage expire old objects with its own lifecycle rule.

**3. Point the service at it** in `.env.production`:

```bash
BACKUP_DEST=offsite:my-bucket/maburvm-panel
BACKUP_AGE_RECIPIENTS=age1...
# Optional, defaults shown. Days to keep (0 keeps all), daily time in UTC, and a
# URL fetched after each successful backup.
BACKUP_RETENTION_DAYS=30
BACKUP_TIME=02:30
BACKUP_HEARTBEAT_URL=
```

`BACKUP_DEST` has to name a remote from `rclone.conf` (`name:path`). A plain
path is refused: it would be a directory inside the container, on this host.
`BACKUP_AGE_RECIPIENTS` takes several keys separated by commas; each can decrypt
on its own. The service refuses to start a backup if a private key is pasted
there.

**4. Start it and take the first backup now.**

```bash
C="docker compose -f docker-compose.production.yml --env-file .env.production"
$C up -d --build panel-backup
$C exec panel-backup panel-backup check    # database reachable, destination writable?
$C exec panel-backup panel-backup once     # one backup, now
```

`check` uploads a one-line file, `panel-backup-check.txt`, to prove the
credentials can write. It also runs each time the container starts.

**5. Know when it stops working.** A backup that failed last week is the usual
way to discover there is no backup.

- `docker compose ... logs panel-backup` shows every run (`backup ok: <file>` or
  `ERROR: backup FAILED`). A failed run is tried once more an hour later.
- The container turns `unhealthy` after a failed backup or a failed start-up
  check, and healthy again after the next good run.
- Set `BACKUP_HEARTBEAT_URL` to a monitor that alerts when the nightly fetch does
  not arrive. This is the only one of the three that also catches a host that is
  down.

Then do 9.4 once. Until a backup has been restored, you do not know that you
have one.

### 9.3 Restore onto a fresh host

You need: a host with Docker and the Compose plugin, this repository at the
version that took the backup **or newer** (the panel migrates a database forward
on boot; never start an older panel on a newer database), the backup file, and
the age private key.

```bash
# Anywhere with access to the storage: fetch the newest backup.
rclone lsf --include 'maburvm-panel-*.tar.age' offsite:my-bucket/maburvm-panel/ | sort | tail -1
rclone copy offsite:my-bucket/maburvm-panel/maburvm-panel-<time>.tar.age .

# On the new host, in the repo directory, with the backup and the key copied over:
scripts/panel-restore.sh maburvm-panel-<time>.tar.age maburvm-backup.key
```

The script:

1. puts back `.env.production` and `deploy/secrets/` from the backup where they
   are missing (it never overwrites a file that exists),
2. starts PostgreSQL and restores the database,
3. restores the panel data volume, `secrets.json` included,
4. starts the whole stack, `panel-backup` included, and prints what to do next:
   switch off the old host for good, delete the key from the new host, move DNS
   and the TLS proxy (section 7), allow the new address on each node's agent
   port, sign in with a two-factor account, check every node comes online.

It refuses, before changing anything, when a panel is already running on the
host, when the database already holds tables, or when the data volume already
holds a different `secrets.json` (which happens if the stack was started once
before the restore). `--force` overrides all three: the database is replaced,
and the old `secrets.json` is kept beside the restored one as
`secrets.json.replaced-<time>`.

**The restored panel knows the nodes as they were when the backup was taken**,
up to a day earlier. Everything done since is missing from it: a VM created
after the backup still runs on its node but is not in the panel, which does
not know its addresses are taken; a VM deleted after the backup is listed but gone; a
password, firewall rule or plan changed since is back at its old value; jobs
that were queued at backup time run again. Before letting customers back in or
creating VMs, compare each node's VM list (`virsh list --all`) with the panel's
and settle the differences.

Only restore files that came from your own destination. Encryption keeps a
backup secret; it does not prove who made it.

To look inside a backup without restoring it:
`age -d -i maburvm-backup.key maburvm-panel-<time>.tar.age | tar -x`.

### 9.4 Rehearse a restore

Do this after first setting up backups, after every PostgreSQL major upgrade, and
at least once a quarter. Use a scratch machine or VM with Docker, **never the
production host**.

```bash
scripts/panel-restore.sh --rehearsal maburvm-panel-<time>.tar.age maburvm-backup.key
```

A panel restored from a production backup believes it is production. Left
connected it would drive the real nodes, send mail, call the DNS provider and
billing, and upload its own backups over the real ones. `--rehearsal` adds
`docker-compose.rehearsal.yml`, which removes the panel's route to anything
outside the machine, and does not start `panel-backup`. With `--rehearsal` the
script refuses to run on a host where a panel is running, `--force` or not.

Then check, through an SSH tunnel to the scratch machine's loopback web port
(`ssh -L 3000:127.0.0.1:3000 scratch-host`, open `http://localhost:3000`):

1. Sign in with an account that has two-factor authentication. An accepted code
   proves `secrets.json` matches the database, which is the part a plain
   database dump never covers.
2. The VM, node and user lists match production as of the backup time. Nodes
   show offline; they are unreachable on purpose.

Write down the date and how long it took, then destroy the copy:

```bash
docker compose -f docker-compose.production.yml -f docker-compose.rehearsal.yml \
  --env-file .env.production down -v
```

The script also put production's `.env.production` and `deploy/secrets/` on the
scratch machine. Delete them there, with the key and the backup file.

`scripts/test-panel-backup.sh` runs the same dump, encrypt, decrypt and restore
round trip against any PostgreSQL with throwaway databases, without Docker. CI
runs it inside the backup image on every push.

### 9.5 Object storage (MinIO / S3)

The bundled MinIO is not covered by the panel backup. Back up the `minio_data`
volume or use `mc mirror` against the bucket, to a different host:
```bash
mc mirror maburvm/ /mnt/backups/maburvm-$(date +%F)/
```
If using external S3, use your provider's snapshot/versioning.

### 9.6 Backups of customers' VMs

Where VM backups are kept is set in the panel: **Settings, System, Backup**.
There can be several destinations; new backups go to the default one, and
every backup records the destination it was made in, so changing the default
keeps older backups restorable and deletable. A destination is one of:

- an **S3-compatible bucket** (endpoint, region, bucket, optional path prefix,
  access key, secret key, HTTPS on or off);
- an **SFTP server** (host, port, user, password or private key without a
  passphrase, directory). Its host key is pinned: paste the line from
  `ssh-keyscan`, or use **Read from server** and compare the fingerprint
  with your provider's before saving. A server presenting another key is
  refused;
- a **directory on the nodes**: an NFS or CIFS share, or a disk of its own,
  mounted at the same path on each node that backs up there. A node refuses a
  directory on its root file system and stops writing when the mount is gone.
  A disk mounted on one node holds that node's backups only.

Secrets are stored encrypted with the panel's AES key and are never shown
again. The S3 destination saved before this version becomes the default when
the panel upgrades.

- An S3 bucket or SFTP server must be reachable from **every node** (they
  write and read the backups) and from the **panel** (it deletes them, by hand
  or by a schedule's retention). A directory on the nodes is deleted from
  through the node that wrote the backup. Use storage outside the panel host
  and outside the nodes' own disks.
- SFTP servers and directories need the current agent on every node that uses
  them; an older one is refused with "needs an agent update" instead of
  writing to its own storage.
- **Test destination** on that page connects from the panel and from each
  selected node, writes a small file, reads it back and deletes it. A node
  that answers "Needs an agent update" runs an agent from before this setting:
  update it under Nodes. Until then that node keeps using the keys in its own
  environment file, and its backups are marked as kept in the node's storage.
- A node checks that the store accepts a write before it exports a disk, so a
  wrong key fails in seconds. A failed backup is tried at most three times, and
  not at all when the reason will not change (no destination, refused
  credentials, missing VM or disk). The reason is shown on the backup.
- Deleting a backup deletes its file first. If the store cannot be reached the
  backup stays listed and the delete can be repeated.
- Backups made before a destination was set are marked as kept in the node's
  own storage. The panel deletes a backup's file only when it finds it in the
  destination's bucket; otherwise the backup is removed from the panel and the
  administrator is told that no file was deleted.

**Restore as a new VM** (VM page, Backups, administrators only) creates a new
VM from a backup on any active node: the case where the original node is gone.
The new VM belongs to the same owner, has the same size, limits and firewall
rules, and gets a new address from the chosen node's pools. The node must pass
the destination test first; nothing is created on a node that cannot read the
bucket, on a node whose agent predates this feature, or for a backup whose file
is no longer in the bucket. A backup kept in a node's own storage can only be
restored on a node whose environment file reaches that storage. The download
may take hours for a large disk; the new VM shows as creating until it is done,
and a panel restart in the meantime does not interrupt it. The original VM and
the backup are not changed.

**Incremental backups.** Staff can set a VM's schedule to take a full backup
every N days and, in between, copy only what changed since the backup before
(customers cannot turn it on). With it set, a running VM on a node with libvirt
7.2 or newer and QEMU 6.0 or newer, whose disks are all qcow2 files, is copied
with libvirt's backup API and leaves a checkpoint (a change-tracking bitmap
inside each disk) for the next backup. Anything else gets a full backup, and
the backup says why: an older node, a disk on LVM, a VM that was off, a
checkpoint lost when the VM was killed rather than shut down, or disks replaced
by a restore, a reinstall, a snapshot revert or a move to another node. Every
other VM is backed up with temporary overlays exactly as before. The API copy
needs room beside the disks for the disks' size plus the largest one, held for
the whole backup so that other backups on the node leave it alone, and falls
back to overlays when that room is not there. Restoring an incremental backup downloads it with every backup
it builds on, back to its full one; retention and plan allowances never delete
a backup another kept one needs, and such a backup cannot be deleted by hand
before the newer ones. Customer snapshots and their reverts remove the VM's
checkpoints, so the next backup after one is full.

The **Backups** page in the sidebar lists every VM with its last successful
backup, its last result, schedule and next run, failing and stale ones first,
with what each destination holds and how many backups run and wait on each
node.

After setting a destination, prove it once per node with a throwaway VM: make a
backup, restore it in place, and restore it as a new VM on another node. For
incremental backups, take two scheduled backups of a running throwaway VM, write
some files in between, and restore the second.

## 10. Static validation

Validate that the file parses and all referenced secrets exist — this does
**not** deploy and does not require containers to be running:

```bash
make docker-prod-validate
# equivalent to:
docker compose -f docker-compose.production.yml --env-file .env.production config >/dev/null

# (Optional, recommended) Reproducible gate: a FRESH host web build with the
# production API base, immediately followed by the exact rewrite assertion against
# the newly produced host manifest. Avoids the stale-manifest bypass of running
# the assertion alone. Builds on the host with npm (NOT Docker); it does not
# populate a Docker image.
make web-routes-build-check
# (Alternative, assertion-only) Check an EXISTING web/.next manifest without
# rebuilding — may be stale; use only right after you built it.
make web-routes-check
```

> Validation was **not** executed as part of writing this guide (no Docker
> runtime in the authoring environment). Run it on a host with Docker before
> deploying.

## 11. Start / stop

```bash
make docker-prod-up      # up -d
make docker-prod-down    # down
```

The web service waits for `panel` (`/readyz` healthy); `panel` waits for
`postgres` and `minio` (`service_healthy`). Supported `depends_on` relationships
include `restart: true` so a depended-on service restart is propagated.

## 12. Templates that are not Linux

A template records what kind of system its image holds (Templates, Add from
URL, "System type"). The kind decides how a VM built from it is set up. A VM
can be reinstalled with a template of any kind: the node rewrites the VM's
virtual hardware to suit the new system and keeps its network card, addresses
and console password. Windows needs at least 2 GB of memory and a 32 GB disk,
so a smaller VM cannot be reinstalled as Windows.

Each node needs `guestfish` (package `libguestfs-tools`) for RouterOS, the
same package the Linux path already uses for `virt-customize`.

### MikroTik RouterOS (CHR)

The template catalog lists the builds that were tested: 7.24.6 and 6.49.20.
The node downloads MikroTik's zipped image itself. A router gets its address,
gateway, DNS, name and a generated admin password before its first start.

- Do not use RouterOS 6.49.21, 6.49.22 or 6.49.23. As a VM they never save
  their configuration, so every restart returns the router to its first-boot
  state, password included.
- A password reset on RouterOS 7 restarts the router: version 7 does not let
  the guest agent change users, so the new password is applied at start-up.
- Routers get no default firewall rules, SSH keys or first-boot script.
- MikroTik's free licence limits each interface to 1 Mbps of upload until the
  owner adds a licence of their own.

### Windows Server

Microsoft does not allow a Windows image to be redistributed, so none is
listed. Build one on a node (about fifteen minutes once the media is
downloaded, unattended, nothing installed on the host):

```bash
scripts/build-windows-template.sh --version 2022 \
  --out /var/lib/libvirt/templates/windows-2022.qcow2
```

Then add it under Templates, Add from URL, with the system type set to Windows
and that path. The file must be at the same path on every node that should
offer it, or be served from a URL the nodes can reach.

- By default the script uses Microsoft's evaluation media, which runs
  unactivated for 180 days. The owner of a VM activates it with their own key:
  `DISM /Online /Set-Edition:ServerStandard /ProductKey:XXXXX-XXXXX-XXXXX-XXXXX-XXXXX /AcceptEula`.
  Pass `--iso` to build from your own licensed media instead. Whether your
  licensing terms allow hosting it for customers is yours to check.
- A Windows VM needs at least 2 GB of memory and a 32 GB disk; smaller orders
  are refused.
- Windows takes about two minutes after creation to finish setting itself up.
  It is reached over Remote Desktop as Administrator; the default firewall
  opens 3389 instead of 22.
- An image built another way must carry the virtio drivers, the QEMU guest
  agent and cloudbase-init reading the NoCloud disc, and must have
  `RealTimeIsUniversal` set, or its clock will be off by its time-zone offset.
- `--uefi` builds an image that boots with UEFI (a GPT disk with an EFI system
  partition) under OVMF, which the build host needs (the `ovmf` package). Add
  it with the template's boot firmware set to UEFI, and tick Secure Boot and
  TPM if its VMs should have them. VMs are created from it only on nodes with
  OVMF (and swtpm for a TPM); BIOS images (the default) work everywhere. A VM
  is reinstalled only from an image for the firmware it was created with.
- To install Windows by hand instead, add the installer as an ISO, mark the
  virtio drivers ISO (`virtio-win.iso`) as the drivers disc on the ISOs page,
  and create a VM with the Windows ISO as its system: it gets an empty disk,
  the installer in drive 1 and the drivers in drive 2.

## 13. Storage pools and disk overcommit

A storage pool is a directory on a node that holds VM disks as qcow2 files. To
keep disks on a ZFS dataset, an NFS export or CephFS, mount it on the node and
register the mount point under Storage, Add Pool. The panel measures each
pool's capacity and free space on the node every minute; they are not set by
hand. The pool marked for new VMs is where that node creates disks, and a VM's
extra disks are created beside its own.

Disks are thin: a 100 GB disk takes a few GB on the day it is created and grows
as the guest writes. So a pool can be promised more disk than it holds, and it
fills later, without a new order. A pool that fills pauses every VM on it. The
Storage page and each node's page show, per pool, how much is allocated next to
its capacity.

Two rules decide whether a new or grown disk is accepted:

- The whole disk must fit in the pool's free space right now, with 20 GB kept
  free. This always applies.
- With a disk overcommit ratio set (Settings, System, Quotas), everything
  allocated on the pool, plus the new disk, must stay within capacity times
  that ratio. It is not set by default, because a pool that is already past the
  ratio you pick would refuse every new order. Read the allocated figure for
  each pool first and set the ratio above the highest one.

The count covers the VMs the panel manages. Disks of machines on the node that
the panel does not know about are not in it.

## 14. Alerts and node monitoring

The panel checks every node once a minute, all at the same time and each under
its own time limit, so a node that hangs does not delay the others. A node that
misses three checks in a row is marked offline: no new VM is placed on it, and
its VMs are shown as "State unknown" to staff and to their owners, because the
panel can no longer tell whether they run. It is marked active again on the
first check it answers. The number of checks is a setting.

Set up delivery on the Alerts page. Until you do, alerts are only listed there.

| Alert | Raised when | Closes |
|---|---|---|
| Node offline | the agent port stops answering for the set number of checks | when the node has answered again for the settle time |
| Node not reporting | the port answers but the agent sends no figures (hung, refusing the panel, or too old) | same |
| Storage pool | a pool passes its own alert level (warning) or the panel-wide critical level | when it is back under |
| Node not filtering guest traffic | a node reports bridge netfilter off | when it reports it on |
| Backup failed | a backup fails with no retry left | one message, not a state |
| Job failed | any other background job fails for good | one message, not a state |

An alert is sent once when it opens and once when it closes. A node that goes
up and down inside the settle time (ten minutes by default) is one alert, and
the recovery is sent only after it has stayed up that long. The same backup or
job failing again within the settle time is recorded but not sent again.
Alerts raised in the same minute arrive as one message. Open alerts are kept
in the database, so a panel restart neither loses nor repeats them. A node in
maintenance raises no node alerts.

Email uses the mail server under Settings, System, Email. Note that the panel
cannot report its own death, and cannot send mail through a mail server that
runs on the node that just went down: keep one check of the panel from outside.

### Webhook

Choose Slack or Discord to post a line of text to that service's incoming
webhook. The JSON format posts this body to any receiver:

```json
{
  "version": "1",
  "source": "MaburVM",
  "panel_url": "https://panel.example.com",
  "sent_at": "2026-01-01T10:00:00Z",
  "text": "Critical: Node node1 is offline\nIt has missed 3 checks in a row. ...",
  "alerts": [
    {
      "id": "0b6f...",
      "key": "node_offline:<node id>",
      "kind": "node_offline",
      "status": "firing",
      "severity": "critical",
      "title": "Node node1 is offline",
      "detail": "It has missed 3 checks in a row. ...",
      "resource": { "type": "node", "id": "<node id>", "name": "node1" },
      "fired_at": "2026-01-01T09:59:00Z",
      "resolved_at": null
    }
  ]
}
```

`status` is `firing`, `resolved` or `event` (a failure that has no "resolved").
`kind` is one of `node_offline`, `node_silent`, `storage_pool`, `firewall_off`,
`backup_failed`, `job_failed`, `network_drift`. `severity` is `critical` or `warning`. `key` is
the same for the firing and the resolved message of one alert. A test alert
has `"test": true`. Fields may be added; none of these are renamed or removed.

With a signing secret saved, each request carries `X-Webhook-Signature`: the
HMAC-SHA256 of the raw body with that secret, in hex. A receiver must answer
2xx within ten seconds; anything else is tried again a minute later, five times
in all. After that a backup or job failure is left to the Alerts page, while an
alert that is still open keeps being tried about every fifteen minutes until it
is delivered or closes. The address and the secret are stored encrypted with the
panel's AES key and are never shown again.

### Prometheus

With a metrics token set (Settings, System, Panel, or `METRICS_TOKEN`),
`/metrics` (bearer token) carries two signals made for alerting on a node, both
taken from the panel's own checks:

- `maburvm_node_up{node}` is 1 when the node's agent reported on the last
  check and 0 when that check got no report.
- `maburvm_node_last_report_timestamp_seconds{node}` is when it last reported.
  It stops advancing the moment a node goes quiet.

```yaml
groups:
  - name: maburvm
    rules:
      - alert: NodeDown
        expr: maburvm_node_up == 0
        for: 3m
      - alert: NodeNotReporting
        expr: time() - maburvm_node_last_report_timestamp_seconds > 300
      - alert: PanelCollectorStalled
        expr: time() - max(maburvm_node_last_report_timestamp_seconds) > 300
      - alert: PanelDown
        expr: up{job="maburvm"} == 0
        for: 2m
```

The usage gauges (`maburvm_node_cpu_usage_percent` and the rest) are absent for
a node with no reading in the last five minutes, instead of holding their last
value. `maburvm_node_status` is the status the panel stores, which follows the
checks above.

The same endpoint carries the panel's own state, read from the database at
scrape time:

| Metric | Labels | Meaning |
|---|---|---|
| `maburvm_queue_jobs` | `kind`, `state` | Background jobs not finished (available, scheduled, retryable, running, pending). |
| `maburvm_queue_oldest_available_job_age_seconds` | `queue` | How long the oldest job ready to run has waited. Grows when a queue is stuck. |
| `maburvm_queue_jobs_finished_recently` | `kind`, `state` | Jobs cancelled or discarded that the queue still keeps (about a week). |
| `maburvm_queue_job_failures_total` | `kind`, `outcome` | Counter of failed tries since the panel started: `retry` (tried again) or `failed` (for good). |
| `maburvm_backups` | `status` | Backups by status. |
| `maburvm_backup_last_success_timestamp_seconds` | `vm_id`, `vm` | When the VM's newest completed backup finished. Absent for a VM with none. |
| `maburvm_backup_last_failed` | `vm_id`, `vm` | 1 when the VM's newest backup failed. |
| `maburvm_storage_pool_capacity_bytes`, `_available_bytes`, `_allocated_bytes` | `node`, `pool`, `pool_id` | Pool size and free space as the node last reported them, and the virtual disk placed in it. |
| `maburvm_ip_pool_free_addresses` | `pool_id`, `pool` | Addresses free to hand out. |
| `maburvm_ip_pool_addresses` | `pool_id`, `pool`, `status` | Addresses by status. |
| `maburvm_alerts_firing` | `kind`, `severity` | Alerts open now. |
| `maburvm_ops_collect_success` | `group` | 0 when a group above could not be read on this scrape. |

```yaml
      - alert: JobQueueStuck
        expr: max(maburvm_queue_oldest_available_job_age_seconds) > 900
      - alert: JobsFailing
        expr: increase(maburvm_queue_job_failures_total{outcome="failed"}[1h]) > 0
      - alert: BackupFailed
        expr: maburvm_backup_last_failed == 1
      - alert: BackupStale
        expr: time() - maburvm_backup_last_success_timestamp_seconds > 2 * 86400
      - alert: PoolNearlyFull
        expr: maburvm_storage_pool_available_bytes / maburvm_storage_pool_capacity_bytes < 0.1
      - alert: IPPoolNearlyEmpty
        expr: maburvm_ip_pool_free_addresses < 5
```

The panel logs one JSON object per line on standard error: requests (method,
path without its query string, route, status, latency), its own messages and
the job queue's. `docker compose logs panel | jq` reads them.

### Background job retries

A job that drives a node is tried a bounded number of times, spaced further
apart each time: VM operations, snapshots, template syncs and imports 5 times
(about six minutes), network changes 10 times (about four hours, so a node that
is briefly away still gets them), backups, restores and image captures 3 times
(each try moves a whole disk), customer emails 8 times (about an hour). A
failure that another try cannot change (the template missing on the node, the
VM's domain gone, no space left, a refusal) stops at once. Jobs queued before
an upgrade stop at the same point. When a job fails for good, a "Job failed" or
"Backup failed" alert is raised, and a VM whose creation or reinstall failed is
marked as failed.

Jobs run in four queues with their own workers (Settings, Job queue): VM
operations, restores, snapshots and network changes in `critical`; backups,
image captures and imports in `batch`; audit records in `audit`; the rest in
`default`. Panels before this release put every job in `default`, whatever the
queue settings said; jobs already queued there when you upgrade still run. A
second request for the same operation on the same VM while one is still queued
or running is answered with the job already there.

## 15. Customer emails, notifications and rescue mode

**Emails.** Every email to a customer is written to the email log (Settings,
System, Email) and sent by the job queue, which retries a mail server that
refuses or is down. Failed emails can be sent again from the log. The log keeps
90 days and never shows the message body (it can hold a password-reset link).
Besides account and server emails (welcome, password reset, server ready,
reinstalled, rescue on and off), customers are emailed when a server is
suspended or unsuspended, when a backup of their server fails after its last
try, when its transfer reaches a warning level, and when their password is
changed, two-factor sign-in is turned off or an API key is created for their
account. Each can be reworded or switched off under Email templates. Nothing is
sent until a mail server is set up.

**Notifications.** Customers see the same events under Notifications in their
area, with a read state kept per account. The administrator's bell is
unchanged.

**Rescue mode.** Enter rescue boots a stopped server into a rescue system so
its owner can repair it. Under Settings, System, Rescue choose the **built-in
rescue system**, a small qcow2 cloud image (an http(s) URL with its sha256, or a
path on every node; one no template is built from): each rescue copies it beside
the server's disk, sets it up with libguestfs with the server's own address, a
one-time root password shown once in the panel and the owner's chosen SSH keys,
and boots it first with the server's disks attached. And/or a **rescue ISO**
(an http(s) URL each node downloads, or the path of an ISO on every node), used
where no built-in system is set or the node's agent is older. Until an ISO is
saved there, `RESCUE_ISO_URL` in the panel's environment is used; with nothing
at all, the button is disabled for customers and says rescue mode is not
available.

**Deleting an account that owns VMs** is refused until you choose, on the
account's page, to give its VMs (and their floating IPs) to another account or
to delete them with it. Each VM given away is recorded in the audit log.

**Detaching a data disk deletes it.** A detached disk is not kept anywhere it
could be reattached from, so the panel asks for confirmation and deletes the
disk with everything on it.

## 16. Releases, upgrades and rollback

### 16.1 Releases

Pushing a tag such as `v1.2.0` runs `.github/workflows/release.yml`. It builds
and pushes `ghcr.io/<owner>/maburvm-panel:<tag>` and `maburvm-web:<tag>` for
linux/amd64 and linux/arm64 (and `:latest` for a tag without a `-` suffix),
stamped with the tag and commit (Settings, System, Version shows them), builds
the agent binaries (amd64 and arm64) with a `SHA256SUMS` file, and creates a
**draft** GitHub release with notes drafted from the Conventional Commit
subjects (`scripts/release-notes.sh`), the database migrations the release adds,
and the install and upgrade commands. Review the draft, then publish it. New
packages on the registry are private until you make them public.

Agent signatures are optional (§6): set the repository secret
`AGENT_SIGNING_KEY` to sign in the workflow, and the repository variable
`AGENT_UPDATE_PUBKEY` to have the workflow verify its own signatures. The
workflow checks that the image bundles exactly the released agent binaries.

To run published images, set in `.env.production`:

```bash
MABURVM_IMAGE_PREFIX=ghcr.io/OWNER/maburvm
MABURVM_VERSION=v1.2.0
```

Leave both unset to keep building on the host (`maburvm-panel:local`). Never
`build` an install that runs published images: it would overwrite the tag.

### 16.2 Upgrading the panel

```bash
scripts/upgrade-panel.sh --version v1.3.0   # published images
scripts/upgrade-panel.sh --build            # built from this directory
```

In order, it:

1. tags the running images `maburvm-panel:rollback` and `maburvm-web:rollback`
   and copies `.env.production` to `backups/`;
2. runs an off-host backup now (`panel-backup once`) when panel-backup is
   configured (§9), and stops if that fails (`--skip-offsite-backup` to go on);
3. pulls (or builds) the new images while the current panel keeps running;
4. turns maintenance mode on (`panel maintenance on --hold-jobs`): customers and
   the billing webhook can read but not change anything, new jobs are held, and
   running ones get `--drain-timeout` seconds (default 900) to finish. If some
   are still running then, maintenance is switched off and the script stops with
   nothing else changed (`--no-drain` skips this step; a panel from before
   maintenance mode is upgraded without it);
5. stops the panel (web stays up) and dumps the database through the postgres
   container into `backups/pre-upgrade-<time>.dump` (mode 0600, the newest five
   kept), checks the dump reads back, and records it in the database. If the
   dump fails, maintenance is switched off, the previous panel is started again
   and nothing else changes;
6. starts the new panel alone, which applies pending migrations as it starts,
   then web, and waits for both health checks (`--timeout`, default 30 minutes
   for the panel), then switches maintenance off so the held jobs start.

If the panel or web does not become healthy, it puts the previous images and
`.env.production` back. If migrations ran, it asks whether to restore the dump
(`--yes` restores without asking, `--no-restore` never does): the dump is
restored into a new database which then replaces the current one, kept as
`<db>_replaced_<time>` until you drop it. The panel was stopped for the dump, so
a restore loses only what the new version wrote. Then it switches maintenance
off in the database (the dump was taken in maintenance mode), starts the
previous images (by their `:rollback` tags, whatever `.env.production` names)
and exits non-zero.

A restore with `scripts/panel-restore.sh` (§9) records the restored archive the
same way, so a newer panel image may migrate the restored database.

**Migrations at boot.** Each migration runs in its own transaction with its own
time limit, `MIGRATION_TIMEOUT` (default `30m`); one that runs out is rolled
back and the panel stops with a message naming it. Raise it for an upgrade whose
notes warn of a long migration. With `MIGRATION_REQUIRE_BACKUP_WITHIN` set (for
example `6h`; the installer sets it), the panel refuses to apply pending
migrations to a database that already has accounts in it unless the upgrade script
recorded a dump within that time, so an upgrade started with a plain
`docker compose up` cannot migrate without a way back. The panel's health check
allows ten minutes before a slow start counts as unhealthy.

```bash
# What is applied and what is pending, and the newest recorded dump:
docker compose -f docker-compose.production.yml --env-file .env.production \
  run --rm --no-deps panel migrate status
```

### 16.3 Rolling back by hand

Prefer restoring the dump taken before the upgrade: it returns the database to
exactly what the previous version knew. Stop the panel and web, then:

```bash
DC="docker compose -f docker-compose.production.yml --env-file .env.production"
$DC stop panel web
$DC exec -T postgres createdb -U maburvm maburvm_restoring
$DC exec -T postgres pg_restore -U maburvm -d maburvm_restoring --no-owner --no-acl \
  --single-transaction --exit-on-error < backups/pre-upgrade-<time>.dump
$DC exec -T postgres psql -U maburvm -d postgres \
  -c 'ALTER DATABASE maburvm RENAME TO maburvm_replaced' \
  -c 'ALTER DATABASE maburvm_restoring RENAME TO maburvm'
# Put back the previous images: the old MABURVM_VERSION in .env.production, or
docker tag maburvm-panel:rollback maburvm-panel:local
docker tag maburvm-web:rollback maburvm-web:local
$DC up -d --no-build --force-recreate panel web
```

Without a dump, the new version can undo its own migrations, newest first. It
lists them and changes nothing until `--yes` is added; run it with the panel
stopped and only after a backup:

```bash
$DC stop panel web
$DC run --rm --no-deps panel migrate down 2
$DC run --rm --no-deps panel migrate down 2 --yes
```

Some changes are not undone by going back:

- **Credentials encrypted at first boot.** A panel from before encryption at
  rest (§5) cannot read the agent tokens, VNC and mail passwords it finds
  encrypted. Started on the upgraded database it sends nodes tokens they refuse,
  and consoles and mail stop. Restore the pre-upgrade dump instead.
- **Migration 065 down** removes the per-node column from port forwards but
  does not put back the fleet-wide unique index: forwards made since may share
  a port across nodes, and the index would not build.
- **Data a newer version wrote** is dropped by a down migration that removes its
  table or column. Restoring the dump loses everything written after it.
- **Panel settings saved on the Settings, System, Panel page** are read only by
  this version and later. Rolled back to an older panel, the environment
  variables apply again; keep them in `.env.production` until you no longer
  expect to go back.
