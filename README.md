<div align="center">

# MaburVM

**A modern QEMU/KVM virtual machine management platform.**

Multi-tenant VM lifecycle, networking, and storage management across multiple physical nodes.

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Next.js](https://img.shields.io/badge/Next.js-15-000000?logo=nextdotjs&logoColor=white)](https://nextjs.org)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-4169E1?logo=postgresql&logoColor=white)](https://www.postgresql.org)
[![libvirt](https://img.shields.io/badge/libvirt%2FQEMU-KVM-CC0000?logo=qemu&logoColor=white)](https://libvirt.org)

[Features](#features) · [Architecture](#architecture) · [Quick Start](#quick-start) · [Configuration](#configuration) · [Deployment](docs/DEPLOYMENT.md)

</div>

---

> [!WARNING]
> **Project maturity: not yet recommended for commercial production use.**
>
> MaburVM is under active development and has **not** been hardened, audited, or
> battle-tested to the standard a paying customer workload deserves. It is not
> currently suitable as the control plane for a commercial VPS/cloud hosting
> business, where a defect can mean customer data loss, downtime, or a security
> incident affecting tenants.
>
> There is **no warranty** — see the [MIT License](LICENSE). Expect breaking
> changes, incomplete features, and rough edges. Independent security review,
> tested backups, and your own staging validation are strongly advised before
> running anything you can't afford to lose.
>
> **Scale actually exercised to date: ~66 VMs across 2 physical nodes.** The
> architecture (per-node agents, a job queue, horizontal node scaling) is intended
> to go further, but larger fleets have not been load-tested — treat any bigger
> number as an untested design target, not a capability.
>
> Suitable today for: evaluation, homelabs, internal/self-hosted infrastructure,
> and development. If you do run it in anger, please report what breaks.

## Table of Contents

- [Features](#features)
- [Architecture](#architecture)
- [Tech Stack](#tech-stack)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [API Routes](#api-routes)
- [Development](#development)
- [Project Structure](#project-structure)
- [Contributing](#contributing)
- [License](#license)

## Features

| | |
|---|---|
| 🖥️ **VM lifecycle** | Create, start/stop/restart, rebuild/reinstall, delete, live status, resource resize |
| 🕹️ **In-browser consoles** | VNC (noVNC) and SSH (xterm.js) proxied through the panel |
| 💿 **Images** | Capture a VM's disk to object storage; images survive VM deletion and can seed a new VM (Vultr/DigitalOcean-style) |
| 📸 **Snapshots & backups** | libvirt snapshots, scheduled/manual backups to S3-compatible storage, restore |
| 🌐 **Networking** | IP pools/IPAM, managed private networks (VLAN), firewall rules, port forwarding, bandwidth shaping & monthly data quotas |
| 👥 **Multi-tenant** | Admin panel + self-service client portal, per-tenant ownership isolation, role-based access |
| 🔐 **Accounts** | 2FA (TOTP), IP whitelisting, forgot/reset password, SSH-key management, WHMCS provisioning module |
| 📊 **Operations** | Audit logging, dashboard stats, OS-template sync across nodes, cloud-init guest configuration |

## Architecture

```
┌──────────────────────────── Panel Server ────────────────────────────┐
│                                                                       │
│   Next.js Frontend (:3000) ──▶ Go API (Echo :8080) ──▶ PostgreSQL     │
│                                       │                (DB + River)   │
│                                       │ gRPC (:50051, TLS)            │
└───────────────────────────────────────┼───────────────────────────────┘
                                        ▼
                        KVM Node Agent ──▶ libvirt/QEMU ──▶ Virtual Machines
```

| Component | Role |
|-----------|------|
| **Panel** | Central management server (Go API + Next.js frontend) |
| **Agent** | Lightweight Go agent running on each KVM hypervisor |
| **Communication** | gRPC with TLS between panel and agents |

## Tech Stack

| Layer | Technology |
|-------|-----------|
| Backend API | Go 1.25, Echo v4 (HTTP) |
| Frontend | Next.js 15, React 19, TypeScript 5, Tailwind CSS 3, shadcn/ui |
| Database | PostgreSQL 16 (via GORM) |
| Job Queue | River (PostgreSQL-based) |
| Object Storage | S3-compatible (MinIO / Cloudflare R2) for backups & images |
| Panel–Agent Comm | gRPC with TLS |
| Agent–KVM | libvirt-go bindings |

## Quick Start

### Prerequisites

- Go 1.25+
- Node.js 20+
- PostgreSQL 16+
- Docker & Docker Compose (recommended)

### Development Setup

```bash
# 1. Clone the repository
git clone https://github.com/billyriantono/maburvm.git
cd maburvm

# 2. Start infrastructure
docker compose up -d postgres minio mailpit

# 3. Copy and configure environment
cp .env.example .env
# Edit .env with your settings (JWT_SECRET_KEY, AES_KEY, DB credentials)

# 4. Install dependencies
make install

# 5. Run database migrations (forward-only: applied migrations are immutable)
make migrate

# 6. Create admin user
go run ./cmd/create-admin

# 7. Start development servers
make dev
```

The panel API will be available at `http://localhost:8080` and the web interface at `http://localhost:3000`.

### Production Deployment

Production is a separate, explicit composition — it is **not** started by the
`docker compose up` development flow. See **[docs/DEPLOYMENT.md](docs/DEPLOYMENT.md)**
for the full guide (secrets, TLS, agent deployment, backups, image review).

<details>
<summary><b>Short version</b></summary>

```bash
# 1. Copy and fill in the production env. The template ships EMPTY guarded values
#    (no CHANGE_ME* placeholders); Compose refuses to start if any required value
#    is blank, so replace every empty mandatory value with a real one.
cp .env.production.example .env.production

# 2. Create the host secret files (mode 0400, owned by root; the container root
#    entrypoint/init reads them before dropping to the app UID). Never commit
#    deploy/secrets/.
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
```

</details>

> [!IMPORTANT]
> **Not a production certification.** `docker-compose.production.yml` is a safer,
> more explicit entrypoint with least-privilege networks (`webpanel`, `data`,
> `agent-egress`, `edge`) and a restricted S3 user for the panel. The panel API is
> **not** published — only the web UI (loopback by default) is exposed, and an
> external TLS reverse proxy must target `web`. You remain responsible for TLS
> termination, real secrets, image-tag review, backups, and deploying the KVM
> agent separately on each hypervisor host.

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
| `SERVER_PORT` | `8080` | API server port |
| `ALLOWED_ORIGINS` | `http://localhost:3000` | Comma-separated CORS origins |
| `AGENT_GRPC_PORT` | `50051` | Agent gRPC port |
| `LIBVIRT_URI` | `qemu:///system` | Libvirt connection URI |

## API Routes

All routes are under `/api/v1/`:

| Group | Endpoints |
|-------|-----------|
| **Auth** | login, register, logout, forgot-password, reset-password, `GET` me |
| **Nodes** | CRUD + token regeneration |
| **VMs** | CRUD + lifecycle (start/stop/restart/rebuild) + reset-password + console |
| **Images** | Capture from VM, list, delete, create-VM-from-image |
| **Templates** | CRUD + sync to nodes |
| **Networks** | Network & firewall management |
| **Snapshots** | Create/restore/delete |
| **Backups** | Create, schedule, restore |
| **Storage** | Pool & volume management |
| **Users** | CRUD (admin only) |
| **Audit Logs** | Action history (admin only) |
| **Dashboard** | Statistics overview |

## Development

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
│   ├── panel/           # Panel API server
│   ├── agent/           # Node agent
│   ├── create-admin/    # CLI: create admin user
│   └── migrate/         # Database migrations
├── internal/
│   ├── agent/           # Node agent code
│   ├── panel/           # Panel API code
│   └── shared/          # Shared code (models, config, DB)
├── web/                 # Next.js frontend
├── docker-compose.yml   # Development infrastructure
├── Dockerfile.*         # Production containers
└── Makefile             # Development commands
```

## Contributing

Contributions are welcome! See [CONTRIBUTING.md](CONTRIBUTING.md) for setup
instructions, coding conventions, and the pull-request process.

> **Note for contributors:** `go build ./...` only works on Linux with libvirt
> installed — the node agent links against libvirt via cgo. Scope builds to
> `./internal/panel/... ./internal/shared/...` on other platforms.

## License

Released under the [MIT License](LICENSE).
