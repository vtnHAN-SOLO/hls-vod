# HLS VOD — see CLAUDE.md "Definition of done".

SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := check

GOBIN ?= $(shell go env GOPATH)/bin
BUILDINFO := github.com/vtnHAN-SOLO/hls-vod/internal/buildinfo
export PATH := $(GOBIN):$(PATH)

# Pinned dev-tool versions. Bump deliberately, never use @latest.
GOLANGCI_LINT_VERSION := v2.15.0
GITLEAKS_VERSION      := v8.30.1
GOVULNCHECK_VERSION   := v1.9.0
GOSEC_VERSION         := v2.30.0
GOOSE_VERSION         := v3.29.0
# The local cluster (make kind-up). kind from source (go install); kubectl
# the release binary, checked against its published SHA-256.
KIND_VERSION          := v0.33.0
KUBECTL_VERSION       := v1.37.2

BINARIES := api dispatcher migrate transcoder useradmin watcher

# hadolint (Dockerfile linter) runs from its official image, pinned by tag and
# digest; `make setup` pulls it, `make check` runs it. To update: pick a
# release at least 14 days old, then docker buildx imagetools inspect
# hadolint/hadolint:<tag> and replace both.
HADOLINT_IMAGE := hadolint/hadolint:v2.15.1@sha256:32dac94127fd60b7b7e3fbfc65e1383b9b5e25c9bfd7b8536de7a539fe68a12d
DOCKERFILES := docker/Dockerfile docker/web.Dockerfile docker/media-delivery.Dockerfile
# kubeconform (manifest validation) the same way: its image, by tag and
# digest. make check validates the rendered Kustomize output against the
# schemas of the Kubernetes version the kind node runs, in strict mode.
KUBECONFORM_IMAGE := ghcr.io/yannh/kubeconform:v0.8.0@sha256:faffaf43f95aa6425306e1ab8d6fcad72acb9049158f38e574c085ea1ec0f64e
KUBERNETES_VERSION := 1.37.0
KUSTOMIZE_DIRS := deploy/base deploy/overlays/local deploy/kind/metrics-server

# Container images (make images). VERSION is stamped into every Go binary
# (internal/buildinfo) and, with REVISION, into the images' OCI labels.
VERSION  ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || echo dev)$(shell git status --porcelain 2>/dev/null | grep -q . && echo -dirty)
REVISION ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
IMAGE_PREFIX   := hls-vod
IMAGE_TAG      := dev
IMAGE_PLATFORM := linux/amd64
IMAGE_ARGS = --platform $(IMAGE_PLATFORM) --build-arg VERSION=$(VERSION) --build-arg REVISION=$(REVISION)
# The web image's brand, baked in at build time: the public defaults.
WEB_BRAND_NAME   := HLS VOD
WEB_BRAND_ACCENT := VOD

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

.PHONY: setup tools system-tools web-tools hadolint-image hooks check build vet lint test secrets vulncheck gosec frontend run clean \
	images images-smoke manifests kind-tools kind-up kind-smoke kind-down \
	db-up db-down migrate migrate-down scan dispatch user-add dev-ingest demo-seed media-up media-check web-dev web-build

## setup: install dev tools and git hooks, then verify system tools (run after a fresh clone)
setup: tools kind-tools hooks system-tools web-tools hadolint-image

## hadolint-image: pull the pinned hadolint image (make check lints the Dockerfiles with it)
hadolint-image:
	docker pull -q $(HADOLINT_IMAGE)

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

## kind-tools: kind and kubectl at the pinned versions, into $$(GOBIN) (first on PATH for make)
kind-tools:
	go install sigs.k8s.io/kind@$(KIND_VERSION)
	@scripts/install-kubectl.sh "$(KUBECTL_VERSION)" "$(GOBIN)"

hooks:
	./scripts/install-hooks.sh

## check: the full gate — build, vet, lint, test, secrets, vulns, gosec, frontend
check:
	@echo "==> build";     go build ./...
	@echo "==> vet";       go vet ./...
	@echo "==> lint";      golangci-lint run
	@echo "==> hadolint";  docker run --rm -v "$(CURDIR)/docker:/docker:ro" $(HADOLINT_IMAGE) hadolint $(addprefix /,$(DOCKERFILES))
	@$(MAKE) --no-print-directory manifests
	@echo "==> test";      go test ./...
	@echo "==> secrets";   gitleaks detect --source . --no-banner
	@echo "==> vulncheck"; govulncheck ./...
	@echo "==> gosec";     gosec -quiet ./...
	@$(MAKE) --no-print-directory frontend

## manifests: kustomize build of every kustomization + the kind Traefik manifest, validated by
##   kubeconform (strict, Kubernetes $(KUBERNETES_VERSION) schemas)
manifests:
	@echo "==> manifests"
	@for d in $(KUSTOMIZE_DIRS); do \
		printf '%s: ' "$$d"; \
		kubectl kustomize "$$d" | docker run --rm -i $(KUBECONFORM_IMAGE) -strict -summary -kubernetes-version $(KUBERNETES_VERSION) -; \
	done
	@printf 'deploy/kind/traefik.yaml: '; \
		docker run --rm -i $(KUBECONFORM_IMAGE) -strict -summary -kubernetes-version $(KUBERNETES_VERSION) - < deploy/kind/traefik.yaml

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
		CGO_ENABLED=0 go build -trimpath -ldflags='-s -w -X $(BUILDINFO).Version=$(VERSION)' -o bin/$$b ./cmd/$$b; \
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

## media-up: build the media-delivery image and start it on 127.0.0.1:8081, serving $$MEDIA_ROOT/library
media-up:
	@$(LOAD_ENV); docker compose up -d --build --wait media-delivery

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
##   make dev-ingest INPUT=~/Videos/movie.mp4 TITLE="My test" [YEAR=2020] [OVERVIEW="..."]
##     [POSTER=poster.png] [PUBLISH=1]   (prints the new movie id; PUBLISH needs POSTER and OVERVIEW)
# INPUT, TITLE, YEAR, OVERVIEW and POSTER reach the tool through the environment
# (make exports command-line variables), never spliced into the shell command,
# so quotes or $ in a title cannot break out.
dev-ingest:
	@$(LOAD_ENV); go run ./tools/devingest --input "$${INPUT:-}" --title "$${TITLE:-}" \
		--year "$${YEAR:-0}" --overview "$${OVERVIEW:-}" --poster "$${POSTER:-}" \
		$${PUBLISH:+--publish}

## demo-seed: put 8 demo movies (synthetic ffmpeg clips, invented titles) in the local catalog,
##   ready and published, each with a poster cut from one of its frames (dev only; writes to
##   $$MEDIA_ROOT and the compose Postgres; twice adds them twice)
demo-seed:
	@$(LOAD_ENV); scripts/demo-seed.sh

## images: build the four images for linux/amd64, tagged $(IMAGE_PREFIX)/<name>:$(IMAGE_TAG)
images:
	@echo "==> $(IMAGE_PREFIX)/app:$(IMAGE_TAG) (version $(VERSION))"
	@docker build $(IMAGE_ARGS) -f docker/Dockerfile --target app -t $(IMAGE_PREFIX)/app:$(IMAGE_TAG) .
	@echo "==> $(IMAGE_PREFIX)/transcoder:$(IMAGE_TAG)"
	@docker build $(IMAGE_ARGS) -f docker/Dockerfile --target transcoder -t $(IMAGE_PREFIX)/transcoder:$(IMAGE_TAG) .
	@echo "==> $(IMAGE_PREFIX)/web:$(IMAGE_TAG) (brand $(WEB_BRAND_NAME))"
	@docker build $(IMAGE_ARGS) --build-arg VITE_BRAND_NAME="$(WEB_BRAND_NAME)" --build-arg VITE_BRAND_ACCENT="$(WEB_BRAND_ACCENT)" \
		-f docker/web.Dockerfile -t $(IMAGE_PREFIX)/web:$(IMAGE_TAG) .
	@echo "==> $(IMAGE_PREFIX)/media-delivery:$(IMAGE_TAG)"
	@docker build $(IMAGE_ARGS) -f docker/media-delivery.Dockerfile -t $(IMAGE_PREFIX)/media-delivery:$(IMAGE_TAG) .

## images-smoke: run the built images hardened and check them (needs make images, and the compose
##   Postgres up and migrated: make db-up && make migrate). Not part of make check
images-smoke:
	@$(LOAD_ENV); IMAGE_PREFIX=$(IMAGE_PREFIX) IMAGE_TAG=$(IMAGE_TAG) VERSION="$(VERSION)" \
		BRAND_NAME="$(WEB_BRAND_NAME)" scripts/images-smoke.sh

## kind-up: the whole stack on a local kind cluster (http://localhost:8080) from the images of
##   make images (built if missing); kubeconfig in var/kind/, the admin password in
##   var/kind/admin-password. Again after make images: loads and rolls out the new images
kind-up:
	@IMAGE_PREFIX=$(IMAGE_PREFIX) IMAGE_TAG=$(IMAGE_TAG) scripts/kind-up.sh

## kind-smoke: prove the kind cluster works end to end (a clip ingested, published, played)
kind-smoke:
	@IMAGE_PREFIX=$(IMAGE_PREFIX) IMAGE_TAG=$(IMAGE_TAG) scripts/kind-smoke.sh

## kind-down: delete the kind cluster and var/kind/ (its kubeconfig, the admin password)
kind-down:
	@scripts/kind-down.sh

clean:
	rm -rf bin coverage.out

