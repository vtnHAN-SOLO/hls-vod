#!/usr/bin/env bash
# make kind-down: delete the kind cluster (its node, volumes and all data
# with it) and the files kind-up wrote under var/kind/.
set -euo pipefail

CLUSTER=${KIND_CLUSTER:-hls-vod}
STATE=var/kind

if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
	kind delete cluster --name "$CLUSTER" --kubeconfig "$STATE/kubeconfig"
else
	echo "no kind cluster named $CLUSTER"
fi
for f in kubeconfig admin-password images; do
	rm -f "$STATE/$f"
done
[[ -d $STATE ]] && rmdir "$STATE" 2>/dev/null || true
echo "removed $STATE/ (kubeconfig, admin password)"
