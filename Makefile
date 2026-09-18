.DEFAULT_GOAL := help

# Load variables from .env if present so local commands pick up DATABASE_URL etc.
ifneq (,$(wildcard .env))
	include .env
	export
endif

DATABASE_URL ?= postgres://teamflow:teamflow@localhost:5432/teamflow?sslmode=disable
MIGRATIONS_DIR := migrations

# Use a dockerized migrate CLI so contributors need nothing installed locally.
MIGRATE := docker run --rm --network host -v $(PWD)/$(MIGRATIONS_DIR):/migrations migrate/migrate \
	-path=/migrations -database "$(DATABASE_URL)"

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: dev
dev: ## Run the API locally (reads .env)
	go run ./cmd/api

.PHONY: worker
worker: ## Run the worker locally (reads .env)
	go run ./cmd/worker

.PHONY: build
build: ## Build api and worker binaries into ./bin
	@mkdir -p bin
	go build -o bin/api ./cmd/api
	go build -o bin/worker ./cmd/worker

.PHONY: test
test: ## Run all tests
	go test ./...

.PHONY: test-race
test-race: ## Run all tests with the race detector
	go test -race ./...

.PHONY: cover
cover: ## Run tests with coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

.PHONY: fmt
fmt: ## Format all Go code
	gofmt -w .
	go mod tidy

.PHONY: lint
lint: ## Run static analysis (go vet; golangci-lint if installed)
	go vet ./...
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run ./... || \
		echo "golangci-lint not installed; ran go vet only"

.PHONY: migrate-up
migrate-up: ## Apply all up migrations
	$(MIGRATE) up

.PHONY: migrate-down
migrate-down: ## Roll back the most recent migration
	$(MIGRATE) down 1

.PHONY: migrate-create
migrate-create: ## Create a new migration: make migrate-create name=create_users
	$(MIGRATE) create -ext sql -dir /migrations -seq $(name)

.PHONY: sqlc
sqlc: ## Generate type-safe SQL code (requires sqlc)
	@command -v sqlc >/dev/null 2>&1 && sqlc generate || \
		echo "sqlc not installed; skipping (added in a later phase)"

.PHONY: docker-up
docker-up: ## Start all services via docker compose
	docker compose up -d --build

.PHONY: docker-down
docker-down: ## Stop all services
	docker compose down

.PHONY: docker-logs
docker-logs: ## Tail logs from all services
	docker compose logs -f
