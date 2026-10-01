# Load local env (server/.env holds PCODER_LOGIN_EMAIL and SMTP_*
# credentials) for host runs. The server itself loads the same file via
# config.Load; the JWT signing key is auto-generated into server/data.
-include server/.env
export PCODER_LOGIN_EMAIL SMTP_HOST SMTP_PORT SMTP_USER SMTP_PASSWORD SMTP_FROM

.DEFAULT_GOAL := help
.PHONY: help setup start-docker dev-seed test nuke

help: ## Show available commands
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

setup: ## First-time local setup: check tools, create server/.env, install deps, seed demo data
	@command -v go >/dev/null 2>&1 || { echo "missing: go (https://go.dev/dl/)"; exit 1; }
	@command -v node >/dev/null 2>&1 || { echo "missing: node (https://nodejs.org/)"; exit 1; }
	@command -v docker >/dev/null 2>&1 || { echo "missing: docker (https://docs.docker.com/get-docker/)"; exit 1; }
	@if [ ! -f server/.env ]; then \
		echo "PCODER_LOGIN_EMAIL=you@example.com" > server/.env; \
		echo "created server/.env — edit PCODER_LOGIN_EMAIL, then re-run make setup"; \
		exit 1; \
	fi
	@test -n "$(PCODER_LOGIN_EMAIL)" || { echo 'PCODER_LOGIN_EMAIL missing — set it in server/.env'; exit 1; }
	@cd web && npm install
	@$(MAKE) dev-seed
	@echo ""
	@echo "setup done — run: make start-docker"

start-docker: ## Start full stack locally via docker compose (dev: Vite HMR)
	@command -v docker >/dev/null 2>&1 || { echo "missing: docker (https://docs.docker.com/get-docker/)"; exit 1; }
	@test -n "$(PCODER_LOGIN_EMAIL)" || { echo 'PCODER_LOGIN_EMAIL missing — run make setup first'; exit 1; }
	@trap 'docker compose -f docker-compose.dev.yml down' EXIT; \
	docker compose -f docker-compose.dev.yml up --build

dev-seed: ## Reset server/data from test/state.mock.json using PCODER_LOGIN_EMAIL (keeps jwt-secret, wipes state + butler + codemaps + observe + events)
	@test -n "$(PCODER_LOGIN_EMAIL)" || { echo 'PCODER_LOGIN_EMAIL missing — set it in server/.env'; exit 1; }
	@mkdir -p server/data
	@rm -f server/data/state.json
	@rm -rf server/data/butler server/data/codemaps server/data/observe server/data/events.log
	@sed "s/dev@example.com/$(PCODER_LOGIN_EMAIL)/" test/state.mock.json > server/data/state.json
	@chmod 600 server/data/state.json
	@echo "seeded server/data/state.json — login as $(PCODER_LOGIN_EMAIL) (PIN in server log unless SMTP_* set)"

test: ## Full stack tests: setup.sh self-test + Go (unit+integration) + web (unit+build+e2e)
	bash deploy/setup.sh --test
	go -C server test ./...
	go -C server test -tags integration -count=1 ./internal/...
	cd web && npm run test:unit
	cd web && npm run build
	cd web && npm run test:e2e:parallel

nuke: ## Teardown containers, volumes, network and local state
	-docker rm -f $$(docker ps -aq --filter name=pcoder-) 2>/dev/null || true
	-docker volume rm $$(docker volume ls -q --filter name=pcoder-) 2>/dev/null || true
	-docker network rm pcoder-net 2>/dev/null || true
	rm -rf server/data
	@echo "nuked pcoder containers, volumes, network and server/data"
