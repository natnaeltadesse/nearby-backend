SHELL := /bin/bash
.DEFAULT_GOAL := help

# ---------------------------------------------------------------------------
# Config. Every DB-touching target reads DATABASE_URL from .env — no target in
# this file may hardcode a connection string.
# ---------------------------------------------------------------------------
ifneq (,$(wildcard ./.env))
include .env
export
endif

COMPOSE     := docker compose
DB_SERVICE  := postgres
API_SERVICE := api
MIGRATIONS  := migrations
SEED_FILE   := scripts/seed.sql

# Pinned tool versions — run through `go run` so a clean clone needs nothing
# installed but Go and Docker.
GOOSE_VERSION     ?= v3.27.3
SQLC_VERSION      ?= v1.31.1
OAPI_VERSION      ?= v2.8.0
AIR_VERSION       ?= v1.67.4
GOLANGCI_VERSION  ?= v2.12.2
GOTESTSUM_VERSION ?= v1.13.0

GOOSE      := go run github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)
SQLC       := go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
OAPI       := go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_VERSION)
AIR        := go run github.com/air-verse/air@$(AIR_VERSION)
GOLANGCI   := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
GOTESTSUM  := go run gotest.tools/gotestsum@$(GOTESTSUM_VERSION)

# One line per package, packages with no tests hidden, and a
# "DONE n tests, n skipped in Xs" summary with every failure reprinted at the
# end — so a red run does not have to be scrolled for.
TESTSUM_FLAGS = --format pkgname --format-hide-empty-pkg --hide-summary=skipped

# DATABASE_URL is written from the host's point of view (localhost:5433).
# Rewrite the host:port half so the same credentials/database work from inside
# the Postgres container, where the server listens on 127.0.0.1:5432.
CONTAINER_DATABASE_URL = $(shell printf '%s' '$(DATABASE_URL)' | sed -E 's#@[^/@:]+:[0-9]+/#@127.0.0.1:5432/#')

GOOSE_ENV = GOOSE_DRIVER=postgres GOOSE_DBSTRING="$(DATABASE_URL)" GOOSE_MIGRATION_DIR=$(MIGRATIONS)

.PHONY: help setup up up-all down nuke restart logs psql \
        migrate-up migrate-down migrate-status migrate-create db-reset seed \
        sqlc generate run dev build test test-integration lint fmt tidy clean \
        require-env require-name

# ---------------------------------------------------------------------------
help: ## Show this help
	@echo "booking-backend — make targets"
	@echo
	@awk 'BEGIN {FS = ":.*?## "} \
	     /^# =+$$/ {next} \
	     /^##@/ {printf "\n\033[1m%s\033[0m\n", substr($$0, 5); next} \
	     /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo

require-env:
	@test -f .env || { echo "error: .env not found — run 'make setup' first"; exit 1; }
	@test -n "$(DATABASE_URL)" || { echo "error: DATABASE_URL is not set in .env"; exit 1; }

##@ Setup

setup: ## One command from clone to ready: .env, deps, Postgres, migrations
	@if [ ! -f .env ]; then cp .env.example .env; echo "created .env from .env.example"; \
	 else echo ".env already exists — leaving it alone"; fi
	go mod download
	@$(MAKE) --no-print-directory up
	@$(MAKE) --no-print-directory migrate-up
	@echo
	@echo "ready. next: make dev   (API on :$${HTTP_PORT:-8081})"

##@ Docker

up: ## Start Postgres and block until its healthcheck passes
	$(COMPOSE) up -d $(DB_SERVICE)
	@printf "waiting for postgres to become healthy"
	@for i in $$(seq 1 60); do \
	  status=$$($(COMPOSE) ps --format '{{.Health}}' $(DB_SERVICE) 2>/dev/null | head -1); \
	  if [ "$$status" = "healthy" ]; then echo " ok"; exit 0; fi; \
	  printf "."; sleep 1; \
	done; \
	echo " timed out after 60s"; $(COMPOSE) logs --tail=40 $(DB_SERVICE); exit 1

up-all: up ## Start Postgres + the API in Docker
	$(COMPOSE) --profile api up -d --build $(API_SERVICE)
	@echo "api listening on :$${HTTP_PORT:-8081}  (make logs-api to tail)"

down: ## Stop containers, keep data
	$(COMPOSE) --profile api down

nuke: ## Stop containers and delete the data volume
	$(COMPOSE) --profile api down -v
	@echo "volume booking_pgdata deleted"

restart: down up ## down, then up

logs: ## Tail Postgres logs
	$(COMPOSE) logs -f --tail=100 $(DB_SERVICE)

logs-api: ## Tail API logs (when running via up-all)
	$(COMPOSE) logs -f --tail=100 $(API_SERVICE)

psql: require-env ## Open a psql shell in the container
	$(COMPOSE) exec $(DB_SERVICE) psql "$(CONTAINER_DATABASE_URL)"

##@ Migrations (goose)

migrate-up: require-env ## Apply all pending migrations
	$(GOOSE_ENV) $(GOOSE) up

migrate-down: require-env ## Roll back the last migration
	$(GOOSE_ENV) $(GOOSE) down

migrate-status: require-env ## Show migration state
	$(GOOSE_ENV) $(GOOSE) status

require-name:
	@test -n "$(name)" || { \
	  echo "usage: make migrate-create name=add_bookings"; \
	  echo "       (lower_snake_case, no .sql extension)"; \
	  exit 1; }

migrate-create: require-name ## New SQL migration — make migrate-create name=add_bookings
	$(GOOSE_ENV) $(GOOSE) create $(name) sql

seed: require-env ## Load scripts/seed.sql (dev data)
	@test -f $(SEED_FILE) || { echo "error: $(SEED_FILE) not found"; exit 1; }
	$(COMPOSE) exec -T $(DB_SERVICE) psql "$(CONTAINER_DATABASE_URL)" \
	  -v ON_ERROR_STOP=1 -q -f - < $(SEED_FILE)
	@echo "seeded from $(SEED_FILE)"

db-reset: ## nuke → up → migrate-up → seed
	@$(MAKE) --no-print-directory nuke
	@$(MAKE) --no-print-directory up
	@$(MAKE) --no-print-directory migrate-up
	@$(MAKE) --no-print-directory seed

##@ Code

sqlc: ## Regenerate type-safe queries
	$(SQLC) generate

generate: sqlc ## sqlc + oapi-codegen from api/openapi.yaml
	$(OAPI) -config api/oapi-codegen.yaml api/openapi.yaml

run: ## Build and run the API
	go run ./cmd/api

dev: ## Run the API with hot reload
	$(AIR) -c .air.toml

build: ## Compile to bin/api
	CGO_ENABLED=0 go build -trimpath -o bin/api ./cmd/api
	CGO_ENABLED=0 go build -trimpath -o bin/worker ./cmd/worker

test: ## Unit tests (fast, no Docker)
	$(GOTESTSUM) $(TESTSUM_FLAGS) -- ./... -short -race -count=1

test-integration: ## Integration tests (testcontainers; needs Docker)
	$(GOTESTSUM) $(TESTSUM_FLAGS) -- ./test/... -race -count=1 -timeout 10m

test-all: ## Everything: unit + integration
	$(GOTESTSUM) $(TESTSUM_FLAGS) -- ./... -race -count=1 -timeout 10m

test-cover: ## Everything, with a coverage total
	$(GOTESTSUM) $(TESTSUM_FLAGS) -- ./... -race -count=1 -timeout 10m \
	  -coverprofile=coverage.out -coverpkg=./internal/...
	@go tool cover -func=coverage.out | tail -1
	@echo "full report: go tool cover -html=coverage.out"

lint: ## golangci-lint
	$(GOLANGCI) run ./...

fmt: ## gofmt
	go fmt ./...

tidy: ## go mod tidy
	go mod tidy

clean: ## Remove build output
	rm -rf bin tmp coverage.out
	go clean -testcache
