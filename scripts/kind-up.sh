#!/usr/bin/env bash
# make kind-up: the whole stack on a local kind cluster, from the images
# `make images` builds. Idempotent: run it again after `make images` and the
# new images are loaded and rolled out; the secrets, the database and the
# admin account are kept.
#
# Nothing outside var/kind/ (gitignored) is written: the cluster's
# kubeconfig is var/kind/kubeconfig, so ~/.kube/config and its current
# context are left alone. Secrets are generated here (openssl rand) and
# piped to kubectl on stdin: never in argv, never in a file, except the
# admin password, written once to var/kind/admin-password (mode 0600).
set -euo pipefail

CLUSTER=${KIND_CLUSTER:-hls-vod}
NS=hls-vod
STATE=var/kind
export KUBECONFIG=$PWD/$STATE/kubeconfig
PREFIX=${IMAGE_PREFIX:-hls-vod}
TAG=${IMAGE_TAG:-dev}
IMAGES=(app transcoder web media-delivery)
HOST_PORT=8080

step() { printf '\n== %s\n' "$1"; }
ok() { printf '  ok    %s\n' "$1"; }
die() {
	printf '  FAIL  %s\n' "$1" >&2
	exit 1
}
k() { kubectl --context "kind-$CLUSTER" "$@"; }
# wait_job NAME SECONDS: 0 once the Job succeeded, 1 as soon as it failed or
# the time is up.
wait_job() {
	local i s f
	for ((i = 0; i < $2; i++)); do
		s=$(k -n "$NS" get job "$1" -o jsonpath='{.status.succeeded}')
		f=$(k -n "$NS" get job "$1" -o jsonpath='{.status.failed}')
		[[ $s == 1 ]] && return 0
		[[ -n $f && $f != 0 ]] && return 1
		sleep 1
	done
	return 1
}

for t in docker kind kubectl openssl; do
	command -v "$t" >/dev/null || die "$t is missing (make setup installs kind and kubectl)"
done
mkdir -p "$STATE"
chmod 0700 "$STATE"

step "1. cluster $CLUSTER (kind $(kind version | awk '{print $2}'), deploy/kind/cluster.yaml)"
if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
	kind export kubeconfig --name "$CLUSTER" --kubeconfig "$KUBECONFIG" >/dev/null 2>&1
	ok "already exists: reused"
else
	if (exec 3<>"/dev/tcp/127.0.0.1/$HOST_PORT") 2>/dev/null; then
		die "127.0.0.1:$HOST_PORT is in use (make run?): stop it, the cluster's ingress listens there"
	fi
	kind create cluster --name "$CLUSTER" --config deploy/kind/cluster.yaml --kubeconfig "$KUBECONFIG" --wait 120s
	ok "created: one node, host 127.0.0.1:$HOST_PORT -> ingress NodePort 30080"
fi
k get nodes -o wide --no-headers | awk '{print "  node  " $1 " " $2 " " $5}'

step "2. ingress controller and metrics-server"
k apply -f deploy/kind/traefik.yaml >/dev/null
k apply -k deploy/kind/metrics-server >/dev/null
k -n traefik rollout status deploy/traefik --timeout=180s >/dev/null
ok "Traefik ready in namespace traefik (IngressClass traefik)"
k -n metrics-server rollout status deploy/metrics-server --timeout=180s >/dev/null
ok "metrics-server ready in namespace metrics-server"

step "3. images $PREFIX/<name>:$TAG"
missing=0
for i in "${IMAGES[@]}"; do
	docker image inspect "$PREFIX/$i:$TAG" >/dev/null 2>&1 || missing=1
done
if ((missing)); then
	echo "  some images are missing: make images"
	make --no-print-directory images
fi
ids=""
for i in "${IMAGES[@]}"; do
	ids+="$i $(docker image inspect -f '{{.Id}}' "$PREFIX/$i:$TAG")"$'\n'
done
# kind load skips an image the node already holds with the same ID.
for i in "${IMAGES[@]}"; do
	kind load docker-image --name "$CLUSTER" "$PREFIX/$i:$TAG" >/dev/null 2>&1 ||
		die "kind load $PREFIX/$i:$TAG failed"
done
ok "in the node: ${IMAGES[*]}"
# Same tag, new content: the running pods are restarted below.
changed=0
if [[ ! -f $STATE/images || $(cat "$STATE/images") != "${ids%$'\n'}" ]]; then
	changed=1
	printf '%s' "$ids" >"$STATE/images"
fi
# The dispatcher refuses an image without a digest: the digest the node's
# containerd recorded for the transcoder image.
node=$CLUSTER-control-plane
digest=$(docker exec "$node" ctr -n k8s.io images ls "name==docker.io/$PREFIX/transcoder:$TAG" |
	awk 'NR == 2 {print $3}')
[[ $digest == sha256:* ]] || die "no digest for $PREFIX/transcoder:$TAG in the node"
# kind load records the digest under a temporary "import-…" name only, so
# the kubelet would not find name@digest and try to pull it: give the image
# that name in the node too.
docker exec "$node" ctr -n k8s.io images tag --force "docker.io/$PREFIX/transcoder:$TAG" \
	"docker.io/$PREFIX/transcoder@$digest" >/dev/null
transcoder_image=$PREFIX/transcoder:$TAG@$digest
ok "TRANSCODER_IMAGE=$transcoder_image"

step "4. namespace $NS and secrets"
k apply -f deploy/base/namespace.yaml >/dev/null
ok "namespace $NS, Pod Security Standards enforce=restricted"
# secret NAME KEY=VALUE…: applied from stdin, values never in argv.
secret() {
	local name=$1 kv
	shift
	{
		printf 'apiVersion: v1\nkind: Secret\nmetadata:\n  name: %s\n  namespace: %s\ntype: Opaque\nstringData:\n' "$name" "$NS"
		for kv in "$@"; do printf '  %s: "%s"\n' "${kv%%=*}" "${kv#*=}"; done
	} | k apply -f - >/dev/null
}
if k -n "$NS" get secret hls-vod-postgres >/dev/null 2>&1; then
	ok "secrets exist: kept (the database was initialised with them)"
else
	db_user=hls_vod db_name=hls_vod
	db_pass=$(openssl rand -hex 24)
	secret hls-vod-postgres "POSTGRES_USER=$db_user" "POSTGRES_PASSWORD=$db_pass" "POSTGRES_DB=$db_name"
	secret hls-vod-db "DATABASE_URL=postgres://$db_user:$db_pass@postgres:5432/$db_name?sslmode=disable"
	secret hls-vod-api "JWT_SIGNING_KEY=$(openssl rand -base64 48 | tr -d '\n')" \
		"MEDIA_SIGNING_KEY=$(openssl rand -base64 48 | tr -d '\n')"
	unset db_pass
	ok "generated (openssl rand) and applied: hls-vod-postgres, hls-vod-db, hls-vod-api"
fi
k -n "$NS" create configmap hls-vod-transcoder-image --from-literal=TRANSCODER_IMAGE="$transcoder_image" \
	--dry-run=client -o yaml | k apply -f - >/dev/null

step "5. kubectl apply -k deploy/overlays/local"
# Pods already running an older build of the same tag are restarted below.
existed=0
k -n "$NS" get deploy api >/dev/null 2>&1 && existed=1
k apply -k deploy/overlays/local >/dev/null
ok "applied"
k -n "$NS" rollout status statefulset/postgres --timeout=180s >/dev/null
ok "postgres ready"
if ((changed && existed)); then
	for d in api web media-delivery dispatcher; do k -n "$NS" rollout restart "deploy/$d" >/dev/null; done
	ok "new images: every Deployment restarted"
fi
# The api's init container applies the migrations: the watcher needs the tables.
k -n "$NS" rollout status deploy/api --timeout=300s >/dev/null || die "deploy/api never became ready"
ok "deploy/api ready (migrations applied by its init container)"
# The first watcher run creates inbox/, sources/ and library/ on the new
# volume; media-delivery's readiness lists library/.
job=watcher-up-$(date +%s)
k -n "$NS" create job "$job" --from=cronjob/watcher >/dev/null
wait_job "$job" 180 || { k -n "$NS" logs "job/$job" >&2 || true; die "the watcher run failed"; }
ok "watcher ran once: inbox/, sources/, library/ exist"
for d in web media-delivery dispatcher; do
	k -n "$NS" rollout status "deploy/$d" --timeout=300s >/dev/null || die "deploy/$d never became ready"
	ok "deploy/$d ready"
done

step "6. admin account"
pwfile=$STATE/admin-password
if [[ -s $pwfile ]]; then
	ok "already created; the password is in $pwfile"
else
	# useradmin reads the password from stdin when it is not a terminal: a
	# one-off Job whose container waits on stdin, fed through kubectl attach.
	job=useradmin-$(date +%s)
	k -n "$NS" apply -f - >/dev/null <<EOF
apiVersion: batch/v1
kind: Job
metadata:
  name: $job
  namespace: $NS
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 300
  template:
    metadata:
      labels:
        app: useradmin
    spec:
      restartPolicy: Never
      automountServiceAccountToken: false
      enableServiceLinks: false
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: useradmin
          image: $PREFIX/app:$TAG
          imagePullPolicy: Never
          command: ["/usr/local/bin/useradmin", "add", "--role", "admin", "admin"]
          stdin: true
          stdinOnce: true
          env:
            - name: DATABASE_URL
              valueFrom:
                secretKeyRef:
                  name: hls-vod-db
                  key: DATABASE_URL
          resources:
            requests: { cpu: 50m, memory: 64Mi }
            limits: { cpu: "1", memory: 256Mi }
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities:
              drop: ["ALL"]
EOF
	k -n "$NS" wait --for=jsonpath='{.status.ready}'=1 "job/$job" --timeout=120s >/dev/null
	pod=$(k -n "$NS" get pods -l "job-name=$job" -o jsonpath='{.items[0].metadata.name}')
	password=$(openssl rand -base64 18)
	printf '%s\n' "$password" | k -n "$NS" attach -i -q "$pod" -c useradmin >/dev/null
	wait_job "$job" 60 || { k -n "$NS" logs "$pod" >&2 || true; die "useradmin failed"; }
	(umask 077 && printf '%s\n' "$password" >"$pwfile")
	ok "user admin (role admin) created by a one-off useradmin Job"
	echo "  admin password: $password   (also in $pwfile)"
fi

printf '\nThe site: http://localhost:%s   (log in as admin, password in %s)\n' "$HOST_PORT" "$pwfile"
printf 'kubectl:  export KUBECONFIG=%s\n' "$KUBECONFIG"
