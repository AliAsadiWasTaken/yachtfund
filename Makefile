# ---- config ----
-include .env
export

MAKEFLAGS += --warn-undefined-variables

COMPOSE := docker compose
DB_SVC  := postgres
BINARY  := api
SVC     ?=

# Config lives in .env as POSTGRES_* parts: Compose reads them to initialise the
# server, and internal/config assembles the client DSN from the same values with
# net/url, so passwords are encoded correctly. Set DATABASE_URL in .env only to
# point at a database Compose does not manage.

.PHONY: up down restart logs psql ps build run test tidy check-env

# ---- docker ----
up: check-env
	$(COMPOSE) up -d --wait

down:
	$(COMPOSE) down

restart:
	$(MAKE) down
	$(MAKE) up

logs:
	$(COMPOSE) logs -f $(SVC)

psql:
	$(COMPOSE) exec $(DB_SVC) psql -U $(POSTGRES_USER) -d $(POSTGRES_DB)

ps:
	$(COMPOSE) ps

# ---- go ----
build:
	go build -o bin/$(BINARY) ./cmd/$(BINARY)

run: check-env
	go run ./cmd/$(BINARY)

test:
	go test ./... -race

tidy:
	go mod tidy

# ---- helpers ----
check-env:
	@test -f .env || { echo "missing .env — copy .env.example"; exit 1; }
