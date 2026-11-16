-- +goose Up
-- Posters and publishing.
--
-- backdrop_path: the landscape video frame the transcoder writes (it was
-- poster_path). The poster is now a portrait 2:3 image an admin uploads,
-- stored in movie_posters.
ALTER TABLE movies RENAME COLUMN poster_path TO backdrop_path;

-- published_at: visible to viewers. Existing movies start unpublished.
-- poster_hash: SHA-256 of the largest poster variant, set and cleared in the
-- same transaction as the movie_posters rows, so lists know whether a poster
-- exists (and its URLs) without reading image data.
ALTER TABLE movies
    ADD COLUMN published_at timestamptz,
    ADD COLUMN poster_hash bytea CHECK (poster_hash IS NULL OR length(poster_hash) = 32);

-- The publishing rule, enforced by the database itself: only a ready movie
-- with a poster and a non-empty overview can be published.
ALTER TABLE movies ADD CONSTRAINT movies_published_check CHECK (
    published_at IS NULL OR (
        status = 'ready' AND poster_hash IS NOT NULL
        AND overview IS NOT NULL AND length(trim(overview)) > 0
    )
);

-- One row per width (300, 600, 1000, never above the cropped upload).
-- Never selected by list queries: the bytes are read one variant at a time.
CREATE TABLE movie_posters (
    movie_id   uuid        NOT NULL REFERENCES movies (id) ON DELETE CASCADE,
    width      integer     NOT NULL CHECK (width > 0),
    height     integer     NOT NULL CHECK (height > 0),
    bytes      bytea       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (movie_id, width)
);

CREATE TRIGGER movie_posters_set_updated_at
    BEFORE UPDATE ON movie_posters
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE movie_posters;
ALTER TABLE movies DROP CONSTRAINT movies_published_check;
ALTER TABLE movies DROP COLUMN poster_hash, DROP COLUMN published_at;
ALTER TABLE movies RENAME COLUMN backdrop_path TO poster_path;
