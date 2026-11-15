-- +goose Up
CREATE TABLE renditions (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    movie_id      uuid        NOT NULL REFERENCES movies (id) ON DELETE CASCADE,
    -- 720, 480 ... never above the source height (enforced by the transcoder)
    height        integer     NOT NULL,
    bitrate_kbps  integer,
    playlist_path text        NOT NULL,
    size_bytes    bigint,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    -- also serves as the index for lookups by movie_id
    UNIQUE (movie_id, height)
);

CREATE TRIGGER renditions_set_updated_at
    BEFORE UPDATE ON renditions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE renditions;
