#!/usr/bin/env bash
#
# CORE-113: every metric family an alert rule in prometheus-rules.yml reads
# must be one the daemon really exports (internal/metrics/exposition.go) or
# one heartbeat-metrics.sh writes (itervox_heartbeat_<key>). A renamed family
# otherwise turns its alert silently into "no data, never fires".
# Run: make deploy-test.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
RULES="$HERE/prometheus-rules.yml"
EXPO="$ROOT/internal/metrics/exposition.go"

if [[ ! -f "$EXPO" ]]; then
  echo "rules_names_test: SKIP (no internal/metrics next to deploy/)"; exit 0
fi

exported="$( { grep -oE '"itervox_[a-z_]+"' "$EXPO" | tr -d '"'
  sed -nE 's/^[[:space:]]*"([a-z_]+)=.*/itervox_heartbeat_\1/p' "$HERE/heartbeat-metrics.sh"; } | sort -u)"
used="$(grep -E '^[[:space:]]*expr:' "$RULES" | grep -oE 'itervox_[a-z_]+' | sort -u)"

fails=0
while IFS= read -r m; do
  if grep -qxF "$m" <<<"$exported"; then
    printf 'ok   %s\n' "$m"
  else
    printf 'FAIL %s is used by an alert but exported by neither /metrics nor heartbeat-metrics.sh\n' "$m"
    fails=$((fails + 1))
  fi
done <<<"$used"

if grep -nE 'expr:.*(heartbeat_age|mtime)' "$RULES"; then
  echo "FAIL an alert reads HEARTBEAT.md age/mtime, which is not a liveness signal"
  fails=$((fails + 1))
else
  echo "ok   no alert reads heartbeat age/mtime"
fi

[[ $fails -eq 0 ]] || { echo "rules_names_test: $fails FAILED"; exit 1; }
echo "rules_names_test: PASS ($(wc -l <<<"$used" | tr -d ' ') families)"
