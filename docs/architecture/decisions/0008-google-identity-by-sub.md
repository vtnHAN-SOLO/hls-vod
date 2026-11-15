# 0008. Google identity by `sub`

**Status:** Accepted

## Context

A Google account's email address can change, and the same address could later belong to a
different Google account. Matching on email alone would let a changed or recycled address take
over a local account.

## Decision

- A linked Google account is identified only by its **`sub`** (`UNIQUE (provider, subject)`).
  The email seen at link time is kept for display only.
- Signing in with an unlinked `sub` and a verified Google email:
  - a **verified** local account with that email is linked, and its owner is mailed;
  - an **unverified** one is deleted (its owner never proved the address) and a new account is
    created;
  - otherwise a new user is created, if registration is open.
- Linking from the account page needs the current password, so a stolen session cannot add the
  attacker's Google account as a backdoor.

## Consequences

- A later change of the Google address changes nothing locally.
- One Google account per user and one user per Google account.
- Removing Google from an account with no password is refused: it would be the last way in.
