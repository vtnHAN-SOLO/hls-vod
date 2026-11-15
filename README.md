# hls-vod

A self-hosted **HLS video-on-demand** platform. Movies are transcoded to adaptive HLS once, at
ingest; playback is static files served by a stateless nginx tier to holders of a URL the API
signed. All state lives in Postgres, so everything else scales and restarts freely.

```mermaid
flowchart LR
    browser(["Browser"]) --> ingress["Ingress"]
    ingress -- "/" --> web["web<br/>React SPA"]
    ingress -- "/api" --> api["api<br/>Go"]
    ingress -- "/media (signed)" --> md["media-delivery<br/>nginx, HPA 2-5"]
    api --> pg[("Postgres")]
    watcher["watcher<br/>CronJob"] --> pg
    dispatcher["dispatcher"] --> pg
    dispatcher -- "creates" --> tj["transcoder<br/>Kubernetes Job (ffmpeg)"]
    tj --> pg
    tj --> vol[("shared media volume")]
    watcher --> vol
    md -- "read-only" --> vol
```

## Features

- **Ingest pipeline:** drop a file in `inbox/` and it is claimed, queued and transcoded into an
  adaptive HLS ladder. The top rung is a remux when the codec allows; the source is never
  upscaled. Posters, seek-preview thumbnail sprites and subtitles are produced in the same run.
- **Playback:** hls.js with adaptive bitrate and a quality menu, seek previews, and resume
  where you left off ("continue watching"). Expired media URLs are renewed mid-movie, without
  interrupting playback.
- **Catalog:** search, sort and year filters, all in the URL.
- **Accounts:**
  - open registration with email verification (Resend) and a Cloudflare Turnstile challenge;
  - forgot / reset password;
  - sign in with Google (OIDC, PKCE);
  - an account page with password change and per-device sessions ("sign out everywhere
    else").
- **Admin area:**
  - users: disable, role; never zero admins;
  - transcode jobs with their errors;
  - movies: retry a failed one, delete, and retranscode a ready movie into a new version
    without downtime.

## Backend highlights

- **No live transcoding, ever:** playback costs nginx and bandwidth, not CPU.
- **Signed media URLs checked by nginx itself** (`secure_link_md5`, key last, token in the
  path so relative HLS URLs inherit it): the delivery tier never calls the API and holds no
  state.
- **Postgres as the job queue:**
  - the dispatcher claims jobs with an advisory lock + `FOR UPDATE SKIP LOCKED`, exact
    `MAX_CONCURRENT` across replicas;
  - transcoders run as Kubernetes Jobs, which only the dispatcher may create;
  - the database itself refuses a second active job per movie.
- **Crash-safe file work:**
  - the rename into `sources/` is the claim lock;
  - versions are written aside and published by one atomic rename and one transaction;
  - a failed `COMMIT` is re-read before any undo.
- **Sessions:**
  - refresh-token families with rotation and reuse detection;
  - 15-minute access tokens kept in memory only;
  - refresh serialised across browser tabs with a Web Lock.
- **Abuse resistance:**
  - login throttles per username+IP and per IP (password spraying), with in-flight
    reservations so parallel bursts cannot slip through;
  - the client IP is believed from `X-Forwarded-For` only through trusted proxies;
  - no email enumeration anywhere.
- **Security headers:** strict CSP with no inline scripts; Pod Security `restricted`; non-root
  read-only containers; no secrets in code, config or tests.

## Tech stack

| Area | Choice |
|---|---|
| Backend | Go (stdlib `net/http`), pgx/v5, goose, `log/slog`, Prometheus client, golang-jwt, go-oidc |
| Media | ffmpeg / ffprobe, HLS, nginx (`secure_link`) |
| Frontend | React, Vite, TypeScript, Tailwind CSS, hls.js, TanStack Query, openapi-typescript + openapi-fetch |
| Data | Postgres 16 |
| Platform | Docker, Kubernetes (Jobs, CronJob, HPA); Kustomize manifests and Flux in progress |
| Quality | golangci-lint, gosec, govulncheck, gitleaks, testcontainers-go, vitest |

## Quick start

Needs Go, Docker, ffmpeg and Node 22+ (with corepack).

```bash
make setup                 # pinned dev tools, git hooks, web dependencies
cp .env.example .env       # then fill in the values (names only in the example; no defaults for secrets)
make db-up                 # Postgres in docker compose, on 127.0.0.1
make migrate
make user-add USER=admin ROLE=admin
make run                   # api on 127.0.0.1:8080
make media-up              # media-delivery on 127.0.0.1:8081
make web-dev               # http://127.0.0.1:5173
```

Put a movie in the catalog: drop it into `$MEDIA_ROOT/inbox/`, then run `make scan` (watcher)
and `make dispatch` (dispatcher with the local backend), or use `make dev-ingest
INPUT=movie.mp4 TITLE="My test"`.

## Testing

```bash
make check
```

This runs build, vet, golangci-lint, every test (integration tests start their own Postgres
with testcontainers, so **Docker must be running**), gitleaks, govulncheck, gosec, and the
frontend checks (generated API types up to date, eslint, typecheck, vitest). External services
(Google, Resend, Turnstile) are faked in tests; no test uses a real credential.

## Documentation

- [docs/architecture/](docs/architecture/README.md): 4+1 views (logical, process, development,
  physical, scenarios) and ADRs
- [docs/openapi.yaml](docs/openapi.yaml): the API contract, validated by the handler tests and
  the source of the web client's types
- [CLAUDE.md](CLAUDE.md): the project's constraints and conventions

## AI-assisted development

This project was built with [Claude Code](https://claude.com/claude-code), working under the
constraints in [CLAUDE.md](CLAUDE.md):

- hard architectural rules;
- conventions enforced by lint;
- a definition of done.

Every change went through a pull request and had to pass `make check` before it was merged.
The behaviour each PR described was then checked by hand.
