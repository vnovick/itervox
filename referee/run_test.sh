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
[ -n "${FAKE_ARGS:-}" ] && printf '%s\n' "$@" >"$FAKE_ARGS"
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

# The referee record: a key adds --key and --record (default under
# RUNNER_TEMP) and the record output; verify passes its expectations on.
has_args() { # name expected-argument...
  local name=$1 want
  shift
  for want in "$@"; do
    grep -qxF -- "$want" "$tmp/args" || { echo "FAIL $name: no argument '$want' in: $(tr '\n' ' ' <"$tmp/args")"; fail=1; }
  done
}
check "lanes with a key writes a referee record" 20 MODE=lanes FAKE_CODE=20 FAKE_LANE=human KEY=/keys/attest.key RUNNER_TEMP=/rt FAKE_ARGS="$tmp/args"
has_args "lanes key" --key /keys/attest.key --record /rt/referee.jsonl
grep -qx 'record=/rt/referee.jsonl' "$tmp/out" || { echo "FAIL record output: $(cat "$tmp/out")"; fail=1; }
check "audit with a key and a record path" 0 MODE=audit FAKE_CODE=0 KEY=/k RECORD=/r.jsonl FAKE_ARGS="$tmp/args"
has_args "audit key" --key /k --record /r.jsonl
grep -qx 'record=/r.jsonl' "$tmp/out" || { echo "FAIL record output: $(cat "$tmp/out")"; fail=1; }
check "a record without a key is passed on and fails closed" 2 MODE=lanes FAKE_CODE=2 RECORD=/r.jsonl FAKE_ARGS="$tmp/args"
has_args "record without key" --key "" --record /r.jsonl
check "no key, no referee record" 0 MODE=lanes FAKE_CODE=0 FAKE_ARGS="$tmp/args"
if grep -qxF -- --key "$tmp/args"; then echo "FAIL no key: --key passed"; fail=1; fi
grep -qx 'record=' "$tmp/out" || { echo "FAIL empty record output: $(cat "$tmp/out")"; fail=1; }
check "verify checks the referee record" 0 MODE=verify FAKE_CODE=0 RECORDS=r PUBKEY=p BASE=main REQUIRE_REFEREE=true \
  EXPECT_REFEREE_COMMIT=abcdef0 EXPECT_RULESET=1-0123456789abcdef EXPECT_EXIT=20 FAKE_ARGS="$tmp/args"
has_args "verify expectations" --base main --head HEAD --require-referee --expect-referee-commit abcdef0 \
  --expect-ruleset 1-0123456789abcdef --expect-exit 20
check "verify referee mismatch fails" 1 MODE=verify FAKE_CODE=1 RECORDS=r PUBKEY=p EXPECT_EXIT=0
check "bad require-referee fails closed" 2 MODE=verify FAKE_CODE=0 RECORDS=r PUBKEY=p REQUIRE_REFEREE=yes

exit "$fail"
