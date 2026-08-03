# MaburVM

A modern QEMU/KVM Virtual Machine management panel built as a replacement for Virtualizor. Designed for VPS hosting businesses managing 500–2000 VMs across multiple physical nodes.

## Architecture

```
Panel Server:
  Next.js Frontend (:3000) --> Go API (Echo :8080) --> PostgreSQL (DB + River Queue)
                                      |
                                      | gRPC (:50051)
                                      v
  KVM Node Agent --> libvirt/QEMU --> Virtual Machines
```

- **Panel**: Central management server (Go API + Next.js frontend)
- **Agent**: Lightweight Go agent running on each KVM hypervisor
- **Communication**: gRPC with TLS between panel and agents

## Features

- **VM lifecycle** — create, start/stop/restart, rebuild/reinstall, delete, live status, resource resize
- **In-browser consoles** — VNC (noVNC) and SSH (xterm.js) proxied through the panel
- **Images** — capture a VM's disk to object storage; images survive VM deletion and can seed a new VM (Vultr/DigitalOcean-style)
- **Snapshots & backups** — libvirt snapshots, scheduled/manual backups to S3-compatible storage, restore
- **Networking** — IP pools/IPAM, managed private networks (VLAN), firewall rules, port forwarding, bandwidth shaping & monthly data quotas
- **Multi-tenant** — admin panel + self-service client portal, per-tenant ownership isolation, role-based access
- **Accounts** — 2FA (TOTP), IP whitelisting, forgot/reset password, SSH-key management, WHMCS provisioning module
- **Operations** — audit logging, dashboard stats, OS-template sync across nodes, cloud-init guest configuration

## Tech Stack

| Layer | Technology |
|-------|-----------|
| Backend API | Go 1.25, Echo v4 (HTTP) |
| Frontend | Next.js 15, React 19, TypeScript 5, Tailwind CSS 3, shadcn/ui |
| Database | PostgreSQL 16 (via GORM) |
| Job Queue | River (PostgreSQL-based) |
| Object Storage | S3-compatible (MinIO / Cloudflare R2) for backups & images |
| Panel-Agent Comm | gRPC with TLS |
| Agent-KVM | libvirt-go bindings |

## Quick Start

### Prerequisites

- Go 1.25+
- Node.js 20+
- PostgreSQL 16+
- Docker & Docker Compose (recommended)

### Development Setup

```bash
# 1. Clone the repository
git clone https://github.com/maburvm/panel.git
cd panel

# 2. Start infrastructure
docker compose up -d postgres minio mailpit

# 3. Copy and configure environment
cp .env.example .env
# Edit .env with your settings (JWT_SECRET_KEY, AES_KEY, DB credentials)

# 4. Install dependencies
make install

# 5. Run database migrations (forward-only: applied migrations are immutable;
#    see docs/MIGRATION_RECOVERY.md for rollback/recovery policy)
make migrate

# 6. Create admin user
go run ./cmd/create-admin

# 7. Start development servers
make dev
```

The panel API will be available at `http://localhost:8080` and the web interface at `http://localhost:3000`.

### Production Deployment

Production is a separate, explicit composition and is **not** started by the
`docker compose up` development flow (and never via `--profile production`, which
this repo does not define). See [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) for the
full guide (secrets, TLS, agent deployment, backups, image review).

The short version:

```bash
# 1. Copy and fill in the production env. The template ships EMPTY guarded values
#    (no CHANGE_ME* placeholders); Compose refuses to start if any required value
#    is blank, so replace every empty mandatory value with a real one.
cp .env.production.example .env.production

# 2. Create the host secret files (mode 0400, owned by root; the container root
#    entrypoint/init reads them before dropping to the app UID). A trailing
#    newline is NOT preserved as part of the secret (postgres strips it via
#    command substitution; the panel trims whitespace) — prefer a precisely
#    written value. Never commit deploy/secrets/.
mkdir -p deploy/secrets
install -m 0400 -o root /path/to/password.txt deploy/secrets/postgres_password
install -m 0400 -o root /path/to/s3pass.txt   deploy/secrets/s3_restricted_password
# Also set PANEL_PUBLIC_URL + ALLOWED_ORIGINS + MINIO_ROOT_* in .env.production.

# 3. Build the images you need (panel + web at minimum). web builds from the
#    repo-root Dockerfile.web and MUST pass a non-localhost API_BASE_URL with
#    ENFORCE_API_BASE_URL=1 (the build fails if it bakes localhost).
docker build -f Dockerfile.panel -t maburvm-panel:local .
docker build -f Dockerfile.web -t maburvm-web:local \
  --build-arg API_BASE_URL=http://panel:8080 --build-arg ENFORCE_API_BASE_URL=1 .

# 4. Start ONLY the explicit production composition.
docker compose -f docker-compose.production.yml --env-file .env.production up -d

# 5. (Optional) Validate the file parses before deploying.
make docker-prod-validate
# (Optional, recommended) Reproducible gate: fresh host web build with the
# production API base, then assert the freshly produced manifest has the exact
# panel:8080 rewrites (no localhost). Builds on the host with npm (not Docker).
make web-routes-build-check
# (Alternative, assertion-only) Check an EXISTING web/.next manifest without
# rebuilding — may be stale, use only when you just built it.
make web-routes-check
```

> **Not a production certification.** `docker-compose.production.yml` is a safer,
> more explicit entrypoint with least-privilege networks (`webpanel`, `data`,
> `agent-egress`, `edge`) and a restricted S3 user for the panel (MinIO root creds
> stay confined to `minio`/`minio-init`). The panel API is NOT published — only the
> web UI (loopback by default) is exposed, and an external TLS reverse proxy must
> target web. You remain responsible for TLS termination, real secrets, image-tag
> review/SBOM/CVE, backups, and deploying the KVM agent separately on each
> hypervisor host. Source secret files remain host-side bind mounts; managed
> secret isolation is phase 1. The automated agent installer path is intentionally
> unavailable until Phase 1.

## Configuration

All configuration is done via environment variables. See `.env.example` for the full list.

### Required Variables

| Variable | Description |
|----------|-------------|
| `DB_HOST` | PostgreSQL host |
| `DB_USER` | PostgreSQL user |
| `DB_PASSWORD` | PostgreSQL password |
| `DB_NAME` | Database name |
| `JWT_SECRET_KEY` | JWT signing key (min 32 chars) |
| `AES_KEY` | AES-256 encryption key (exactly 32 bytes) |
| `S3_ENDPOINT` | S3-compatible storage endpoint |
| `S3_ACCESS_KEY` | S3 access key |
| `S3_SECRET_KEY` | S3 secret key |
| `S3_BUCKET` | S3 bucket name |

### Optional Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `SERVER_PORT` | 8080 | API server port |
| `ALLOWED_ORIGINS` | http://localhost:3000 | Comma-separated CORS origins |
| `AGENT_GRPC_PORT` | 50051 | Agent gRPC port |
| `LIBVIRT_URI` | qemu:///system | Libvirt connection URI |

## API Routes

All routes are under `/api/v1/`:

- **Auth**: POST login, register, logout, forgot-password, reset-password, GET me
- **Nodes**: CRUD + token regeneration
- **VMs**: CRUD + lifecycle (start/stop/restart/rebuild) + reset-password + console
- **Images**: Capture from VM, list, delete, create-VM-from-image
- **Templates**: CRUD + sync to nodes
- **Networks**: Network & firewall management
- **Snapshots**: Create/restore/delete
- **Backups**: Create, schedule, restore
- **Storage**: Pool & volume management
- **Users**: CRUD (admin only)
- **Audit Logs**: Action history (admin only)
- **Dashboard**: Statistics overview

## Development Commands

```bash
make run-panel    # Run panel API on :8080
make run-agent    # Run agent gRPC on :50051
make run-web      # Run Next.js on :3000
make dev          # Run panel + web concurrently
make build        # Build all binaries
make proto        # Regenerate protobuf code
make migrate      # Run database migrations
make test         # Run Go tests
```

## Project Structure

```
maburvm/
├── api/proto/           # gRPC service definitions
├── cmd/                 # Application entrypoints
│   ├── panel/          # Panel API server
│   ├── agent/          # Node agent
│   ├── create-admin/   # CLI: create admin user
│   └── migrate/        # Database migrations
├── internal/
│   ├── agent/          # Node agent code
│   ├── panel/          # Panel API code
│   └── shared/         # Shared code (models, config, DB)
├── web/                # Next.js frontend
├── docker-compose.yml  # Development infrastructure
├── Dockerfile.*        # Production containers
└── Makefile            # Development commands
```

## License

Released under the [MIT License](LICENSE).
