#!/usr/bin/env bash
# check-deadcode.sh — CORE-112 guard: no production-unreachable Go function
# outside scripts/deadcode-allowlist.txt.
#
# Runs golang.org/x/tools/cmd/deadcode over the daemon's entry point with the
# `dev` tag (internal/server embeds web/dist otherwise). Override the binary
# with DEADCODE=/path/to/deadcode (e.g. an offline build); the default runs
# the pinned version through `go run`.
#
# Exits 0 when every finding is allowlisted, 1 otherwise (also when an
# allowlist entry no longer matches a finding, so the list cannot rot).
set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo_root"

DEADCODE_VERSION=${DEADCODE_VERSION:-v0.39.0}
if [[ -n "${DEADCODE:-}" ]]; then
  deadcode_bin=$DEADCODE
else
  # Built for the host first: `go run` under the GOOS below would build a
  # binary this machine cannot execute.
  tooldir=$(mktemp -d)
  trap 'rm -rf "$tooldir"' EXIT
  GOBIN="$tooldir" go install "golang.org/x/tools/cmd/deadcode@${DEADCODE_VERSION}"
  deadcode_bin=$tooldir/deadcode
fi

# The allowlist describes the Linux build (what CI and releases analyse).
# Analyse that build on every host, so Linux-only callers (bubblewrap, the
# /proc parsers) keep their callees reachable when this runs on a Mac.
findings=$(GOOS=linux "$deadcode_bin" -tags dev ./cmd/...)
allow=$(grep -v '^#' scripts/deadcode-allowlist.txt | sed -E 's/[[:space:]]*\|.*$//' | sed '/^[[:space:]]*$/d')

status=0
while IFS= read -r line; do
  [[ -z "$line" ]] && continue
  file=${line%%:*}
  fn=${line##*unreachable func: }
  if ! grep -qxF "$file $fn" <<<"$allow"; then
    echo "check-deadcode: unreachable and not allowlisted: $line" >&2
    status=1
  fi
done <<<"$findings"

while IFS= read -r entry; do
  [[ -z "$entry" ]] && continue
  file=${entry%% *}
  fn=${entry#* }
  if ! grep -qF "$file:" <<<"$findings" || ! grep -q "^$file:.*unreachable func: ${fn}\$" <<<"$findings"; then
    echo "check-deadcode: stale allowlist entry (no longer reported): $entry" >&2
    status=1
  fi
done <<<"$allow"

if [[ $status -eq 0 ]]; then
  echo "check-deadcode: clean ($(grep -c . <<<"$allow") allowlisted)"
fi
exit $status
