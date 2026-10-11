#!/usr/bin/env bash
#
# Release check (#137): a real cloud deployment with the deploy kit, end to
# end, then teardown. BILLABLE: it creates a GCP VM, a data disk and (unless
# they already exist) a Cloud Router + Cloud NAT and an IAP firewall rule, for
# the few minutes the check runs.
#
# Follows the deploy skill (.claude/skills/deploy/SKILL.md) with its own
# commands: deploy/gcp/provision.sh, deploy/bootstrap.sh at the release under
# test, `itervox secret set` for the agent credential, `itervox doctor
# --deploy`, `systemctl enable --now itervox`, and the IAP tunnel to the
# dashboard. The project on the VM uses `tracker.kind: local`, so no tracker
# credential is involved.
#
# Usage:
#   deploy/acceptance/gcp-deploy-smoke.sh --version <tag> [--project <id>]
#       [--zone us-central1-a] [--name itervox-smoke-<time>]
#       [--machine e2-standard-2] [--disk 30]
#       [--repo https://github.com/vnovick/itervox.git] [--keep]
#
# Needs: gcloud (logged in, with a project), curl; ANTHROPIC_API_KEY or
# CLAUDE_CODE_OAUTH_TOKEN, which reaches the VM on stdin and is never printed
# or put on a command line. --keep skips the teardown (to debug a failure; you
# then delete the resources by hand: the summary lists them).
#
# Exit: 0 every step passed and everything was torn down; 1 a step failed
# (teardown still ran); 2 usage error; 3 the check passed but the teardown did
# not finish (delete what the summary lists by hand).
set -euo pipefail

VERSION=""
PROJECT=""
ZONE="us-central1-a"
NAME="itervox-smoke-$(date -u +%Y%m%d%H%M%S)"
MACHINE="e2-standard-2"
DISK_GB=30
REPO="https://github.com/vnovick/itervox.git"
KEEP=false
TUNNEL_PORT="${ITERVOX_SMOKE_TUNNEL_PORT:-18090}"
WAIT_SECONDS="${ITERVOX_SMOKE_WAIT_SECONDS:-10}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) VERSION="$2"; shift 2 ;;
    --project) PROJECT="$2"; shift 2 ;;
    --zone)    ZONE="$2"; shift 2 ;;
    --name)    NAME="$2"; shift 2 ;;
    --machine) MACHINE="$2"; shift 2 ;;
    --disk)    DISK_GB="$2"; shift 2 ;;
    --repo)    REPO="$2"; shift 2 ;;
    --keep)    KEEP=true; shift ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

die() { echo "gcp-deploy-smoke: $*" >&2; exit 2; }

[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || die "--version <tag> is required (e.g. v0.2.4)"
# `itervox secret` and `tracker.kind: local` first shipped in v0.2.2; on an
# older release `itervox secret` is not a command and starts the daemon.
[[ ! "$VERSION" =~ ^v0\.(0|1)\. && ! "$VERSION" =~ ^v0\.2\.(0|1)(-|$) ]] || die "$VERSION predates itervox secret; the check needs v0.2.2 or later"
[[ "$NAME" =~ ^[a-z]([-a-z0-9]{0,40}[a-z0-9])?$ ]] || die "--name must be a valid GCE instance name"
for tool in gcloud curl; do
  command -v "$tool" >/dev/null || die "$tool is not on PATH"
done
if [[ -n "${ANTHROPIC_API_KEY:-}" ]]; then
  AGENT_KEY_NAME=ANTHROPIC_API_KEY
elif [[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]]; then
  AGENT_KEY_NAME=CLAUDE_CODE_OAUTH_TOKEN
else
  die "set ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN (doctor --deploy fails without an agent credential)"
fi
if [[ -n "$PROJECT" ]]; then
  export CLOUDSDK_CORE_PROJECT="$PROJECT"
fi

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
KIT="$(cd "$HERE/.." && pwd)" # deploy/
REGION="${ZONE%-*}"
REPO_NAME="$(basename "${REPO%.git}")"
WORKDIR="/srv/itervox/$REPO_NAME"
AS_SVC="sudo -H -u itervox bash -c"

step() { echo; echo "==> $*"; }
vm() { gcloud compute ssh "$NAME" --tunnel-through-iap --zone="$ZONE" --quiet --command "$1"; }

# ── teardown ────────────────────────────────────────────────────────────────
# The shared router/NAT and firewall rule are removed only when this run
# created them: another deployment in the project may use them.
had_router=false; had_nat=false; had_fw=false
gcloud compute routers describe itervox-router --region="$REGION" --format='value(name)' >/dev/null 2>&1 && had_router=true
gcloud compute routers nats describe itervox-nat --router=itervox-router --region="$REGION" --format='value(name)' >/dev/null 2>&1 && had_nat=true
gcloud compute firewall-rules describe allow-iap-ssh --format='value(name)' >/dev/null 2>&1 && had_fw=true

TUNNEL_PID=""
LEFT=()
teardown() {
  local rc=$?
  if [[ -n "$TUNNEL_PID" ]]; then kill "$TUNNEL_PID" 2>/dev/null || true; fi
  if $KEEP; then
    echo
    echo "==> --keep: NOT torn down. Delete by hand: instance $NAME and disk $NAME-data ($ZONE)$($had_router || echo ", router itervox-router ($REGION)")$($had_fw || echo ", firewall rule allow-iap-ssh")"
    exit "$rc"
  fi
  step "teardown"
  gcloud compute instances delete "$NAME" --zone="$ZONE" --quiet >/dev/null 2>&1 \
    || { gcloud compute instances describe "$NAME" --zone="$ZONE" >/dev/null 2>&1 && LEFT+=("instance $NAME ($ZONE)"); }
  gcloud compute disks delete "$NAME-data" --zone="$ZONE" --quiet >/dev/null 2>&1 \
    || { gcloud compute disks describe "$NAME-data" --zone="$ZONE" >/dev/null 2>&1 && LEFT+=("disk $NAME-data ($ZONE)"); }
  if ! $had_nat && ! $had_router; then
    gcloud compute routers delete itervox-router --region="$REGION" --quiet >/dev/null 2>&1 \
      || { gcloud compute routers describe itervox-router --region="$REGION" >/dev/null 2>&1 && LEFT+=("router itervox-router ($REGION)"); }
  elif ! $had_nat; then
    gcloud compute routers nats delete itervox-nat --router=itervox-router --region="$REGION" --quiet >/dev/null 2>&1 \
      || LEFT+=("NAT itervox-nat on itervox-router ($REGION)")
  fi
  if ! $had_fw; then
    gcloud compute firewall-rules delete allow-iap-ssh --quiet >/dev/null 2>&1 \
      || { gcloud compute firewall-rules describe allow-iap-ssh >/dev/null 2>&1 && LEFT+=("firewall rule allow-iap-ssh"); }
  fi
  if [[ ${#LEFT[@]} -gt 0 ]]; then
    echo "TEARDOWN INCOMPLETE — delete by hand: ${LEFT[*]}" >&2
    [[ $rc -eq 0 ]] && rc=3
  else
    echo "    removed everything this run created"
  fi
  exit "$rc"
}
trap teardown EXIT

# ── 1. provision ────────────────────────────────────────────────────────────
step "provision $NAME ($MACHINE, ${DISK_GB} GB) in $ZONE"
"$KIT/gcp/provision.sh" --name "$NAME" --zone "$ZONE" --machine "$MACHINE" --disk "$DISK_GB" >/dev/null

step "wait for SSH through IAP"
for i in $(seq 1 30); do
  if vm true >/dev/null 2>&1; then break; fi
  [[ $i -eq 30 ]] && { echo "SSH never came up" >&2; exit 1; }
  sleep "$WAIT_SECONDS"
done

# ── 2. bootstrap ────────────────────────────────────────────────────────────
step "bootstrap $VERSION"
gcloud compute scp --recurse --tunnel-through-iap --zone="$ZONE" --quiet "$KIT" "$NAME:~/deploy" >/dev/null
vm "sudo ~/deploy/bootstrap.sh --repo $REPO --version $VERSION --data-disk /dev/disk/by-id/google-itervox-data"

# ── 3. workflow and secret ──────────────────────────────────────────────────
step "workflow (tracker.kind: local) and the agent credential"
vm "$AS_SVC 'cd $WORKDIR && itervox init --tracker local --force'"
printf '%s' "${!AGENT_KEY_NAME}" | vm "$AS_SVC 'cd $WORKDIR && itervox secret set $AGENT_KEY_NAME --workflow WORKFLOW.md'"
vm "$AS_SVC 'cd $WORKDIR && itervox secret list --workflow WORKFLOW.md'"

# ── 4. doctor --deploy ──────────────────────────────────────────────────────
step "itervox doctor --deploy"
doctor=$(vm "$AS_SVC 'cd $WORKDIR && itervox doctor --deploy --workflow WORKFLOW.md'" 2>&1) || true
echo "$doctor"
grep -q '^deploy: OK' <<<"$doctor" || { echo "doctor --deploy did not end with deploy: OK" >&2; exit 1; }

# ── 5. start ────────────────────────────────────────────────────────────────
step "start the service"
vm "sudo systemctl enable --now itervox"
active=""
for _ in $(seq 1 30); do
  active=$(vm "systemctl is-active itervox" 2>/dev/null || true)
  [[ "$active" == "active" ]] && break
  sleep "$WAIT_SECONDS"
done
[[ "$active" == "active" ]] || { echo "systemctl is-active itervox: $active" >&2; exit 1; }
echo "    systemctl is-active itervox: active"

# The API with the bearer token, checked on the VM so the token never leaves it.
vm "$AS_SVC 'cd $WORKDIR && set -a && . .itervox/.env && set +a && for i in \$(seq 1 30); do curl -fsS -o /dev/null -H \"Authorization: Bearer \$ITERVOX_API_TOKEN\" http://127.0.0.1:8090/api/v1/state && exit 0; sleep 5; done; exit 1'" \
  || { echo "GET /api/v1/state with the token failed on the VM" >&2; exit 1; }
echo "    GET /api/v1/state with the token: 200"

# ── 6. the dashboard through the tunnel ─────────────────────────────────────
step "dashboard through the IAP tunnel (localhost:$TUNNEL_PORT)"
gcloud compute ssh "$NAME" --tunnel-through-iap --zone="$ZONE" --quiet -- -N -L "$TUNNEL_PORT:localhost:8090" >/dev/null 2>&1 &
TUNNEL_PID=$!
page=""
for _ in $(seq 1 30); do
  page=$(curl -fsS "http://127.0.0.1:$TUNNEL_PORT/" 2>/dev/null || true)
  [[ "$page" == *'id="root"'* ]] && break
  sleep "$WAIT_SECONDS"
done
[[ "$page" == *'id="root"'* ]] || { echo "the dashboard did not load through the tunnel" >&2; exit 1; }
curl -fsS -o /dev/null "http://127.0.0.1:$TUNNEL_PORT/api/v1/ready" \
  || { echo "GET /api/v1/ready through the tunnel failed" >&2; exit 1; }
echo "    the dashboard and /api/v1/ready load through the tunnel"

step "deploy smoke: OK ($VERSION on $NAME)"
