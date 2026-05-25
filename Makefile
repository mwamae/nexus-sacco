# nexusSacco — workflow shortcuts.
# Most targets shell into docker-compose or the identity service binary.

SHELL := /bin/bash
COMPOSE := docker compose
IDENTITY_DIR := services/identity
MEMBER_DIR := services/member
WORKFLOW_DIR := services/workflow

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
migrate: ## Run pending migrations (identity + member + workflow)
	cd $(IDENTITY_DIR) && go run ./cmd/server -migrate
	cd $(MEMBER_DIR)   && go run ./cmd/server -migrate
	cd $(WORKFLOW_DIR) && go run ./cmd/server -migrate

.PHONY: migrate-identity
migrate-identity: ## Run only identity migrations
	cd $(IDENTITY_DIR) && go run ./cmd/server -migrate

.PHONY: migrate-member
migrate-member: ## Run only member migrations
	cd $(MEMBER_DIR) && go run ./cmd/server -migrate

.PHONY: migrate-workflow
migrate-workflow: ## Run only workflow migrations
	cd $(WORKFLOW_DIR) && go run ./cmd/server -migrate

.PHONY: seed
seed: ## Create platform super-admin from .env
	cd $(IDENTITY_DIR) && go run ./cmd/server -seed

# ───────── Go ─────────
.PHONY: build
build: ## Build identity + member binaries into bin/
	mkdir -p bin
	cd $(IDENTITY_DIR) && go build -o ../../bin/identity ./cmd/server
	cd $(MEMBER_DIR)   && go build -o ../../bin/member   ./cmd/server

.PHONY: run
run: ## Run identity locally (not in docker)
	cd $(IDENTITY_DIR) && go run ./cmd/server

.PHONY: run-member
run-member: ## Run member service locally (not in docker)
	cd $(MEMBER_DIR) && go run ./cmd/server

.PHONY: run-workflow
run-workflow: ## Run workflow service locally
	cd $(WORKFLOW_DIR) && go run ./cmd/server

.PHONY: test
test: ## Run all Go tests
	cd $(IDENTITY_DIR) && go test ./...
	cd $(MEMBER_DIR)   && go test ./...

.PHONY: tidy
tidy: ## go mod tidy across services
	cd $(IDENTITY_DIR) && go mod tidy
	cd $(MEMBER_DIR)   && go mod tidy

# ───────── Lint ─────────
# postingcheck — static analyzer that catches regressions where a
# money-moving handler bypasses the GL outbox (R10 spec). Runs across
# every service's handler/ package. Exits non-zero on any flag, so
# CI / pre-push hooks can gate merges on it.
.PHONY: lint
lint: ## Run postingcheck analyzer across all services
	cd tools/postingcheck && go build -o $(CURDIR)/bin/postingcheck ./cmd/postingcheck
	@for svc in savings accounting member workflow notification identity; do \
	  if [ -d "services/$$svc/internal/handler" ]; then \
	    echo "→ postingcheck services/$$svc/internal/handler/..."; \
	    (cd services/$$svc && $(CURDIR)/bin/postingcheck ./internal/handler/...) || exit 1; \
	  fi; \
	done
	@echo "✓ postingcheck clean"

# ───────── Web ─────────
.PHONY: web-dev
web-dev: ## Start admin web in dev mode
	cd web/admin && npm run dev

.PHONY: web-build
web-build: ## Build admin web for production
	cd web/admin && npm run build
