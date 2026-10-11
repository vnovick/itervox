#!/usr/bin/env bash
#
# #137 test for deploy/acceptance/gcp-deploy-smoke.sh. Run: make deploy-test.
# No cloud: gcloud and curl are stubs. The stub gcloud keeps the resources it
# "creates" as files, so the test can see what is left after the teardown,
# and logs every call.
#
# Proves:
#   - usage: no --version, a release before v0.2.2, or no agent credential
#     exit 2 before any gcloud call that creates something;
#   - a passing run provisions, bootstraps the release, writes a local-tracker
#     workflow, sets the credential, runs doctor --deploy, starts the service,
#     loads the dashboard through the tunnel, in that order, and exits 0;
#   - the credential reaches the VM on stdin only: never in gcloud's argv or
#     the output;
#   - teardown removes the instance, the data disk, and the router/NAT and
#     firewall rule only when this run created them;
#   - a failing doctor --deploy exits 1, never starts the service, and still
#     tears everything down;
#   - --keep leaves the resources and lists them.

# Stub bodies are single-quoted on purpose: they expand in the child process.
# `cond && pass || fail` is safe: pass only prints, so it never fails.
# shellcheck disable=SC2016,SC2015
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SMOKE="$HERE/gcp-deploy-smoke.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fails=0
pass() { printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; fails=$((fails + 1)); }

BIN="$WORK/bin"
mkdir -p "$BIN"
SECRET="sk-ant-fixture-secret-0123456789"

cat > "$BIN/gcloud" <<'EOF'
#!/usr/bin/env bash
R="$STUB_DIR/res"; mkdir -p "$R"
printf 'gcloud %s\n' "$*" >> "$STUB_DIR/gcloud.log"
has() { [[ -e "$R/$1" ]]; }
case "$2 $3" in
  "instances create") touch "$R/instance" ;;
  "instances delete") rm -f "$R/instance" ;;
  "instances describe") has instance ;;
  "disks create") touch "$R/disk" ;;
  "disks delete") rm -f "$R/disk" ;;
  "disks describe") has disk ;;
  "instances attach-disk") ;;
  "routers describe") has router ;;
  "routers create") touch "$R/router" ;;
  "routers delete") rm -f "$R/router" "$R/nat" ;;
  "routers nats")
    case "$4" in describe) has nat ;; create) touch "$R/nat" ;; delete) rm -f "$R/nat" ;; esac ;;
  "firewall-rules describe") has fw ;;
  "firewall-rules create") touch "$R/fw" ;;
  "firewall-rules delete") rm -f "$R/fw" ;;
  "compute scp") ;;
  *)
    if [[ "$2" == "ssh" ]]; then
      if [[ " $* " == *" -N "* ]]; then trap 'exit 0' TERM; while true; do sleep 0.1; done; fi
      cmd="${*##*--command }"
      case "$cmd" in
        *"secret set"*) cat > "$STUB_DIR/secret.stdin" ;;
        *"doctor --deploy"*) if [[ -n "${STUB_DOCTOR_FAIL:-}" ]]; then echo "deploy: FAILED — fix the [fail] lines above"; exit 1; fi; echo "deploy: OK" ;;
        *"systemctl is-active"*) echo active ;;
      esac
    fi ;;
esac
exit 0
EOF
cat > "$BIN/curl" <<'EOF'
#!/usr/bin/env bash
printf 'curl %s\n' "$*" >> "$STUB_DIR/curl.log"
case "$*" in
  */api/v1/ready*) exit 0 ;;
  *) echo '<!doctype html><div id="root"></div>' ;;
esac
EOF
chmod +x "$BIN"/*

# run <case> [env options/assignments...] -- [script args...]; AGENT_ENV
# (default ANTHROPIC_API_KEY=<secret>) is the credential assignment.
run() {
  local name="$1"; shift
  local envs=()
  while [[ $# -gt 0 && "$1" != "--" ]]; do envs+=("$1"); shift; done
  [[ "${1:-}" == "--" ]] && shift
  STUB="$WORK/$name"; mkdir -p "$STUB/res"
  for r in ${PRE:-}; do touch "$STUB/res/$r"; done
  set +e
  env "${envs[@]}" STUB_DIR="$STUB" PATH="$BIN:$PATH" ITERVOX_SMOKE_WAIT_SECONDS=0 \
    "${AGENT_ENV-ANTHROPIC_API_KEY=$SECRET}" bash "$SMOKE" --name itervox-smoke-t --zone us-central1-a "$@" > "$STUB/run.out" 2>&1
  rc=$?
  set -e
  LOG="$STUB/gcloud.log"
}
line() { grep -n -- "$1" "$LOG" | head -1 | cut -d: -f1; }

# ── usage ───────────────────────────────────────────────────────────────────
PRE=""
run nover --
[[ $rc -eq 2 ]] && grep -q -- "--version <tag> is required" "$STUB/run.out" && pass "no --version: exit 2" || fail "no --version: exit $rc"
run old -- --version v0.2.1
[[ $rc -eq 2 ]] && grep -q "predates itervox secret" "$STUB/run.out" && pass "v0.2.1: refused" || fail "v0.2.1: exit $rc"
AGENT_ENV="NO_AGENT_KEY=1" run nokey -u ANTHROPIC_API_KEY -u CLAUDE_CODE_OAUTH_TOKEN -- --version v0.2.4
[[ $rc -eq 2 ]] && grep -q "ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN" "$STUB/run.out" && pass "no agent credential: exit 2" || fail "no credential: exit $rc"
if grep -qs "create" "$WORK"/{nover,old,nokey}/gcloud.log; then fail "a usage error created something"; else pass "usage errors create nothing"; fi

# ── a passing run, nothing shared pre-existing ──────────────────────────────
PRE=""
run ok -- --version v0.2.4
[[ $rc -eq 0 ]] && pass "passing run: exit 0" || { fail "passing run: exit $rc"; cat "$STUB/run.out"; }
p=$(line "instances create"); b=$(line "bootstrap.sh --repo https://github.com/vnovick/itervox.git --version v0.2.4 --data-disk /dev/disk/by-id/google-itervox-data")
i=$(line "init --tracker local --force"); s=$(line "secret set ANTHROPIC_API_KEY"); d=$(line "doctor --deploy")
e=$(line "systemctl enable --now itervox"); t=$(line "-N -L 18090:localhost:8090")
[[ -n "$p" && -n "$b" && -n "$i" && -n "$s" && -n "$d" && -n "$e" && -n "$t" && $p -lt $b && $b -lt $i && $i -lt $s && $s -lt $d && $d -lt $e && $e -lt $t ]] \
  && pass "provision → bootstrap → local workflow → secret → doctor → start → tunnel" || fail "step order: p=$p b=$b i=$i s=$s d=$d e=$e t=$t"
grep -q "deploy smoke: OK (v0.2.4" "$STUB/run.out" && pass "reports deploy smoke: OK" || fail "no OK line"
[[ "$(cat "$STUB/secret.stdin" 2>/dev/null)" == "$SECRET" ]] && pass "the credential reaches the VM on stdin" || fail "secret not delivered on stdin"
if grep -q "$SECRET" "$LOG" "$STUB/run.out" "$STUB/curl.log"; then fail "the credential leaked into argv or output"; else pass "the credential never appears in argv or output"; fi
grep -q "/api/v1/ready" "$STUB/curl.log" && grep -qx "curl -fsS http://127.0.0.1:18090/" "$STUB/curl.log" && pass "dashboard and /ready fetched through the tunnel" || fail "curl calls: $(cat "$STUB/curl.log")"
left=$(ls "$STUB/res"); [[ -z "$left" ]] && pass "teardown removed everything it created" || fail "left behind: $left"

# ── shared resources that already existed stay ──────────────────────────────
PRE="router nat fw"
run shared -- --version v0.2.4
left=""; for f in "$STUB"/res/*; do [[ -e "$f" ]] && left+="${f##*/} "; done
[[ $rc -eq 0 && "$left" == "fw nat router " ]] && pass "pre-existing router, NAT and firewall rule are kept" || fail "shared: exit $rc, left: $left"

# ── a failing doctor ────────────────────────────────────────────────────────
PRE=""
run doctorfail STUB_DOCTOR_FAIL=1 -- --version v0.2.4
[[ $rc -eq 1 ]] && pass "failing doctor --deploy: exit 1" || fail "failing doctor: exit $rc"
grep -q "systemctl enable" "$LOG" && fail "service started after a failed doctor" || pass "service not started after a failed doctor"
left=$(ls "$STUB/res"); [[ -z "$left" ]] && pass "teardown still ran after the failure" || fail "left behind after failure: $left"

# ── --keep ──────────────────────────────────────────────────────────────────
PRE=""
run keep -- --version v0.2.4 --keep
[[ $rc -eq 0 && -e "$STUB/res/instance" && -e "$STUB/res/disk" ]] && grep -q "NOT torn down. Delete by hand: instance itervox-smoke-t and disk itervox-smoke-t-data" "$STUB/run.out" \
  && pass "--keep leaves the resources and names them" || fail "--keep: exit $rc; $(tail -2 "$STUB/run.out")"

if [[ $fails -gt 0 ]]; then
  echo "gcp-deploy-smoke_test: $fails failure(s)"
  exit 1
fi
echo "gcp-deploy-smoke_test: PASS"
