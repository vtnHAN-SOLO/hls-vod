-- +goose Up
-- The subtitle tracks of a movie's current version: the WebVTT files the
-- transcoder extracted into {version}/subs/. Replaced in the transaction
-- that publishes a version, so the rows always describe the files that
-- library_path points at. Movies published before this table get their rows
-- from the watcher's one-time backfill (a subs/ folder and no rows).
CREATE TABLE subtitle_tracks (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    movie_id   uuid        NOT NULL REFERENCES movies (id) ON DELETE CASCADE,
    -- relative to library/{id}/, starts with the version: "{version}/subs/0_eng.vtt"
    path       text        NOT NULL,
    -- the language tag as the source stored it (ISO 639-2, "und" when unknown)
    language   text        NOT NULL,
    -- the stream's title, e.g. "SDH" or "Commentary"; NULL when it had none
    label      text,
    -- the order of the subtitle streams in the source
    position   integer     NOT NULL CHECK (position >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (movie_id, path)
);

CREATE TRIGGER subtitle_tracks_set_updated_at
    BEFORE UPDATE ON subtitle_tracks
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE subtitle_tracks;
