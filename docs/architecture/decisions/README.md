# Architecture decision records

Short records of the decisions that shape the system: the context, the decision, and what it
costs. A decision is changed by a new ADR that supersedes the old one, never by editing history.

| # | Decision | Status |
|---|---|---|
| [0001](0001-pre-transcoded-hls.md) | Pre-transcoded HLS, never live transcoding | Accepted |
| [0002](0002-all-state-in-postgres.md) | All state in Postgres | Accepted |
| [0003](0003-signed-urls-stateless-delivery.md) | Signed URLs on a stateless delivery tier | Accepted |
| [0004](0004-refresh-token-families.md) | Refresh-token families with reuse detection | Accepted |
| [0005](0005-postgres-job-queue-kubernetes-jobs.md) | Job queue in Postgres, transcoders as Kubernetes Jobs | Accepted |
| [0006](0006-versioned-library-folders.md) | Versioned library folders | Accepted |
| [0007](0007-email-fixed-at-registration.md) | Email fixed at registration | Accepted |
| [0008](0008-google-identity-by-sub.md) | Google identity by `sub` | Accepted |
| [0009](0009-client-ip-from-trusted-proxies.md) | Client IP from trusted proxies only | Accepted |
