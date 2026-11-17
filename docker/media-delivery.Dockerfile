# The media-delivery image: nginx-unprivileged with the signed-URL config
# template and the start-up key guard baked in. The signing key is never in
# the image: MEDIA_SIGNING_KEY arrives from the environment, and the image's
# entrypoint renders the template into /etc/nginx/conf.d at start, which
# must be a writable, memory-only mount (tmpfs / emptyDir) owned by 65532.
#
# The base image is the one docker/nginx-media/nginx_test.go tests the
# template with: keep the two tags and digests in sync. Every FROM is pinned
# by tag AND digest; to update it, see docker/Dockerfile.
FROM nginxinc/nginx-unprivileged:1.30.6-alpine3.24@sha256:f5116ed4606f3eb015dd4a8e28cb9b351476dc0ad4bc6e9f93b19cec130cdf03
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
# The image's own default server would also listen on 8080; the rendered
# template is the only server.
USER 0:0
RUN rm -f /etc/nginx/conf.d/default.conf
COPY docker/nginx-media/default.conf.template /etc/nginx/templates/default.conf.template
COPY --chmod=0755 docker/nginx-media/05-require-media-signing-key.sh /docker-entrypoint.d/05-require-media-signing-key.sh
# Substitute the key and nothing else (the guard refuses any other filter).
ENV NGINX_ENVSUBST_FILTER=^MEDIA_SIGNING_KEY$
USER 65532:65532
