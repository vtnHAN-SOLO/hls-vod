# 0007. Email fixed at registration

**Status:** Accepted

## Context

The email address is the recovery channel (password reset) and the key for linking a Google
account. A changeable email would let anyone holding a stolen session redirect recovery to
themselves.

## Decision

The email is set once, at registration, and verified before the first login. There is no email
change. Accounts created by the `useradmin` tool have no email (and no reset by mail: an admin
sets the password).

## Consequences

- A stolen session cannot take over recovery or Google linking.
- A user who loses access to their mailbox needs an admin.
- Registration never reveals whether an address has an account: the existing owner gets an
  "account exists" mail instead.
