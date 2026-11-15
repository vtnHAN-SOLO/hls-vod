-- +goose Up
-- The local dispatcher backend runs the transcoder as a child process on the
-- same machine; its pid is how a restarted dispatcher finds a running job
-- again instead of starting a duplicate. NULL for Kubernetes jobs (they
-- record k8s_job_name) and until the process has started.
ALTER TABLE transcode_jobs
    ADD COLUMN local_pid integer CHECK (local_pid > 0);

-- +goose Down
ALTER TABLE transcode_jobs DROP COLUMN local_pid;
