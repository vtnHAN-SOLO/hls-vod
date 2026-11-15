# 0006. Versioned library folders

**Status:** Accepted

## Context

Re-transcoding a ready movie (a new ladder, a fixed encode) by overwriting its files would break
viewers mid-playback, and a failed run would leave a broken movie.

## Decision

- Every publish writes `library/{id}/{version}/`, where `version` is the job id: written aside
  as `.{version}.partial-*`, then renamed in one step.
- `movies.library_path` points at the current version, and every URL is built from it.
- A retranscode leaves the movie `ready` on its current version. One transaction switches
  paths, renditions and runtime to the new version, and sets `library_cleanup_after` =
  now + the old-version grace.
- The **grace** (25 h) is derived in code from the longest signed URL lifetime (posters, 24 h,
  rounded up), never written as its own number.
- The watcher removes everything but the current version once the grace has passed and no job
  is active, under the movie's row lock.

## Consequences

- No downtime and no broken playback: an old URL keeps working until it expires, and its files
  outlive it.
- Two versions take disk space for a day. A retranscode checks for 1.5 × the current version's
  size first.
- Movies published before versioning keep their original layout and still play.
