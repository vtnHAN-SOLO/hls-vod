-- +goose Up
-- The browser that started a login, for the account page's sessions list
-- ("Firefox on Linux"). Set at login, truncated to 200 characters by the api,
-- and copied onto every rotated token, so the newest row of a family always
-- carries it. NULL when the client sent none, and for rows from before this
-- migration.
ALTER TABLE refresh_tokens
    ADD COLUMN user_agent text CHECK (char_length(user_agent) <= 200);

-- +goose Down
ALTER TABLE refresh_tokens DROP COLUMN user_agent;
