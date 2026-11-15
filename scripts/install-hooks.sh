#!/usr/bin/env bash
# Installs tracked git hooks into .git/hooks (which git does not version).
# Run via `make setup` after every fresh clone.
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
hooks_dir="$(git rev-parse --git-path hooks)"
mkdir -p "$hooks_dir"

for src in "$repo_root"/scripts/hooks/*; do
  name="$(basename "$src")"
  install -m 0755 "$src" "$hooks_dir/$name"
  echo "installed $hooks_dir/$name"
done
