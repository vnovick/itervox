#!/usr/bin/env bash
#
# Parse .itervox/HEARTBEAT.md and push its counters as cloud custom metrics.
# Run from cron/systemd-timer every 60s.
#
# Itervox exposes no metrics endpoint — there is no Prometheus, OpenTelemetry,
# or expvar surface anywhere in internal/ or cmd/ — so HEARTBEAT.md is the only
# machine-readable view of queue pressure and attention state.
#
# Usage: ./heartbeat-metrics.sh --cloud gcp|aws|azure --heartbeat /srv/itervox/<repo>/.itervox/HEARTBEAT.md
#
set -euo pipefail

CLOUD=""
HEARTBEAT=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --cloud)     CLOUD="$2"; shift 2 ;;
    --heartbeat) HEARTBEAT="$2"; shift 2 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

[[ -n "$CLOUD" && -n "$HEARTBEAT" ]] || {
  echo "usage: $0 --cloud gcp|aws|azure --heartbeat <path>" >&2; exit 2; }

if [[ ! -f "$HEARTBEAT" ]]; then
  echo "heartbeat file not found: $HEARTBEAT" >&2
  exit 1
fi

# ── parse ───────────────────────────────────────────────────────────────────
# Format is stable Markdown written by cmd/itervox/heartbeat.go.
field() { grep -m1 "^- $1:" "$HEARTBEAT" | sed "s/^- $1: *//" || true; }

RUNNING="$(field 'Running'        | cut -d/ -f1)"
CAPACITY="$(field 'Running'       | cut -d/ -f2)"
QUEUE="$(field 'Automation queue' | cut -d/ -f1)"
QUEUE_MAX="$(field 'Automation queue' | cut -d/ -f2)"
SATURATED="$(field 'Saturated')"
PAUSED="$(field 'Producers paused')"
INPUT_REQUIRED="$(field 'Input required')"
RETRY_QUEUE="$(field 'Retry queue')"
BLOCKED="$(field 'Blocked')"

DEGRADED=0
grep -q '^Daemon: degraded' "$HEARTBEAT" && DEGRADED=1

bool01() { [[ "$1" == "true" ]] && echo 1 || echo 0; }
SATURATED_N="$(bool01 "${SATURATED:-false}")"
PAUSED_N="$(bool01 "${PAUSED:-false}")"

# HEARTBEAT.md is only rewritten when state is dirty AND the 15s throttle has
# elapsed, so an idle-but-healthy daemon legitimately stops touching it.
# Age is reported as a gauge for context — do NOT alert on it as liveness.
NOW="$(date +%s)"
MTIME="$(stat -c %Y "$HEARTBEAT" 2>/dev/null || stat -f %m "$HEARTBEAT")"
AGE=$(( NOW - MTIME ))

metrics=(
  "running=${RUNNING:-0}"
  "capacity=${CAPACITY:-0}"
  "queue_depth=${QUEUE:-0}"
  "queue_max=${QUEUE_MAX:-0}"
  "saturated=$SATURATED_N"
  "producers_paused=$PAUSED_N"
  "input_required=${INPUT_REQUIRED:-0}"
  "retry_queue=${RETRY_QUEUE:-0}"
  "deps_blocked=${BLOCKED:-0}"
  "degraded=$DEGRADED"
  "heartbeat_age_seconds=$AGE"
)

# ── push ────────────────────────────────────────────────────────────────────
case "$CLOUD" in
  gcp)
    # Cloud Monitoring has no CLI write path for custom metrics; use the API.
    TOKEN="$(gcloud auth print-access-token)"
    PROJECT="$(curl -s -H 'Metadata-Flavor: Google' \
      http://metadata.google.internal/computeMetadata/v1/project/project-id)"
    NOW_RFC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    for m in "${metrics[@]}"; do
      key="${m%%=*}"; val="${m#*=}"
      curl -s -X POST \
        "https://monitoring.googleapis.com/v3/projects/$PROJECT/timeSeries" \
        -H "Authorization: Bearer $TOKEN" \
        -H "Content-Type: application/json" \
        -d "{\"timeSeries\":[{
              \"metric\":{\"type\":\"custom.googleapis.com/itervox/$key\"},
              \"resource\":{\"type\":\"global\"},
              \"points\":[{\"interval\":{\"endTime\":\"$NOW_RFC\"},
                           \"value\":{\"int64Value\":\"$val\"}}]}]}" >/dev/null
    done
    ;;
  aws)
    ARGS=()
    for m in "${metrics[@]}"; do
      ARGS+=("MetricName=${m%%=*},Value=${m#*=},Unit=Count")
    done
    aws cloudwatch put-metric-data --namespace Itervox --metric-data "${ARGS[@]}"
    ;;
  azure)
    # Azure custom metrics go through the regional ingestion endpoint; simplest
    # reliable path from a VM is a Log Analytics custom table via the AMA, so
    # emit structured lines the agent picks up instead of calling the API here.
    for m in "${metrics[@]}"; do
      logger -t itervox-metrics "itervox_metric ${m%%=*}=${m#*=}"
    done
    ;;
  *) echo "unknown cloud: $CLOUD" >&2; exit 2 ;;
esac
