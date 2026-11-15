-- +goose Up
-- Sign in with Google. A Google-only account has no password.
ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;

-- A Google account is identified only by its subject ("sub"), never by its
-- email: the local email stays as registered, and a later change of the
-- Google address is ignored. email_at_link is shown on /account only.
CREATE TABLE user_identities (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    provider      text        NOT NULL CHECK (provider = 'google'),
    subject       text        NOT NULL,
    email_at_link text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    -- One user per Google account, one Google account per user.
    CONSTRAINT user_identities_provider_subject_key UNIQUE (provider, subject),
    CONSTRAINT user_identities_user_id_provider_key UNIQUE (user_id, provider)
);

CREATE TRIGGER user_identities_set_updated_at
    BEFORE UPDATE ON user_identities
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- One row per started Google sign-in or link, single use: deleted when the
-- callback consumes it; expired rows are deleted on the next start. Never
-- updated, so no updated_at. Only the SHA-256 of the state (the value in
-- the browser's cookie) is kept; nonce and PKCE verifier never leave the
-- api and live 10 minutes.
CREATE TABLE oauth_states (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    state_hash    bytea       NOT NULL UNIQUE,
    nonce         text        NOT NULL,
    pkce_verifier text        NOT NULL,
    intent        text        NOT NULL CHECK (intent IN ('login', 'link')),
    user_id       uuid        REFERENCES users (id) ON DELETE CASCADE,
    expires_at    timestamptz NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    CHECK ((intent = 'link') = (user_id IS NOT NULL))
);

CREATE INDEX oauth_states_expires_at_idx ON oauth_states (expires_at);

ALTER TABLE outgoing_mail DROP CONSTRAINT outgoing_mail_kind_check;
ALTER TABLE outgoing_mail ADD CONSTRAINT outgoing_mail_kind_check
    CHECK (kind IN ('verify', 'account_exists', 'reset', 'google_linked'));

-- +goose Down
DELETE FROM outgoing_mail WHERE kind = 'google_linked';
ALTER TABLE outgoing_mail DROP CONSTRAINT outgoing_mail_kind_check;
ALTER TABLE outgoing_mail ADD CONSTRAINT outgoing_mail_kind_check
    CHECK (kind IN ('verify', 'account_exists', 'reset'));
DROP TABLE oauth_states;
DROP TABLE user_identities;
-- Google-only accounts cannot exist without the column being NULL-able:
-- going down deletes them (and, by cascade, their sessions and progress).
DELETE FROM users WHERE password_hash IS NULL;
ALTER TABLE users ALTER COLUMN password_hash SET NOT NULL;
