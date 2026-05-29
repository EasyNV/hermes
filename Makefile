.PHONY: proto-gen migrate dev test build tools clean

# Proto code generation
proto-gen:
	buf generate

# Install required tools
tools:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	go install github.com/bufbuild/buf/cmd/buf@latest
	go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest

# Run all database migrations
migrate:
	@for svc in gateway wa mbs campaign inbox contacts proxy notify; do \
		echo "Migrating $$svc..."; \
		migrate -path migrations/$$svc -database "$${DATABASE_URL}?x-migrations-table=schema_migrations_$$svc" up || true; \
	done

# Run migrations down (rollback)
migrate-down:
	@for svc in gateway wa mbs campaign inbox contacts proxy notify; do \
		echo "Rolling back $$svc..."; \
		migrate -path migrations/$$svc -database "$${DATABASE_URL}?x-migrations-table=schema_migrations_$$svc" down 1 || true; \
	done

# Start all infra via Docker Compose
infra:
	docker compose up -d postgres redis nats

# Start all services in dev mode (requires infra running)
dev: infra
	@echo "Starting services..."
	@for svc in proxy contacts notify wa mbs campaign inbox gateway; do \
		echo "Starting $$svc..."; \
		go run ./cmd/$$svc & \
	done
	@echo "All services starting. Frontend: cd web && npm run dev"

# Run tests
test:
	go test ./... -v -race -count=1

# Build all services
build:
	@for svc in gateway wa mbs campaign inbox contacts proxy notify; do \
		echo "Building $$svc..."; \
		CGO_ENABLED=0 go build -o bin/hermes-$$svc ./cmd/$$svc; \
	done
	@echo "Building mbs-import (one-shot operator tool)..."
	@CGO_ENABLED=0 go build -o bin/mbs-import ./cmd/mbs-import

# Clean generated files
clean:
	rm -rf gen/go gen/ts bin/
