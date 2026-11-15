-- +goose Up
-- The admin area.
--
-- A disabled account cannot log in, refresh, sign in with Google or verify
-- its email; disabling it revokes every session in the same transaction.
ALTER TABLE users ADD COLUMN disabled_at timestamptz;

-- deleting: an admin deleted the movie. It is hidden from everyone but
-- admins at once; the watcher's next run removes library/{id}/ and the
-- source file, then the row.
ALTER TABLE movies DROP CONSTRAINT movies_status_check;
ALTER TABLE movies ADD CONSTRAINT movies_status_check
    CHECK (status IN ('pending', 'processing', 'ready', 'failed', 'deleting'));

-- +goose Down
-- A movie still waiting for the watcher goes back to failed, not away: its
-- files are still there, and an admin can delete it again once 00013 is back.
UPDATE movies SET status = 'failed' WHERE status = 'deleting';
ALTER TABLE movies DROP CONSTRAINT movies_status_check;
ALTER TABLE movies ADD CONSTRAINT movies_status_check
    CHECK (status IN ('pending', 'processing', 'ready', 'failed'));

-- Disabled accounts become usable again: their sessions were revoked when
-- they were disabled, but they can log in once more.
ALTER TABLE users DROP COLUMN disabled_at;
