# MaburVM Panel Makefile

.PHONY: build test proto migrate clean

# Build binaries
build:
	@mkdir -p bin
	@echo "Building panel..."
	@go build -o bin/panel ./cmd/panel
	@echo "Building agent..."
	@go build -o bin/agent ./cmd/agent
	@echo "Build complete: bin/panel bin/agent"

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

# Clean build artifacts
clean:
	@rm -rf bin/
	@go clean