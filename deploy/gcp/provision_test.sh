#!/usr/bin/env bash
#
# #154 test for deploy/gcp/provision.sh, against a stubbed `gcloud` CLI on
# PATH (no GCP project, no network). Run: make deploy-test.
#
# Proves:
#   - without --public the run completes: instance, data disk, attach, the
#     IAP firewall rule, the router and the Cloud NAT, in that order;
#   - `routers nats create` gets only flags the real gcloud accepts (the
#     stub refuses any other with gcloud's own "unrecognized arguments"
#     error), including --auto-allocate-nat-external-ips (plural);
#   - existing shared resources (firewall rule, router, NAT) are not
#     created again;
#   - --public creates no firewall rule, router or NAT.

# The stub body is single-quoted on purpose: it expands in the stub.
# shellcheck disable=SC2016
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROVISION="$HERE/provision.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fails=0
pass() { printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; fails=$((fails + 1)); }

mkdir -p "$WORK/bin"
CALLS="$WORK/calls.log"
# gcloud stub: logs every call. `describe` answers from $EXISTING (space-
# separated names that already exist). `routers nats create` accepts only
# the flags `gcloud compute routers nats create` documents for this use and
# refuses anything else, as gcloud does.
cat > "$WORK/bin/gcloud" <<'STUB'
#!/usr/bin/env bash
echo "gcloud $*" >> "$CALLS"
args=" $* "
if [[ "$args" == *" describe "* ]]; then
  for name in ${EXISTING:-}; do
    if [[ "$args" == *" describe $name "* ]]; then echo "$name"; exit 0; fi
  done
  echo "ERROR: (gcloud) NOT_FOUND" >&2
  exit 1
fi
if [[ "$args" == *" routers nats create "* ]]; then
  for a in "$@"; do
    case "$a" in
      compute|routers|nats|create|itervox-nat) ;;
      --router=*|--region=*|--auto-allocate-nat-external-ips|--nat-all-subnet-ip-ranges) ;;
      *)
        echo "ERROR: (gcloud.compute.routers.nats.create) unrecognized arguments: $a" >&2
        exit 2 ;;
    esac
  done
fi
exit 0
STUB
chmod +x "$WORK/bin/gcloud"

run_provision() {
  : > "$CALLS"
  RC=0
  OUT="$(env PATH="$WORK/bin:$PATH" CALLS="$CALLS" "$@" bash "$PROVISION" --name itx --zone us-central1-a 2>&1)" || RC=$?
}
run_provision_args() {
  : > "$CALLS"
  RC=0
  OUT="$(env PATH="$WORK/bin:$PATH" CALLS="$CALLS" bash "$PROVISION" --name itx --zone us-central1-a "$@" 2>&1)" || RC=$?
}
line_of() { { grep -n -m1 -F -- "$1" "$CALLS" || true; } | cut -d: -f1; }
called() { grep -qF -- "$1" "$CALLS"; }

# --- private VM (default): the whole run completes ---------------------------
run_provision EXISTING=""
if [[ $RC -eq 0 ]]; then
  pass "private VM: exit 0"
else
  fail "private VM: exit $RC"
  printf '%s\n' "$OUT" | sed 's/^/    /'
fi
order=(
  "instances create itx"
  "disks create itx-data"
  "instances attach-disk itx"
  "firewall-rules create allow-iap-ssh"
  "routers create itervox-router"
  "routers nats create itervox-nat"
)
prev=0 in_order=true
for c in "${order[@]}"; do
  n="$(line_of "$c")"
  if [[ -z "$n" || "$n" -le "$prev" ]]; then in_order=false; fi
  prev="${n:-0}"
done
if $in_order; then
  pass "private VM: instance, disk, attach, firewall, router, NAT in order"
else
  fail "private VM: calls out of order or missing:"
  sed 's/^/    /' "$CALLS"
fi
if grep -F "routers nats create" "$CALLS" | grep -qF -- "--auto-allocate-nat-external-ips" &&
  grep -F "routers nats create" "$CALLS" | grep -qF -- "--region=us-central1"; then
  pass "NAT: plural --auto-allocate-nat-external-ips, region from the zone"
else
  fail "NAT flags: $(grep -F 'routers nats create' "$CALLS" || echo 'no nats create call')"
fi

# --- shared resources already exist: not created again ----------------------
run_provision EXISTING="allow-iap-ssh itervox-router itervox-nat"
if [[ $RC -eq 0 ]] && ! called "firewall-rules create" && ! called "routers create" && ! called "nats create"; then
  pass "existing firewall rule, router and NAT are reused"
else
  fail "existing shared resources: exit $RC, calls:"
  sed 's/^/    /' "$CALLS"
fi

# --- --public: no IAP or NAT plumbing ---------------------------------------
run_provision_args --public
if [[ $RC -eq 0 ]] && called "instances create itx" && ! called "firewall-rules" && ! called "routers"; then
  pass "--public: no firewall rule, router or NAT"
else
  fail "--public: exit $RC, calls:"
  sed 's/^/    /' "$CALLS"
fi

if ((fails)); then
  echo "gcp/provision_test: $fails failure(s)"
  exit 1
fi
echo "gcp/provision_test: PASS"
