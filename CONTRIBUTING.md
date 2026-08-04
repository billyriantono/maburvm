# Contributing to MaburVM

Thanks for your interest in contributing! This document covers how to get the
project running, the conventions we follow, and how to submit changes.

By contributing, you agree that your contributions will be licensed under the
[MIT License](LICENSE).

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [Getting Started](#getting-started)
- [Building & Testing](#building--testing)
- [Project Layout](#project-layout)
- [Database Migrations](#database-migrations)
- [Coding Conventions](#coding-conventions)
- [Commit Messages](#commit-messages)
- [Pull Requests](#pull-requests)
- [Reporting Bugs](#reporting-bugs)
- [Security Issues](#security-issues)

## Code of Conduct

Be respectful and constructive. Harassment, personal attacks, and dismissive
behavior are not welcome. Assume good faith and keep discussions technical.

## Getting Started

### Prerequisites

- Go 1.25+
- Node.js 20+
- PostgreSQL 16+
- Docker & Docker Compose (recommended for local infrastructure)

### Setup

```bash
git clone https://github.com/billyriantono/maburvm.git
cd maburvm

docker compose up -d postgres minio mailpit   # local infrastructure
cp .env.example .env                          # then fill in JWT_SECRET_KEY, AES_KEY, DB creds

make install    # Go + web dependencies
make migrate    # apply database migrations
make dev        # panel API on :8080, web UI on :3000
```

## Building & Testing

> [!IMPORTANT]
> **`go build ./...` and `go test ./...` only work on Linux with libvirt installed.**
> The node agent links against libvirt via cgo and is guarded by `//go:build linux`,
> so on macOS/Windows those commands fail with `Package libvirt was not found in
> the pkg-config search path`. This is expected — scope your commands to the
> packages you're changing:

```bash
# Panel / shared code (works on any OS)
go build ./internal/panel/... ./internal/shared/...
go test  ./internal/panel/... ./internal/shared/...
go vet   ./internal/panel/... ./internal/shared/...

# Node agent — cross-compile for Linux (no local libvirt headers needed)
make build-agent-linux
```

Web:

```bash
cd web
npx tsc --noEmit    # type check (must be clean)
npm run build       # production build (must succeed)
npm run lint
```

Please make sure the relevant builds and tests pass before opening a pull request.

## Project Layout

```
api/proto/       gRPC service definitions (run `make proto` after editing)
cmd/             entrypoints: panel, agent, create-admin, migrate
internal/
  agent/         node agent — libvirt/QEMU, storage, networking (Linux-only)
  panel/         panel API — handlers, services, repositories
  shared/        models, config, DB migrations, job queue (River)
web/             Next.js frontend
docs/            deployment documentation
```

The panel talks to each node agent over gRPC; the agent is the only component
that touches libvirt/QEMU directly.

## Database Migrations

Migrations are **forward-only**: once a migration has been applied it is
immutable. Never edit an existing migration — add a new one that alters the
schema forward.

- Files live in `internal/shared/db/migrations/` and are embedded into the binary.
- Name them `NNN_short_description.up.sql` with a matching `.down.sql`, where
  `NNN` is the next unused number (e.g. `048_...`).
- The panel applies pending SQL and River queue migrations automatically on boot,
  so a restart is enough to pick up a new migration.

## Coding Conventions

**Go**
- Format with `gofmt` (or `goimports`); code must be gofmt-clean.
- Standard Go idioms: wrap errors with `%w`, accept `context.Context` as the
  first parameter, keep exported identifiers documented.
- Comments should explain *why*, not restate *what* the code does.

**TypeScript / React**
- TypeScript strict mode; no new `tsc` errors.
- Use the existing shadcn/ui primitives in `web/src/components/ui/` rather than
  introducing another component library.
- Data fetching goes through the hooks in `web/src/lib/hooks/`.

**General**
- Match the style of the surrounding code.
- Never commit secrets. `.env*` files and `deploy/secrets/` are gitignored and
  must stay that way.

## Commit Messages

We use [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <short summary>

<body — what changed and why>
```

Types: `feat`, `fix`, `docs`, `refactor`, `test`, `chore`, `perf`.

Examples:

```
feat(images): capture a VM disk to object storage
fix(agent): raise disk-export timeout to 60m
docs(readme): document the production deployment flow
```

## Pull Requests

1. Fork the repository and create a branch from `main`
   (e.g. `feat/vm-resize`, `fix/console-timeout`).
2. Make your change, keeping the diff focused — unrelated refactors make review
   harder.
3. Run the relevant builds/tests (see [Building & Testing](#building--testing)).
4. Write a clear PR description: what problem it solves, how you approached it,
   and how you verified it.
5. Link any related issue (`Fixes #123`).

Small, well-scoped PRs get reviewed fastest. If you're planning a large change,
please open an issue first so we can agree on the approach.

## Reporting Bugs

Open an issue including:

- What you expected to happen, and what actually happened
- Steps to reproduce
- Environment: OS, Go/Node versions, deployment method (dev or Docker)
- Relevant logs — **redact IP addresses, hostnames, tokens, and credentials**

## Security Issues

**Please do not open a public issue for security vulnerabilities.** This project
manages virtual machines and network configuration, so disclosure carries real
risk for operators.

Report vulnerabilities privately via GitHub's
[security advisories](https://github.com/billyriantono/maburvm/security/advisories/new),
and allow time for a fix before public disclosure.
