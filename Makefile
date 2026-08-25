BIN    := bin/sps
VERSION ?= dev

# Load local env (.env holds SPS_LOGIN_EMAIL and SMTP_* credentials) for host runs.
-include .env
export SPS_LOGIN_EMAIL SMTP_HOST SMTP_PORT SMTP_USER SMTP_PASSWORD SMTP_FROM

.PHONY: dev test docker-test build check lint e2e generate web-install web-test clean

dev: ## Run the server on the host (Go toolchain; web dev server runs separately)
	go -C server run ./cmd/server

test: ## Run Go unit tests
	go -C server test ./...

docker-test: ## Docker integration tests (requires a running Docker engine)
	go -C server test -tags integration -count=1 ./internal/docker/

build: ## Build the server binary into bin/
	mkdir -p bin
	go -C server build -ldflags "-X main.version=$(VERSION)" -o ../bin/sps ./cmd/server

lint: ## Format + vet (Go) and lint (web)
	cd server && test -z "$$(gofmt -l .)"
	go -C server vet ./...
	cd web && npm run lint

check: ## Lint, test, typecheck/build the web app, run FE unit + e2e tests (e2e needs Docker)
	$(MAKE) lint
	go -C server test ./...
	cd web && npm run test:unit
	cd web && npm run build
	cd web && npm run test:e2e

e2e: ## Stub — the end-to-end compose flow lands in Phase 6 (see docs/plan.md)
	@echo "e2e: not implemented until Phase 6"

web-install: ## Install web dependencies
	cd web && npm install

web-test: ## Run web e2e tests (real backend; needs Go toolchain + Docker engine)
	cd web && npm run test:e2e

web-test-parallel: ## Run web e2e tests in parallel, one process per test type
	cd web && npm run test:e2e:parallel

generate: ## Regenerate Go mocks (mockery) into server/mocks
	cd server && go generate ./...

clean:
	rm -rf bin web/dist web/test-results web/playwright-report
