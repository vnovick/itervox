#!/bin/sh
# Runs `itervox run` for the itervox GitHub Action (#252) and turns its JSON
# result into step outputs: status, exit-code, pr-url and summary (Markdown).
# `itervox run` appends the same Markdown to $GITHUB_STEP_SUMMARY itself.
#
# Inputs (environment, never interpolated into the script): ITERVOX_ISSUE,
# ITERVOX_ANSWER, ITERVOX_APPROVE (an issue comment's body may be passed
# as is: the "/itervox " and "/itervox approve " prefixes are removed),
# ITERVOX_TIMEOUT, ITERVOX_WORKFLOW,
# ITERVOX_LOG_DIR, ITERVOX_FAIL_ON_NEEDS_INPUT ("true" makes exit 3 fail the
# step), GITHUB_OUTPUT. Exit status: itervox's, except 3 (needs input),
# which is a success unless ITERVOX_FAIL_ON_NEEDS_INPUT is true.
set -u

if [ -z "${ITERVOX_ISSUE:-}" ]; then
	echo "itervox action: the issue input is required" >&2
	exit 5
fi
set -- run --issue "$ITERVOX_ISSUE" --json --workflow "${ITERVOX_WORKFLOW:-WORKFLOW.md}"
# A comment body can be passed as is: "/itervox <answer>" and
# "/itervox approve <command>" lose their prefix.
answer=${ITERVOX_ANSWER:-}
answer=${answer#/itervox }
approve=${ITERVOX_APPROVE:-}
approve=${approve#/itervox approve }
if [ -n "$answer" ]; then
	set -- "$@" --answer "$answer"
fi
if [ -n "$approve" ]; then
	set -- "$@" --approve "$approve"
fi
if [ -n "${ITERVOX_TIMEOUT:-}" ]; then
	set -- "$@" --timeout "$ITERVOX_TIMEOUT"
fi
if [ -n "${ITERVOX_LOG_DIR:-}" ]; then
	set -- "$@" --log-dir "$ITERVOX_LOG_DIR"
fi

result="${RUNNER_TEMP:-/tmp}/itervox-run-result.json"
itervox "$@" >"$result"
code=$?

status=$(jq -r '.status // "failed"' "$result" 2>/dev/null || echo failed)
pr_url=$(jq -r '.pr_url // ""' "$result" 2>/dev/null || true)
if [ -n "${GITHUB_OUTPUT:-}" ]; then
	delim="ITERVOX_SUMMARY_$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"
	{
		echo "status=$status"
		echo "exit-code=$code"
		echo "pr-url=$pr_url"
		echo "summary<<$delim"
		jq -r '.summary // ""' "$result" 2>/dev/null || true
		echo "$delim"
	} >>"$GITHUB_OUTPUT"
fi
echo "itervox run: $status (exit $code)${pr_url:+ $pr_url}"

if [ "$code" = 3 ] && [ "${ITERVOX_FAIL_ON_NEEDS_INPUT:-false}" != true ]; then
	exit 0
fi
exit "$code"
