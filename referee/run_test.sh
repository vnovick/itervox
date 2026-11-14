#!/usr/bin/env bash
# run_test.sh: tests run.sh's fail-on mapping and outputs with a fake
# referee binary. Run: bash referee/run_test.sh
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -r "$tmp"' EXIT

# The fake prints a lanes-shaped JSON report and exits with $FAKE_CODE.
cat >"$tmp/referee" <<'FAKE'
#!/usr/bin/env bash
printf '{\n  "version": 1,\n  "lane": "%s",\n  "exit_code": %s\n}\n' "${FAKE_LANE:-auto}" "${FAKE_CODE:-0}"
exit "${FAKE_CODE:-0}"
FAKE
chmod +x "$tmp/referee"

fail=0
check() { # name want-exit [env...]
  local name=$1 want=$2
  shift 2
  : >"$tmp/out"
  env REFEREE_BIN="$tmp/referee" GITHUB_OUTPUT="$tmp/out" "$@" bash "$here/run.sh" >/dev/null 2>&1
  local got=$?
  if [ "$got" != "$want" ]; then
    echo "FAIL $name: exit $got, want $want"
    fail=1
  else
    echo "ok   $name"
  fi
}

check "auto passes" 0 MODE=lanes FAKE_CODE=0 FAKE_LANE=auto
grep -qx 'lane=auto' "$tmp/out" || { echo "FAIL lane output: $(cat "$tmp/out")"; fail=1; }
check "human fails by default" 20 MODE=lanes FAKE_CODE=20 FAKE_LANE=human
grep -qx 'lane=human' "$tmp/out" || { echo "FAIL lane output: $(cat "$tmp/out")"; fail=1; }
grep -qx 'exit-code=20' "$tmp/out" || { echo "FAIL exit-code output: $(cat "$tmp/out")"; fail=1; }
check "second_opinion passes by default" 0 MODE=lanes FAKE_CODE=10 FAKE_LANE=second_opinion
check "second_opinion fails with fail-on second_opinion" 10 MODE=lanes FAKE_CODE=10 FAKE_LANE=second_opinion FAIL_ON=second_opinion
check "human fails with fail-on second_opinion" 20 MODE=lanes FAKE_CODE=20 FAKE_LANE=human FAIL_ON=second_opinion
check "human passes with fail-on never" 0 MODE=lanes FAKE_CODE=20 FAKE_LANE=human FAIL_ON=never
check "an error fails even with fail-on never" 2 MODE=lanes FAKE_CODE=2 FAIL_ON=never
check "bad fail-on fails closed" 2 MODE=lanes FAKE_CODE=0 FAIL_ON=sometimes
check "bad mode fails closed" 2 MODE=judge
check "audit findings fail" 20 MODE=audit FAKE_CODE=20
check "audit findings reported with fail-on never" 0 MODE=audit FAKE_CODE=20 FAIL_ON=never
check "audit error fails with fail-on never" 2 MODE=audit FAKE_CODE=2 FAIL_ON=never
check "pin mismatch fails" 20 MODE=pin FAKE_CODE=20 POLICY_SHA256=x
check "verify failure fails" 1 MODE=verify FAKE_CODE=1 RECORDS=r PUBKEY=p

exit "$fail"
