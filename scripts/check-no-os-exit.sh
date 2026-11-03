#!/usr/bin/env bash
# check-no-os-exit.sh — guard against new os.Exit() calls outside cmd/itervox/exit.go.
#
# Use fatalExit(code) instead. fatalExit runs the runtime-file cleanup
# (onFatalExit: daemon.pid, dashboard_url, HEARTBEAT.md) that os.Exit would
# skip, so every explicit exit leaves no stale evidence of a live daemon.
#
# CLAUDE.md invariant: no `os.Exit` in cmd/itervox/ outside exit.go.
#
# Run via Makefile or pre-commit hook. Exits 0 if clean, 1 if violations found.

set -euo pipefail

repo_root=$(cd "$(dirname "$0")/.." && pwd)
target_dir="$repo_root/cmd/itervox"

# Find os.Exit( calls in *.go files, excluding the exit.go authoritative site
# and *_test.go files (tests sometimes need explicit exit semantics).
violations=$(grep -rn 'os\.Exit(' "$target_dir" --include='*.go' \
  | grep -v '/exit\.go:' \
  | grep -v '_test\.go:' \
  || true)

if [ -n "$violations" ]; then
  echo "ERROR: os.Exit() found in cmd/itervox/ outside exit.go — use fatalExit() instead." >&2
  echo "" >&2
  echo "$violations" >&2
  echo "" >&2
  echo "See cmd/itervox/exit.go for rationale (the runtime-file cleanup os.Exit skips)." >&2
  exit 1
fi

echo "check-no-os-exit: clean (no os.Exit outside exit.go)"
