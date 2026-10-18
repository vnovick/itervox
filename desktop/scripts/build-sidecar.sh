#!/usr/bin/env bash
# Builds the itervox daemon as the Tauri sidecar of the menu-bar app (#128).
#
# Tauri bundles `externalBin: binaries/itervox` from
# desktop/src-tauri/binaries/itervox-<rust target triple>, so this script
# builds one Go binary per requested triple and names it that way. The web
# dashboard must already be built into internal/server/web/dist (the daemon
# embeds it); `make desktop-sidecar` runs `make web-build` first.
#
# Usage:
#   desktop/scripts/build-sidecar.sh                 # both macOS triples (+ universal when lipo exists)
#   desktop/scripts/build-sidecar.sh --host          # the current machine's triple only
#   desktop/scripts/build-sidecar.sh <triple>...     # explicit triples
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="$ROOT/desktop/src-tauri/binaries"

if [ ! -f "$ROOT/internal/server/web/dist/index.html" ]; then
  echo "build-sidecar: internal/server/web/dist is empty — run 'make web-build' first" >&2
  exit 1
fi

host_triple() {
  rustc -vV | sed -n 's/^host: //p'
}

goos_goarch() {
  case "$1" in
    aarch64-apple-darwin) echo "darwin arm64" ;;
    x86_64-apple-darwin) echo "darwin amd64" ;;
    x86_64-unknown-linux-gnu) echo "linux amd64" ;;
    aarch64-unknown-linux-gnu) echo "linux arm64" ;;
    *) echo "build-sidecar: unsupported target triple $1" >&2; return 1 ;;
  esac
}

triples=()
if [ "$#" -eq 0 ]; then
  triples=(aarch64-apple-darwin x86_64-apple-darwin)
elif [ "$1" = "--host" ]; then
  triples=("$(host_triple)")
else
  triples=("$@")
fi

VERSION="$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)"
VERSION="${VERSION#v}"
COMMIT="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo none)"
DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.date=$DATE"

mkdir -p "$OUT"
for triple in "${triples[@]}"; do
  read -r goos goarch < <(goos_goarch "$triple")
  dest="$OUT/itervox-$triple"
  echo "build-sidecar: $triple (GOOS=$goos GOARCH=$goarch) -> ${dest#"$ROOT"/}"
  (cd "$ROOT" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$dest" ./cmd/itervox)
done

# `tauri build --target universal-apple-darwin` looks for a universal sidecar.
if [ -f "$OUT/itervox-aarch64-apple-darwin" ] && [ -f "$OUT/itervox-x86_64-apple-darwin" ] \
  && command -v lipo >/dev/null 2>&1; then
  lipo -create -output "$OUT/itervox-universal-apple-darwin" \
    "$OUT/itervox-aarch64-apple-darwin" "$OUT/itervox-x86_64-apple-darwin"
  echo "build-sidecar: universal-apple-darwin (lipo)"
fi
