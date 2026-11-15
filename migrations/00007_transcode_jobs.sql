-- +goose Up
CREATE TABLE transcode_jobs (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    movie_id     uuid        NOT NULL REFERENCES movies (id) ON DELETE CASCADE,
    status       text        NOT NULL DEFAULT 'queued'
                             CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
    k8s_job_name text        UNIQUE, -- NULL until the Job is created
    started_at   timestamptz,
    finished_at  timestamptz,
    error_msg    text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX transcode_jobs_status_idx ON transcode_jobs (status);

-- the partial index below only covers active jobs; ON DELETE CASCADE from
-- movies and per-movie job history need every row
CREATE INDEX transcode_jobs_movie_id_idx ON transcode_jobs (movie_id);

-- The database itself refuses a second active job for the same movie.
CREATE UNIQUE INDEX transcode_jobs_one_active_per_movie
    ON transcode_jobs (movie_id)
    WHERE status IN ('queued', 'running');

CREATE TRIGGER transcode_jobs_set_updated_at
    BEFORE UPDATE ON transcode_jobs
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- +goose Down
DROP TABLE transcode_jobs;
