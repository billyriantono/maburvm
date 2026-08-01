# MaburVM Panel Makefile

.PHONY: build test proto migrate clean run-panel run-agent build-agent-linux run-web dev

# Default: build both binaries
build:
	@mkdir -p bin
	@echo "Building panel..."
	@go build -o bin/panel ./cmd/panel
	@echo "Building agent..."
	@go build -o bin/agent ./cmd/agent
	@echo "Build complete: bin/panel bin/agent"

# Run panel backend (dev mode)
run-panel:
	@echo "Starting panel backend on :8080..."
	@go run ./cmd/panel

# Run agent (dev mode - requires token)
run-agent:
	@if [ -z "$(TOKEN)" ]; then \
		echo "Usage: make run-agent TOKEN=<node-token>"; \
		echo "Example: make run-agent TOKEN=abc123"; \
		exit 1; \
	fi
	@echo "Starting agent with token: $(TOKEN)..."
	@go run ./cmd/agent -token=$(TOKEN)

# Build agent for Linux — the artifact the panel serves to new nodes.
#
# Uses -tags libvirt_dlopen so the binary loads libvirt at RUNTIME via dlopen:
#   * build host needs NO libvirt-dev / headers
#   * target node needs only the libvirt RUNTIME (present on every KVM host)
# CGO is still required (dlopen is via cgo), so run this on a Linux host/CI
# (native) or with a linux cross C toolchain (e.g. CC=x86_64-linux-gnu-gcc).
build-agent-linux:
	@echo "Building agent for Linux (libvirt_dlopen, no libvirt-dev needed)..."
	@mkdir -p bin/linux
	@CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -tags libvirt_dlopen -o bin/linux/agent-amd64 ./cmd/agent
	@echo "Linux agent built: bin/linux/agent-amd64"
	@echo "Drop it where the panel serves it (AGENT_BINARY_DIR, default ./bin/linux)."
	@echo "ARM64: CGO_ENABLED=1 GOOS=linux GOARCH=arm64 CC=aarch64-linux-gnu-gcc go build -tags libvirt_dlopen -o bin/linux/agent-arm64 ./cmd/agent"

# Run frontend dev server on all interfaces
run-web:
	@echo "Starting frontend on http://0.0.0.0:3000..."
	@cd web && npm run dev -- -H 0.0.0.0

# Dev: Run everything (panel + web)
dev:
	@echo "Starting MaburVM development servers..."
	@echo "Panel will run on http://localhost:8080"
	@echo "Web will run on http://0.0.0.0:3000"
	@echo ""
	@make -j2 run-panel run-web

# Run tests
test:
	@echo "Running tests..."
	@go test -v ./...

# Generate protobuf code
proto:
	@echo "Generating protobuf..."
	@mkdir -p internal/shared/grpc/pb
	@if command -v protoc >/dev/null 2>&1; then \
		protoc --go_out=internal/shared/grpc/pb --go_opt=paths=source_relative \
			--go-grpc_out=internal/shared/grpc/pb --go-grpc_opt=paths=source_relative \
			api/proto/*.proto; \
		echo "Proto generation complete: internal/shared/grpc/pb/"; \
	else \
		echo "protoc not found, skipping proto generation"; \
	fi

# Run database migrations
migrate:
	@echo "Running migrations..."
	@go run -tags migrate ./internal/shared/db/migrate.go

# Install dependencies (Go + Node)
install:
	@echo "Installing Go dependencies..."
	@go mod download
	@echo "Installing Node dependencies..."
	@cd web && npm install
	@echo "Dependencies installed!"

# Docker build commands
docker-build-panel:
	@echo "Building panel Docker image..."
	@docker build -f Dockerfile.panel -t maburvm-panel .

docker-build-agent:
	@echo "Building agent Docker image..."
	@docker build -f Dockerfile.agent -t maburvm-agent .

docker-build-web:
	@echo "Building web Docker image (context repo root, Dockerfile.web)..."
	@docker build -f Dockerfile.web -t maburvm-web:local \
		--build-arg API_BASE_URL=http://panel:8080 \
		--build-arg ENFORCE_API_BASE_URL=1 .

docker-build: docker-build-panel docker-build-agent docker-build-web
	@echo "All Docker images built!"

docker-up:
	@echo "Starting development services (explicit docker-compose.yml)..."
	@docker compose -f docker-compose.yml up -d

docker-down:
	@echo "Stopping development services (explicit docker-compose.yml)..."
	@docker compose -f docker-compose.yml down

# Production: explicit file + env file only. No override discovery, no profile.
docker-prod-up:
	@echo "Starting production services (docker-compose.production.yml)..."
	@docker compose -f docker-compose.production.yml --env-file .env.production up -d

docker-prod-down:
	@echo "Stopping production services (docker-compose.production.yml)..."
	@docker compose -f docker-compose.production.yml --env-file .env.production down

# Static validation of the production compose file. This does NOT deploy and
# does NOT require Docker to be running; it only checks that the file parses
# and the referenced secret files exist. Requires `docker compose` on PATH.
# Fails closed: the target returns Docker Compose's nonzero status on failure.
docker-prod-validate:
	@echo "Validating docker-compose.production.yml..."
	@docker compose -f docker-compose.production.yml --env-file .env.production config >/dev/null
	@echo "production compose: config OK"

# Assertion-only check of an EXISTING web/.next route manifest (no Docker, no
# build): assert a BUILT web/.next/routes-manifest.json contains the four required
# rewrites (/api, /ws, /install-agent.sh, /webhooks) resolving exactly to
# http://panel:8080 and no loopback destination. Run after `npm run build` (or
# `make docker-build-web`) produces the manifest. NOTE: this inspects whatever
# manifest is present and may therefore assert a STALE prebuilt manifest if the
# web UI was not rebuilt since; prefer `web-routes-build-check` for the
# reproducible gate. It is not a substitute for a Docker image build.
web-routes-check:
	@echo "Checking production-built web/.next/routes-manifest.json rewrites..."
	@python3 scripts/check-web-routes.py

# Reproducible gate: a FRESH host-side production build of the web UI on the HOST,
# immediately followed by the exact rewrite assertion against the NEWLY produced
# host manifest. This avoids the stale-manifest bypass risk of running
# `web-routes-check` alone against a previously built web/.next. It is evidence of
# a correct HOST build + manifest assertion, NOT evidence of a Docker image build.
# NOTE: this builds on the host with npm (not Docker); it does NOT populate a
# Docker image. `API_BASE_URL` is pinned to the production internal panel address.
# If the web config supports a production enforcement variable, set it here so a
# localhost fallback is rejected.
web-routes-build-check:
	@echo "Building web (host) with API_BASE_URL=http://panel:8080 ..."
	@cd web && API_BASE_URL=http://panel:8080 NEXT_PUBLIC_API_URL=http://panel:8080 npm run build
	@echo "Asserting freshly built web/.next/routes-manifest.json rewrites..."
	@python3 scripts/check-web-routes.py --manifest web/.next/routes-manifest.json

# Clean build artifacts
clean:
	@rm -rf bin/
	@cd web && rm -rf .next/ node_modules/
	@go clean

# Help
help:
	@echo "MaburVM Panel - Available Commands:"
	@echo ""
	@echo "  make build              - Build both panel and agent binaries"
	@echo "  make run-panel          - Run panel backend (dev mode)"
	@echo "  make run-agent TOKEN=x  - Run agent with token"
	@echo "  make build-agent-linux  - Build agent for Linux"
	@echo "  make run-web            - Run frontend dev server (0.0.0.0:3000)"
	@echo "  make dev                - Run both panel and web concurrently"
	@echo "  make test               - Run all tests"
	@echo "  make proto              - Generate protobuf code"
	@echo "  make migrate            - Run database migrations"
	@echo "  make install            - Install Go and Node dependencies"
	@echo "  make docker-build       - Build all Docker images"
	@echo "  make docker-build-panel - Build panel Docker image"
	@echo "  make docker-build-agent - Build agent Docker image"
	@echo "  make docker-build-web   - Build web Docker image (repo-root ctx)"
	@echo "  make docker-up          - Start development services (docker-compose.yml) with Docker"
	@echo "  make docker-down        - Stop development services"
	@echo "  make docker-prod-up     - Start production services (docker-compose.production.yml)"
	@echo "  make docker-prod-down   - Stop production services"
	@echo "  make web-routes-check   - Assert an EXISTING web/.next manifest (no build; may be stale)"
	@echo "  make web-routes-build-check - Fresh host web build + assert rewrites (reproducible gate)"
	@echo "  make clean              - Clean build artifacts"
	@echo "  make help               - Show this help"