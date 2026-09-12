BIN    := bin/pcoder
VERSION ?= dev

# Load local env (server/.env holds PCODER_LOGIN_EMAIL and SMTP_* credentials)
# for host runs. The server itself loads the same file via config.Load.
# PCODER_JWT_SECRET lives in root .env so logins survive state resets.
-include .env
-include server/.env
export PCODER_LOGIN_EMAIL PCODER_JWT_SECRET SMTP_HOST SMTP_PORT SMTP_USER SMTP_PASSWORD SMTP_FROM

.DEFAULT_GOAL := help
.PHONY: help setup dev-seed test check-ci lint generate clean nuke start-local start-docker

help: ## Show available commands
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

setup: ## First-time setup: check tools, reset docker state, create server/.env, install deps, seed demo data (keeps login)
	@command -v go >/dev/null 2>&1 || { echo "missing: go (https://go.dev/dl/)"; exit 1; }
	@command -v node >/dev/null 2>&1 || { echo "missing: node (https://nodejs.org/)"; exit 1; }
	@command -v docker >/dev/null 2>&1 || { echo "missing: docker (https://docs.docker.com/get-docker/)"; exit 1; }
	@if ! grep -qs '^PCODER_JWT_SECRET=' .env 2>/dev/null; then \
		SECRET=$$(openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'); \
		touch .env; echo "" >> .env; echo "PCODER_JWT_SECRET=$$SECRET" >> .env; \
		echo "generated PCODER_JWT_SECRET in .env (keeps login across resets)"; \
	fi
	@-docker compose -f docker-compose.dev.yml down -v --remove-orphans >/dev/null 2>&1 || true
	@-docker rm -f $$(docker ps -aq --filter name=pcoder-) >/dev/null 2>&1 || true
	@-docker volume rm $$(docker volume ls -q --filter name=pcoder-) >/dev/null 2>&1 || true
	@if [ ! -f server/.env ]; then \
		echo "PCODER_LOGIN_EMAIL=you@example.com" > server/.env; \
		echo "created server/.env — edit PCODER_LOGIN_EMAIL, then re-run make setup"; \
		exit 1; \
	fi
	@test -n "$(PCODER_LOGIN_EMAIL)" || { echo 'PCODER_LOGIN_EMAIL missing — set it in server/.env'; exit 1; }
	@cd web && npm install
	@$(MAKE) dev-seed
	@echo ""
	@echo "setup done — run:"
	@echo "  make start-local              # backend :8080 + frontend :5173"
	@echo "  make start-docker             # full stack via docker compose"
	@echo "  make check-ci                 # run CI locally via act"

start-local: ## Start backend + frontend locally (no docker)
	@command -v go >/dev/null 2>&1 || { echo "missing: go (https://go.dev/dl/)"; exit 1; }
	@command -v node >/dev/null 2>&1 || { echo "missing: node (https://nodejs.org/)"; exit 1; }
	@test -n "$(PCODER_LOGIN_EMAIL)" || { echo 'PCODER_LOGIN_EMAIL missing — set it in server/.env'; exit 1; }
	@$(MAKE) -B dev-seed
	@trap 'kill $$(jobs -p) 2>/dev/null || true' EXIT; \
	go -C server run ./cmd/server & \
	cd web && npm run dev

start-docker: ## Start full stack via docker compose
	@command -v docker >/dev/null 2>&1 || { echo "missing: docker (https://docs.docker.com/get-docker/)"; exit 1; }
	@if [ ! -f .env ]; then \
		echo "PCODER_LOGIN_EMAIL=local@example.com" > .env; \
		echo "SMTP_HOST=smtp.gmail.com" >> .env; \
		echo "SMTP_PORT=587" >> .env; \
		echo "SMTP_USER=local@example.com" >> .env; \
		echo "SMTP_PASSWORD=local" >> .env; \
		echo "SMTP_FROM=local@example.com" >> .env; \
	fi
	@if ! grep -qs '^PCODER_JWT_SECRET=' .env 2>/dev/null; then \
		SECRET=$$(openssl rand -hex 32 2>/dev/null || head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'); \
		echo "" >> .env; echo "PCODER_JWT_SECRET=$$SECRET" >> .env; \
	fi
	@trap 'docker compose -f docker-compose.dev.yml down' EXIT; \
	docker compose -f docker-compose.dev.yml up --build

dev-seed: ## Reset server/data from test/state.mock.json using PCODER_LOGIN_EMAIL (keeps jwt-secret so login survives)
	@test -n "$(PCODER_LOGIN_EMAIL)" || { echo 'PCODER_LOGIN_EMAIL missing — set it in server/.env'; exit 1; }
	@TMP=$$(mktemp); \
	if [ -f server/data/jwt-secret ]; then cp server/data/jwt-secret "$$TMP"; fi; \
	rm -rf server/data; \
	mkdir -p server/data; \
	sed "s/dev@example.com/$(PCODER_LOGIN_EMAIL)/" test/state.mock.json > server/data/state.json; \
	chmod 600 server/data/state.json; \
	if [ -s "$$TMP" ]; then cp "$$TMP" server/data/jwt-secret; chmod 600 server/data/jwt-secret; fi; \
	rm -f "$$TMP"
	@echo "seeded server/data/state.json — login as $(PCODER_LOGIN_EMAIL) (PIN in server log unless SMTP_* set)"

test: ## Full stack tests: Go (unit+integration) + web (unit+build+e2e)
	go -C server test ./...
	go -C server test -tags integration -count=1 ./internal/...
	cd web && npm run test:unit
	cd web && npm run build
	cd web && npm run test:e2e:parallel

check-ci: ## Run CI workflow locally via act (mirrors GitHub Actions exactly)
	@command -v docker >/dev/null 2>&1 || { echo "missing: docker (https://docs.docker.com/get-docker/)"; exit 1; }; \
	if ! command -v act >/dev/null 2>&1; then \
		echo "act not found — installing ..."; \
		OS=$$(uname -s); \
		ARCH=$$(uname -m); \
		case "$$OS" in \
			Darwin) OS=Darwin ;; \
			Linux) OS=Linux ;; \
			*) echo "unsupported OS: $$OS — install act manually: https://github.com/nektos/act"; exit 1 ;; \
		esac; \
		case "$$ARCH" in \
			x86_64) ARCH=amd64 ;; \
			aarch64|arm64) ARCH=arm64 ;; \
			*) echo "unsupported arch: $$ARCH"; exit 1 ;; \
		esac; \
		VERSION=$$(curl -sL https://api.github.com/repos/nektos/act/releases/latest | grep '"tag_name":' | sed -E 's/.*"v([^"]+)".*/\1/'); \
		URL="https://github.com/nektos/act/releases/download/v$$VERSION/act_$$VERSION"_"$$OS"_"$$ARCH".tar.gz"; \
		TMPDIR=$$(mktemp -d); \
		curl -sL "$$URL" -o "$$TMPDIR/act.tar.gz"; \
		tar -xzf "$$TMPDIR/act.tar.gz" -C "$$TMPDIR"; \
		INSTALL_DIR="/usr/local/bin"; \
		[ -w "$$INSTALL_DIR" ] || INSTALL_DIR="$$HOME/.local/bin"; \
		mkdir -p "$$INSTALL_DIR"; \
		cp "$$TMPDIR/act" "$$INSTALL_DIR/act"; \
		chmod +x "$$INSTALL_DIR/act"; \
		rm -rf "$$TMPDIR"; \
		[ "$$INSTALL_DIR" = "/usr/local/bin" ] || export PATH="$$INSTALL_DIR:$$PATH"; \
	fi; \
	command -v act >/dev/null 2>&1 || { echo "act installation failed — install manually: https://github.com/nektos/act"; exit 1; }; \
	trap 'docker compose -f docker-compose.dev.yml down 2>/dev/null || true' EXIT; \
	act -W .github/workflows/ci.yml \
		-P ubuntu-latest=nektos/act-environments-ubuntu:22.04 \
		--container-options "--privileged" \
		--env GIT_SSL_NO_VERIFY=true \
		--log-prefix-job-id \
		--rm

lint: ## Format check + vet + web lint
	cd server && test -z "$$(gofmt -l .)"
	go -C server vet ./...
	cd web && npm run lint

generate: ## Regenerate Go mocks
	cd server && go generate ./...

clean: ## Remove build artifacts
	rm -rf bin web/dist web/test-results web/playwright-report

nuke: ## Teardown containers, volumes, network and local state
	-docker rm -f $$(docker ps -aq --filter name=pcoder-) 2>/dev/null || true
	-docker volume rm $$(docker volume ls -q --filter name=pcoder-) 2>/dev/null || true
	-docker network rm pcoder-net 2>/dev/null || true
	rm -rf server/data
	@echo "nuked pcoder containers, volumes, network and server/data"
