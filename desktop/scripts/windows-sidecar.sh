#!/usr/bin/env bash
# Windows (#260): builds the native itervox daemon as the app's sidecar
# when the daemon builds for Windows (#259), and prints the Tauri `--config`
# that bundles it; prints nothing when it does not build yet. The app then
# ships without a local daemon and tells the user that local projects need
# WSL or a remote daemon. Once the daemon builds for Windows, this activates
# by itself: no workflow change is needed.
#
# Usage: desktop/scripts/windows-sidecar.sh [triple]   (default x86_64-pc-windows-msvc)
# In GitHub Actions the config also goes to the step output `config`.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TRIPLE="${1:-x86_64-pc-windows-msvc}"
CONFIG='{"bundle":{"externalBin":["binaries/itervox"]}}'

# The daemon embeds the dashboard.
if [ ! -f "$ROOT/internal/server/web/dist/index.html" ]; then
  (cd "$ROOT/web" && pnpm install --frozen-lockfile && pnpm build)
fi

config=""
if bash "$ROOT/desktop/scripts/build-sidecar.sh" "$TRIPLE"; then
  config="$CONFIG"
  echo "windows-sidecar: bundling binaries/itervox-$TRIPLE.exe" >&2
else
  msg="the itervox daemon does not build for Windows yet (#259): the app ships without a local daemon"
  if [ -n "${GITHUB_ACTIONS:-}" ]; then
    echo "::notice::$msg"
  else
    echo "windows-sidecar: $msg" >&2
  fi
fi

if [ -n "${GITHUB_OUTPUT:-}" ]; then
  echo "config=$config" >>"$GITHUB_OUTPUT"
fi
if [ -n "$config" ]; then
  echo "$config"
fi
