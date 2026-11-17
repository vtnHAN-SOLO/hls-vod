#!/usr/bin/env bash
# make kind-smoke: proves the kind cluster from make kind-up works end to
# end. Each step prints what it proves; the script stops at the first
# failure. Everything goes through http://localhost:8080 (the ingress) or
# kubectl; the test pods it starts are removed on exit. The movie it
# ingests stays, published, so it can be played in the browser afterwards.
#
# Needs: kubectl, docker (the clip and the poster are made with the
# transcoder image's own ffmpeg), curl, python3 (JSON).
set -euo pipefail

CLUSTER=${KIND_CLUSTER:-hls-vod}
NS=hls-vod
STATE=var/kind
export KUBECONFIG=$PWD/$STATE/kubeconfig
PREFIX=${IMAGE_PREFIX:-hls-vod}
TAG=${IMAGE_TAG:-dev}
BASE=http://localhost:8080
BUSYBOX=busybox:1.37.0-musl@sha256:f6a95d611e0a8780fabdb2aec43e31275b2720dfefa3e41b16835bfc42454478

step() { printf '\n== %s\n' "$1"; }
ok() { printf '  ok    %s\n' "$1"; }
note() { printf '  NOTE  %s\n' "$1"; }
die() {
	printf '  FAIL  %s\n' "$1" >&2
	exit 1
}
k() { kubectl --context "kind-$CLUSTER" -n "$NS" "$@"; }
code() { curl -s -o /dev/null -w '%{http_code}' "$@"; }
# json EXPR: evaluates a Python expression over the JSON on stdin, bound to j.
json() { python3 -c 'import json,sys; j=json.load(sys.stdin); print(eval(sys.argv[1]))' "$1"; }

[[ -f $KUBECONFIG ]] || die "no $KUBECONFIG: run make kind-up first"
[[ -s $STATE/admin-password ]] || die "no $STATE/admin-password: run make kind-up first"

work=$(mktemp -d)
pods=()
cleanup() {
	((${#pods[@]} == 0)) || k delete pod "${pods[@]}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
	rm -f "$work"/*
	rmdir "$work"
}
trap cleanup EXIT

# test_pod NAME APP_LABEL [MOUNT_MEDIA] -- COMMAND…: a busybox pod that meets
# "restricted" (non-root, no capabilities, seccomp, read-only root).
test_pod() {
	local name=$1 app=$2 media=${3:-} vol="" mount=""
	shift 3
	[[ $1 == -- ]] && shift
	if [[ -n $media ]]; then
		mount='      volumeMounts: [{name: media, mountPath: /mnt/media}]'
		vol='  volumes: [{name: media, persistentVolumeClaim: {claimName: media}}]'
	fi
	pods+=("$name")
	k apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: $name
  labels: {app: $app}
spec:
  restartPolicy: Never
  automountServiceAccountToken: false
  enableServiceLinks: false
  securityContext:
    runAsNonRoot: true
    runAsUser: 65532
    runAsGroup: 65532
    seccompProfile: {type: RuntimeDefault}
  containers:
    - name: c
      image: $BUSYBOX
      command: $(python3 -c 'import json,sys; print(json.dumps(sys.argv[1:]))' "$@")
      resources: {requests: {cpu: 10m, memory: 16Mi}, limits: {cpu: 200m, memory: 64Mi}}
      securityContext:
        allowPrivilegeEscalation: false
        readOnlyRootFilesystem: true
        capabilities: {drop: [ALL]}
$mount
$vol
EOF
}
# exit_code POD: waits for the pod to finish, prints its exit code.
exit_code() {
	local i phase
	for i in $(seq 60); do
		phase=$(k get pod "$1" -o jsonpath='{.status.phase}')
		[[ $phase == Succeeded || $phase == Failed ]] && break
		sleep 1
	done
	k get pod "$1" -o jsonpath='{.status.containerStatuses[0].state.terminated.exitCode}'
}

step "1. every workload ready; migrations applied"
k rollout status statefulset/postgres --timeout=60s >/dev/null
for d in api web media-delivery dispatcher; do
	k rollout status "deploy/$d" --timeout=60s >/dev/null || die "deploy/$d not ready"
	ready=$(k get deploy "$d" -o jsonpath='{.status.readyReplicas}/{.status.replicas}')
	ok "deploy/$d ready ($ready)"
done
ok "statefulset/postgres ready"
k get cronjob watcher >/dev/null && ok "cronjob/watcher scheduled: $(k get cronjob watcher -o jsonpath='{.spec.schedule}')"
status=$(k exec deploy/api -c api -- /usr/local/bin/migrate status 2>&1 | grep '"migration status"')
pending=$(json 'j["pending"]' <<<"$status")
total=$(json 'j["total"]' <<<"$status")
[[ $pending == 0 && $total -gt 0 ]] || die "migrations: $status"
ok "migrate status (in an api pod): $total migrations, 0 pending"

step "2. the site through the ingress ($BASE)"
csp=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["Content-Security-Policy"])' web/security-headers.json)
for p in / /about /browse; do
	curl -s -D "$work/h" -o "$work/b" "$BASE$p"
	head -n1 "$work/h" | grep -q ' 200' || die "$p: $(head -n1 "$work/h")"
	grep -q '<div id="root"' "$work/b" || die "$p is not the app shell"
	got=$(tr -d '\r' <"$work/h" | awk -F': ' 'tolower($1)=="content-security-policy" {print substr($0, length($1)+3)}')
	[[ $got == "$csp" ]] || die "$p: CSP differs from web/security-headers.json"
done
ok "/, /about and /browse (a client route): 200, index.html, the CSP of web/security-headers.json"
cfg=$(curl -s -w '\n%{http_code}' "$BASE/api/v1/auth/config")
[[ $(tail -n1 <<<"$cfg") == 200 ]] || die "/api/v1/auth/config: $cfg"
[[ $(head -n1 <<<"$cfg" | json 'j["registration_open"]') == False ]] || die "registration is open"
ok "/api/v1/auth/config 200 (the api), registration_open false"
for p in /healthz /readyz; do
	[[ $(code "$BASE$p") == 404 ]] || die "$p is reachable through the ingress"
done
curl -s "$BASE/metrics" | grep -q '^# HELP' && die "/metrics is reachable through the ingress"
ok "/healthz and /readyz 404, /metrics is the app shell: probes and metrics never routed"

step "3. admin login through the api"
password=$(cat "$STATE/admin-password")
login=$(python3 -c 'import json,sys; print(json.dumps({"username": "admin", "password": sys.stdin.read().strip()}))' <<<"$password" |
	curl -s -w '\n%{http_code}' -H 'Content-Type: application/json' --data-binary @- "$BASE/api/v1/auth/login")
[[ $(tail -n1 <<<"$login") == 200 ]] || die "login: $(tail -n1 <<<"$login")"
token=$(head -n1 <<<"$login" | json 'j["access_token"]')
auth=(-H "Authorization: Bearer $token")
[[ $(curl -s "${auth[@]}" "$BASE/api/v1/me" | json 'j["role"]') == admin ]] || die "/me is not the admin"
ok "POST /api/v1/auth/login 200, /api/v1/me role admin"

step "4. a 20 s clip: inbox -> watcher -> dispatcher -> transcoder Job -> ready"
docker run --rm --user "$(id -u):$(id -g)" -v "$work:/w" --entrypoint /usr/local/bin/ffmpeg "$PREFIX/transcoder:$TAG" \
	-nostdin -loglevel error -y -f lavfi -i testsrc2=size=1280x720:rate=25:duration=20 \
	-f lavfi -i sine=frequency=440:duration=20 -c:v libx264 -preset veryfast -pix_fmt yuv420p -c:a aac -shortest /w/clip.mp4
docker run --rm --user "$(id -u):$(id -g)" -v "$work:/w" --entrypoint /usr/local/bin/ffmpeg "$PREFIX/transcoder:$TAG" \
	-nostdin -loglevel error -y -f lavfi -i testsrc2=size=1000x1500:rate=1:duration=1 -frames:v 1 /w/poster.png
ok "clip (1280x720, H.264/AAC) and a 1000x1500 poster made with the transcoder image's ffmpeg"
title="Kind Smoke $(date -u +%Y%m%d-%H%M%S)"
test_pod media-loader media-loader media -- sleep 300
k wait --for=condition=Ready pod/media-loader --timeout=60s >/dev/null
# Written under a hidden name (the watcher skips those), dated 10 minutes
# back so it counts as settled, then renamed into place in one step.
settled=$(date -u -d '-10 min' '+%Y-%m-%d %H:%M:%S')
k exec -i media-loader -- sh -c 'cat > /mnt/media/inbox/.upload && touch -d "$1" /mnt/media/inbox/.upload && mv /mnt/media/inbox/.upload "/mnt/media/inbox/$2.mp4"' \
	sh "$settled" "$title" <"$work/clip.mp4"
ok "copied into inbox/ by a non-root busybox pod mounting the media PVC: $title.mp4"
job=watcher-smoke-$(date +%s)
k create job "$job" --from=cronjob/watcher >/dev/null
k wait --for=condition=complete "job/$job" --timeout=120s >/dev/null || die "watcher run failed"
ok "watcher triggered (job/$job from cronjob/watcher), completed"
q=$(python3 -c 'import sys,urllib.parse; print(urllib.parse.quote(sys.argv[1]))' "$title")
movie=""
for i in $(seq 150); do
	list=$(curl -s "${auth[@]}" "$BASE/api/v1/admin/movies?q=$q")
	movie=$(json '(j["items"] or [{}])[0].get("id", "")' <<<"$list")
	st=$(json '(j["items"] or [{}])[0].get("status", "")' <<<"$list")
	[[ $st == ready || $st == failed ]] && break
	sleep 2
done
[[ -n $movie ]] || die "the movie never appeared"
tjob=$(k get jobs -l app=transcoder,movie-id="$movie" -o jsonpath='{.items[0].metadata.name}')
[[ -n $tjob ]] || die "no transcoder Job for $movie"
ok "the dispatcher created job/$tjob (ServiceAccount $(k get job "$tjob" -o jsonpath='{.spec.template.spec.serviceAccountName}'))"
if [[ $st != ready ]]; then
	k get pods -l "job-name=$tjob" -o jsonpath='{range .items[*].status.containerStatuses[*]}{.state}{"\n"}{end}' >&2 || true
	k logs "job/$tjob" --tail=20 >&2 || true
	die "movie $movie is $st after 5 min"
fi
ok "movie $movie ready after ~$((i * 2)) s"

step "5. poster, overview, publish (admin API)"
[[ $(code "${auth[@]}" -X PUT -H 'Content-Type: image/png' --data-binary @"$work/poster.png" "$BASE/api/v1/admin/movies/$movie/poster") == 200 ]] ||
	die "poster upload"
ok "PUT /admin/movies/{id}/poster 200"
[[ $(code "${auth[@]}" -X PATCH -H 'Content-Type: application/json' \
	-d '{"overview":"A test pattern and a tone, ingested by the kind smoke test."}' "$BASE/api/v1/admin/movies/$movie") == 200 ]] ||
	die "overview"
ok "PATCH /admin/movies/{id} overview 200"
[[ $(code "${auth[@]}" -X POST "$BASE/api/v1/admin/movies/$movie/publish") == 204 ]] || die "publish"
ok "POST /admin/movies/{id}/publish 204"

step "6. playback through /media"
playback=$(curl -s "${auth[@]}" "$BASE/api/v1/movies/$movie/playback")
master=$(json 'j["playlist_url"]' <<<"$playback")
[[ $(code "$BASE$master") == 200 ]] || die "master.m3u8: $(code "$BASE$master")"
ok "signed master.m3u8 200 (media-delivery)"
rung=$(curl -s "$BASE$master" | grep -v '^#' | head -n1)
rung_url=${master%/*}/$rung
segment=$(curl -s "$BASE$rung_url" | grep -v '^#' | head -n1)
seg_url=${rung_url%/*}/$segment
[[ $(code "$BASE$seg_url") == 200 ]] || die "segment $seg_url"
ok "rung $rung and segment $segment 200"
IFS=/ read -r _ _ tok rest <<<"$master"
first=${tok:0:1}
[[ $first == A ]] && swap=B || swap=A
bad=/media/$swap${tok:1}/$rest
[[ $(code "$BASE$bad") == 403 ]] || die "a tampered token was not 403: $(code "$BASE$bad")"
ok "the same URL with one character of the token changed: 403"

step "7. Pod Security Standards: a root pod is refused"
out=$(k apply --dry-run=server -f - 2>&1 <<EOF || true
apiVersion: v1
kind: Pod
metadata:
  name: root-test
spec:
  containers:
    - name: c
      image: $BUSYBOX
      securityContext:
        runAsUser: 0
EOF
)
grep -q 'violates PodSecurity "restricted' <<<"$out" || die "a root pod was not refused: $out"
ok "server-side dry run refused: $(grep -o 'violates PodSecurity "restricted:[^"]*"' <<<"$out")"

step "8. RBAC"
can() { kubectl --context "kind-$CLUSTER" auth can-i "$1" jobs -n "$NS" --as="system:serviceaccount:$NS:$2" 2>/dev/null || true; }
[[ $(can create api) == no ]] || die "the api's ServiceAccount can create Jobs"
ok "api cannot create jobs"
[[ $(can create transcoder) == no ]] || die "the transcoder's ServiceAccount can create Jobs"
ok "transcoder cannot create jobs"
[[ $(can create dispatcher) == yes ]] || die "the dispatcher's ServiceAccount cannot create Jobs"
[[ $(kubectl --context "kind-$CLUSTER" auth can-i create pods -n "$NS" --as="system:serviceaccount:$NS:dispatcher" 2>/dev/null || true) == no ]] ||
	die "the dispatcher can create pods"
ok "dispatcher can create jobs, and not pods"

step "9. HPA media-delivery reports CPU"
cpu=""
for i in $(seq 60); do
	cpu=$(k get hpa media-delivery -o jsonpath='{.status.currentMetrics[0].resource.current.averageUtilization}')
	[[ -n $cpu ]] && break
	sleep 2
done
[[ -n $cpu ]] || die "the HPA has no CPU numbers after 2 min (metrics-server?)"
ok "HPA: CPU ${cpu}% of request (target 70%), replicas $(k get hpa media-delivery -o jsonpath='{.status.currentReplicas}') (2-5)"

step "10. NetworkPolicy: a pod labelled like web cannot reach postgres:5432"
test_pod np-web web "" -- nc -w 3 postgres 5432
test_pod np-watcher watcher "" -- nc -w 3 postgres 5432
web_rc=$(exit_code np-web)
watcher_rc=$(exit_code np-watcher)
[[ $watcher_rc == 0 ]] || die "control failed: a pod labelled watcher cannot reach postgres (exit $watcher_rc)"
ok "control: a pod labelled app=watcher connects to postgres:5432"
if [[ $web_rc == 0 ]]; then
	note "a pod labelled app=web reached postgres:5432: this cluster's CNI does not enforce"
	note "NetworkPolicy. The policies are validated (make manifests), not enforced locally;"
	note "production enforces them (Cilium)."
else
	ok "a pod labelled app=web is refused (exit $web_rc): NetworkPolicy enforced by kind's CNI (kindnet)"
fi

step "11. a poster near the 25 MP limit: the api pods are not OOMKilled"
# 4080x6120 = 24.97 MP, under poster.MaxPixels: decoding it takes ~100 MB
# before the crop and the three variants. Two uploads, so that both api
# replicas are likely to take one (the Service balances them).
docker run --rm --user "$(id -u):$(id -g)" -v "$work:/w" --entrypoint /usr/local/bin/ffmpeg "$PREFIX/transcoder:$TAG" \
	-nostdin -loglevel error -y -f lavfi -i testsrc2=size=4080x6120:rate=1:duration=1 -frames:v 1 -q:v 3 /w/big.jpg
size=$(stat -c %s "$work/big.jpg")
((size <= 10 << 20)) || die "big.jpg is $size bytes, over the 10 MB upload limit"
ok "a 4080x6120 JPEG (24.97 MP, $((size / 1024)) KiB) made with the transcoder image's ffmpeg"
# restarts: "pod=count:lastReason" for every api pod.
restarts() {
	k get pods -l app=api -o jsonpath='{range .items[*]}{.metadata.name}={.status.containerStatuses[0].restartCount}:{.status.containerStatuses[0].lastState.terminated.reason}{"\n"}{end}' | sort
}
before=$(restarts)
for n in 1 2; do
	rc=$(code "${auth[@]}" --max-time 120 -X PUT -H 'Content-Type: image/jpeg' --data-binary @"$work/big.jpg" \
		"$BASE/api/v1/admin/movies/$movie/poster")
	if [[ $rc != 200 ]]; then
		sleep 5 # the kubelet records an OOMKill a few seconds later
		restarts >&2
		die "upload $n of the 25 MP poster: HTTP $rc (an api pod killed mid-request shows as 500)"
	fi
done
ok "PUT /admin/movies/{id}/poster 200, twice"
sleep 5
after=$(restarts)
[[ $after == "$before" ]] || die "an api pod restarted (before: $(tr '\n' ' ' <<<"$before")after: $(tr '\n' ' ' <<<"$after"))"
ok "no api pod restarted ($(tr '\n' ' ' <<<"$after" | sed 's/ $//'); limit $(k get deploy api -o jsonpath='{.spec.template.spec.containers[0].resources.limits.memory}'))"

printf '\nAll kind smoke checks passed. Open %s, log in as admin (password in %s) and play "%s".\n' \
	"$BASE" "$STATE/admin-password" "$title"
