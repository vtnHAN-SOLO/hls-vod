-- +goose Up
-- Retranscode. Every publish now goes to library/{id}/{version}/,
-- version = the job id, so a retranscode never overwrites files that
-- signed URLs already point at.
--
-- purpose: ingest (pending → processing → ready / failed, as before) or
-- retranscode (a ready movie gets a new version; its status never changes).
ALTER TABLE transcode_jobs ADD COLUMN purpose text NOT NULL DEFAULT 'ingest'
    CHECK (purpose IN ('ingest', 'retranscode'));

-- When set, the watcher removes everything in library/{id}/ except the
-- current version once this time has passed (no URL signed for an older
-- version outlives it), then clears it.
ALTER TABLE movies ADD COLUMN library_cleanup_after timestamptz;

-- +goose Down
ALTER TABLE movies DROP COLUMN library_cleanup_after;
ALTER TABLE transcode_jobs DROP COLUMN purpose;
