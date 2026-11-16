# Scenarios (+1)

The flows that matter most, end to end. Each one touches several views: the logical states, the
process guarantees, and the physical tiers.

## Register and verify the email

```mermaid
sequenceDiagram
    actor U as Visitor
    participant W as web
    participant A as api
    participant C as Turnstile
    participant P as Postgres
    participant M as Resend
    U->>W: /register: email, username, password, challenge
    W->>A: POST /api/v1/auth/register
    A->>C: siteverify (5 s timeout, fail closed: 503)
    A->>A: argon2id hash (also when the address is taken)
    A->>P: one tx: user (unverified) + verify token (SHA-256, 24 h)
    A-->>W: 202 (the same answer whether or not the address had an account)
    A-)M: background outbox: link /verify-email#token=…
    Note over A,M: an address that already has an account gets an "account exists" mail instead
    U->>W: opens the link
    W->>W: read the token from the fragment, remove it from the address bar
    W->>A: POST /api/v1/auth/verify-email {token}
    A->>P: one tx: token used, email_verified_at, new session family
    A-->>W: access token + refresh cookie: logged in
```

- **No enumeration.** Register, resend and forgot-password give the same answer for every
  address, and do the same work. Mail goes out in the background, so timing tells nothing.
- **Rate limits.** At most 3 mails per address per hour, and a daily cap across replicas
  (recorded before sending, under an advisory lock). 10 attempts per hour per client IP.
- **Email cannot change.** It is fixed at registration, because it is the recovery channel and
  the key for linking Google. See [ADR 0007](decisions/0007-email-fixed-at-registration.md).
- **Squatting is bounded.** An unverified account with no live verify token is deleted by the
  next registration that wants its email or username.

## Sign in with Google

```mermaid
sequenceDiagram
    actor U as User
    participant W as web
    participant A as api
    participant G as Google
    participant P as Postgres
    U->>W: "Continue with Google" (a plain link)
    W->>A: GET /api/v1/auth/google/start
    A->>P: oauth_states row (state hash, nonce, PKCE verifier, 10 min)
    A-->>U: 302 to Google + HttpOnly state cookie (SameSite=Lax)
    U->>G: consent
    G-->>U: 302 /api/v1/auth/google/callback?code&state
    U->>A: callback (state parameter = cookie = an unexpired row, deleted now)
    A->>G: exchange the code with the PKCE verifier
    A->>A: verify the ID token: JWKS signature, iss, aud, exp, nonce, email_verified
    alt sub already linked
        A->>P: that user
    else a verified account has this email
        A->>P: link it (and mail the owner)
    else an unverified account has it
        A->>P: delete it, then create a new user
    else nobody
        A->>P: new user (role user, verified, no password)
    end
    A-->>U: refresh cookie + 302 /auth/google/done
    W->>A: POST /api/v1/auth/refresh: session in the SPA, never a token in a URL
```

The Google account is identified by its `sub` only, never its email. A later change of the Google
address changes nothing locally. See [ADR 0008](decisions/0008-google-identity-by-sub.md).

## Play a movie, across an expired URL

```mermaid
sequenceDiagram
    actor U as Viewer
    participant W as web (hls.js)
    participant A as api
    participant N as media-delivery
    U->>W: open /watch/{id}
    W->>A: GET /api/v1/movies/{id}/playback
    A->>A: MediaSigner: token = base64url(md5("{expires}/{id} {key}")), 6 h
    A-->>W: /media/{token}/{expires}/{id}/{version}/master.m3u8 + resume_sec + subtitle URLs
    W->>N: master.m3u8, then rung playlists and segments (relative URLs keep the token)
    N->>N: secure_link_md5 with the shared key, no call to the api
    N-->>W: 200, Cache-Control private
    W->>A: POST /api/v1/progress every 10 s while playing, and on pause
    Note over W,N: hours later the URL expires
    W->>N: GET …/seg_0470.ts
    N-->>W: 410
    W->>A: GET /playback again (at most once per 60 s)
    A-->>W: fresh URLs (maybe a newer version folder)
    W->>N: new master.m3u8, then the segment at the same position
    W-->>U: playback continues where it was, playing if it was playing
```

- **Subtitles** are `<track>` elements on the same origin, signed with the playlist's token; only
  the one chosen is enabled, so a movie with dozens of tracks fetches one.
- **Feedback.** Skips (buttons, ←/→, a double tap on a side third) show a bubble that adds up
  within 600 ms ("5s", "10s"); ↑/↓ show the volume; the viewer's volume, speed and subtitle
  choice are remembered in the browser.
- **403 vs 410.** A wrong, tampered or malformed token is 403 and is never retried; only 410
  (a valid token that has expired) triggers a renewal.
- **One token per movie folder.** The token covers `library/{id}/`, so it also covers every
  version folder in it, and hls.js's relative URLs inherit it from the path.
- **Images.** A poster or backdrop that fails to load makes its page refetch its data once,
  to pick up fresh signed URLs.
- See [ADR 0003](decisions/0003-signed-urls-stateless-delivery.md).

## Upload a poster and publish

```mermaid
sequenceDiagram
    actor Ad as Admin
    participant W as web
    participant A as api
    participant P as Postgres
    actor V as Viewer
    Ad->>W: Edit, drop an image
    W->>W: checks type, 10 MB, 300x450, previews the 2:3 crop
    W->>A: PUT /api/v1/admin/movies/{id}/poster (the bytes, with progress)
    A->>P: admin, live session, not disabled
    A->>A: type from the bytes, size from the header (max 25 MP)
    A->>A: one at a time: crop 2:3, resize 300/600/1000, JPEG 85
    A->>P: one tx: replace movie_posters rows, set poster_hash
    A-->>W: the movie with its poster URLs
    Ad->>W: write the description, Save and publish
    W->>A: PATCH the overview, then POST /publish
    A->>P: ready, poster, overview? set published_at
    Note over A,P: the CHECK on movies refuses an incomplete publish anyway
    V->>W: Home, Browse, the detail page
    W->>A: GET /api/v1/movies ...
    A-->>W: published movies only, poster URLs signed for 24 h
    W->>A: GET /api/v1/posters/{id}/{width}/{hash}.jpg?exp&sig
    A-->>W: JPEG, private, immutable until exp
```

- **Refusals.** Publishing says why it cannot (`not_ready`, `missing_poster`,
  `missing_overview`); the admin page shows the same reasons as badges before anyone clicks. A
  published movie's poster cannot be removed nor its overview cleared: unpublish first.
- **A new upload** changes the hash and so every poster URL; an old URL then answers 404.
- **Signatures.** A wrong or missing signature is 403, an expired one 410, as on media-delivery.

## Open the home page

```mermaid
sequenceDiagram
    actor U as Viewer
    participant W as web
    participant A as api
    participant P as Postgres
    participant N as media-delivery
    U->>W: open /
    par one request each, in parallel
        W->>A: GET /api/v1/movies/featured
        A->>P: featured published movies, newest featured first
        Note over A,P: none featured: the 5 newest published movies instead
    and
        W->>A: GET /api/v1/continue-watching
    and
        W->>A: GET /api/v1/movies/popular?window=week
        A->>P: progress rows saved in the last 7 days, per movie, published only
    and
        W->>A: GET /api/v1/movies?sort=added_desc
    end
    A-->>W: movies with top_height, a signed backdrop URL and signed poster variant URLs (24 h)
    W->>N: backdrops (the spotlight, continue watching)
    W->>A: poster variants (the cards, the spotlight), the size srcset picks
    A->>P: one variant's bytes, while the hash in the URL is the movie's
    W-->>U: spotlight (advances every 8 s, never with reduced motion), rows, ranking
```

- **Resume in the spotlight.** The Play button says "Resume 23:10" when the Continue Watching data
  has a position for that movie; no extra request.
- **Most popular counts people, not plays.** One row per (user, movie) in `watch_progress`, so a
  viewer who watches every night counts once. Tabs switch the window: week, month, all time.

## Ingest a new movie

```mermaid
sequenceDiagram
    actor O as Operator
    participant Wa as watcher
    participant P as Postgres
    participant D as dispatcher
    participant K as Kubernetes
    participant T as transcoder Job
    participant F as media volume
    O->>F: copy "My Movie (2020).mkv" into inbox/
    Wa->>F: unchanged for 2 min? rename to sources/{id}.mkv (the rename is the lock)
    Wa->>P: same tx: movie pending + job queued (shared advisory lock)
    D->>P: claim the oldest queued job (advisory lock + SKIP LOCKED, ≤ MAX_CONCURRENT)
    D->>K: create Job: transcoder --movie-id {id}
    K->>T: start
    T->>P: job running, movie processing
    T->>T: ffprobe, then the ladder: top rung at source height (remux if H.264/AAC, else encode), lower rungs encoded
    T->>F: write library/{id}/.{version}.partial-*, then one rename to library/{id}/{version}/
    T->>P: one tx: renditions + movie ready + job succeeded
    D->>P: reconcile: Job exited without a result, gone, or past 6 h → failed
```

The top rung never exceeds the source resolution (a 720p source gets a 720p top rung). Posters,
thumbnail sprites and subtitles are produced in the same run. See
[ADR 0001](decisions/0001-pre-transcoded-hls.md) and
[ADR 0005](decisions/0005-postgres-job-queue-kubernetes-jobs.md).

## Retranscode a ready movie

```mermaid
sequenceDiagram
    actor Ad as Admin
    participant A as api
    participant P as Postgres
    participant T as transcoder
    participant F as media volume
    participant Wa as watcher
    Ad->>A: POST /api/v1/admin/movies/{id}/retranscode
    A->>P: ready and no active job? job queued (purpose retranscode) → 202
    Note over A,P: the movie stays ready and keeps playing its current version
    T->>F: free space ≥ 1.5 × the current version? (else failed, nothing written)
    T->>F: write library/{id}/{new version}/
    T->>P: one tx: library_path, poster, renditions, runtime → new version, cleanup_after = now + 25 h
    Note over T,P: new playback requests get the new version from this commit on
    Wa->>P: later: cleanup due and no active job? lock the movie row
    Wa->>F: remove every entry of library/{id}/ except the current version
    Wa->>P: library_cleanup_after = NULL
```

The 25 h grace is derived in code from the longest signed URL lifetime (posters: 24 h, rounded
up), so no URL issued for the old version outlives its files. A failed retranscode leaves the
movie `ready` on its old version. See
[ADR 0006](decisions/0006-versioned-library-folders.md).

## Admin deletes a movie

```mermaid
sequenceDiagram
    actor Ad as Admin
    participant A as api
    participant P as Postgres
    participant Wa as watcher
    participant F as media volume
    Ad->>A: DELETE /api/v1/admin/movies/{id}
    A->>P: check the role in the database (not the token's claim)
    A->>P: lock the movie row: a job queued or running? 409 job_active
    A->>P: status deleting → 202
    Note over A,P: hidden at once from every non-admin endpoint, playback refused
    Wa->>P: next run, first step: movies in status deleting
    Wa->>F: remove library/{id}/ (every version), then sources/{id}.{ext}
    Wa->>P: DELETE the row (renditions, jobs, progress cascade)
```

The row goes last. While it exists, reconcile treats the source as claimed and never sends it
back to the inbox. The api never touches the files itself.
