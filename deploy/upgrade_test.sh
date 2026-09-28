#!/usr/bin/env bash
#
# CORE-107 test for deploy/upgrade.sh. Run: make deploy-test (or bash
# deploy/upgrade_test.sh). No root, no network: curl and systemctl are stubs on
# PATH. The curl stub serves release files from a fixture directory and
# answers the readiness URL with the status the case asks for; systemctl
# records every call.
#
# Proves:
#   - a tampered archive (checksum mismatch) and a checksums.txt without the
#     archive's line both exit 1 with the binary untouched and NO systemctl call;
#   - a new binary that cannot run aborts before the swap;
#   - the happy path swaps the binary, keeps <bin>.prev, restarts once, exits 0,
#     leaves no <bin>.new behind;
#   - a new binary that never turns ready is rolled back: <bin> is the old
#     build again, systemctl restart ran twice, exit 2;
#   - a failed `systemctl restart` also rolls back;
#   - when the rollback does not turn ready either, exit 3 (old binary in place);
#   - the rollback runs `systemctl reset-failed` before its restart, so a unit
#     that hit its start limit on the bad build can start again (M6-close);
#   - a rollback whose copy of <bin>.prev fails exits 3 with a message instead
#     of dying under `set -e` with exit 1 (M6-close).

# Stub bodies are single-quoted on purpose: they expand in the child process.
# shellcheck disable=SC2016
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UPGRADE="$HERE/upgrade.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fails=0
pass() { printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; fails=$((fails + 1)); }

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

mkdir -p "$WORK/bin"
mkstub() { printf '#!/usr/bin/env bash\n%s\n' "$2" > "$WORK/bin/$1"; chmod +x "$WORK/bin/$1"; }

# curl stub: `-o FILE URL` copies $RELEASES/<tag>/<file>; a URL containing
# /ready prints the HTTP code from $STATE/ready (per restart generation: the
# file holds one code per line, line N = the code after the Nth restart).
mkstub curl 'out=""; url=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -w|--max-time) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
echo "curl $url" >> "$CALLS"
if [[ "$url" == *"/ready"* ]]; then
  n="$(grep -c "^systemctl restart" "$CALLS" || true)"
  code="$(sed -n "${n}p" "$STATE/ready")"
  printf "%s" "${code:-000}"
  exit 0
fi
rel="${url#"$BASE"/}"
src="$RELEASES/$rel"
[[ -f "$src" ]] || exit 22
cp "$src" "$out"'
# cp stub: fails the rollback copy (source <bin>.prev) when the case asks;
# every other copy is the real cp.
mkstub cp 'if [[ -f "$STATE/cp-prev-fails" ]]; then
  for a in "$@"; do [[ "$a" == *.prev ]] && { echo "cp: $a: Input/output error" >&2; exit 1; }; done
fi
exec /bin/cp "$@"'
mkstub systemctl 'echo "systemctl $*" >> "$CALLS"
if [[ "$1" == restart && -f "$STATE/restart-fails" ]]; then exit 1; fi
exit 0'

# fake_binary PATH LABEL [broken] — a shell script standing in for itervox.
fake_binary() {
  if [[ "${3:-}" == broken ]]; then
    printf '#!/usr/bin/env bash\necho "exec format error" >&2\nexit 126\n' > "$1"
  else
    printf '#!/usr/bin/env bash\necho "itervox %s"\n' "$2" > "$1"
  fi
  chmod +x "$1"
}

# make_release TAG LABEL [broken|tamper|noline]
make_release() {
  local tag="$1" label="$2" mode="${3:-}" d="$WORK/releases/$1" stage
  stage="$(mktemp -d "$WORK/stage.XXXX")"
  mkdir -p "$d"
  fake_binary "$stage/itervox" "$label" "$mode"
  tar -czf "$d/itervox_linux_amd64.tar.gz" -C "$stage" itervox
  local sum; sum="$(sha256_of "$d/itervox_linux_amd64.tar.gz")"
  case "$mode" in
    tamper) sum="$(printf '%064d' 0)" ;;
  esac
  if [[ "$mode" == noline ]]; then
    printf '%s  itervox_linux_arm64.tar.gz\n' "$sum" > "$d/checksums.txt"
  else
    printf '%s  itervox_darwin_amd64.tar.gz\n%s  itervox_linux_amd64.tar.gz\n' "$(printf '%064d' 1)" "$sum" > "$d/checksums.txt"
  fi
}

make_release v9.0.0 v9.0.0
make_release v9.0.1 v9.0.1 tamper
make_release v9.0.2 v9.0.2 noline
make_release v9.0.3 v9.0.3 broken

# run_case NAME TAG READY_CODES [restart-fails]
run_case() {
  local name="$1" tag="$2" codes="$3" extra="${4:-}"
  CASE="$WORK/case-$name"; mkdir -p "$CASE/state" "$CASE/install"
  CALLS="$CASE/calls.log"; : > "$CALLS"
  printf '%s\n' "$codes" | tr ',' '\n' > "$CASE/state/ready"
  [[ "$extra" == restart-fails ]] && touch "$CASE/state/restart-fails"
  [[ "$extra" == cp-prev-fails ]] && touch "$CASE/state/cp-prev-fails"
  BIN="$CASE/install/itervox"
  fake_binary "$BIN" v8.0.0
  RC=0
  OUT="$(env PATH="$WORK/bin:$PATH" CALLS="$CALLS" STATE="$CASE/state" \
    RELEASES="$WORK/releases" BASE="https://example.invalid/dl" ITERVOX_UPGRADE_POLL_INTERVAL=0 \
    bash "$UPGRADE" --version "$tag" --bin "$BIN" --arch amd64 --ready-timeout 0 \
      --base-url https://example.invalid/dl --ready-url http://127.0.0.1:8090/api/v1/ready 2>&1)" || RC=$?
}
restarts() { grep -c '^systemctl restart' "$CALLS" || true; }
bin_is() { [[ "$("$BIN" --version)" == "itervox $1" ]]; }

# 1. tampered archive
run_case tamper v9.0.1 200
if [[ $RC -eq 1 ]] && ! grep -q systemctl "$CALLS" && bin_is v8.0.0 && [[ ! -e "$BIN.prev" ]] \
   && grep -q 'sha256 mismatch' <<<"$OUT"; then
  pass "tampered checksum: exit 1 before any systemctl call, binary untouched"
else
  fail "tampered checksum: rc=$RC calls=$(tr '\n' ';' < "$CALLS") out=$OUT"
fi

# 2. checksums.txt without our line
run_case noline v9.0.2 200
if [[ $RC -eq 1 ]] && ! grep -q systemctl "$CALLS" && bin_is v8.0.0 && grep -q 'no line for' <<<"$OUT"; then
  pass "checksums.txt without the archive's line: exit 1, no systemctl call"
else
  fail "noline: rc=$RC out=$OUT"
fi

# 3. binary that does not run
run_case broken v9.0.3 200
if [[ $RC -eq 1 ]] && ! grep -q systemctl "$CALLS" && bin_is v8.0.0 && [[ ! -e "$BIN.prev" ]]; then
  pass "new binary that cannot run: exit 1 before the swap"
else
  fail "broken binary: rc=$RC out=$OUT"
fi

# 4. happy path
run_case happy v9.0.0 200
if [[ $RC -eq 0 ]] && bin_is v9.0.0 && [[ "$("$BIN.prev" --version)" == "itervox v8.0.0" ]] \
   && [[ "$(restarts)" == 1 ]] && [[ ! -e "$BIN.new" ]] && [[ -x "$BIN" ]]; then
  pass "happy path: swapped, previous kept as .prev, one restart, no .new left"
else
  fail "happy: rc=$RC restarts=$(restarts) out=$OUT"
fi

# 5. never ready -> rollback, ready on the old binary
run_case notready v9.0.0 503,200
if [[ $RC -eq 2 ]] && bin_is v8.0.0 && [[ "$(restarts)" == 2 ]] && grep -q 'ROLLING BACK' <<<"$OUT" \
   && [[ ! -e "$BIN.new" ]]; then
  pass "failed /ready after the swap: rolled back to the previous binary, 2 restarts, exit 2"
else
  fail "notready: rc=$RC restarts=$(restarts) version=$("$BIN" --version) out=$OUT"
fi

# 6. systemctl restart fails -> rollback attempted; restart keeps failing -> exit 3
run_case restartfail v9.0.0 200,200 restart-fails
if [[ $RC -eq 3 ]] && bin_is v8.0.0 && [[ "$(restarts)" == 2 ]]; then
  pass "systemctl restart failure: binary rolled back, exit 3 when the service still cannot start"
else
  fail "restartfail: rc=$RC restarts=$(restarts) out=$OUT"
fi

# 7. rollback not ready either -> exit 3, old binary in place
run_case bothbad v9.0.0 503,503
if [[ $RC -eq 3 ]] && bin_is v8.0.0 && grep -q 'rollback did not turn ready' <<<"$OUT"; then
  pass "rollback that does not turn ready: exit 3, previous binary in place"
else
  fail "bothbad: rc=$RC out=$OUT"
fi

# 7b. the rollback clears the unit's failed state before restarting it
run_case resetfailed v9.0.0 503,200
order="$(grep -E '^systemctl (restart|reset-failed)' "$CALLS" | tr '\n' ';')"
if [[ $RC -eq 2 ]] && [[ "$order" == "systemctl restart itervox;systemctl reset-failed itervox;systemctl restart itervox;" ]]; then
  pass "rollback runs systemctl reset-failed before its restart"
else
  fail "reset-failed: rc=$RC order=$order out=$OUT"
fi

# 7c. the rollback copy of <bin>.prev fails -> exit 3 with a message
run_case cpfail v9.0.0 503 cp-prev-fails
if [[ $RC -eq 3 ]] && grep -q 'could not restore' <<<"$OUT" && [[ "$(restarts)" == 1 ]]; then
  pass "rollback copy failure: exit 3 with a message, no restart of the bad build"
else
  fail "cpfail: rc=$RC restarts=$(restarts) out=$OUT"
fi

# 8. same build already installed -> no restart
run_case same v9.0.0 200
cp "$BIN" "$WORK/v9copy"; : > "$CALLS"
tar -xzf "$WORK/releases/v9.0.0/itervox_linux_amd64.tar.gz" -C "$CASE/install" itervox
RC=0
OUT="$(env PATH="$WORK/bin:$PATH" CALLS="$CALLS" STATE="$CASE/state" RELEASES="$WORK/releases" \
  BASE="https://example.invalid/dl" ITERVOX_UPGRADE_POLL_INTERVAL=0 \
  bash "$UPGRADE" --version v9.0.0 --bin "$BIN" --arch amd64 --ready-timeout 0 \
    --base-url https://example.invalid/dl 2>&1)" || RC=$?
if [[ $RC -eq 0 ]] && ! grep -q systemctl "$CALLS"; then
  pass "same build already installed: exit 0 without a restart"
else
  fail "same: rc=$RC out=$OUT"
fi

if [[ $fails -gt 0 ]]; then
  echo "upgrade_test: $fails FAILED"
  exit 1
fi
echo "upgrade_test: PASS"
