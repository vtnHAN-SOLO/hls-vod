# The Go images: one shared build stage, two final targets.
#
#   docker build -f docker/Dockerfile --target app        → api, dispatcher, watcher, useradmin, migrate
#   docker build -f docker/Dockerfile --target transcoder → transcoder + ffmpeg/ffprobe
#
# Normally through `make images`, which passes VERSION (the git commit) and
# REVISION. Build context: the repository root, filtered by .dockerignore.
#
# Every FROM is pinned by tag AND digest. To update one: pick the new tag
# (at least 14 days old), then
#   docker buildx imagetools inspect <image>:<tag>   # the index digest, "Digest:"
# and replace both here. The digest is what is pulled; the tag is for people.

# Go: the toolchain in go.mod, on Debian 13 (trixie). Build stage only.
FROM golang:1.26.11-trixie@sha256:fa340cb59ebeb1d53ac1cccfc334201d938fe5f3458003cccb767096d8e032b8 AS build
WORKDIR /src

# Modules first: this layer is reused until go.mod or go.sum change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd/ cmd/
COPY internal/ internal/
COPY migrations/ migrations/

# Set by BuildKit from --platform; nothing here assumes amd64.
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
# One go build for every binary; the final stages copy what they need.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath \
      -ldflags "-s -w -X github.com/vtnHAN-SOLO/hls-vod/internal/buildinfo.Version=${VERSION}" \
      -o /out/ ./cmd/api ./cmd/dispatcher ./cmd/watcher ./cmd/useradmin ./cmd/migrate ./cmd/transcoder

# Static ffmpeg and ffprobe 8.1 (musl, no shared libraries), copied into the
# transcoder image below. This build includes GPL components (libx264), so
# distributing an image that contains it carries the GPL's obligations
# (offering the corresponding source). We build these images for our own
# cluster and do not publish them.
FROM mwader/static-ffmpeg:8.1@sha256:6fb848850b647688910ccf88112a96afe5ee46bcd54ee41daf875cac98b02bff AS ffmpeg

# Final stages: distroless static, not scratch. It adds what a static Go
# binary still needs from the filesystem: CA certificates (TLS to Resend,
# Google and Cloudflare Turnstile), tzdata, /etc/passwd with the nonroot
# user (65532) and a world-writable /tmp. No shell, no package manager.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931 AS app
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"
COPY --from=build /out/api /out/dispatcher /out/watcher /out/useradmin /out/migrate /usr/local/bin/
USER 65532:65532
# No ENTRYPOINT and no CMD: five programs live here and none is the obvious
# default. Each workload names its command (e.g. ["/usr/local/bin/api"]);
# `docker run` without one fails instead of silently starting the api.

FROM gcr.io/distroless/static-debian13:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931 AS transcoder
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"
COPY --from=ffmpeg /ffmpeg /ffprobe /usr/local/bin/
COPY --from=build /out/transcoder /usr/local/bin/transcoder
# internal/ffmpeg runs "ffmpeg" and "ffprobe" by name, found through PATH:
# set here explicitly to the one directory that holds them.
ENV PATH=/usr/local/bin
USER 65532:65532
# The dispatcher's Kubernetes Job passes only the arguments (--movie-id <id>).
ENTRYPOINT ["/usr/local/bin/transcoder"]
