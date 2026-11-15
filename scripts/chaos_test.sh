#!/usr/bin/env bash
#
# Tests for scripts/chaos.sh (#126). Run: make deploy-test. No Go build: go is
# a stub on PATH whose behaviour each case sets through FAKE_* variables.
#
# Proves:
#   - go list failing (no output, non-zero status) fails the run;
#   - go list matching nothing, or only packages without tests, fails it;
#   - a passing test binary passes the run, run from its package directory;
#   - a failing test binary fails the run and keeps its log;
#   - the script uses no Bash 4+ only feature (macOS ships Bash 3.2).

# Stub bodies are single-quoted on purpose: they expand in the child process.
# shellcheck disable=SC2016
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHAOS="$HERE/chaos.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fails=0
pass() { printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; fails=$((fails + 1)); }

mkdir -p "$WORK/bin" "$WORK/pkgdir"
# go stub:
#   go list -f {{.Dir}} PKG  -> $WORK/pkgdir
#   go list PKGS...          -> $FAKE_LIST lines, exit $FAKE_LIST_STATUS
#   go test ... -c -o OUT P  -> OUT = a test binary exiting $FAKE_TEST_STATUS
#                               (none when FAKE_NO_TESTS=1)
cat >"$WORK/bin/go" <<'STUB'
#!/usr/bin/env bash
if [[ $1 == list && $2 == -f ]]; then
  echo "$FAKE_PKGDIR"
  exit 0
fi
if [[ $1 == list ]]; then
  if [[ -n ${FAKE_LIST:-} ]]; then printf '%s\n' $FAKE_LIST; fi
  exit "${FAKE_LIST_STATUS:-0}"
fi
if [[ $1 == test ]]; then
  out=""
  while [[ $# -gt 0 ]]; do
    if [[ $1 == -o ]]; then out=$2; shift; fi
    shift
  done
  if [[ ${FAKE_NO_TESTS:-0} != 1 ]]; then
    printf '#!/usr/bin/env bash\npwd >"%s"\necho "--- FAIL: TestX" >&2\nexit %s\n' \
      "$FAKE_PKGDIR.ran" "${FAKE_TEST_STATUS:-0}" >"$out"
    chmod +x "$out"
  fi
  exit 0
fi
exit 3
STUB
chmod +x "$WORK/bin/go"

# run_chaos NAME [VAR=VALUE...]: runs chaos.sh with the stub; sets $status
# and $output.
run_chaos() {
  local name=$1
  shift
  status=0
  output=$(env PATH="$WORK/bin:$PATH" FAKE_PKGDIR="$WORK/pkgdir" CHAOS_HOGS=0 CHAOS_COUNT=1 \
    CHAOS_OUT="$WORK/out-$name" "$@" bash "$CHAOS" 2>&1) || status=$?
}

run_chaos listfail FAKE_LIST_STATUS=42
if [[ $status -ne 0 && $output == *"go list failed"* && $output != *"all packages passed"* ]]; then
  pass "go list failing fails the run"
else
  fail "go list failing: status=$status output=$output"
fi

run_chaos partial FAKE_LIST=example/a FAKE_LIST_STATUS=1
if [[ $status -ne 0 && $output != *"all packages passed"* ]]; then
  pass "a partial listing (output, then a failure) fails the run"
else
  fail "partial listing: status=$status output=$output"
fi

run_chaos empty
if [[ $status -ne 0 && $output == *"no packages match"* ]]; then
  pass "no matching package fails the run"
else
  fail "empty listing: status=$status output=$output"
fi

run_chaos notests FAKE_LIST=example/a FAKE_NO_TESTS=1
if [[ $status -ne 0 && $output == *"none of the packages has tests"* ]]; then
  pass "packages without tests fail the run"
else
  fail "no tests: status=$status output=$output"
fi

rm -f "$WORK/pkgdir.ran"
run_chaos ok FAKE_LIST="example/a example/b"
if [[ $status -eq 0 && $output == *"chaos: 2 package(s)"* && $output == *"all packages passed"* &&
  $(cat "$WORK/pkgdir.ran" 2>/dev/null) == "$WORK/pkgdir" ]]; then
  pass "passing binaries pass the run, run from the package directory"
else
  fail "passing run: status=$status output=$output"
fi

run_chaos red FAKE_LIST=example/a FAKE_TEST_STATUS=1
if [[ $status -ne 0 && $output == *"FAIL example/a"* && -f "$WORK/out-red/example_a.log" ]]; then
  pass "a failing binary fails the run and keeps its log"
else
  fail "failing run: status=$status output=$output"
fi

if grep -nE '\b(mapfile|readarray|coproc)\b|declare -[aA]*A|\$\{[A-Za-z_]+(,,|\^\^)|&>>|\|&' "$CHAOS" | grep -v '^[0-9]*:#'; then
  fail "chaos.sh uses a Bash 4+ only feature (above)"
else
  pass "chaos.sh uses no Bash 4+ only feature"
fi

if ((fails)); then
  echo "chaos_test: $fails failure(s)"
  exit 1
fi
echo "chaos_test: all passed"
