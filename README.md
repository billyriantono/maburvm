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

## Tech Stack

| Layer | Technology |
|-------|-----------|
| Backend API | Go 1.25, Echo v4 (HTTP) |
| Frontend | Next.js 14, React 18, TypeScript, Tailwind CSS, shadcn/ui |
| Database | PostgreSQL 16 (via GORM) |
| Job Queue | River (PostgreSQL-based) |
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

# 5. Run database migrations
make migrate

# 6. Create admin user
go run ./cmd/create-admin

# 7. Start development servers
make dev
```

The panel API will be available at `http://localhost:8080` and the web interface at `http://localhost:3000`.

### Production Deployment

```bash
# Build and start all services
docker compose --profile production up -d

# Or build individually
docker build -f Dockerfile.panel -t maburvm-panel .
docker build -f Dockerfile.agent -t maburvm-agent .
docker build -f Dockerfile.web -t maburvm-web .
```

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

- **Auth**: POST login, register, logout, GET me
- **Nodes**: CRUD + token regeneration
- **VMs**: CRUD + lifecycle (start/stop/restart/rebuild)
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

Private - All rights reserved.
