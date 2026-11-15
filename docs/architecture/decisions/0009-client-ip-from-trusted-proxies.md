# 0009. Client IP from trusted proxies only

**Status:** Accepted

## Context

Behind an edge reverse proxy and an ingress controller, the TCP peer the api sees is a proxy.
Throttles keyed on that address would throttle everyone together. `X-Forwarded-For`, however,
is partly written by the client: its leftmost entries can be anything.

## Decision

- `TRUSTED_PROXIES` is a CIDR list. It is required with no default, but may be empty.
- Empty: the client is the TCP peer, and `X-Forwarded-For` is ignored.
- Otherwise the header is read only when the peer is a trusted proxy. It is walked **from the
  right**, skipping trusted addresses: the first untrusted one is the client.
  - A malformed entry stops the walk at the last trusted address.
  - The leftmost entry is never taken just because it is leftmost.
- One resolver feeds every use of the client address: the login throttles, the register /
  resend / forgot limits, Turnstile's `remoteip`, and logs.

## Consequences

- A client cannot spoof its address to escape a throttle.
- The deployment must list its proxies exactly, and the ingress controller must itself trust
  the edge proxy, or it drops the header and every request looks like it comes from the edge.
- Because many real users can share one address (CGNAT), the per-IP spraying limit asks for a
  challenge from 10 failures and blocks only from 100.
