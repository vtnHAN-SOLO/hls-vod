# 0002. All state in Postgres

**Status:** Accepted

## Context

Media servers that keep state in an embedded database such as SQLite are pinned to a single
replica: the file cannot be shared between pods. Sessions, job queues and caches held in memory
have the same effect.

## Decision

Every piece of state lives in Postgres: the catalog, users and sessions, watch progress, the
job queue, OAuth states and the mail log. The services hold none. The only in-memory state is
per-process and disposable: login throttles, and the argon2id concurrency limit.

## Consequences

- web, api, media-delivery and the dispatcher can be scaled and restarted freely; a lost pod
  loses nothing.
- Postgres is the one stateful component, and the one to back up.
- Coordination (claims, limits, last-admin checks) is done with transactions, row locks and
  advisory locks, which the tests can exercise against a real database.
- Throttles being per process means an attacker gets the limit once per api replica; that is
  accepted at this scale.
