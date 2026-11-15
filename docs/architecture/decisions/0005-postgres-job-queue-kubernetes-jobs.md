# 0005. Job queue in Postgres, transcoders as Kubernetes Jobs

**Status:** Accepted

## Context

Transcodes are long and heavy (hours, several cores), rare, and must survive restarts of
everything around them. A job row is needed anyway, to show status and errors.

## Decision

- `transcode_jobs` is the queue. A partial unique index allows at most one queued or running
  job per movie.
- The **dispatcher** claims the oldest queued job under an advisory lock, with `FOR UPDATE
  SKIP LOCKED`, while fewer than `MAX_CONCURRENT` are running. It starts one Kubernetes Job per
  claimed job, and reconciles running rows against reality on every tick.
- The **transcoder** records its own success or failure. The dispatcher fills in only for one
  that exited without a result, is gone, or ran past the 6 h deadline.
- The dispatcher is the only component allowed to create Jobs. The api has no Kubernetes
  permission at all.

## Consequences

- No message broker to run; claims are exact across dispatcher replicas.
- A Job's resources, deadline and security context come from code and the dispatcher's
  environment, not from manifests.
- The same loop has a local backend (a child process), so the whole pipeline runs on a
  developer machine.
