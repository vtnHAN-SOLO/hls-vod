# 0004. Refresh-token families with reuse detection

**Status:** Accepted

## Context

Short-lived access tokens need a refresh mechanism. A long-lived refresh token that never
changes is valuable to steal, and a theft would go unnoticed.

## Decision

- The **access token** lives 15 min (HS256), is kept in memory only, and carries `sid`, the
  session's family id.
- The **refresh token** is an opaque random value in an HttpOnly, SameSite=Strict cookie. The
  database stores only its SHA-256.
- Every refresh **rotates** it: the old row is revoked and points at its replacement, and the
  new row joins the same **family** (one family per login).
- **Presenting a revoked token revokes the whole family**: either the user or a thief is
  replaying an old token, and both lose the session.
- A session is a family with a live token. There is no sessions table: the account page lists
  families, and revoking one ends that device.

## Consequences

- A stolen refresh token works at most until the real client refreshes, and then the theft is
  detected.
- Tabs share one cookie, so the SPA must never refresh in two tabs at once. It serialises
  refreshes with a Web Lock.
- Revocation stops refresh only: an access token already issued works until it expires
  (≤ 15 min). Endpoints that manage sessions and credentials check in the database that the
  caller's family is still live.
- Old families are pruned 7 days after their last live token.
