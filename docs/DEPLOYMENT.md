# MaburVM Production Deployment Guide

> **Status: hardening guidance, NOT a certification.** This document describes a
> safer, explicit production entrypoint (`docker-compose.production.yml`). It does
> **not** claim the system is production-certified. You are responsible for
> reviewing every image tag, supplying real secrets, terminating TLS, backing up
> data, and deploying the KVM agent on each hypervisor host.

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
| `docs/DEPLOYMENT.md` | This guide. |
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

> **Exception / future work:** Privileged-agent minimization is an explicit
> exception that requires a dedicated minimization test before any change. Do not
> attempt to remove `--privileged` without that test.

> **Agent bootstrap is NOT a production solution here.** The `/install-agent.sh`
> script and the bare `docker run` example above are documented only to convey the
> separate/privileged nature of agents and that the panel initiates gRPC. The
> automated installer / agent artifact path is intentionally **unavailable** and
> the public bootstrap endpoints are **503-contained** until Phase 1's verified
> agent deployment and trust contract. Do not represent `/install-agent.sh` or
> this `docker run` as a production deployment method. Privilege minimization and
> trusted deployment automation are owned by Phase 1.

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

## 8. Image / tag review

Before each deploy, confirm every tag in `docker-compose.production.yml` is the
exact pinned version you intend:
- `postgres:16.14`
- `minio/minio:RELEASE.2025-09-07T16-13-09Z` (same image used for `minio-init`)
- `node:22.23.1-alpine3.24` (Dockerfile.web build/runtime)
- `alpine:3.24` (Dockerfile.panel / Dockerfile.agent runtime)
- `maburvm-panel:local` / `maburvm-web:local` (built from the local Dockerfiles;
  `web` builds from the **repo-root** `Dockerfile.web`, not `./web`)

No `latest`, and **no invented digests** — only tags that exist upstream. Image
tag review, SBOM, and CVE scanning — plus actual Docker image build/start and
runtime tests — remain **release gates** (not executed as part of authoring).

For the **self-contained internal Postgres** in THIS file, `DB_SSL_MODE` defaults
to `disable` (the stock `postgres` image has SSL OFF). Pointing at a REMOTE or
MANAGED Postgres is NOT a supported switch in this file — it needs a separately
authored Compose model with real server certs + client config and its own
validation; set `DB_SSL_MODE` there accordingly and verify the cert chain.

## 9. Backups & restore

### PostgreSQL
```bash
# Backup
docker compose -f docker-compose.production.yml --env-file .env.production \
  exec -T postgres pg_dumpall -U "${POSTGRES_USER:-maburvm}" \
  > backup-$(date +%F).sql

# Restore (into a stopped/clean DB or a fresh volume)
docker compose -f docker-compose.production.yml --env-file .env.production \
  exec -T postgres psql -U "${POSTGRES_USER:-maburvm}" < backup.sql
```

### Object storage (MinIO / S3)
Back up the `minio_data` volume or use `mc mirror` against the bucket:
```bash
mc mirror maburvm/ /mnt/backups/maburvm-$(date +%F)/
```
If using external S3, use your provider's snapshot/versioning.

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
