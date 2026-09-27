#!/usr/bin/env bash
#
# Parse .itervox/HEARTBEAT.md and publish its counters. Run from a systemd
# timer every 60 s (see README.md).
#
# HEARTBEAT.md is the content view of the daemon (queue pressure, dependency
# audit, backends, attention). The daemon ALSO serves Prometheus /metrics when
# server.metrics.enabled is true (internal/metrics/exposition.go); scrape that
# for worker/tracker/outbox counters and use this script for what only the
# heartbeat carries (degraded startup, dependency audit, backend breakers).
#
# Usage:
#   heartbeat-metrics.sh --cloud gcp|aws|azure|prometheus --heartbeat <path>
#                        [--instance <name>] [--textfile <path>]
#
#   --instance   label/dimension identifying this daemon (default: $ITERVOX_METRICS_INSTANCE,
#                else the host name). Every series carries it, so two daemons on
#                two VMs (or two repos on one) never collapse into one series.
#   --textfile   prometheus mode: node_exporter textfile-collector output,
#                written atomically (default /var/lib/node_exporter/textfile_collector/itervox.prom)
#
# ITERVOX_METRICS_DRY_RUN=1 prints every series in Prometheus text format and
# exits without touching any cloud API (--cloud is then optional).
#
# Fields parsed (cmd/itervox/heartbeat.go renderHeartbeat; lines that the
# writer omits while zero are reported as 0, never as an error):
#   Daemon: running|degraded             → degraded
#   ## Capacity   Running a/b, Automation queue a/b, Producers paused, Saturated
#   ## Dependency Audit  Blocked, Unknown, Unblocked, Recently unblocked,
#                        Cycles (only when >0), Attention (only when >0)
#   ## Backends   "- <backend>[@host]: ok|limited|probing ..." lines, Auto-switched issues
#   ## Attention  Input required, Retry queue, Outbox pending / Outbox degraded
#                 (only when >0), Last crash (present → 1)
#
set -euo pipefail

CLOUD=""
HEARTBEAT=""
INSTANCE="${ITERVOX_METRICS_INSTANCE:-}"
TEXTFILE="/var/lib/node_exporter/textfile_collector/itervox.prom"
DRY_RUN="${ITERVOX_METRICS_DRY_RUN:-0}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --cloud)     CLOUD="${2:?}"; shift 2 ;;
    --heartbeat) HEARTBEAT="${2:?}"; shift 2 ;;
    --instance)  INSTANCE="${2:?}"; shift 2 ;;
    --textfile)  TEXTFILE="${2:?}"; shift 2 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

[[ -n "$HEARTBEAT" && ( -n "$CLOUD" || "$DRY_RUN" == 1 ) ]] || {
  echo "usage: $0 --cloud gcp|aws|azure|prometheus --heartbeat <path> [--instance <name>]" >&2; exit 2; }

if [[ ! -f "$HEARTBEAT" ]]; then
  echo "heartbeat file not found: $HEARTBEAT" >&2
  exit 1
fi

if [[ -z "$INSTANCE" ]]; then
  INSTANCE="$(hostname 2>/dev/null || uname -n)"
fi
INSTANCE="${INSTANCE:-unknown}"

# ── parse ───────────────────────────────────────────────────────────────────
# field NAME — value after "- NAME:" (first match), empty when absent.
field() { grep -m1 "^- $1:" "$HEARTBEAT" | sed "s/^- $1: *//" || true; }
# num VALUE — the leading integer of VALUE, or 0.
num() { local v; v="$(printf '%s' "$1" | sed -nE 's/^([0-9]+).*/\1/p')"; echo "${v:-0}"; }
bool01() { [[ "$1" == "true" ]] && echo 1 || echo 0; }
# section NAME — the lines of "## NAME" up to the next heading.
section() { awk -v s="## $1" '$0 == s {on=1; next} /^## / {on=0} on' "$HEARTBEAT"; }

running_line="$(field 'Running')"
queue_line="$(field 'Automation queue')"
RUNNING="$(num "${running_line%%/*}")"
CAPACITY="$(num "${running_line#*/}")"
QUEUE="$(num "${queue_line%%/*}")"
QUEUE_MAX="$(num "${queue_line#*/}")"
[[ "$running_line" == */* ]] || CAPACITY=0
[[ "$queue_line" == */* ]] || QUEUE_MAX=0

# Backend keys may contain ':' (host:port), so match the status after ": ".
backend_lines="$(section 'Backends' | grep -Ev '^- (none reported|Auto-switched issues:)' | grep -E '^- ' || true)"
count_status() {
  if [[ -z "$backend_lines" ]]; then echo 0; return; fi
  grep -cE ": $1([ ,(]|\$)" <<<"$backend_lines" || true
}

DEGRADED=0
grep -q '^Daemon: degraded' "$HEARTBEAT" && DEGRADED=1
LAST_CRASH=0
grep -q '^- Last crash:' "$HEARTBEAT" && LAST_CRASH=1

# HEARTBEAT.md is only rewritten when state is dirty AND the 15 s throttle has
# elapsed, so an idle-but-healthy daemon legitimately stops touching it.
# Age is reported as a gauge for context — do NOT alert on it as liveness
# (use /api/v1/ready or itervox_event_loop_last_idle_timestamp_seconds).
NOW="$(date +%s)"
MTIME="$(stat -c %Y "$HEARTBEAT" 2>/dev/null || stat -f %m "$HEARTBEAT")"
AGE=$(( NOW - MTIME ))

metrics=(
  "running=$RUNNING"
  "capacity=$CAPACITY"
  "queue_depth=$QUEUE"
  "queue_max=$QUEUE_MAX"
  "saturated=$(bool01 "$(field 'Saturated')")"
  "producers_paused=$(bool01 "$(field 'Producers paused')")"
  "deps_blocked=$(num "$(field 'Blocked')")"
  "deps_unknown=$(num "$(field 'Unknown')")"
  "deps_unblocked=$(num "$(field 'Unblocked')")"
  "deps_recently_unblocked=$(num "$(field 'Recently unblocked')")"
  "deps_cycles=$(num "$(field 'Cycles')")"
  "deps_attention=$(num "$(field 'Attention')")"
  "backends_limited=$(count_status limited)"
  "backends_probing=$(count_status probing)"
  "auto_switched=$(num "$(field 'Auto-switched issues')")"
  "input_required=$(num "$(field 'Input required')")"
  "retry_queue=$(num "$(field 'Retry queue')")"
  "outbox_pending=$(num "$(field 'Outbox pending')")"
  "outbox_degraded=$(num "$(field 'Outbox degraded')")"
  "degraded=$DEGRADED"
  "last_crash=$LAST_CRASH"
  "heartbeat_age_seconds=$AGE"
)

# Prometheus label values escape \ " and newline.
prom_label() { local v="${1//\\/\\\\}"; v="${v//\"/\\\"}"; printf '%s' "${v//$'\n'/\\n}"; }

prom_text() {
  local m key
  for m in "${metrics[@]}"; do
    key="itervox_heartbeat_${m%%=*}"
    printf '# TYPE %s gauge\n%s{instance="%s"} %s\n' "$key" "$key" "$(prom_label "$INSTANCE")" "${m#*=}"
  done
}

if [[ "$DRY_RUN" == 1 ]]; then
  prom_text
  exit 0
fi

# ── push ────────────────────────────────────────────────────────────────────
case "$CLOUD" in
  prometheus)
    # node_exporter --collector.textfile.directory=<dir of --textfile>.
    # Same-directory temp file + rename: the collector never reads a partial file.
    tmp="$(mktemp "${TEXTFILE}.XXXXXX")"
    prom_text > "$tmp"
    chmod 0644 "$tmp"
    mv -f "$tmp" "$TEXTFILE"
    ;;
  gcp)
    # Cloud Monitoring has no CLI write path for custom metrics; use the API.
    # Series are written against the gce_instance resource (so they sit next
    # to the VM's own metrics) and also carry an `instance` metric label.
    md() { curl -fsS -H 'Metadata-Flavor: Google' "http://metadata.google.internal/computeMetadata/v1/$1"; }
    TOKEN="$(gcloud auth print-access-token)"
    PROJECT="$(md project/project-id)"
    INSTANCE_ID="$(md instance/id)"
    ZONE="$(md instance/zone | sed 's#.*/##')"
    NOW_RFC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    for m in "${metrics[@]}"; do
      key="${m%%=*}"; val="${m#*=}"
      curl -fsS -X POST \
        "https://monitoring.googleapis.com/v3/projects/$PROJECT/timeSeries" \
        -H "Authorization: Bearer $TOKEN" \
        -H "Content-Type: application/json" \
        -d "{\"timeSeries\":[{
              \"metric\":{\"type\":\"custom.googleapis.com/itervox/$key\",
                          \"labels\":{\"instance\":\"$(prom_label "$INSTANCE")\"}},
              \"resource\":{\"type\":\"gce_instance\",\"labels\":{
                  \"project_id\":\"$PROJECT\",\"instance_id\":\"$INSTANCE_ID\",\"zone\":\"$ZONE\"}},
              \"points\":[{\"interval\":{\"endTime\":\"$NOW_RFC\"},
                           \"value\":{\"int64Value\":\"$val\"}}]}]}" >/dev/null
    done
    ;;
  aws)
    ARGS=()
    for m in "${metrics[@]}"; do
      ARGS+=("MetricName=${m%%=*},Value=${m#*=},Unit=Count,Dimensions=[{Name=Instance,Value=$INSTANCE}]")
    done
    aws cloudwatch put-metric-data --namespace Itervox --metric-data "${ARGS[@]}"
    ;;
  azure)
    # Azure custom metrics go through the regional ingestion endpoint; simplest
    # reliable path from a VM is a Log Analytics custom table via the AMA, so
    # emit structured lines the agent picks up instead of calling the API here.
    for m in "${metrics[@]}"; do
      logger -t itervox-metrics "itervox_metric instance=$INSTANCE ${m%%=*}=${m#*=}"
    done
    ;;
  *) echo "unknown cloud: $CLOUD" >&2; exit 2 ;;
esac
