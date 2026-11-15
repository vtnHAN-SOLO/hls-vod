# HLS VOD — see CLAUDE.md "Definition of done".

SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := check

GOBIN ?= $(shell go env GOPATH)/bin
export PATH := $(GOBIN):$(PATH)

# Pinned dev-tool versions. Bump deliberately, never use @latest.
GOLANGCI_LINT_VERSION := v2.15.0
GITLEAKS_VERSION      := v8.30.1
GOVULNCHECK_VERSION   := v1.9.0
GOSEC_VERSION         := v2.30.0
GOOSE_VERSION         := v3.29.0

BINARIES := api dispatcher transcoder useradmin watcher

# System tools that are not Go deps: installed by the OS package manager.
SYSTEM_TOOLS := ffmpeg ffprobe

# web/ toolchain. pnpm's exact version is pinned by "packageManager" in
# web/package.json; corepack (bundled with Node) fetches that version.
# Corepack picks the version from the CURRENT directory, not from -C, so
# recipes cd into web/ first. Nothing user-wide is changed: no pnpm shim on
# PATH, no corepack default; use make web-dev / web-build.
NODE_MIN_MAJOR := 22
PNPM_WEB := cd web && corepack pnpm
export COREPACK_ENABLE_DOWNLOAD_PROMPT := 0

# Exports every variable in .env into the recipe's shell. Recipes are prefixed
# with @ and never echo variables, so values are not printed.
LOAD_ENV = if [ ! -f .env ]; then echo ".env not found: create it from .env.example" >&2; exit 1; fi; \
	set -a; . ./.env; set +a

.PHONY: setup tools system-tools web-tools hooks check build vet lint test secrets vulncheck gosec frontend run clean \
	db-up db-down migrate migrate-down scan dispatch user-add dev-ingest media-up media-check web-dev web-build

## setup: install dev tools and git hooks, then verify system tools (run after a fresh clone)
setup: tools hooks system-tools web-tools

## web-tools: check node + corepack, install web/ deps from the lockfile with the pinned pnpm
web-tools:
	@if ! command -v node >/dev/null 2>&1 || ! command -v corepack >/dev/null 2>&1; then \
		echo "missing node and/or corepack (corepack ships with Node)" >&2; \
		echo "install with: nvm install 24   (https://github.com/nvm-sh/nvm), or your OS's Node 24 package" >&2; \
		exit 1; \
	fi; \
	major=$$(node -p 'process.versions.node.split(".")[0]'); \
	if [ "$$major" -lt $(NODE_MIN_MAJOR) ]; then \
		echo "node $$(node --version) is too old; need >= $(NODE_MIN_MAJOR). install with: nvm install 24" >&2; \
		exit 1; \
	fi; \
	echo "node: $$(node --version), corepack: $$(corepack --version)"; \
	$(PNPM_WEB) install --frozen-lockfile

## web-dev: Vite dev server on 127.0.0.1:5173, proxying /api -> :8080 and /media -> :8081
web-dev:
	@$(PNPM_WEB) dev

## web-build: typecheck and build the SPA into web/dist
web-build:
	@$(PNPM_WEB) build

## system-tools: fail with the install command if ffmpeg/ffprobe are missing
system-tools:
	@missing=""; for t in $(SYSTEM_TOOLS); do command -v $$t >/dev/null 2>&1 || missing="$$missing $$t"; done; \
	if [ -n "$$missing" ]; then \
		echo "missing system tools:$$missing" >&2; \
		echo "install with: sudo apt install ffmpeg" >&2; \
		exit 1; \
	fi; \
	echo "ffmpeg: $$(ffmpeg -hide_banner -version | head -n1)"

tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	go install github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION)
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	go install github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION)
	go install github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)

hooks:
	./scripts/install-hooks.sh

## check: the full gate — build, vet, lint, test, secrets, vulns, gosec, frontend
check:
	@echo "==> build";     go build ./...
	@echo "==> vet";       go vet ./...
	@echo "==> lint";      golangci-lint run
	@echo "==> test";      go test ./...
	@echo "==> secrets";   gitleaks detect --source . --no-banner
	@echo "==> vulncheck"; govulncheck ./...
	@echo "==> gosec";     gosec -quiet ./...
	@$(MAKE) --no-print-directory frontend

## frontend: web/ gate — lockfile install, generated API types fresh, lint, typecheck, unit tests
frontend:
	@echo "==> frontend"
	@$(PNPM_WEB) install --frozen-lockfile --silent
	@$(PNPM_WEB) --silent gen:api:check || { \
		echo "web/src/api/schema.ts is stale: run '(cd web && corepack pnpm gen:api)' and commit it" >&2; exit 1; }
	@$(PNPM_WEB) --silent lint
	@$(PNPM_WEB) --silent typecheck
	@$(PNPM_WEB) --silent test

## build: build all binaries into bin/
build:
	@mkdir -p bin
	@for b in $(BINARIES); do \
		echo "==> bin/$$b"; \
		CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bin/$$b ./cmd/$$b; \
	done

## run: run the api against the compose Postgres (loads .env)
run:
	@$(LOAD_ENV); go run ./cmd/api

## db-up: start the compose Postgres and wait for its healthcheck
db-up:
	@$(LOAD_ENV); docker compose up -d --wait postgres

## db-down: stop the compose Postgres (keeps the data volume)
db-down:
	@$(LOAD_ENV); docker compose down

## media-up: start media-delivery (nginx) on 127.0.0.1:8081, serving $$MEDIA_ROOT/library
media-up:
	@$(LOAD_ENV); docker compose up -d --wait media-delivery

## media-check: headers, CORS, probes and path traversal against a running media-delivery
##   make media-check [MOVIE=<id>] [BASE=http://127.0.0.1:8081]
media-check:
	@$(LOAD_ENV); ./scripts/check-media-delivery.sh

## migrate: apply all pending migrations to $$DATABASE_URL
migrate:
	@$(LOAD_ENV); goose -dir migrations postgres "$$DATABASE_URL" up

## migrate-down: roll back the most recent migration
migrate-down:
	@$(LOAD_ENV); goose -dir migrations postgres "$$DATABASE_URL" down

## scan: run the watcher once against $$MEDIA_ROOT and the compose Postgres
scan:
	@$(LOAD_ENV); go run ./cmd/watcher

## dispatch: build bin/transcoder and bin/dispatcher, then run the dispatcher with the local
##   backend against the compose Postgres and $$MEDIA_ROOT. Ctrl-C stops the dispatcher only:
##   a running transcode finishes, and the next `make dispatch` adopts it.
# exec: Ctrl-C reaches the dispatcher itself, not a `go run` wrapper.
dispatch:
	@mkdir -p bin
	@CGO_ENABLED=0 go build -trimpath -o bin/transcoder ./cmd/transcoder
	@CGO_ENABLED=0 go build -trimpath -o bin/dispatcher ./cmd/dispatcher
	@$(LOAD_ENV); DISPATCH_BACKEND=local TRANSCODER_BIN="$(CURDIR)/bin/transcoder" exec bin/dispatcher

## user-add: create an api user; prompts for the password (never an argument)
##   make user-add USER=<name> [ROLE=admin]
# USER must come from the make command line: it is also the shell's login
# name, and silently creating a user named after it would be a surprise.
user-add:
	@if [ "$(origin USER)" != "command line" ]; then \
		echo "usage: make user-add USER=<name> [ROLE=admin]" >&2; exit 2; fi
	@$(LOAD_ENV); go run ./cmd/useradmin add --role "$${ROLE:-user}" "$(USER)"

## dev-ingest: put one local movie in the local catalog as ready (dev only, until the transcoder Job runs)
##   make dev-ingest INPUT=~/Videos/movie.mp4 TITLE="My test"   (prints the new movie id)
# INPUT and TITLE reach the tool through the environment (make exports
# command-line variables), never spliced into the shell command, so quotes or
# $ in a title cannot break out.
dev-ingest:
	@$(LOAD_ENV); go run ./tools/devingest --input "$${INPUT:-}" --title "$${TITLE:-}"

clean:
	rm -rf bin coverage.out

