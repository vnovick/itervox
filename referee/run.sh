#!/usr/bin/env bash
# run.sh: the referee Action's step. Inputs arrive as environment variables
# (never interpolated into the script). Exit codes follow referee's: 0 auto
# or ok, 10 second_opinion, 20 human / findings / mismatch, 1 a record that
# did not verify, 2 an error. fail-on decides which lanes fail the step
# (and, for audit and envcheck, never reports findings without failing); an error always
# does.
set -uo pipefail

referee_bin=${REFEREE_BIN:-referee}
case "${FAIL_ON:-human}" in
  human | second_opinion | never) ;;
  *)
    echo "::error::referee: fail-on must be human, second_opinion or never (got '${FAIL_ON}')"
    exit 2
    ;;
esac
args=()
case "${MODE:-}" in
  lanes)
    args=(lanes --head "${HEAD_REF:-HEAD}" --policy "${POLICY:-.referee.yml}")
    [ -n "${BASE:-}" ] && args+=(--base "$BASE")
    ;;
  audit)
    args=(audit --head "${HEAD_REF:-HEAD}")
    [ -n "${BASE:-}" ] && args+=(--base "$BASE")
    ;;
  envcheck)
    args=(envcheck --head "${HEAD_REF:-HEAD}" --policy "${POLICY:-.referee.yml}")
    [ -n "${BASE:-}" ] && args+=(--base "$BASE")
    ;;
  pin)
    args=(pin --file "${POLICY:-.referee.yml}" --policy-sha256 "${POLICY_SHA256:-}")
    ;;
  verify)
    args=(verify "${RECORDS:-}" --pubkey "${PUBKEY:-}")
    [ -n "${TREE:-}" ] && args+=(--tree "$TREE")
    ;;
  *)
    echo "::error::referee: mode must be lanes, audit, envcheck, pin or verify (got '${MODE:-}')"
    exit 2
    ;;
esac

code=0
if [ "$MODE" = lanes ]; then
  json=$("$referee_bin" "${args[@]}" --json) || code=$?
  printf '%s\n' "$json"
  lane=$(printf '%s' "$json" | sed -n 's/^  "lane": "\([a-z_]*\)",$/\1/p')
  echo "lane=${lane:-unknown}" >>"$GITHUB_OUTPUT"
else
  "$referee_bin" "${args[@]}" || code=$?
fi
echo "exit-code=$code" >>"$GITHUB_OUTPUT"

if [ "$MODE" = lanes ]; then
  case "$code" in
    0) exit 0 ;;
    10) [ "${FAIL_ON:-human}" = second_opinion ] && { echo "::error::referee: second_opinion lane"; exit 10; }; exit 0 ;;
    20) [ "${FAIL_ON:-human}" = never ] && exit 0; echo "::error::referee: human lane: a maintainer must approve this pipeline change"; exit 20 ;;
    *) echo "::error::referee: error (exit $code)"; exit "$code" ;;
  esac
fi
if { [ "$MODE" = audit ] || [ "$MODE" = envcheck ]; } && [ "$code" = 20 ] && [ "${FAIL_ON:-human}" = never ]; then
  exit 0 # findings reported (step summary, exit-code output), not enforced here
fi
exit "$code"
