.PHONY: proto-gen migrate dev test build tools clean \
        docker-build-all docker-build-web \
        deploy-prod-up deploy-prod-down deploy-prod-logs deploy-prod-ps \
        deploy-prod-restart \
        deploy-dev-up deploy-dev-down deploy-dev-logs deploy-dev-ps \
        deploy-dev-restart

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

# ─── Deploy (production compose, chunk 3) ────────────────────────────────
#
# Wraps `docker compose -f docker-compose.prod.yml --env-file .env.prod`
# so operators get one consistent CLI surface. The runbook in
# docs/runbooks/compose-deploy.md documents the full first-time deploy
# (image build, secret provisioning, .env.prod fill-in). These targets
# are the post-bootstrap day-2 controls.

COMPOSE_PROD = docker-compose -f docker-compose.prod.yml --env-file .env.prod

deploy-prod-up:
	@test -f .env.prod || { echo "ERROR: .env.prod missing. cp .env.prod.example .env.prod && editor .env.prod" >&2; exit 1; }
	@test -f deploy/secrets/prod/mbs-dek.bin || { echo "ERROR: deploy/secrets/prod/mbs-dek.bin missing. Run scripts/dek-generate.sh deploy/secrets/prod/mbs-dek.bin" >&2; exit 1; }
	@test -f deploy/secrets/prod/jwt-signing-key || { echo "ERROR: deploy/secrets/prod/jwt-signing-key missing. Run scripts/dek-generate.sh deploy/secrets/prod/jwt-signing-key" >&2; exit 1; }
	@test -f deploy/secrets/prod/postgres-password || { echo "ERROR: deploy/secrets/prod/postgres-password missing. Run: printf '%s' \"\$$STRONG_PASSWORD\" > deploy/secrets/prod/postgres-password && chmod 0400 deploy/secrets/prod/postgres-password" >&2; exit 1; }
	$(COMPOSE_PROD) up -d

deploy-prod-down:
	$(COMPOSE_PROD) down

deploy-prod-logs:
	$(COMPOSE_PROD) logs -f --tail=200

deploy-prod-ps:
	$(COMPOSE_PROD) ps

deploy-prod-restart:
	$(COMPOSE_PROD) restart

# ─── Deploy (dev compose, chunk 5) ───────────────────────────────────────
#
# Symmetric to deploy-prod-*. Dev compose (`docker-compose.dev.yml`) builds
# each backend service from `Dockerfile.dev` with the local source tree —
# every backend exits without secret pre-flight because dev uses inline
# defaults (e.g. JWT_SECRET in compose env, no DEK file required since
# dev mbs runs against deploy/secrets/dev/mbs-dek.bin which the helper
# script regenerates if absent).
#
# Use these targets for local development:
#   make deploy-dev-up      # bring up the stack with all 12 services
#   make deploy-dev-logs    # follow logs across services
#   make deploy-dev-down    # tear it down
#
# Vite frontend hot-reload runs separately on host port 5173 (started by
# the web service inside compose).

COMPOSE_DEV = docker-compose -f docker-compose.dev.yml

deploy-dev-up:
	$(COMPOSE_DEV) up -d
	@echo ""
	@echo "Dev stack starting. Watch with: make deploy-dev-logs"
	@echo "Verify health: make deploy-dev-ps"
	@echo "Frontend (Vite hot-reload): http://localhost:5173"
	@echo "Gateway REST:                http://localhost:8081"
	@echo "Gateway gRPC:                localhost:8080"
	@echo "MBS gRPC:                    localhost:8082"

deploy-dev-down:
	$(COMPOSE_DEV) down

deploy-dev-logs:
	$(COMPOSE_DEV) logs -f --tail=200

deploy-dev-ps:
	$(COMPOSE_DEV) ps

deploy-dev-restart:
	$(COMPOSE_DEV) restart $(SERVICE)
