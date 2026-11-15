-- +goose Up
-- An account's email is fixed at registration, like its username: it is
-- the account's recovery channel (password reset) and the key for linking
-- Google sign-in. 00010 reserved an email-change flow that is
-- not built; narrow the contract to what exists.
DELETE FROM email_tokens WHERE purpose = 'change_email';
DELETE FROM outgoing_mail WHERE kind = 'change_email';

-- Dropping the column also drops both CHECKs that mention it.
ALTER TABLE email_tokens DROP COLUMN new_email;
ALTER TABLE email_tokens DROP CONSTRAINT email_tokens_purpose_check;
ALTER TABLE email_tokens ADD CONSTRAINT email_tokens_purpose_check CHECK (purpose IN ('verify', 'reset'));

ALTER TABLE outgoing_mail DROP CONSTRAINT outgoing_mail_kind_check;
ALTER TABLE outgoing_mail ADD CONSTRAINT outgoing_mail_kind_check
    CHECK (kind IN ('verify', 'account_exists', 'reset'));

-- +goose Down
ALTER TABLE outgoing_mail DROP CONSTRAINT outgoing_mail_kind_check;
ALTER TABLE outgoing_mail ADD CONSTRAINT outgoing_mail_kind_check
    CHECK (kind IN ('verify', 'account_exists', 'reset', 'change_email'));

ALTER TABLE email_tokens DROP CONSTRAINT email_tokens_purpose_check;
ALTER TABLE email_tokens ADD CONSTRAINT email_tokens_purpose_check
    CHECK (purpose IN ('verify', 'reset', 'change_email'));
ALTER TABLE email_tokens ADD COLUMN new_email text CHECK (new_email = lower(new_email));
ALTER TABLE email_tokens ADD CONSTRAINT email_tokens_check
    CHECK ((purpose = 'change_email') = (new_email IS NOT NULL));
