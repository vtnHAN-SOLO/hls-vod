-- +goose Up
CREATE TABLE movies (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    title        text        NOT NULL,
    year         integer,
    tmdb_id      integer,
    overview     text,
    runtime_sec  integer,
    -- UNIQUE gives the watcher idempotency: INSERT ... ON CONFLICT DO NOTHING
    source_path  text        NOT NULL UNIQUE,
    library_path text,
    poster_path  text,
    status       text        NOT NULL DEFAULT 'pending'
                             CHECK (status IN ('pending', 'processing', 'ready', 'failed')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX movies_status_idx ON movies (status);

CREATE TRIGGER movies_set_updated_at
    BEFORE UPDATE ON movies
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE movies;
