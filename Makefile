.PHONY: proto-gen migrate dev test build tools clean \
        docker-build-all docker-build-web

# ── Build-time stamps for OCI labels (overridable from env) ─────────────
GIT_VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GIT_REVISION ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
BUILD_DATE   ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

DOCKER_BUILD_ARGS = \
  --build-arg VERSION=$(GIT_VERSION) \
  --build-arg REVISION=$(GIT_REVISION) \
  --build-arg CREATED=$(BUILD_DATE)

GO_SERVICES = gateway wa mbs campaign inbox contacts proxy notify

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

# ─── Docker (production images, chunk 2) ─────────────────────────────────
#
# Build every Go service as hermes-<svc>:<version> and hermes-<svc>:latest.
# Depends on `proto-gen` so a clean clone produces working images via:
#     make docker-build-all
#
# Override stamps from env if you don't want git autodetect, e.g.:
#     make docker-build-mbs GIT_VERSION=v0.2.0 GIT_REVISION=$(git rev-parse HEAD)

docker-build-all: proto-gen
	@for svc in $(GO_SERVICES); do \
		echo "Building hermes-$$svc:$(GIT_VERSION) (revision $(GIT_REVISION))..."; \
		docker build -f Dockerfile $(DOCKER_BUILD_ARGS) --build-arg SERVICE=$$svc \
			-t hermes-$$svc:$(GIT_VERSION) -t hermes-$$svc:latest . || exit 1; \
	done
	$(MAKE) docker-build-web

docker-build-web:
	@echo "Building hermes-web:$(GIT_VERSION) (revision $(GIT_REVISION))..."
	docker build -f Dockerfile.web $(DOCKER_BUILD_ARGS) \
		-t hermes-web:$(GIT_VERSION) -t hermes-web:latest .

# Single-service build: `make docker-build-mbs`, `make docker-build-gateway`, etc.
# Pattern target — runs proto-gen first.
docker-build-%: proto-gen
	@echo "Building hermes-$*:$(GIT_VERSION) (revision $(GIT_REVISION))..."
	docker build -f Dockerfile $(DOCKER_BUILD_ARGS) --build-arg SERVICE=$* \
		-t hermes-$*:$(GIT_VERSION) -t hermes-$*:latest .
