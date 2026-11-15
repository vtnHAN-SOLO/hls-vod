# Logical view

What the system is made of, independent of where it runs: the data, the services that own it,
and the states the two central objects go through.

## Domain model

Every table has a random `uuid` primary key (`gen_random_uuid()`), except `watch_progress`, keyed
by (user, movie). Ids appear in URLs and folder names, so they must not be guessable, and a
string that parses as a UUID cannot contain `../`. Status columns are `text` with a `CHECK`, not
Postgres enums. Every table but `oauth_states` and `outgoing_mail` (both never updated) has
`created_at` / `updated_at`, kept current by one shared trigger.

```mermaid
erDiagram
    movies ||--o{ renditions : "has"
    movies ||--o{ transcode_jobs : "has"
    movies ||--o{ watch_progress : "watched in"
    users ||--o{ watch_progress : "watches"
    users ||--o{ refresh_tokens : "sessions"
    users ||--o{ email_tokens : "verify / reset"
    users ||--o| user_identities : "Google"
    users ||--o{ oauth_states : "link in progress"
    refresh_tokens ||--o| refresh_tokens : "replaced_by"

    movies {
        uuid id PK
        text title
        int year
        int tmdb_id
        text overview
        int runtime_sec
        text source_path UK "sources/{id}.{ext}"
        text library_path "library/{id}/{version}"
        text poster_path "relative to library/{id}/"
        timestamptz library_cleanup_after
        text status "pending|processing|ready|failed|deleting"
    }
    renditions {
        uuid id PK
        uuid movie_id FK
        int height "never above the source"
        int bitrate_kbps
        text playlist_path
        bigint size_bytes
    }
    transcode_jobs {
        uuid id PK "also the version folder name"
        uuid movie_id FK "at most one queued or running"
        text status "queued|running|succeeded|failed"
        text purpose "ingest|retranscode"
        text k8s_job_name UK
        int local_pid
        timestamptz started_at
        timestamptz finished_at
        text error_msg
    }
    users {
        uuid id PK
        text username UK "lowercase"
        text password_hash "argon2id, NULL for Google-only"
        text role "user|admin"
        text email UK "lowercase, fixed at registration"
        timestamptz email_verified_at
        timestamptz disabled_at
    }
    refresh_tokens {
        uuid id PK
        uuid user_id FK
        uuid family_id "one login = one family"
        bytea token_hash UK "SHA-256"
        timestamptz expires_at
        timestamptz revoked_at
        uuid replaced_by FK
        text user_agent
    }
    email_tokens {
        uuid id PK
        uuid user_id FK
        text purpose "verify|reset"
        bytea token_hash UK "SHA-256"
        timestamptz expires_at
        timestamptz used_at
    }
    user_identities {
        uuid id PK
        uuid user_id FK "UNIQUE (user_id, provider)"
        text provider "google"
        text subject "UNIQUE (provider, subject)"
        text email_at_link
    }
    oauth_states {
        uuid id PK
        bytea state_hash UK
        text nonce
        text pkce_verifier
        text intent "login|link"
        uuid user_id FK "set iff intent = link"
        timestamptz expires_at
    }
    outgoing_mail {
        uuid id PK
        bytea to_hash "SHA-256 of the address"
        text kind "verify|account_exists|reset|google_linked"
        timestamptz created_at
    }
    watch_progress {
        uuid user_id PK
        uuid movie_id PK
        int position_sec
    }
```

Notes on the model:

- **Versions.** A publish writes `library/{id}/{version}/` with `version` = the job id;
  `library_path` points at the current one and every playback URL is built from it. Older
  movies keep the original layout (`library_path = library/{id}`). See
  [ADR 0006](decisions/0006-versioned-library-folders.md).
- **Sessions are refresh-token families.** A login creates a family; each refresh inserts a new
  row and revokes the old one (`replaced_by`). Presenting a revoked token revokes the whole
  family. There is no sessions table: a session is a family with a live token. See
  [ADR 0004](decisions/0004-refresh-token-families.md).
- **Secrets are stored as hashes only**: passwords as argon2id, every token (refresh, email,
  OAuth state) as SHA-256, mail recipients as SHA-256 of the address.
- **One active job per movie** is enforced by the database: a partial unique index on
  `transcode_jobs (movie_id) WHERE status IN ('queued', 'running')`.
- **Cleanup.** The watcher deletes refresh-token families with no live token for 7 days, email
  tokens used or expired 7 days ago, and expired OAuth states. `outgoing_mail` prunes itself
  (rows older than 2 days) on every send.

## Services

The api is a set of services behind thin HTTP handlers. None of them touches files or has any
Kubernetes permission; file work belongs to the watcher and the transcoder.

```mermaid
flowchart TB
    subgraph api["api"]
        catalog["Catalog<br/>list, search, detail, playback, progress"]
        authsvc["Auth<br/>login, refresh, logout, account, sessions"]
        reg["Registration<br/>register, verify, resend, forgot, reset"]
        google["Google<br/>sign-in and link (OIDC)"]
        admin["Admin<br/>users, jobs, retry, delete, retranscode"]
        signer["MediaSigner<br/>the only place URLs are signed"]
    end
    subgraph ingest["ingest (outside the api)"]
        watcher["watcher<br/>claim, queue, purge, clean"]
        dispatcher["dispatcher<br/>claim jobs, start, reconcile"]
        transcoder["transcoder<br/>ffprobe, remux / encode, publish"]
    end
    catalog --> signer
    reg --> authsvc
    google --> authsvc
    pg[("Postgres")]
    catalog & authsvc & reg & google & admin --> pg
    watcher & dispatcher & transcoder --> pg
```

| Service | Owns | Key rules |
|---|---|---|
| **Catalog** | movies as users see them, playback, watch progress | users see `ready` movies only; playback returns signed URLs (6 h) and the saved position (null when ≥ 95 % watched); progress is always the token's user, never the body's |
| **Auth** | login, refresh, logout, the caller's account and sessions | access token 15 min (HS256, carries `sid` = family), refresh cookie 30 days, rotating; per-username+IP and per-IP throttles; endpoints that manage sessions or credentials re-check in the database that the caller's family is still live |
| **Registration / recovery** | sign-up, email verification, forgot / reset password | the same answer whether or not an address has an account; mail sent in the background; Turnstile fails closed; tokens single use, carried in the URL fragment |
| **Google sign-in** | sign-in and linking with Google (OIDC code flow + PKCE) | the Google account is identified by `sub` only; a verified local account with the same email is linked, an unverified one is replaced. See [ADR 0008](decisions/0008-google-identity-by-sub.md) |
| **Admin** | users (disable, role), jobs, movies (retry, delete, retranscode) | the role is checked in the database on every call; never zero active admins; every action logged |
| **Ingest** | the media pipeline: watcher → dispatcher → transcoder | pre-transcoded HLS, remux when the codec allows, never upscale. See [ADR 0001](decisions/0001-pre-transcoded-hls.md) |

## Movie states

```mermaid
stateDiagram-v2
    [*] --> pending: watcher claims an inbox file (job queued)
    pending --> processing: transcoder starts (ingest)
    processing --> ready: published in one transaction
    processing --> failed: transcoder or dispatcher records the failure
    failed --> pending: admin retry (new queued job)
    ready --> ready: retranscode publishes a new version
    ready --> deleting: admin delete (no active job)
    failed --> deleting: admin delete
    deleting --> [*]: watcher removes files, then the row
```

A `ready` movie never leaves `ready` because of a retranscode: it keeps playing its current
version, and switches to the new one in the transaction that publishes it. A `deleting` movie is
hidden from every non-admin endpoint at once; the watcher removes `library/{id}/`, then the
source, then the row (renditions, jobs and progress cascade).

## Job states

```mermaid
stateDiagram-v2
    [*] --> queued: watcher (ingest), admin retry or retranscode
    queued --> running: dispatcher claims it (≤ MAX_CONCURRENT running)
    running --> succeeded: transcoder publishes
    running --> failed: transcoder error, or dispatcher reconcile
    succeeded --> [*]
    failed --> [*]
```

The dispatcher marks a running job failed when its transcoder exited without recording a
result, is gone, or ran past the 6 h deadline. Every write is guarded on the job still being
`running`, so whichever reports first wins and the other is a no-op.
