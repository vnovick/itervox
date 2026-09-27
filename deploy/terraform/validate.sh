#!/usr/bin/env bash
#
# CORE-106/CORE-105: validate every OpenTofu module and example under
# deploy/terraform without touching the source tree or any cloud API:
#   tofu fmt -check -recursive        (on the tree, read-only)
#   tofu init -backend=false          (in a temp copy; downloads pinned providers)
#   tofu validate
# Runs tofu from the official image, so the only host requirement is docker.
#
# Usage: deploy/terraform/validate.sh [module-or-example-dir ...]
#   default: every dir containing a versions.tf or examples/*/main.tf
#   TOFU_IMAGE overrides the image (default ghcr.io/opentofu/opentofu:1.9.0).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE="${TOFU_IMAGE:-ghcr.io/opentofu/opentofu:1.9.0}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

tofu() { docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -e TF_IN_AUTOMATION=1 -e TF_PLUGIN_CACHE_DIR=/cache \
  -v "$WORK/tree:/tf" -v "$WORK/cache:/cache" -w "/tf/$1" "$IMAGE" "${@:2}"; }

mkdir -p "$WORK/cache"
cp -R "$HERE" "$WORK/tree"

echo "== tofu fmt -check -recursive"
docker run --rm -v "$HERE:/tf:ro" -w /tf "$IMAGE" fmt -check -recursive -diff
echo "fmt: ok"

if [[ $# -gt 0 ]]; then
  dirs=("$@")
else
  dirs=()
  while IFS= read -r d; do dirs+=("$d"); done < <(
    cd "$HERE" && { find . -name versions.tf -not -path '*/.terraform/*' -exec dirname {} \; ;
                    find . -path '*/examples/*' -name main.tf -not -path '*/.terraform/*' -exec dirname {} \; ; } \
      | sed 's#^\./##' | sort -u)
fi

rc=0
for d in "${dirs[@]}"; do
  echo "== $d"
  if tofu "$d" init -backend=false -input=false -no-color >"$WORK/init.log" 2>&1; then
    grep -E '^- Installed|^- Using' "$WORK/init.log" || true
  else
    cat "$WORK/init.log"; rc=1; continue
  fi
  tofu "$d" validate -no-color || rc=1
done
exit "$rc"
