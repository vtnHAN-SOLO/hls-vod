# 0001. Pre-transcoded HLS, never live transcoding

**Status:** Accepted

## Context

A single live transcode takes 2–4 CPU cores. Even a handful of viewers would saturate a small
cluster, and playback capacity would scale with CPU instead of with bandwidth.

## Decision

Every movie is transcoded to HLS **once, at ingest**. Playback is nginx serving static files.

- `ffprobe` decides, never an assumption: if the source is H.264/AAC, the top rung is a remux
  (`ffmpeg -c copy`, seconds instead of hours); only the lower rungs are encoded.
- The top rung is at the source resolution: never upscaled.
- Posters, thumbnail sprites and subtitles are produced in the same run.

## Consequences

- Playback costs almost no CPU. Capacity grows by adding media-delivery replicas (HPA 2–5).
- Storage holds every rung of every movie. Ingest takes time before a movie is playable.
- Changing the ladder means re-transcoding: [0006](0006-versioned-library-folders.md) makes
  that possible without downtime.
- On-demand transcoding stays out of scope; it would break the scaling model.
