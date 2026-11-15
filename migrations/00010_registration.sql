-- +goose Up
-- Open registration. Accounts created with cmd/useradmin keep email
-- NULL and log in as before; an account with an email logs in only once
-- email_verified_at is set.
ALTER TABLE users
    ADD COLUMN email             text UNIQUE CHECK (email = lower(email)),
    ADD COLUMN email_verified_at timestamptz;

-- Single-use links sent by mail. Only the SHA-256 of the token is stored;
-- a token is marked used in the transaction that applies it.
CREATE TABLE email_tokens (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    purpose    text        NOT NULL CHECK (purpose IN ('verify', 'reset', 'change_email')),
    token_hash bytea       NOT NULL UNIQUE,
    -- change_email only: the address the link confirms
    new_email  text        CHECK (new_email = lower(new_email)),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((purpose = 'change_email') = (new_email IS NOT NULL))
);

CREATE INDEX email_tokens_user_id_idx ON email_tokens (user_id);

CREATE TRIGGER email_tokens_set_updated_at
    BEFORE UPDATE ON email_tokens
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- One row per mail the api decided to send, written before sending. Backs
-- the per-address limit and the daily cap across api replicas. Append-only
-- (never updated), so no updated_at. The address is kept only as the
-- SHA-256 of its lowercase form.
CREATE TABLE outgoing_mail (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    to_hash    bytea       NOT NULL,
    kind       text        NOT NULL CHECK (kind IN ('verify', 'account_exists', 'reset', 'change_email')),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX outgoing_mail_to_hash_created_at_idx ON outgoing_mail (to_hash, created_at);
CREATE INDEX outgoing_mail_created_at_idx ON outgoing_mail (created_at);

-- +goose Down
DROP TABLE outgoing_mail;
DROP TABLE email_tokens;
ALTER TABLE users
    DROP COLUMN email_verified_at,
    DROP COLUMN email;
