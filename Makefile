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
	@echo "  make clean              - Clean build artifacts"
	@echo "  make help               - Show this help"