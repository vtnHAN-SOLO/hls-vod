-- +goose Up
CREATE TABLE watch_progress (
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    movie_id     uuid        NOT NULL REFERENCES movies (id) ON DELETE CASCADE,
    position_sec integer     NOT NULL CHECK (position_sec >= 0),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    -- no surrogate id; progress saves upsert on this key
    PRIMARY KEY (user_id, movie_id)
);

-- continue-watching: a user's most recently touched movies
CREATE INDEX watch_progress_user_recent_idx ON watch_progress (user_id, updated_at DESC);

-- the PK leads with user_id, so ON DELETE CASCADE from movies needs its own index
CREATE INDEX watch_progress_movie_id_idx ON watch_progress (movie_id);

CREATE TRIGGER watch_progress_set_updated_at
    BEFORE UPDATE ON watch_progress
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE watch_progress;
