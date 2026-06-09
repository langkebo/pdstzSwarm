.PHONY: build dev test test-integration lint fmt generate docs snapshot release-check clean help docker docker-build docker-up docker-down docker-health docker-logs web-build

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS  = -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)
COMPOSE_FILE := deploy/docker-compose.yml
COMPOSE_OBS  := deploy/docker-compose.full.yml

## build: Compile binary to ./bin/pentestswarm (web bundle must already be in internal/webfs/out)
build:
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o bin/pentestswarm ./cmd/pentestswarm/

## build-all: Build web frontend, sync into embed directory, then compile Go binary
build-all: web-build sync-web build

## sync-web: Copy web/out → internal/webfs/out so go:embed picks up the latest bundle
sync-web:
	@rm -rf internal/webfs/out
	@cp -r web/out internal/webfs/out
	@echo "✓ web/out → internal/webfs/out"

## web-build: Build the Next.js dashboard to ./web/out (embedded by the Docker build)
web-build:
	@cd web && npm install --no-audit --no-fund && npm run build

## dev: Start full local development stack
dev: build
	@echo "Starting development environment..."
	docker compose -f deploy/docker-compose.dev.yml up -d
	@echo "Running pentestswarm..."
	./bin/pentestswarm

## docker: Build the production image
docker docker-build:
	docker build -t pentest-swarm-ai:$(VERSION) -t pentest-swarm-ai:latest \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg COMMIT=$(COMMIT) \
	  --build-arg DATE=$(DATE) \
	  .

## docker-up: Bring up the integrated stack (waits for /healthz)
docker-up:
	./deploy/scripts/up.sh

## docker-up-full: Integrated stack + Prometheus + Grafana
docker-up-full:
	./deploy/scripts/up.sh --profile=full

## docker-down: Stop the stack (keeps volumes)
docker-down:
	./deploy/scripts/down.sh

## docker-down-clean: Stop the stack and delete volumes
docker-down-clean:
	./deploy/scripts/down.sh --volumes

## docker-health: Report health of every service
docker-health:
	./deploy/scripts/healthcheck.sh

## docker-logs: Tail logs for the swarm container
docker-logs:
	docker compose -f $(COMPOSE_FILE) logs -f pentestswarm

## docker-ps: Show running services and their state
docker-ps:
	docker compose -f $(COMPOSE_FILE) ps

## migrate: Apply SQL migrations to the configured database
migrate:
	./bin/pentestswarm migrate

## test: Run unit tests with race detector
test:
	go test -race -count=1 ./...

## test-integration: Run integration tests (requires running services)
test-integration:
	go test -race -count=1 -tags=integration ./tests/integration/...

## test-e2e: Run end-to-end tests
test-e2e:
	go test -race -count=1 -tags=e2e ./tests/e2e/...

## test-coverage: Run tests with coverage report
test-coverage:
	go test -race -coverprofile=coverage.txt -covermode=atomic ./...
	go tool cover -html=coverage.txt -o coverage.html
	@echo "Coverage report: coverage.html"

## lint: Run golangci-lint
lint:
	golangci-lint run ./...

## fmt: Format code with gofmt and goimports
fmt:
	gofmt -s -w .
	goimports -w -local github.com/Armur-Ai/Pentest-Swarm-AI .

## generate: Run code generation (sqlc, etc.)
generate:
	@echo "Running code generation..."
	@command -v sqlc >/dev/null 2>&1 && sqlc generate || echo "sqlc not installed, skipping"

## docs: Generate API documentation
docs:
	@echo "Generating API docs..."
	@command -v swag >/dev/null 2>&1 && swag init -g internal/api/server.go -o docs/swagger || echo "swag not installed, skipping"

## snapshot: GoReleaser dry-run — builds for every platform locally without publishing
snapshot:
	@command -v goreleaser >/dev/null 2>&1 || { echo "goreleaser not installed: brew install goreleaser, or go install github.com/goreleaser/goreleaser/v2@latest"; exit 1; }
	goreleaser release --snapshot --clean

## release-check: Validate .goreleaser.yaml without building anything
release-check:
	@command -v goreleaser >/dev/null 2>&1 || { echo "goreleaser not installed: brew install goreleaser, or go install github.com/goreleaser/goreleaser/v2@latest"; exit 1; }
	goreleaser check

## clean: Remove build artifacts
clean:
	rm -rf bin/ coverage.txt coverage.html dist/

## help: Show this help message
help:
	@echo "Usage: make [target]"
	@echo ""
	@sed -n 's/^## //p' $(MAKEFILE_LIST) | column -t -s ':' | sed 's/^/  /'
