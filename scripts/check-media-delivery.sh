#!/usr/bin/env bash
# Checks a running media-delivery: probes, signed URLs (valid, tampered,
# other movie, expired, missing), MIME types, cache policy, CORS, methods and
# path traversal. Exits non-zero on the first summary with a failure. Usage
# (normally via `make media-check`, which loads .env):
#
#   MEDIA_SIGNING_KEY=... MOVIE=<movie id> BASE=http://127.0.0.1:8081 scripts/check-media-delivery.sh
#
# MOVIE defaults to the first movie directory under $MEDIA_ROOT/library.
# VERSION is the version folder inside it that holds master.m3u8
# (library/{id}/{version}/); it defaults to the newest one, or none for a
# movie published before versions (files directly in library/{id}/). URLs
# are signed here with MEDIA_SIGNING_KEY, the way the api signs them, with
# openssl (so this also cross-checks the api's signer).
set -euo pipefail

if [[ -z ${MEDIA_SIGNING_KEY:-} ]]; then
	echo "MEDIA_SIGNING_KEY is not set: put it in .env (openssl rand -base64 48)" >&2
	exit 2
fi

BASE=${BASE:-http://127.0.0.1:8081}
MOVIE=${MOVIE:-}
if [[ -z $MOVIE && -n ${MEDIA_ROOT:-} && -d $MEDIA_ROOT/library ]]; then
	MOVIE=$(find "$MEDIA_ROOT/library" -mindepth 1 -maxdepth 1 -type d ! -name '.*' -printf '%f\n' | sort | head -n1)
fi
if [[ -z $MOVIE ]]; then
	echo "no movie: set MOVIE=<id> or ingest one with make dev-ingest" >&2
	exit 2
fi
VERSION=${VERSION:-}
if [[ -z $VERSION && -n ${MEDIA_ROOT:-} && -d $MEDIA_ROOT/library/$MOVIE && ! -f $MEDIA_ROOT/library/$MOVIE/master.m3u8 ]]; then
	VERSION=$(find "$MEDIA_ROOT/library/$MOVIE" -mindepth 1 -maxdepth 1 -type d ! -name '.*' -printf '%T@ %f\n' | sort -n | tail -n1 | cut -d' ' -f2)
fi
VP=${VERSION:+/$VERSION} # "" or "/{version}"

fail=0
body=$(mktemp)
trap 'rm -f "$body"' EXIT

pass() { printf '  ok    %s\n' "$1"; }
bad() {
	printf '  FAIL  %s\n' "$1"
	fail=1
}

# header <name> <path> [curl args...]: prints the value of one response header.
header() {
	local name=$1 path=$2
	shift 2
	curl -s --path-as-is -o /dev/null -D - "$@" "$BASE$path" |
		tr -d '\r' | awk -v h="$(tr '[:upper:]' '[:lower:]' <<<"$name")" -F': ' 'tolower($1)==h {print $2}'
}
status() {
	local path=$1
	shift
	curl -s --path-as-is -o "$body" -w '%{http_code}' "$@" "$BASE$path"
}
expect_header() { # label name want path [curl args...]
	local label=$1 name=$2 want=$3 path=$4
	shift 4
	local got
	got=$(header "$name" "$path" "$@")
	if [[ $got == "$want" ]]; then pass "$label: $name: $got"; else bad "$label: $name = '$got', want '$want'"; fi
}

# token <expires> <movie id>: base64url(md5("{expires}/{movie id} {key}")),
# unpadded, as nginx secure_link_md5 and internal/service.MediaSigner compute it.
token() {
	printf '%s/%s %s' "$1" "$2" "$MEDIA_SIGNING_KEY" | openssl md5 -binary | openssl base64 -A | tr '+/' '-_' | tr -d '='
}
# signed <movie id> [seconds from now]: the signed folder prefix, no trailing slash.
signed() {
	local exp=$(($(date +%s) + ${2:-3600}))
	printf '/media/%s/%s/%s' "$(token "$exp" "$1")" "$exp" "$1"
}
# A different, valid-looking movie id.
OTHER=$( [[ $MOVIE == 00000000-* ]] && echo 11111111-1111-4111-8111-111111111111 || echo 00000000-0000-4000-8000-000000000000)

m=$(signed "$MOVIE")
v=$m$VP # the version folder, under the movie's one token
seg=$(curl -s "$BASE$v/master.m3u8" | grep -m1 -v '^#' || true)
seg_dir=${seg%/*}
first_ts=$(curl -s "$BASE$v/$seg" | grep -m1 -v '^#' || true)

echo "media-delivery at $BASE, movie $MOVIE${VERSION:+, version $VERSION}"

echo "probes"
[[ $(status /healthz) == 200 ]] && pass "/healthz 200" || bad "/healthz not 200"
[[ $(status /readyz) == 200 ]] && pass "/readyz 200 ($(cat "$body" | tr -d '\n'))" || bad "/readyz not 200: $(cat "$body")"

echo "signed URLs"
tok=${m#/media/}
tok=${tok%%/*}
rest=${m#/media/"$tok"/}
first=${tok:0:1}
[[ $first == A ]] && swap=B || swap=A
tampered="/media/$swap${tok:1}/$rest"
other=$(signed "$OTHER")
other_tok=${other#/media/}
other_tok=${other_tok%%/*}
expect_status() { # label want path
	local code
	code=$(status "$3")
	if [[ $code == "$2" ]]; then pass "$1: $code"; else bad "$1 = $code, want $2"; fi
}
expect_status "valid token, master playlist" 200 "$v/master.m3u8"
expect_status "valid token, segment" 200 "$v/$seg_dir/$first_ts"
expect_status "valid token, poster" 200 "$v/poster.jpg"
expect_status "one character of the token changed" 403 "$tampered$VP/master.m3u8"
expect_status "another movie's token" 403 "/media/$other_tok/$rest$VP/master.m3u8"
expect_status "this token on another movie" 403 "${m%"$MOVIE"}$OTHER$VP/master.m3u8"
expect_status "expired" 410 "$(signed "$MOVIE" -60)$VP/master.m3u8"
expect_status "no token" 403 "/media/$MOVIE$VP/master.m3u8"
expect_status "no token, poster" 403 "/media/$MOVIE$VP/poster.jpg"

echo "types and cache policy"
expect_header "master" Content-Type "application/vnd.apple.mpegurl" "$v/master.m3u8"
expect_header "master" Cache-Control "private, max-age=10" "$v/master.m3u8"
expect_header "segment" Content-Type "video/mp2t" "$v/$seg_dir/$first_ts"
expect_header "segment" Cache-Control "private, max-age=31536000, immutable" "$v/$seg_dir/$first_ts"
expect_header "poster" Content-Type "image/jpeg" "$v/poster.jpg"
expect_header "missing segment" Cache-Control "" "$v/$seg_dir/seg_99999.ts"

echo "CORS"
expect_header "GET" Access-Control-Allow-Origin "*" "$v/master.m3u8" -H 'Origin: https://hlsjs.video-dev.org'
expect_header "404" Access-Control-Allow-Origin "*" "$v/nope.ts"
code=$(status "$v/master.m3u8" -X OPTIONS -H 'Origin: https://hlsjs.video-dev.org' -H 'Access-Control-Request-Method: GET')
[[ $code == 204 ]] && pass "preflight 204" || bad "preflight = $code"

echo "methods"
for method in POST PUT DELETE; do
	code=$(status "$v/master.m3u8" -X "$method")
	[[ $code == 405 ]] && pass "$method 405" || bad "$method = $code"
done

echo "path traversal (must not be 200, must not leak)"
paths=(
	'/media/../../etc/passwd'
	"$m/../../../etc/passwd"
	'/media/%2e%2e/%2e%2e/etc/passwd'
	"$m/%2e%2e%2f%2e%2e%2f%2e%2e%2fetc%2fpasswd"
	"$m/..%2f..%2f..%2fetc%2fpasswd"
	"$m/%252e%252e%252fetc%252fpasswd"
	'/media/..%2f..%2fetc%2fpasswd'
	'/media//etc/passwd'
	'/media/%2fetc%2fpasswd'
	"$v/master.m3u8%00.jpg"
	"$m/%00"
	'/media/%00'
	"$m/../$OTHER/master.m3u8"
	"$m/%2e%2e/$OTHER/master.m3u8"
	"${m%"$MOVIE"}.$MOVIE.partial-1/master.m3u8"
	"$m/.$VERSION.partial-1/master.m3u8"
	"$m/"
	'/_library_check/'
	'/stub_status'
)
for p in "${paths[@]}"; do
	code=$(status "$p")
	if [[ $code == 200 || $code == 206 ]] || grep -q -e 'root:' -e "\"$MOVIE\"" "$body"; then
		bad "$p -> $code"
	else
		pass "$p -> $code"
	fi
done

if ((fail)); then
	echo "FAILED"
	exit 1
fi
echo "all checks passed"
