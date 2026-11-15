# 0003. Signed URLs on a stateless delivery tier

**Status:** Accepted

## Context

Media must not be public, but asking the api for every segment would make the api part of the
playback path. It would also make the delivery tier depend on the database and on sessions.

## Decision

The api signs URLs; nginx checks them itself with the shared `MEDIA_SIGNING_KEY`
(`secure_link_md5`), with no call to anything.

- `token = base64url(md5("{expires}/{movie_id} {key}"))`, with the key **last** (key-first MD5
  allows length extension). One token covers the whole `library/{id}/` folder.
- The token lives in the **path**, `/media/{token}/{expires}/{id}/…`, so the relative playlist
  and segment URLs that hls.js resolves inherit it (a query string would be dropped).
- Lifetimes: 6 h for playback and thumbnails, 24 h for posters. Expiry is rounded up, so URLs
  stay stable and cacheable in the browser. Media responses are `private`.
- A missing, malformed or wrong token is 403; an expired one is 410, and the player renews the
  URLs on a 410.

## Consequences

- media-delivery is stateless and read-only, and scales horizontally.
- A valid URL is a capability: anyone holding it can watch that movie until it expires. It is
  deliberately not bound to the client IP, which changes on mobile networks.
- MD5 is what nginx's module offers. It is acceptable because the client does not choose the
  signed message and tokens are short-lived.
- Rotating the key kills every URL issued so far.
