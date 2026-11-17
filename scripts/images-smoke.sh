#!/usr/bin/env bash
# Smoke tests for the four images `make images` builds. Each step prints what
# it proves; the script stops at the first failure. Normally via
# `make images-smoke`, which loads .env (DATABASE_URL, for the compose
# Postgres) and passes VERSION and BRAND_NAME, the values the images were
# built with.
#
# Needs: Docker, curl, openssl, and the compose Postgres running and migrated
# (make db-up && make migrate): step 3 runs `migrate status` against it,
# which only reads. Nothing else of the local setup is touched: containers
# listen on 127.0.0.1:18080 (api, host network), :18090 (web) and :18091
# (media-delivery), media goes to a temp dir, all removed on exit.
set -euo pipefail

PREFIX=${IMAGE_PREFIX:-hls-vod}
TAG=${IMAGE_TAG:-dev}
VERSION=${VERSION:?VERSION is not set: run through make images-smoke}
BRAND_NAME=${BRAND_NAME:-HLS VOD}
APP=$PREFIX/app:$TAG
TRANSCODER=$PREFIX/transcoder:$TAG
WEB=$PREFIX/web:$TAG
MEDIA=$PREFIX/media-delivery:$TAG
HARDEN=(--read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges)

work=$(mktemp -d)
containers=()
cleanup() {
	((${#containers[@]} == 0)) || docker rm -f "${containers[@]}" >/dev/null 2>&1 || true
	# The transcoder's output belongs to 65532: removed from a container
	# running as that user (the media-delivery image has a shell).
	if [[ -d $work/media/out ]]; then
		docker run --rm --user 65532:65532 -v "$work/media:/w" --entrypoint /bin/sh "$MEDIA" -c 'rm -rf /w/out' >/dev/null 2>&1 || true
	fi
	rm -rf "$work"
}
trap cleanup EXIT

step() { printf '\n== %s\n' "$1"; }
ok() { printf '  ok    %s\n' "$1"; }
die() {
	printf '  FAIL  %s\n' "$1" >&2
	exit 1
}
# wait_http URL: up to 30 s for a 200.
wait_http() {
	local i
	for i in $(seq 60); do
		[[ $(curl -s -o /dev/null -w '%{http_code}' "$1" || true) == 200 ]] && return 0
		sleep 0.5
	done
	return 1
}
# header NAME URL: one response header's value.
header() {
	curl -s -o /dev/null -D - "$2" | tr -d '\r' |
		awk -v h="$(tr '[:upper:]' '[:lower:]' <<<"$1")" -F': ' 'tolower($1)==h {print substr($0, length($1)+3)}'
}
uid_of() { docker top "$1" -o pid,uid | awk 'NR > 1 {print $2}' | sort -u | tr '\n' ' ' | sed 's/ $//'; }

for img in "$APP" "$TRANSCODER" "$WEB" "$MEDIA"; do
	docker image inspect "$img" >/dev/null 2>&1 || die "$img not found: run make images first"
done

step "1. every image runs as UID 65532"
for img in "$APP" "$TRANSCODER" "$WEB" "$MEDIA"; do
	user=$(docker image inspect -f '{{.Config.User}}' "$img")
	[[ $user == 65532:65532 ]] || die "$img: USER is '$user', want 65532:65532"
	ok "$img: USER $user"
	labels=$(docker image inspect -f '{{range $k, $v := .Config.Labels}}{{if $v}}{{$k}}={{$v}} {{end}}{{end}}' "$img")
	[[ $labels == "org.opencontainers.image.revision=$(git rev-parse HEAD 2>/dev/null || echo unknown) org.opencontainers.image.version=$VERSION " ]] ||
		die "$img: labels with a value are '$labels', want only revision and version $VERSION"
	ok "$img: labels: version $VERSION and revision only"
done
# (the running processes are checked below, in steps 3, 5 and 6, and the
# transcoder's output files are owned by 65532 in step 4)

step "2. app and transcoder have no shell"
for img in "$APP" "$TRANSCODER"; do
	for sh in /bin/sh /bin/bash /bin/ash /busybox/sh; do
		if docker run --rm --entrypoint "$sh" "$img" -c true >/dev/null 2>&1; then
			die "$img: $sh ran"
		fi
	done
	ok "$img: no /bin/sh, /bin/bash, /bin/ash or /busybox/sh can be started"
done
if docker run --rm "$APP" >/dev/null 2>&1; then
	die "$APP: started something without a command (it must have no default entrypoint)"
fi
ok "$APP: has no default command; each workload names its own"

step "3. app: migrate status against the compose Postgres; the api, hardened"
[[ -n ${DATABASE_URL:-} ]] || die "DATABASE_URL is not set (.env)"
if ! out=$(docker run --rm --network host -e DATABASE_URL "${HARDEN[@]}" \
	--entrypoint /usr/local/bin/migrate "$APP" status 2>&1); then
	printf '%s\n' "$out" >&2
	die "migrate status failed: is the compose Postgres up and migrated? (make db-up && make migrate)"
fi
grep -q "\"version\":\"$VERSION\"" <<<"$out" || die "migrate did not log version $VERSION"
summary=$(grep '"msg":"migration status"' <<<"$out")
grep -q '"pending":0' <<<"$summary" || die "migrations pending on the compose Postgres: $summary"
ok "migrate status: $(sed -E 's/.*"total":([0-9]+),"pending":([0-9]+).*/\1 migrations, \2 pending/' <<<"$summary"), version $VERSION in its start line"

# Throwaway keys: the api needs them to start, nothing here signs anything.
JWT_SIGNING_KEY=$(openssl rand -base64 48 | tr -d '\n')
MEDIA_SIGNING_KEY=$(openssl rand -base64 48 | tr -d '\n')
export JWT_SIGNING_KEY MEDIA_SIGNING_KEY
api=$(docker run -d --network host "${HARDEN[@]}" \
	-e DATABASE_URL -e JWT_SIGNING_KEY -e MEDIA_SIGNING_KEY \
	-e HTTP_ADDR=127.0.0.1:18080 -e TRUSTED_PROXIES= -e REGISTRATION_OPEN=false -e MAIL_BACKEND=log \
	--entrypoint /usr/local/bin/api "$APP")
containers+=("$api")
wait_http http://127.0.0.1:18080/healthz || { docker logs "$api" >&2; die "api /healthz never answered 200"; }
ok "api started with --read-only --tmpfs /tmp --cap-drop ALL; process UID $(uid_of "$api")"
[[ $(uid_of "$api") == 65532 ]] || die "api runs as $(uid_of "$api")"
[[ $(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/healthz) == 200 ]] && ok "/healthz 200"
[[ $(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/readyz) == 200 ]] || die "/readyz not 200"
ok "/readyz 200 (Postgres reachable)"
metric=$(curl -s http://127.0.0.1:18080/metrics | grep '^build_info' || true)
[[ $metric == "build_info{version=\"$VERSION\"} 1" ]] || die "/metrics build_info = '$metric'"
ok "/metrics: $metric"
docker rm -f "$api" >/dev/null

step "4. transcoder: CLI mode on a 10 s clip with a subtitle stream (ffmpeg 8.1)"
mkdir -p "$work/media"
chmod 0777 "$work/media" # the container writes as 65532
cat >"$work/media/subs.srt" <<'EOF'
1
00:00:01,000 --> 00:00:04,000
A subtitle line.

2
00:00:05,000 --> 00:00:09,000
Another one.
EOF
chmod 0644 "$work/media/subs.srt"
docker run --rm "${HARDEN[@]}" -v "$work/media:/work" --entrypoint /usr/local/bin/ffmpeg "$TRANSCODER" \
	-hide_banner -loglevel error \
	-f lavfi -i testsrc2=size=1280x720:rate=25:duration=10 \
	-f lavfi -i sine=frequency=440:duration=10 \
	-i /work/subs.srt \
	-map 0:v -map 1:a -map 2:s -c:v libx264 -preset veryfast -pix_fmt yuv420p -c:a aac -c:s srt \
	-metadata:s:s:0 language=eng /work/clip.mkv
ok "clip made with the image's own ffmpeg: $(docker run --rm --entrypoint /usr/local/bin/ffmpeg "$TRANSCODER" -hide_banner -version | head -n1)"
docker run --rm "${HARDEN[@]}" -v "$work/media:/work" "$TRANSCODER" \
	--input /work/clip.mkv --output /work/out >"$work/transcoder.log" 2>&1 ||
	{ cat "$work/transcoder.log" >&2; die "transcoder failed"; }
grep -q "\"version\":\"$VERSION\"" "$work/transcoder.log" || die "transcoder did not log version $VERSION"
out=$work/media/out
[[ -f $out/master.m3u8 ]] || die "no master.m3u8"
segs=$(find "$out" -name '*.ts' | wc -l)
((segs > 0)) || die "no segments"
vtt=$(find "$out" -path '*/subs/*.vtt' | head -n1)
[[ -n $vtt ]] || die "no subtitle .vtt"
grep -q "A subtitle line." "$vtt" || die "$vtt does not hold the subtitle text"
owner=$(stat -c %u "$out/master.m3u8")
[[ $owner == 65532 ]] || die "output owned by $owner, want 65532"
ok "--read-only, UID $owner: master.m3u8, $segs segments, ${vtt#"$out"/} with the subtitle text"

step "5. web: read-only nginx, routes, cache, CSP and brand"
web=$(docker run -d "${HARDEN[@]}" -p 127.0.0.1:18090:8080 "$WEB")
containers+=("$web")
W=http://127.0.0.1:18090
wait_http "$W/healthz" || { docker logs "$web" >&2; die "web /healthz never answered 200"; }
[[ $(uid_of "$web") == 65532 ]] || die "web runs as $(uid_of "$web")"
ok "nginx started with --read-only --tmpfs /tmp --cap-drop ALL; processes UID $(uid_of "$web")"
for p in /healthz /readyz; do
	[[ $(curl -s -o /dev/null -w '%{http_code}' -H 'X-Forwarded-For: 203.0.113.7' "$W$p") == 404 ]] ||
		die "web $p answers a proxied request"
done
ok "/healthz and /readyz 200 for the kubelet, 404 through a proxy (X-Forwarded-For)"
csp=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["Content-Security-Policy"])' web/security-headers.json)
asset=$(curl -s "$W/" | grep -oE '/assets/[^"]+\.js' | head -n1)
[[ -n $asset ]] || die "no /assets/*.js in index.html"
check() { # label path status [cache-control]
	local code got
	code=$(curl -s -o /dev/null -w '%{http_code}' "$W$2")
	[[ $code == "$3" ]] || die "$1 ($2) = $code, want $3"
	got=$(header Content-Security-Policy "$W$2")
	[[ $got == "$csp" ]] || die "$1 ($2): CSP = '$got'"
	[[ $(header X-Content-Type-Options "$W$2") == nosniff ]] || die "$1 ($2): no nosniff"
	if [[ -n ${4:-} ]]; then
		[[ $(header Cache-Control "$W$2") == "$4" ]] || die "$1 ($2): Cache-Control = '$(header Cache-Control "$W$2")', want '$4'"
	fi
	ok "$1 $2: $code, CSP + nosniff${4:+, Cache-Control: $4}"
}
check "index" / 200 "no-cache"
check "client route" /browse 200 "no-cache"
check "About" /about 200 "no-cache"
check "asset" "$asset" 200 "public, max-age=31536000, immutable"
check "missing asset" /assets/missing-0000.js 404
check "manifest" /manifest.webmanifest 200 "public, max-age=86400"
[[ $(header Content-Type "$W/assets/missing-0000.js") != text/html* ]] || die "a missing asset answered with HTML"
ok "a missing asset is a 404, never the HTML shell"
title=$(curl -s "$W/" | grep -oE '<title>[^<]*</title>' | sed -E 's#</?title>##g')
[[ $title == "$BRAND_NAME" ]] || die "<title> is '$title', want '$BRAND_NAME' (the build arg)"
mname=$(curl -s "$W/manifest.webmanifest" | python3 -c 'import json,sys; print(json.load(sys.stdin)["name"])')
[[ $mname == "$BRAND_NAME" ]] || die "manifest name is '$mname', want '$BRAND_NAME'"
ok "brand: <title> and the manifest say '$BRAND_NAME', the build arg"
[[ $(header Server "$W/") == nginx ]] || die "Server header shows a version: $(header Server "$W/")"
ok "server_tokens off (Server: nginx)"
[[ $(docker exec "$web" wget -qO- http://127.0.0.1:8082/stub_status | head -n1) == Active\ connections:* ]] ||
	die "stub_status not answering on loopback inside the container"
ok "stub_status on 127.0.0.1:8082 inside the container (not published)"
docker rm -f "$web" >/dev/null

step "6. media-delivery: scripts/check-media-delivery.sh against the image"
movie=$(cat /proc/sys/kernel/random/uuid)
version=$(cat /proc/sys/kernel/random/uuid)
mkdir -p "$work/library/$movie"
chmod -R 0755 "$work/library"
cp -r "$out" "$work/library/$movie/$version"
chmod -R a+rX "$work/library"
media=$(docker run -d "${HARDEN[@]}" -p 127.0.0.1:18091:8080 \
	--tmpfs /etc/nginx/conf.d:mode=0755,uid=65532,gid=65532 \
	-e MEDIA_SIGNING_KEY -v "$work/library:/srv/media/library:ro" "$MEDIA")
containers+=("$media")
wait_http http://127.0.0.1:18091/healthz || { docker logs "$media" >&2; die "media-delivery /healthz never answered 200"; }
[[ $(uid_of "$media") == 65532 ]] || die "media-delivery runs as $(uid_of "$media")"
ok "nginx started with --read-only, the key from the environment only; processes UID $(uid_of "$media")"
BASE=http://127.0.0.1:18091 MOVIE=$movie VERSION=$version scripts/check-media-delivery.sh
docker rm -f "$media" >/dev/null

step "7. image sizes"
docker image ls --format '{{.Repository}}:{{.Tag}}\t{{.Size}}' | grep "^$PREFIX/" | grep ":$TAG" | sort | column -t
printf '\nall image smoke tests passed\n'
