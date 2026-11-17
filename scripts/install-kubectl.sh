#!/usr/bin/env bash
# make kind-tools: install kubectl VERSION into DIR, the release binary from
# dl.k8s.io checked against its published SHA-256. Nothing is done when
# that version is already the one on PATH.
set -euo pipefail

version=${1:?usage: install-kubectl.sh VERSION DIR}
dir=${2:?usage: install-kubectl.sh VERSION DIR}

have=$(kubectl version --client -o json 2>/dev/null | sed -n 's/.*"gitVersion": *"\(v[0-9.]*\)".*/\1/p' | head -n1 || true)
if [[ $have == "$version" ]]; then
	echo "kubectl $version already installed ($(command -v kubectl))"
	exit 0
fi

tmp=$(mktemp -d)
trap 'rm -f "$tmp/kubectl"; rmdir "$tmp"' EXIT
url=https://dl.k8s.io/release/$version/bin/$(go env GOOS)/$(go env GOARCH)/kubectl
curl -fsSL -o "$tmp/kubectl" "$url"
echo "$(curl -fsSL "$url.sha256")  $tmp/kubectl" | sha256sum --check --quiet
mkdir -p "$dir"
install -m 0755 "$tmp/kubectl" "$dir/kubectl"
echo "kubectl $version installed into $dir (checksum verified)"
