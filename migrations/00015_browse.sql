-- +goose Up
-- Browsing: featured movies and the most-popular ranking.
--
-- featured_at: set when an admin features the movie, NULL otherwise. The
-- home page's spotlight shows featured ready movies, newest featured first;
-- with none it falls back to the newest ready movies.
ALTER TABLE movies ADD COLUMN featured_at timestamptz;
CREATE INDEX movies_featured_at_idx ON movies (featured_at DESC) WHERE featured_at IS NOT NULL;

-- Most popular in a window: the progress rows updated since a time, grouped
-- by movie. The primary key leads with user_id, so the window needs its own
-- index; movie_id in it makes the grouping index-only.
CREATE INDEX watch_progress_updated_idx ON watch_progress (updated_at, movie_id);

-- +goose Down
DROP INDEX watch_progress_updated_idx;
DROP INDEX movies_featured_at_idx;
ALTER TABLE movies DROP COLUMN featured_at;
