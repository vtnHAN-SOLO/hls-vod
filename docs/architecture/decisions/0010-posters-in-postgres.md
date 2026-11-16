# 0010. Posters in Postgres

**Status:** Accepted

## Context

Admins upload a portrait poster for each movie (2:3, three JPEG variants of at most about
300 kB each). The bytes have to live somewhere every api replica can read and write. Two rules
are already fixed: the api never touches files (`handler`, `service` and `store` have no file
access, enforced by lint), and all state lives in Postgres ([0002](0002-all-state-in-postgres.md)).
The video library is written by the transcoder and served read-only by media-delivery
([0003](0003-signed-urls-stateless-delivery.md)).

## Decision

- The variants are rows of `movie_posters` (`movie_id`, `width`, `height`, `bytes bytea`),
  replaced whole in one transaction with `movies.poster_hash` (the SHA-256 of the largest
  variant). Lists read the hash and the widths, never the bytes.
- The api serves them itself at `/api/v1/posters/{movie_id}/{width}/{hash}.jpg`, signed with an
  HMAC key derived from `MEDIA_SIGNING_KEY` for this purpose only. The hash is in the URL, so a
  response is cached as immutable until the URL expires.
- Everything else goes through one seam: `internal/poster` processes uploads and defines the
  `Store` interface (`Put`, `Get`, `Delete`); `internal/store` implements it. Nothing else knows
  where the bytes live.

### Alternatives

- **Files on the shared media volume.** The api would have to write files, which it must not
  (and it has no write mount), and the posters would sit outside the database backups, so a
  restore could bring back rows whose images are gone.
- **S3-compatible object storage** (MinIO, or a cloud bucket). The standard answer at scale:
  cheap, CDN-friendly, no load on the database. Here it is one more stateful service to run,
  secure, back up and monitor, for a few hundred kilobytes per movie.

## Consequences

- One backup holds the catalogue and its posters, consistent with each other. Nothing new to
  run.
- The database grows by about 0.5 to 1 MB per movie; a thousand movies is under a gigabyte.
- Every first view of a poster is a database read through the api. Variant URLs are cached
  privately and immutably, and lists never read the bytes, so this stays small; the api
  replicas, not media-delivery, carry it.
- Upload processing runs in the api pod: at most 25 megapixels, checked from the header before
  decoding, and one poster at a time per pod, so memory stays bounded (about 100 MB at worst
  in a 512 MB pod).

### When to switch

Move the bytes to object storage, behind the same `poster.Store`, when posters reach tens of
gigabytes, when poster reads show up in the database's load, or when the images should come
from a CDN. Only the `Store` implementation and the URL signer change; the rows keep the hash.
