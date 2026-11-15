#!/bin/sh
# Runs from the nginx image's entrypoint before 20-envsubst-on-templates.sh
# renders default.conf.template. Without this, a missing MEDIA_SIGNING_KEY
# would leave "${MEDIA_SIGNING_KEY}" in the config, and an empty one would
# make the secret "" and every token computable by anyone. The rule is the
# api's (internal/config.ValidMediaSigningKey): at least 32 bytes of base64
# or base64url characters, because the key is written inside a quoted
# string in the nginx config, where a quote, "$", ";" or newline would
# change the config itself. A non-zero exit stops the container.
set -eu

fail() {
	echo "$0: $1" >&2
	exit 1
}
hint="generate one with: openssl rand -base64 48"

key=${MEDIA_SIGNING_KEY:-}
[ -n "$key" ] || fail "MEDIA_SIGNING_KEY is not set ($hint)"
[ "${#key}" -ge 32 ] || fail "MEDIA_SIGNING_KEY must be at least 32 bytes, got ${#key} ($hint)"
case $key in
*[!A-Za-z0-9+/=_-]*) fail "MEDIA_SIGNING_KEY may contain only base64 characters (A-Z a-z 0-9 + / = _ -) ($hint)" ;;
esac

# Substitute the key and nothing else: nginx's own $variables stay as written.
[ "${NGINX_ENVSUBST_FILTER:-}" = '^MEDIA_SIGNING_KEY$' ] ||
	fail "NGINX_ENVSUBST_FILTER must be ^MEDIA_SIGNING_KEY\$, so the template substitutes nothing else"

# The image's envsubst step only logs an ERROR and carries on when it cannot
# write the rendered config; nginx would then start without it.
[ -w /etc/nginx/conf.d ] ||
	fail "/etc/nginx/conf.d is not writable: mount a tmpfs (compose) or an emptyDir (Kubernetes) the nginx user can write"
