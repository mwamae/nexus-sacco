# nexusSacco — workflow shortcuts.
# Most targets shell into docker-compose or the identity service binary.

SHELL := /bin/bash
COMPOSE := docker compose
IDENTITY_DIR := services/identity

ifneq (,$(wildcard ./.env))
  include .env
  export
endif

.PHONY: help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

# ───────── Stack ─────────
.PHONY: up
up: ## Bring up postgres + redis + identity (detached)
	$(COMPOSE) up -d

.PHONY: down
down: ## Stop the stack
	$(COMPOSE) down

.PHONY: logs
logs: ## Tail identity logs
	$(COMPOSE) logs -f identity

.PHONY: ps
ps: ## Show stack status
	$(COMPOSE) ps

# ───────── Database ─────────
.PHONY: psql
psql: ## Open psql in the dev DB
	$(COMPOSE) exec postgres psql -U $(POSTGRES_USER) $(POSTGRES_DB)

.PHONY: migrate
migrate: ## Run pending migrations (uses identity binary)
	cd $(IDENTITY_DIR) && go run ./cmd/server -migrate

.PHONY: seed
seed: ## Create platform super-admin from .env
	cd $(IDENTITY_DIR) && go run ./cmd/server -seed

# ───────── Go ─────────
.PHONY: build
build: ## Build identity binary into bin/
	mkdir -p bin
	cd $(IDENTITY_DIR) && go build -o ../../bin/identity ./cmd/server

.PHONY: run
run: ## Run identity locally (not in docker)
	cd $(IDENTITY_DIR) && go run ./cmd/server

.PHONY: test
test: ## Run all Go tests
	cd $(IDENTITY_DIR) && go test ./...

.PHONY: tidy
tidy: ## go mod tidy across services
	cd $(IDENTITY_DIR) && go mod tidy

# ───────── Web ─────────
.PHONY: web-dev
web-dev: ## Start admin web in dev mode
	cd web/admin && npm run dev

.PHONY: web-build
web-build: ## Build admin web for production
	cd web/admin && npm run build
