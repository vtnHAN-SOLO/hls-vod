# The web image: the SPA built by Node, served by nginx-unprivileged.
# Normally through `make images`. Build context: the repository root,
# filtered by .dockerignore (web/.env.local never enters it).
#
# Every FROM is pinned by tag AND digest; to update one, see docker/Dockerfile.

# Node: the current LTS line (26). Build stage only, never shipped.
FROM node:26.13.0-trixie-slim@sha256:5adad64f9a1fca2d51972caa65983634c8a774eb300d72b20f7f2a0f60368072 AS build
WORKDIR /web
# Node 25+ no longer bundles corepack; install a pinned one. corepack then
# runs the exact pnpm that web/package.json's "packageManager" names.
RUN npm install --global corepack@0.36.0
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0

# Dependencies first: reused until the manifest or the lockfile change.
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN --mount=type=cache,target=/root/.cache/node/corepack \
    --mount=type=cache,target=/root/.local/share/pnpm/store \
    corepack pnpm install --frozen-lockfile

COPY web/ ./
# The brand is baked in at build time (src/lib/brand.ts reads the build's
# environment). The defaults are the public ones; a deployment passes its own.
ARG VITE_BRAND_NAME="HLS VOD"
ARG VITE_BRAND_ACCENT="VOD"
# The same build as `make web-build` (typecheck, then vite build: the
# manifest and the About page are generated here), and the nginx header
# include generated from security-headers.json.
RUN --mount=type=cache,target=/root/.cache/node/corepack \
    corepack pnpm build \
 && node scripts/nginx-headers.mjs > /web/security-headers.conf

FROM nginxinc/nginx-unprivileged:1.30.6-alpine3.24@sha256:f5116ed4606f3eb015dd4a8e28cb9b351476dc0ad4bc6e9f93b19cec130cdf03 AS web
ARG VERSION=dev
ARG REVISION=unknown
# Only version and revision. The base image's own labels (nginx's title,
# repository URL, build date…) would describe nginx, not this image: a
# Dockerfile cannot remove an inherited label, so they are blanked.
LABEL org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="" \
      org.opencontainers.image.description="" \
      org.opencontainers.image.licenses="" \
      org.opencontainers.image.source="" \
      org.opencontainers.image.title="" \
      org.opencontainers.image.url="" \
      maintainer=""
COPY docker/web/default.conf /etc/nginx/conf.d/default.conf
COPY --from=build /web/security-headers.conf /etc/nginx/snippets/security-headers.conf
COPY --from=build /web/dist/ /srv/web/
USER 65532:65532
# nginx itself, not the image's entrypoint script: there is nothing to
# render at start, and the script's steps would try to write to a
# read-only root filesystem. nginx writes only its pid and temp files, in /tmp.
ENTRYPOINT ["nginx", "-g", "daemon off;"]
