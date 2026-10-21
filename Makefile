# PCODER_AUTH_EMAIL is optional in dev: the default below makes
# `make setup && make start-docker` work without a .env file. An existing
# server/.env (prod setup writes one) still wins. Host runs also load it
# via config.Load.
-include server/.env
PCODER_AUTH_EMAIL ?= dev@example.com
export PCODER_AUTH_EMAIL

# Dev-only login password. start-docker applies it once when state.json has
# no credential yet, so a fresh checkout can log in immediately. The
# production stack never reads this.
DEV_PASSWORD := dev-password-12345

COMPOSE := docker compose -f docker-compose.dev.yml
PUSH_FILES := server/data/vapid.json server/data/push.json
PUSH_CLEAR = rm -f $(PUSH_FILES)

.DEFAULT_GOAL := help
.PHONY: help setup start-docker dev-seed reset-push _push-clear test nuke

help: ## Show available commands
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

setup: ## First-time local setup: check tools, install deps, seed demo data
	@command -v go >/dev/null 2>&1 || { echo "missing: go (https://go.dev/dl/)"; exit 1; }
	@command -v node >/dev/null 2>&1 || { echo "missing: node (https://nodejs.org/)"; exit 1; }
	@command -v docker >/dev/null 2>&1 || { echo "missing: docker (https://docs.docker.com/get-docker/)"; exit 1; }
	@cd web && npm install
	@$(MAKE) dev-seed
	@echo ""
	@echo "setup done — run: make start-docker"

start-docker: ## Start full stack locally (clears push setup so Enable starts fresh)
	@command -v docker >/dev/null 2>&1 || { echo "missing: docker (https://docs.docker.com/get-docker/)"; exit 1; }
	@test -n "$(PCODER_AUTH_EMAIL)" || { echo 'PCODER_AUTH_EMAIL is empty — set it or unset it for the dev default'; exit 1; }
	@$(COMPOSE) down >/dev/null 2>&1 || true
	@$(PUSH_CLEAR)
	@if ! grep -Eq '"passwordHash"[[:space:]]*:[[:space:]]*"[^"]+"' server/data/state.json 2>/dev/null; then \
		command -v go >/dev/null 2>&1 || { echo "missing: go (https://go.dev/dl/)"; exit 1; }; \
		echo "no dev password set — configuring $(PCODER_AUTH_EMAIL) / $(DEV_PASSWORD)"; \
		printf '%s' '$(DEV_PASSWORD)' | env PCODER_DATA_DIR=./data go -C server run ./cmd/server set-password; \
	fi
	@docker build -t pcoder-browser:v14 server/internal/preview/image
	@trap '$(COMPOSE) down' EXIT; \
	$(COMPOSE) up --build

dev-seed: _push-clear ## Reset server/data from test/state.mock.json using PCODER_AUTH_EMAIL (keeps jwt-secret, wipes state + butler + codemaps + observe + events + push)
	@test -n "$(PCODER_AUTH_EMAIL)" || { echo 'PCODER_AUTH_EMAIL is empty — set it or unset it for the dev default'; exit 1; }
	@mkdir -p server/data
	@rm -f server/data/state.json
	@rm -rf server/data/butler server/data/codemaps server/data/observe server/data/events.log
	@sed "s/dev@example.com/$(PCODER_AUTH_EMAIL)/" test/state.mock.json > server/data/state.json
	@chmod 600 server/data/state.json
	@echo "seeded server/data/state.json — login as $(PCODER_AUTH_EMAIL) / $(DEV_PASSWORD)"

reset-push: _push-clear ## Delete VAPID keys + push subscriptions (restart the server, then clear browser site data)
	@echo "push state cleared — restart the stack and clear site data in each browser to resubscribe"

_push-clear:
	@$(PUSH_CLEAR)

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
