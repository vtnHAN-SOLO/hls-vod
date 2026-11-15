# CLAUDE.md — HLS VOD

Self-hosted movie streaming platform (HLS video on demand). It runs on Kubernetes, published
through an edge reverse proxy. This file covers the application code. `docs/architecture/`
describes the architecture (4+1 views and ADRs).


---

## Hard constraints — never violate

1. **No live/on-demand transcoding.** All media is pre-transcoded to HLS at ingest time.
   Playback is nginx serving static files. One live transcode would eat 2-4 cores and break
   the entire scaling model.
2. **`media-delivery` stays stateless.** No sessions, no local cache, no writes. It mounts
   media read-only and serves bytes. This is the only tier that scales horizontally.
3. **All state lives in Postgres.** No SQLite anywhere — that's the exact mistake that makes
   Jellyfin single-replica.
4. **Remux when the codec allows.** If source is H.264/AAC, the top rung is `ffmpeg -c copy`
   (~30s). Only encode lower rungs. Let `ffprobe` decide, never assume.
5. **Never upscale.** Top rung resolution = source resolution. A 720p source gets a 720p top
   rung, not 1080p.
6. **No `hostPath` volumes.** Pod Security Standards are set to `restricted`. Media comes
   from an NFS PV with `ReadWriteMany`.
7. **No secrets in code, config, manifests, tests or compose files.** This includes
   placeholder values — no `password: changeme`, no `jwtSecret = "dev"`, no
   `POSTGRES_PASSWORD: postgres`. Every secret is read from an environment variable with
   **no default**; a missing variable fails fast at startup with a clear error. Commit
   `.env.example` listing variable *names* only. Secrets that reach Kubernetes are
   SOPS-encrypted.
8. **Every long-running service exposes `/healthz`, `/readyz` and `/metrics`.**
   - `/healthz` = **liveness**. Process-only: no database, no NFS, no network calls. If it
     checked Postgres, one DB blip would make Kubernetes restart every api pod at once
   - `/readyz` = **readiness**. Checks the dependencies this pod needs to serve (Postgres for
     `api`, a readable media mount for `media-delivery`). Failing it removes the pod from the
     Service without restarting it
   - `/metrics` = Prometheus scraping
   - Run-to-completion workloads (`transcoder` Job, `watcher` CronJob) get no probes; they
     report through logs and exit code
9. **Listen on unprivileged ports (≥1024).** Containers run as non-root with all capabilities
   dropped, so binding port 80 is not an option. Convention: `8080` for every HTTP container,
   exposed as a named port `http`.

---

## Stack

### Backend — Go

| Concern | Choice |
|---|---|
| HTTP router | stdlib `net/http` `ServeMux` (Go 1.22+ method + path patterns) |
| DB driver | `pgx/v5` |
| Migrations | `goose` |
| Config | `caarlos0/env/v11`, in `internal/config` — env vars only, fail fast on missing secrets |
| Logging | `log/slog`, JSON output |
| Metrics | `prometheus/client_golang` |
| Auth | JWT, `golang-jwt/jwt/v5` |
| Sign in with Google | `golang.org/x/oauth2` (code flow, PKCE) + `coreos/go-oidc/v3` (discovery, JWKS, ID token checks) |
| Testing | stdlib `testing`; integration tests use `testcontainers-go` (see Testing below) |

Go was chosen deliberately: static binary, small container, and a concurrent job queue is
idiomatic. Keep dependencies minimal — prefer stdlib where it's reasonable.

### Frontend — React

| Concern | Choice |
|---|---|
| Build | Vite + TypeScript |
| Styling | Tailwind CSS |
| Player | **hls.js** (handles adaptive bitrate) |
| State | TanStack Query for server state, no Redux |
| Session | `src/lib/session.ts`: access token in memory only; refresh single-flight within the tab and across tabs (Web Locks) |
| Router | React Router |
| API client | **openapi-typescript** + **openapi-fetch** — types generated from `docs/openapi.yaml` with no runtime codegen; a ~6 kB fetch wrapper typed by them, so a spec change breaks typecheck |
| Testing | **vitest** — runs on the Vite config already there; unit tests for pure logic in `src/lib` |

The frontend is a thin client over the API. Don't over-engineer it.

### Infrastructure

Postgres 16 · nginx (media delivery) · ffmpeg (transcode) · Kubernetes · Flux (GitOps)

---

## Architecture

```
web (React SPA) ──► api (Go, read-only) ──► postgres ◄── watcher (CronJob)
                    no K8s permissions        ▲  ▲
browser ──► media-delivery                    │  └──── dispatcher (Go, long-running)
            (nginx, HPA 2-5)                  │        the only ServiceAccount that
                 │                            │        may create Jobs
                 │                       transcoder ◄─── started by the dispatcher
                 │                       (Go + ffmpeg,   (K8s Job, or a local child
                 ▼                        --movie-id)     process on a laptop)
   NFS /mnt/media/library ◄─────────────────┘
```

### Services

| Service | Language | Replicas | Purpose |
|---|---|---|---|
| `api` | Go | 2 | Catalog, login / refresh / logout (JWT access token + rotating refresh cookie); open registration with email verification (Turnstile, mail through Resend); forgot / reset password; sign in with Google (OIDC); watch progress (save, continue-watching, resume position); the caller's account (password change or set, Google link, sessions); the admin area (users, jobs, retry and delete of movies; the role checked in the database on every call). Never touches files, no Kubernetes permissions |
| `web` | React (static) | 2 | SPA, served by nginx |
| `media-delivery` | nginx conf only | 2-5 HPA | Serves HLS segments, posters and thumbnails from NFS, read-only, only to URLs the api signed (secure_link, shared `MEDIA_SIGNING_KEY`) |
| `dispatcher` | Go | 1-2 | Starts the transcoder for queued jobs (`DISPATCH_BACKEND` local or kubernetes), at most `MAX_CONCURRENT` at once; reconciles running jobs; the only component that may create Jobs |
| `transcoder` | Go + ffmpeg | 0-1 Job | Ingest pipeline: `--movie-id` job mode records its own result |
| `watcher` | Go | CronJob /5min | One run, in order: purge (removes the movies admins deleted: `library/{id}/`, the source, then the row), cleanup (versions a retranscode replaced, once their URLs have expired), prune (auth rows no longer in use, bounded batches), reconcile (orphaned sources back to `inbox/`), claim (`inbox/` files into `sources/`, jobs queued in Postgres) |

### Media flow

```
inbox/My Movie (2020).mkv
  → watcher: file settled (mtime > 2 min) → rename to sources/{id}.mkv (the rename is the lock)
             → one tx: INSERT movies(status=pending, title="My Movie (2020)")
                       + transcode_jobs(status=queued)
  → dispatcher: SELECT … FOR UPDATE SKIP LOCKED the oldest queued job (≤ MAX_CONCURRENT
                running) → job running → K8s Job / local child `transcoder --movie-id {id}`
  → transcoder: movie processing → ffprobe → top rung at source height (remux, copy video +
                AAC, or encode, as ffprobe allows) → encode lower rungs
                → poster + thumbnail sprite → subtitles → library/{id}/{version}/ (version =
                  the job id; written in .{version}.partial-*, then one atomic rename)
                → one tx: renditions + movie ready + job succeeded (or both failed + error)
  → dispatcher reconciles: exited without a result / gone / past the 6h deadline → failed
  → library/{id}/{version}/master.m3u8 served by media-delivery at
    /media/{token}/{expires}/{id}/{version}/master.m3u8 (one token covers library/{id}/)

Movies published before versioned folders have their files directly in library/{id}/ (library_path
"library/{id}") and keep playing from there. Every URL is built from the stored library_path.

admin retranscodes a ready movie → job queued with purpose retranscode; the movie STAYS ready
  → transcoder: free space ≥ 1.5 × the current version (else failed, nothing written)
    → library/{id}/{new version}/ → one tx: library_path, poster_path, renditions, runtime
    switch to the new version, library_cleanup_after = now + mediattl.OldVersionGrace (25 h:
    the longest signed URL lifetime, posters 24 h rounded up), job succeeded. Failure: job
    failed, movie still ready on its version
  → watcher, each run after the purge: movies whose library_cleanup_after has passed and that
    have no active job: under the movie's row lock, remove every entry of library/{id}/ except
    the current version (old versions, old-layout files, .partial-* leftovers), then NULL it.
    A second retranscode inside the 25 h resets the timer

admin deletes a movie (no job queued or running) → one tx: movie deleting, hidden from users
  → watcher, next run, before reconcile and scan: remove library/{id}/, then sources/{id}.{ext}
    (missing files are fine), then DELETE the row (renditions, jobs, progress cascade). The row
    goes last, so reconcile never sees that source without a row and never re-ingests it
admin retries a failed movie → one tx: movie pending + a new queued job (the source is still
  in sources/) → the dispatcher picks it up as above
```

---

## Repo layout

One Go module at the root. Binaries under `cmd/`, shared code under `internal/`.

```
./
├── CLAUDE.md
├── go.mod                    # single module: github.com/vtnHAN-SOLO/hls-vod
├── Makefile
├── .env.example              # variable NAMES only, never values
├── .gitignore
├── .golangci.yml
├── docker-compose.yml
├── cmd/
│   ├── api/main.go
│   ├── dispatcher/           # long-running: claims queued jobs, starts and watches transcoders
│   ├── useradmin/            # add / set-password / set-role / list; the password never in argv or env
│   ├── transcoder/           # --movie-id job mode + --input/--output; the pipeline is internal/transcode
│   └── watcher/              # one scan per run: claim inbox files, queue jobs, repair crashes
├── internal/
│   ├── config/               # env loading, fails fast on missing secrets
│   ├── handler/              # HTTP handlers, thin
│   ├── service/              # business logic
│   ├── store/                # Postgres access
│   ├── model/                # domain types shared by store, service and handler
│   ├── mediapath/            # the only place that builds or opens paths from request/DB values
│   ├── medialayout/          # names in the media tree: inbox/, sources/, library/{id}/{version}/, master.m3u8
│   ├── mediattl/             # signed URL lifetimes; the old-version grace is derived from them
│   ├── ffmpeg/               # probe, ladder, remux/encode, master playlist, poster, thumbnails, subtitles
│   ├── transcode/            # the ingest pipeline, shared by cmd/transcoder and tools/devingest
│   ├── ingest/               # transcode into library/{id}/{version}/ + the rows that describe it (shared, one way)
│   ├── auth/                 # argon2id hashes, HS256 access tokens, opaque tokens, throttle (reserves in-flight attempts), username rule
│   ├── clientip/             # the client address: TCP peer, or X-Forwarded-For from the right through TRUSTED_PROXIES
│   ├── mail/                 # Resend (REST, no SDK) and log senders, templates, bounded background outbox
│   ├── turnstile/            # Cloudflare Turnstile siteverify (5 s timeout)
│   ├── dispatch/             # dispatcher loop and its backends: local (child process), kubernetes (client-go)
│   ├── pgtest/               # testcontainers Postgres helper for integration tests
│   └── oidctest/             # fake OpenID provider (discovery, JWKS, authorize, token) for tests
├── tools/
│   └── devingest/            # `make dev-ingest`: dev only, never built into any image
├── migrations/               # goose SQL files + embed.go (embed.FS, so tests apply the same files)
├── scripts/
│   ├── hooks/pre-commit      # gitleaks on staged changes
│   ├── install-hooks.sh      # .git/hooks is not tracked — restore after clone
│   ├── check-media-delivery.sh # `make media-check`: signs its own URLs; tokens, headers, probes, path traversal
├── docker/
│   ├── nginx-media/          # media-delivery conf template, key guard + their testcontainers test
│   ├── api.Dockerfile        # planned
│   └── transcoder.Dockerfile # planned
├── docs/
│   ├── architecture/         # 4+1 views (logical, process, development, physical, scenarios) + ADRs
│   └── openapi.yaml          # API contract, validated against handler tests; source of web types
├── web/                      # React SPA, own toolchain (pnpm pinned via "packageManager")
│   ├── security-headers.json # the SPA's CSP and other headers: vite preview now, the web nginx in the cluster
│   └── src/
│       ├── api/              # schema.ts generated from docs/openapi.yaml (`gen:api`) + client
│       ├── auth/             # AuthProvider, RequireAuth (routes other than /login, /register,
│       │                     #   /verify-email, /forgot-password, /reset-password,
│       │                     #   /auth/google/done need a session), useAuthConfig,
│       │                     #   useLinkToken (a mailed link's #token=…, cleared at once)
│       ├── pages/
│       ├── components/
│       └── lib/              # pure logic with vitest tests
├── var/media/                # local MEDIA_ROOT (inbox/, sources/, library/), gitignored
│
│   # Planned, not created yet:
├── deploy/
│   ├── base/                 # Kustomize base
│   ├── overlays/local/
└── .github/workflows/
```

---

## Data model

This is the contract. Migrations implement exactly this; changing it means a new migration
**and** an edit here in the same PR.

Rules for every table:

- `id uuid PRIMARY KEY DEFAULT gen_random_uuid()` (built into Postgres 13+, no extension).
  UUIDs appear in URLs and filesystem paths (`library/{id}/`), so they must not be guessable
  or enumerable, and a value that parses as a UUID cannot contain `../`
- `created_at` and `updated_at` are `timestamptz NOT NULL DEFAULT now()`; one shared
  `set_updated_at()` trigger keeps `updated_at` current
- Status columns are `text` + `CHECK (...)`, not Postgres `ENUM` — adding a value to a CHECK
  is a simple migration, changing an ENUM is not
- No new Go dependency for UUIDs: use `pgtype.UUID` or scan into `string`

```sql
movies (
  id, title NOT NULL, year, tmdb_id, overview, runtime_sec,
  source_path   NOT NULL UNIQUE,   -- sources/{id}.{ext}; the watcher's rename claims each file once
  library_path, poster_path,       -- library_path: library/{id}/{version} (version = the job
                                   -- id), or library/{id} for movies published before
                                   -- versioned folders.
                                   -- poster_path and renditions.playlist_path are relative to
                                   -- library/{id}/ and start with the version
  library_cleanup_after,           -- set when a retranscode publishes (now + 25 h) or a job
                                   -- fails (now, for .partial-* leftovers); once past and no job
                                   -- is active the watcher keeps only the current version
  status        NOT NULL DEFAULT 'pending',
                                   -- CHECK: pending | processing | ready | failed | deleting
                                   -- deleting: an admin deleted it; admins only see it, the
                                   -- watcher removes its files and then the row
  created_at, updated_at
)
  INDEX (status)

renditions (
  id, movie_id → movies ON DELETE CASCADE,
  height NOT NULL,                 -- 720, 480 … never above source height
  bitrate_kbps, playlist_path NOT NULL, size_bytes,
  created_at, updated_at,
  UNIQUE (movie_id, height)
)

users (
  id, username NOT NULL UNIQUE,    -- CHECK (username = lower(username)): the database
                                   -- enforces lowercase, not only application code
  password_hash,                   -- argon2id encoded string; NULL for a Google-only account
                                   -- (password login then fails like a wrong password, after
                                   -- the dummy hash)
  role NOT NULL DEFAULT 'user',    -- CHECK: user | admin
  email UNIQUE,                    -- CHECK (email = lower(email)); NULL for useradmin accounts.
                                   -- Fixed at registration, like username: the recovery
                                   -- channel and the key for linking Google sign-in;
                                   -- there is no email change
  email_verified_at,               -- login refused while email IS NOT NULL AND this IS NULL,
                                   -- and only after the password checked out
  disabled_at,                     -- set by an admin. Disabling revokes every session in the
                                   -- same tx. Refused while set: login (403 account_disabled,
                                   -- after the right password), refresh, Google sign-in and
                                   -- link, verify-email (token kept); no reset, set-password or
                                   -- verification mail. Access tokens run out within 15 min
  created_at, updated_at
)

email_tokens (
  id, user_id → users ON DELETE CASCADE,
  purpose NOT NULL,                -- CHECK: verify | reset
  token_hash bytea NOT NULL UNIQUE,-- SHA-256 of 32 random bytes; never the token
  expires_at NOT NULL, used_at,    -- single use: marked used in the tx that applies it.
                                   -- verify: 24 h from sign-up; a resend never extends it.
                                   -- reset: 30 min. A new request deletes the user's older
                                   -- unused tokens of that purpose
  created_at, updated_at
)
  INDEX (user_id)
  -- An unverified account with no live verify token is deleted by the next registration
  -- that wants its email or username (same transaction): squatting lasts at most 24 h.
  -- The watcher deletes tokens used or expired more than 7 days ago

user_identities (
  id, user_id → users ON DELETE CASCADE,
  provider NOT NULL,               -- CHECK (provider = 'google')
  subject NOT NULL,                -- Google's "sub": the ONLY thing that identifies the Google
                                   -- account, never its email. A later change of the Google
                                   -- address is ignored; the local email stays fixed
  email_at_link,                   -- shown on /account only
  created_at, updated_at,
  UNIQUE (provider, subject),      -- one user per Google account
  UNIQUE (user_id, provider)       -- one Google account per user
)

oauth_states (                     -- a started Google sign-in or link; never updated
  id, state_hash bytea NOT NULL UNIQUE,
                                   -- SHA-256 of the state (the value in the browser's cookie)
  nonce NOT NULL, pkce_verifier NOT NULL,
                                   -- never leave the api
  intent NOT NULL,                 -- CHECK: login | link
  user_id → users ON DELETE CASCADE,
                                   -- CHECK: set iff intent = link
  expires_at NOT NULL,             -- 10 min. Single use: deleted when the callback consumes
                                   -- it; expired rows deleted on the next start and by the
                                   -- watcher
  created_at
)
  INDEX (expires_at)

outgoing_mail (                    -- append-only (never updated), so no updated_at
  id, to_hash bytea NOT NULL,      -- SHA-256 of the lowercase address; never the address
  kind NOT NULL,                   -- CHECK: verify | account_exists | reset | google_linked
  created_at
)
  INDEX (to_hash, created_at), INDEX (created_at)
  -- Recorded before sending, under an advisory lock: ≤ 3 per address per hour, ≤ MAIL_DAILY_CAP
  -- per rolling 24 h across api replicas. Rows older than 2 days are pruned

refresh_tokens (
  id, user_id → users ON DELETE CASCADE,
  family_id NOT NULL,              -- all tokens from one login share it; reuse of a revoked
                                   -- token revokes the whole family
  token_hash bytea NOT NULL UNIQUE,-- SHA-256 of the token; never store the token itself.
                                   -- (SHA-256 is right here: the token is random and
                                   -- high-entropy. argon2id is for passwords.)
  expires_at NOT NULL, revoked_at, replaced_by → refresh_tokens ON DELETE SET NULL,
  user_agent CHECK (char_length(user_agent) <= 200),
                                   -- set at login (truncated to 200 characters), copied on
                                   -- every rotation; NULL when none was sent
  created_at, updated_at
)
  INDEX (user_id), INDEX (family_id)
  -- A session is a family with a live token (unrevoked, unexpired): created = its oldest
  -- row, last used = its newest (every refresh inserts one). No sessions table.
  -- The watcher deletes a family's rows once it has had no live token for 7 days (every row
  -- expired or revoked more than 7 days ago); a live family keeps every row, for the sessions
  -- list and reuse detection

watch_progress (
  user_id → users ON DELETE CASCADE,
  movie_id → movies ON DELETE CASCADE,
  position_sec NOT NULL CHECK (position_sec >= 0),
  created_at, updated_at,
  PRIMARY KEY (user_id, movie_id)  -- no surrogate id; upsert on this key
)
  INDEX (user_id, updated_at DESC) -- continue-watching
  INDEX (movie_id)                 -- PK leads with user_id; needed for the cascade from movies

transcode_jobs (
  id, movie_id → movies ON DELETE CASCADE,
  status NOT NULL DEFAULT 'queued',-- CHECK: queued | running | succeeded | failed
  purpose NOT NULL DEFAULT 'ingest',
                                   -- CHECK: ingest | retranscode. ingest: pending → processing →
                                   -- ready / failed; retranscode: a ready movie gets a new version
                                   -- and its status never changes
  k8s_job_name UNIQUE,             -- NULL until the Job is created
  local_pid CHECK (local_pid > 0), -- local dispatcher backend: the transcoder's pid; NULL otherwise
  started_at, finished_at, error_msg,
  created_at, updated_at
)
  INDEX (status)
  INDEX (movie_id)                 -- cascade from movies + per-movie history; the partial
                                   -- index below only covers active jobs
  UNIQUE (movie_id) WHERE status IN ('queued', 'running')
                                   -- the database itself refuses a second active job
                                   -- for the same movie
```

---

## API surface

```
POST   /api/v1/auth/login           → access token (JSON body, 15 min) + refresh token (HttpOnly
                                      cookie, 30 days); throttled per username+IP (5 failures →
                                      429 for 15 min). From the 3rd failure on it needs
                                      turnstile_token (401 captcha_required). Spraying: per IP
                                      across usernames, from 10 failures in 15 min every login
                                      from that IP needs the challenge, from 100 it is 429 for
                                      15 min (CGNAT: the challenge long before the block).
                                      Attempts in flight count (auth.Throttle reserves them).
                                      Below the thresholds it never calls Cloudflare. Unverified
                                      email → 403 email_not_verified, only after a right password
POST   /api/v1/auth/refresh         → rotate: old refresh token revoked, new pair issued; reusing a
                                      rotated token revokes the whole family. Needs X-Requested-With
POST   /api/v1/auth/logout          → revoke the session (family). Needs X-Requested-With
GET    /api/v1/auth/config          → {registration_open, turnstile_site_key (null if unset)}
POST   /api/v1/auth/register        → {email, username, password, turnstile_token} → 202, the same
                                      body whether or not the address has an account (that one gets
                                      an "account exists" mail instead); both paths hash. 409 for a
                                      taken username (usernames are public; checked before the
                                      email). 403 when REGISTRATION_OPEN=false; 10/h per IP
POST   /api/v1/auth/verify-email    → {token} from /verify-email#token=… → session, like login
POST   /api/v1/auth/resend-verification → {email, turnstile_token} → 202 always; mails only an
                                      unverified account
POST   /api/v1/auth/forgot-password → {email, turnstile_token} → 202 always (resend's validation,
                                      its own 10/h per-IP key, Turnstile fail closed); mails a reset
                                      link + the username only to a VERIFIED account
POST   /api/v1/auth/reset-password  → {token, new_password} → 204. Policy first (no hash for a bad
                                      password, nor for an unusable token); one tx: new hash, every
                                      family revoked, token used. Unknown / expired / used → the same
                                      400 invalid_token. No login: log in with the new password.
                                      useradmin accounts have no email, so no reset by mail: an
                                      admin runs `useradmin set-password`. Works for Google-only
                                      accounts too (their email is verified)
GET    /api/v1/auth/google/start    → a plain link: new oauth_states row, the state in an HttpOnly
                                      `google_state` cookie (SameSite=Lax: Google's redirect back is
                                      a cross-site navigation; Path=/api/v1/auth/google, 10 min),
                                      302 to Google (code + PKCE, scopes openid email)
GET    /api/v1/auth/google/callback → state parameter = cookie = an unexpired row (deleted); code
                                      exchanged with the verifier; ID token verified (JWKS
                                      signature, iss, aud, exp, nonce), email_verified required.
                                      login: a. sub linked → that user; b. a VERIFIED account with
                                      the email → link (google_linked mail to it); c. an UNVERIFIED
                                      one → deleted, never linked, then d; d. new user (role user,
                                      email verified, no password, username from the local part +
                                      4 random characters, retried), refused while
                                      REGISTRATION_OPEN=false. Success: refresh cookie (sid) + 302
                                      /auth/google/done, the SPA refreshes; never a token in a URL.
                                      link: → /account?google=linked. Failure → /login (or
                                      /account) ?google_error=<code>. Never logs a code or token.
                                      No Turnstile on this flow
GET    /api/v1/movies               → paginated list, ?q= title search (case-insensitive substring;
                                      % and _ match literally); ?sort= added_desc (default) |
                                      title_asc (case-insensitive) | year_desc | year_asc, no year
                                      last, always an id tiebreak; ?year_from= / ?year_to=
                                      inclusive, 1880..2100, from <= to, a bound drops movies with
                                      no year. ?status= is admin-only; a user sees ready movies only
GET    /api/v1/movies/:id           → detail + renditions (non-ready: 404 for a user)
GET    /api/v1/movies/:id/playback  → signed master.m3u8 + thumbnails URLs (6 h) + resume_sec, the
                                      caller's saved position (null when none or watched ≥ 95%)
POST   /api/v1/progress             → save watch position {movie_id, position_sec}: upsert on (user,
                                      movie); the user is the token's subject, never the body. Ready
                                      movies only, 0 ≤ position ≤ runtime_sec
GET    /api/v1/continue-watching    → the caller's ready movies with progress, newest first, at most
                                      20, leaving out those watched to ≥ 95% of runtime

GET    /api/v1/me                   → the caller: id, username, role, email, email_verified_at,
                                      created_at, has_password, google (null or {email_at_link,
                                      linked_at})
POST   /api/v1/me/password          → {current_password, new_password}: 12..1024 chars, ≠ current;
                                      wrong current → 400 invalid_credentials, throttled per user id
                                      (its own auth.Throttle, shared with /me/google/link; 429 +
                                      Retry-After). One tx: new hash,
                                      every family revoked, new family for the caller → answered like
                                      login (token + refresh cookie), so this tab stays logged in
GET    /api/v1/me/sessions          → one per live refresh-token family: id (family), user_agent
                                      summary, created_at, last_used_at, current. Never a token/hash
DELETE /api/v1/me/sessions/{id}     → revoke that family; another user's, unknown or malformed → 404
POST   /api/v1/me/sessions/revoke-others → revoke every family except the caller's (sid)
POST   /api/v1/me/google/link       → {current_password} → live session, the shared password
                                      throttle, the password (a stolen session must not link the
                                      attacker's Google as a backdoor) → {url} + the state cookie;
                                      the SPA navigates there. Callback: sub linked elsewhere or a
                                      second Google for the user → /account?google_error=…;
                                      success mails google_linked
DELETE /api/v1/me/google            → live session; 409 last_login_method without a password; 404
                                      when none is linked
POST   /api/v1/me/password/set-link → "Set password" for an account without one: live session;
                                      400 if it has a password; a reset link (reset token,
                                      template and mail limits) to its own verified email; 202

Access tokens carry `sid` = the family id (login, refresh, password change); a token without
`sid` is refused. Revoking a family stops its refresh token only: access tokens already issued
work until they expire (≤ 15 min), since there is no per-request database check. Except on the
endpoints that manage sessions, the password and login methods (`POST /me/password`, `GET
/me/sessions`, `DELETE /me/sessions/{id}`, `POST /me/sessions/revoke-others`, `POST
/me/google/link`, `DELETE /me/google`, `POST /me/password/set-link`): they first check, in one query, that the
caller's `sid` family still has a live refresh token, else 401. So a revoked device cannot change
the password or revoke the session that revoked it during that window. They are rare, so the
no-per-request-check rule holds everywhere else

GET    /api/v1/admin/users          → paginated, ?q= username or email substring; id, username,
                                      email, email_verified_at, role, google_linked, has_password,
                                      disabled_at, created_at. Never a hash or token
POST   /api/v1/admin/users/{id}/disable → disabled_at + every session revoked, one tx
POST   /api/v1/admin/users/{id}/enable
PUT    /api/v1/admin/users/{id}/role → {role}
                                      Not on yourself (409 self_change); never leaving zero active
                                      (not disabled) admins (409 last_admin): checked in a tx that
                                      locks every active admin row in id order, so two admins
                                      demoting each other at once cannot both succeed
GET    /api/v1/admin/jobs           → paginated, ?status=; id, movie_id + movie_title, status,
                                      purpose, started_at, finished_at, error_msg (as stored), k8s_job_name
                                      or local_pid, created_at; newest first
GET    /api/v1/admin/movies         → every status incl. deleting, ?status= ?q=, each with
                                      active_job (queued | running | null), active_job_purpose and
                                      library_cleanup_after
POST   /api/v1/admin/movies/{id}/retry → failed only (else 409 not_failed): pending + a new
                                      queued job, one tx; a second active job is 409 job_active
DELETE /api/v1/admin/movies/{id}    → 409 job_active while a job is queued or running; else
                                      deleting (one tx) → 202. Hidden from every non-admin
                                      endpoint and continue-watching at once, playback refused;
                                      the watcher removes files and row
POST   /api/v1/admin/movies/{id}/retranscode → ready only (else 409 not_ready), never beside a
                                      queued or running job (409 job_active): a job with purpose
                                      retranscode → 202. The movie stays ready and playable

Every /api/v1/admin endpoint checks, in the database on each request: a live session (as the
/me endpoints), role admin, not disabled; 403 otherwise, whatever role the access token
carries (a demoted admin keeps that claim for up to 15 min). Every admin action logs one INFO
line: admin id, action, target id. The api never touches files: the watcher does the
deleting. There is no "scan now": the watcher already runs every 5 minutes, and a scan the api
started would be either file work in the api or a second way to start the watcher, for a wait
of at most 5 minutes.

Every /api/v1 endpoint needs `Authorization: Bearer <access token>` except the /auth/ ones.
Accounts come from `cmd/useradmin` (no email, log in as always, no password reset by mail: an
admin runs `useradmin set-password`), from registration, or from signing in with Google.

Registration rules: no email enumeration (register, resend and forgot-password answer alike; mail
goes out in the background through `mail.Outbox`, so timing does not tell). Turnstile siteverify on
register, resend and forgot-password fails CLOSED (503 captcha_unavailable); a normal login never depends on
Cloudflare. Mailed links carry the token in the URL fragment, never the query. A failed send
is logged without the token or the address. New users are always role `user`.

Client address: `internal/clientip`, one resolver for the login throttles, the register /
resend / forgot limits, Turnstile's `remoteip` and logs. `TRUSTED_PROXIES` (required, may be
empty, CIDRs): empty → the TCP peer, X-Forwarded-For ignored. Otherwise, only when the peer is a
trusted proxy, X-Forwarded-For is walked from the RIGHT skipping trusted addresses; the first
untrusted one is the client (never the leftmost because it is leftmost: the client writes it); a
malformed entry stops the walk at the last trusted address. Production: edge reverse proxy →
tunnel → ingress controller → api, so `TRUSTED_PROXIES` covers the edge proxy's address as the
cluster sees it and the pod CIDR, and the ingress controller must be told to trust the edge
proxy (Traefik: `forwardedHeaders.trustedIPs`) or it drops the header.

Every api response carries `X-Content-Type-Options: nosniff` (media-delivery too). The SPA's
headers (CSP, nosniff, `Referrer-Policy: no-referrer`, Permissions-Policy) live in
`web/security-headers.json` only. HSTS is set by the edge reverse proxy, not here.

GET    /healthz                     → liveness, process only, always 200 if serving
GET    /readyz                      → readiness, 503 + JSON when Postgres is unreachable
GET    /metrics
```

HLS segments are **not** served by `api`. They come from `media-delivery` at
`/media/{token}/{expires}/{movie_id}/{version}/...`, which is a separate Ingress path.

### Signed media URLs

Everything under `/media/` needs a valid token; there are no unsigned exceptions (posters
and thumbnails included). `/healthz` and `/readyz` stay unsigned.

- `token = base64url(md5("{expires}/{movie_id} {MEDIA_SIGNING_KEY}"))`, unpadded, checked by
  nginx `secure_link_md5`. The key goes **last** (key-first MD5 allows length extension). One
  token covers the whole movie folder, and lives in the path, so hls.js's relative rung and
  segment URLs inherit it, and so does every version folder inside library/{id}/. Not bound to
  the client IP
- Missing, malformed or wrong token → 403; expired → 410
- Signed only by `service.MediaSigner`, never by hand: `playlist_url` and `thumbnails_url`
  (6 h), every `poster_url` (24 h, in list, detail and continue-watching). Expiry is rounded up
  to 1/24 of the lifetime so URLs stay stable and the browser cache works. Responses carrying
  them are `Cache-Control: no-store`; media itself is `private`, never kept by a shared cache
- `MEDIA_SIGNING_KEY`: required, no default, ≥ 32 bytes of base64 characters (it is written
  inside a quoted nginx string). Shared by api and media-delivery; nginx gets it through the
  image's `/etc/nginx/templates` envsubst at start (`NGINX_ENVSUBST_FILTER=^MEDIA_SIGNING_KEY$`,
  rendered into a writable `/etc/nginx/conf.d` tmpfs / emptyDir), never baked into the image.
  `docker/nginx-media/05-require-media-signing-key.sh` stops the container on a bad key
- `DEV_MEDIA_URL_TTL` (dev only) shortens every lifetime to see a 410; the api refuses to start
  with it unless `HTTP_ADDR` is loopback. The transcoder reads it too (the local dispatcher
  passes its environment on; a Kubernetes Job never gets it), so the old-version grace shrinks
  with it
- Lifetimes live in `internal/mediattl`; the retranscode grace (`OldVersionGrace`, 25 h) is
  derived from them, never written as its own number, so changing a TTL cannot leave a URL
  pointing at removed files

---

## Conventions

### Go

- `internal/` for everything not meant to be imported externally
- Handlers thin, logic in `service/`, SQL in `store/`
- `handler`, `service` and `store` never touch the filesystem. All file access from request
  or DB values goes through `internal/mediapath`. Enforced by `depguard` + `forbidigo` in
  `.golangci.yml`. Never silence those two with `//nolint`; if a rule blocks you, stop and ask
- Always `context.Context` as first arg
- Wrap errors with `fmt.Errorf("doing x: %w", err)`
- A COMMIT that returns an error may still have succeeded. Before undoing file work that went
  with a transaction, re-read with a new query (`store.JobPublished`, `store.SourceClaimed`):
  committed → keep the files; not committed → undo; cannot tell → keep the files and leave them
  to the watcher (cleanup of non-current versions, reconcile of orphaned sources)
- `log/slog` structured, never `fmt.Println`
- Table-driven tests
- `golangci-lint` must pass

### TypeScript

- Strict mode on
- No `any`
- Types for API responses generated from OpenAPI spec, not hand-written
- Tokens never touch `localStorage` or `sessionStorage`. The access token lives in the `Session`
  object only; the refresh token is an HttpOnly cookie the code never sees
- Refresh only through `session.refresh()` / `session.withAuth()`; a request that replaces the
  session (the password change) goes through `session.reissue()`, under the same lock. Every tab shares one refresh
  cookie and each refresh rotates it, so two tabs refreshing at once would be token reuse and log
  the user out everywhere; the Web Lock in `Session` serialises them. Never call
  `/api/v1/auth/refresh` directly, and never refresh during `pagehide` (the page can die after the
  server rotated the cookie but before the browser stored the new one)
- Signed media URLs expire mid-playback: on a 410 from media-delivery (playlist or segment) the
  player fetches `/movies/{id}/playback` again and reloads at the same position, keeping
  play/pause (the new URL may be a newer version folder); at most once per 60 s, never for a 403
  (`src/lib/mediaExpiry.ts`). A poster that fails to load refetches its page's data once
- The CSP forbids inline scripts, inline `<style>` and eval: no `style="…"` in HTML, React's
  `style={{…}}` is fine (it sets CSSOM properties). A new source goes into
  `web/security-headers.json` with its reason in the PR
- The `pagehide` progress save is one plain `fetch(url, { keepalive: true })` with the current
  token, not openapi-fetch: Firefox drops a `Request` object's keepalive when the tab closes
- The Turnstile script (`src/lib/turnstile.ts`) loads only when a page shows a challenge
  (register, resend, login after 3 failures), never in `index.html`. A token is good once:
  remount the widget after every submit
- "Continue with Google" is a plain link to `/api/v1/auth/google/start` (a navigation, not a
  fetch). `/auth/google/done` only waits for the session's start-up refresh; Connect Google
  navigates to the `url` the api returns. Failures come back as `?google_error=<code>`
- `/verify-email` and `/reset-password` read the token from the fragment through
  `useLinkToken`, which removes it with `history.replaceState` before anything posts it;
  `/verify-email` posts on load, once (StrictMode runs effects twice)

### Containers

- Multi-stage builds, `scratch` or `distroless` final image for Go
- Non-root user, read-only root filesystem
- Listen on `8080` (see hard constraint 9)
- No `latest` tags anywhere — and no floating major tags like `postgres:16` either; pin a
  patch version (`postgres:16.x-alpine`). Kubernetes manifests additionally pin the digest
- Transcoder Jobs are built in code (`internal/dispatch`), not by manifests, so everything in
  their spec comes from the dispatcher's environment: resources (`TRANSCODER_CPU_REQUEST` /
  `_LIMIT`, `TRANSCODER_MEMORY_REQUEST` / `_LIMIT`, default cpu 2 / 4, memory 1Gi / 2Gi,
  parsed at start-up), a dedicated `TRANSCODER_SERVICE_ACCOUNT` with no RBAC (token never
  mounted), and the optional `IMAGE_PULL_SECRET` for the private GHCR images

### Local dev (docker compose)

- Credentials come from `.env` via `env_file:` — the compose file contains no values
- Publish ports on `127.0.0.1` only (`"127.0.0.1:5432:5432"`). Docker-published ports
  bypass `ufw` on Ubuntu, so `5432:5432` would expose Postgres to the whole LAN
- Processes run on the host listen on loopback by default: the api's `HTTP_ADDR`
  defaults to `127.0.0.1:8080`, so `make run` is never reachable from the LAN. Containers
  set `HTTP_ADDR=:8080` explicitly in their manifests; forgetting it fails the probes
  visibly instead of silently exposing anything. Leave `HTTP_ADDR` empty in `.env`
- **Never modify `var/media` or the compose database without asking the user first.** That
  includes moving, deleting or "migrating" files and running any write against the compose
  Postgres (`INSERT`, `UPDATE`, `DELETE`, migrations). Say what you would change and why, and
  wait. Tests never need either: they use `t.TempDir()` and testcontainers

### Docs

- `docs/architecture/` (English, Mermaid that renders on GitHub) is the public architecture
  description: 4+1 views and short ADRs, derived from the code, this file and
  `docs/openapi.yaml`. A PR that changes components, tables, statuses, flows or a decision
  keeps it consistent in the same PR
- Code, comments, CLAUDE.md, `docs/architecture/`, commit messages and PR descriptions are
  English

### Commits

Conventional commits: `feat:`, `fix:`, `chore:`, `docs:`, `refactor:`

---

## Definition of done

**Run these before reporting any task complete. Do not ask the user to run them.**
If any fails, fix it and re-run — do not report success with a failing check.

```bash
make check
```

That target must run, in order:

| Check | Command | Must |
|---|---|---|
| Build | `go build ./...` | compile |
| Vet | `go vet ./...` | pass |
| Lint | `golangci-lint run` | pass |
| Test | `go test ./...` | pass — includes integration tests, so Docker must be running |
| Secrets | `gitleaks detect --source . --no-banner` | find nothing |
| Go vulns | `govulncheck ./...` | no known-exploitable |
| Static security | `gosec -quiet ./...` | no findings |
| Frontend | `make frontend`: web scripts `gen:api:check`, `lint`, `typecheck`, `test` | pass; generated API types must match `docs/openapi.yaml` |

### Secret hygiene, every single change

Before finishing, re-read the diff you just produced and confirm:

- No string literal that is a credential, token, key, password or connection string —
  including in tests, fixtures, comments, compose files and Kubernetes manifests
- Every new secret has a corresponding entry in `.env.example` (name only, empty value)
- Nothing new is written outside the paths `.gitignore` already covers

If a value is needed to make something runnable locally, add the variable name to
`.env.example` and tell the user to set it. Never invent a working default.

### Tooling (already in place)

- `make setup` installs dev tools at **pinned versions**, never `@latest`, then the hook
- Hook source lives in `scripts/hooks/pre-commit`; `scripts/install-hooks.sh` copies it into
  `.git/hooks/` with mode `0755`. `.git/hooks/` is not tracked, so `make setup` restores it
  after a fresh clone
- `go.mod` pins a patched `toolchain` — when `govulncheck` reports a stdlib vulnerability,
  bump the toolchain rather than suppressing the finding
- `.golangci.yml`, `.env.example`

### Testing

- **Unit tests** have no external dependencies and run anywhere
- **Integration tests** (anything touching Postgres) start their own throwaway Postgres
  with `testcontainers-go`. Never point tests at the docker-compose database — then
  `make check` would pass or fail depending on whether the user left compose running
- Table-driven tests
- No real credentials in tests: generate random values at test time (`crypto/rand`)
- Integration tests apply migrations from `migrations/` via `embed.FS` — the same files
  production uses, never a hand-written test schema
- Test output (HLS segments, posters) goes to `t.TempDir()`, never into the repo tree

---

## Git workflow

**Claude Code owns the git mechanics. The user does not run git commands.**

For every task:

1. Start from an up-to-date `main`: `git checkout main && git pull`
2. Create a branch named for the work: `feat/...`, `fix/...`, `chore/...`
3. Commit in small logical steps, conventional commit messages
4. Run `make check` — must pass (see Definition of done)
5. Push, open a PR with `gh pr create`. The PR body says what changed, how it was
   verified, and the exact command the user can run to see it working
6. **Stop and report.** Do not merge yet
7. After the user replies "merge", run `gh pr merge --squash --delete-branch`, then
   `git checkout main && git pull`

Never commit directly to `main`. Never use `--no-verify`. Never force-push `main`.

### Why the user confirms the merge

Not to review code. The user checks **behaviour**: they run the one command from the PR
body and confirm the result. Some tasks can only be accepted that way — a transcoded movie
actually playing in a browser, Postgres actually starting. `make check` cannot see those.

If a merged change turns out wrong, undo it with `git revert`, never by rewriting history.

---

## Commands

```bash
make setup          # install dev tools + git hooks, verify ffmpeg/ffprobe and node/corepack, install web deps (run after a fresh clone)
make check          # the full gate — see Definition of done
make build          # build all binaries into bin/
make run            # api against the compose Postgres (loads .env). Registration locally:
                    # MAIL_BACKEND=log prints the verify link in this log; Cloudflare's test
                    # Turnstile keys always pass; PUBLIC_BASE_URL=http://127.0.0.1:5173.
                    # Google: GOOGLE_CLIENT_ID / _SECRET of a Web client whose authorized
                    # redirect URI is http://127.0.0.1:5173/api/v1/auth/google/callback

# Local dev stack — make targets load .env, so no manual `source .env`
make db-up          # docker compose up -d, waits for the healthcheck
make migrate        # goose up against $DATABASE_URL
make migrate-down   # goose down one step
make db-down        # docker compose down (keeps the volume)

# Frontend: pnpm is pinned in web/package.json and run through corepack from web/
make web-dev                       # http://127.0.0.1:5173, proxying /api -> :8080 and /media -> :8081;
                                   # log in with a user from `make user-add`
make web-build                     # typecheck + build into web/dist
(cd web && corepack pnpm preview)  # serve web/dist on 127.0.0.1:4173 with the production headers
                                   # (CSP from web/security-headers.json), proxying like web-dev
(cd web && corepack pnpm gen:api)  # after changing docs/openapi.yaml; commit web/src/api/schema.ts

# Transcoder, manual run
go run ./cmd/transcoder --input /path/to/movie.mkv --output /tmp/out

# Needs MEDIA_ROOT=./var/media in .env (var/ is gitignored)
# Watcher, one scan: removes the movies an admin deleted (their library/ and sources/ files,
# then the rows), then claims settled files from $MEDIA_ROOT/inbox, queues them as pending
make scan

# Dispatcher, local backend: builds bin/transcoder, then starts the transcoder for every queued
# job (pending → processing → ready). Ctrl-C stops the dispatcher only; a running transcode
# finishes and the next `make dispatch` adopts it. Probes on 127.0.0.1:8090
make dispatch

# Create an api user (prompts for the password twice; never an argument). JWT_SIGNING_KEY
# in .env is required by the api: generate it with `openssl rand -base64 48`
make user-add USER=alice [ROLE=admin]
go run ./cmd/useradmin list            # also: set-password <user>, set-role <user> <user|admin>

# Transcoder in job mode, as the dispatcher runs it (needs a running job for the movie)
bin/transcoder --movie-id <id>

# Put a real movie in the local catalog as ready in one step, skipping the queue (dev only);
# prints the new movie id
make dev-ingest INPUT=~/Videos/movie.mp4 TITLE="My test"

# media-delivery (nginx) on 127.0.0.1:8081: /media/{token}/{expires}/{id}/... serves
# $MEDIA_ROOT/library/{id}/... Needs MEDIA_SIGNING_KEY in .env (openssl rand -base64 48), the
# same value the api uses; after changing it, re-run `make media-up` and restart `make run`
make media-up
make media-check MOVIE=<id> [VERSION=<folder>]  # probes, MIME, cache, CORS, path traversal;
                                                # VERSION defaults to the newest version folder

# Deploy — never kubectl apply directly, Flux owns the cluster
git push                                           # Flux reconciles within ~1 min
flux reconcile kustomization apps --with-source    # force
```


Note the layout: one `go.mod` at the repo root, binaries under `cmd/`, shared code under
`internal/`. Not a module per service. Dev-only programs go under `tools/`, which
`make build` and the images never include.

---

## Build order

Build in this sequence. Each step should be independently runnable.

1. **Postgres schema + migrations** — the data model is the contract
2. **`transcoder` as a standalone CLI** — takes a file, writes HLS. Test with a real movie
   before touching Kubernetes
3. **`api` read-only endpoints** — list and detail, seeded by hand
4. **`media-delivery` nginx config** — prove a browser can play the HLS output
5. **`web` player page** — one movie, hardcoded ID
6. **`web` catalog page**
7. **`watcher` + Job creation** — wire the pipeline together
8. **Auth + watch progress**
9. **Admin endpoints**

Step 2 is the risky unknown — do it early and standalone.

---

## Out of scope

Don't implement these without asking:

- Live transcoding (breaks the architecture)
- DRM
- Live TV / recording
- Mobile native apps
- Federation / ActivityPub
- Multi-tenancy beyond basic user accounts

---

## Reference

- **VLog** (`github.com/filthyrake/vlog`) — similar k8s transcode-worker architecture. Read
  the manifests and worker structure for ideas. Do not fork; the repo self-describes as
  AI-generated.
- **lmdevv/movies** — validates the HLS-remux approach
- hls.js docs for player integration
- TMDB API for metadata enrichment
