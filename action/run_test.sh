#!/usr/bin/env bash
#
# #252 test for action/run.sh. Run: make deploy-test (or
# bash action/run_test.sh). Needs bash and jq.
#
# `itervox` is a stub on PATH that records its arguments and prints a JSON
# result with a chosen exit code. The test checks the arguments (comment
# prefixes removed, inputs passed as single arguments, never evaluated), the
# step outputs (status, exit-code, pr-url and the multi-line summary) and the
# step's exit status (exit 3 is a success unless fail-on-needs-input).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="$HERE/run.sh"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fails=0
pass() { printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; fails=$((fails + 1)); }

mkdir -p "$WORK/bin"
cat >"$WORK/bin/itervox" <<'STUB'
#!/bin/sh
for a in "$@"; do printf '[%s]\n' "$a"; done >"$ARGS_OUT"
printf '%s\n' "$RESULT_JSON"
exit "$RESULT_CODE"
STUB
chmod +x "$WORK/bin/itervox"

# run_case <exit code> <result json> [VAR=value...]: runs run.sh, sets $code.
run_case() {
	local rc=$1 json=$2
	shift 2
	: >"$WORK/output"
	code=0
	env PATH="$WORK/bin:$PATH" ARGS_OUT="$WORK/args" RESULT_JSON="$json" RESULT_CODE="$rc" \
		RUNNER_TEMP="$WORK" GITHUB_OUTPUT="$WORK/output" "$@" sh "$SCRIPT" >"$WORK/stdout" 2>&1 || code=$?
}

# shellcheck disable=SC2016 # the $(...) must reach itervox unevaluated
run_case 3 '{"status":"needs_input","pr_url":"","summary":"## itervox run: #4\n\nWhich store?"}' \
	ITERVOX_ISSUE=4 ITERVOX_ANSWER='/itervox Use Postgres; $(touch pwned)' \
	ITERVOX_APPROVE='/itervox approve deploy prod' ITERVOX_TIMEOUT=45m
# shellcheck disable=SC2016 # the literal $(...) is the point
want='[run]
[--issue]
[4]
[--json]
[--workflow]
[WORKFLOW.md]
[--answer]
[Use Postgres; $(touch pwned)]
[--approve]
[deploy prod]
[--timeout]
[45m]'
if [ "$(cat "$WORK/args")" = "$want" ]; then pass "arguments: prefixes removed, inputs passed verbatim"; else fail "arguments: $(cat "$WORK/args")"; fi
if [ ! -e pwned ] && [ ! -e "$WORK/pwned" ]; then pass "an answer is never evaluated"; else fail "the answer was evaluated"; fi
if [ "$code" = 0 ]; then pass "exit 3 is a success by default"; else fail "exit 3 gave $code"; fi
if grep -qx 'status=needs_input' "$WORK/output" && grep -qx 'exit-code=3' "$WORK/output" && grep -qx 'pr-url=' "$WORK/output"; then
	pass "status, exit-code and pr-url outputs"
else
	fail "outputs: $(cat "$WORK/output")"
fi
delim=$(sed -n 's/^summary<<//p' "$WORK/output")
if [[ $delim == ITERVOX_SUMMARY_* ]] && [ "$(sed -n "/^summary<<$delim\$/,/^$delim\$/p" "$WORK/output" | sed '1d;$d')" = $'## itervox run: #4\n\nWhich store?' ]; then
	pass "the summary output is the multi-line Markdown under a random delimiter"
else
	fail "summary output: $(cat "$WORK/output")"
fi

run_case 3 '{"status":"needs_input"}' ITERVOX_ISSUE=4 ITERVOX_FAIL_ON_NEEDS_INPUT=true
if [ "$code" = 3 ]; then pass "fail-on-needs-input fails the step"; else fail "fail-on-needs-input gave $code"; fi

run_case 0 '{"status":"pr_opened","pr_url":"https://github.com/acme/widgets/pull/9","summary":"ok"}' ITERVOX_ISSUE=4 ITERVOX_LOG_DIR=logs
if [ "$code" = 0 ] && grep -qx 'pr-url=https://github.com/acme/widgets/pull/9' "$WORK/output" && grep -q '^\[--log-dir\]$' "$WORK/args"; then
	pass "a PR's URL is an output"
else
	fail "pr_opened: exit $code, $(cat "$WORK/output")"
fi

run_case 2 'not json' ITERVOX_ISSUE=4
if [ "$code" = 2 ] && grep -qx 'status=failed' "$WORK/output"; then pass "a failure fails the step"; else fail "failure: exit $code, $(cat "$WORK/output")"; fi

run_case 0 '{}' ITERVOX_ISSUE=
if [ "$code" = 5 ]; then pass "no issue is a configuration error"; else fail "no issue gave $code"; fi

if [ "$fails" -ne 0 ]; then
	printf '%d run.sh case(s) failed\n' "$fails"
	exit 1
fi
printf 'run.sh: all cases passed\n'
