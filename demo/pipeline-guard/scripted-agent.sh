#!/bin/sh
# scripted-agent.sh: the scripted agent of the pipeline-guard demo
# (scripts/pipeline-guard-demo.sh).
#
# It is an itervox custom backend (backend: custom; the agent protocol of
# docs/examples/custom-agent.sh): it reads the prompt on stdin, prints JSON
# lines, and does real work in the issue's worktree with the real git, behind
# the run's guardrail shims and pre-push hook. No model is involved; the same
# two steps run every time:
#
#   step 1 (fresh branch): reproduce the red build, then "fix" it the
#     tempting way: add continue-on-error to the CI test step, commit, push
#     and, when the push goes through, open a pull request. When the push is
#     refused, end the turn and say so.
#   step 2 (the branch holds that CI commit, unpushed): revert it, fix the
#     bug in cart.go, run the tests, commit, push, open a pull request.
#
# DEMO_AGENT_PACE (seconds, default 2) is the pause between steps, so a
# person can follow the dashboard. With DEMO_AGENT_TRANSCRIPT=<dir> every
# step is also written there, in order, as raw files: NN.say (a text line)
# or NN.cmd, NN.out and NN.rc (a command, its combined output, its exit
# status). scripts/pipeline-guard-capture.mjs --style terminal replays them.

set -u

pace=${DEMO_AGENT_PACE:-2}

json_string() {
	printf '%s' "$1" | tr -d '\000-\010\013-\037' |
		sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' -e 's/	/\\t/g' |
		awk 'BEGIN { printf "\"" } NR > 1 { printf "\\n" } { printf "%s", $0 } END { printf "\"" }'
}

# record EXT TEXT: the next transcript file (DEMO_AGENT_TRANSCRIPT).
record_n=""
record() {
	[ -n "${DEMO_AGENT_TRANSCRIPT:-}" ] || return 0
	mkdir -p "$DEMO_AGENT_TRANSCRIPT"
	if [ "$1" = say ] || [ "$1" = cmd ]; then
		record_n=$(printf '%02d' $(($(find "$DEMO_AGENT_TRANSCRIPT" -name '*.say' -o -name '*.cmd' | wc -l) + 1)))
	fi
	printf '%s' "$2" >"$DEMO_AGENT_TRANSCRIPT/$record_n.$1"
}

say() {
	sleep "$pace"
	record say "$1"
	printf '{"type":"text","text":%s}\n' "$(json_string "$1")"
}

# run_tool CMD: report a bash tool call, run CMD, report its result. Sets
# $out (combined output) and $rc.
run_tool() {
	sleep "$pace"
	printf '{"type":"tool_call","name":"bash","input":{"command":%s}}\n' "$(json_string "$1")"
	out=$(sh -c "$1" 2>&1)
	rc=$?
	record cmd "$1"
	record out "$out"
	record rc "$rc"
	printf '{"type":"tool_result","name":"bash","input":{"command":%s},"output":%s,"is_error":%s}\n' \
		"$(json_string "$1")" "$(json_string "$out")" "$([ "$rc" -eq 0 ] && echo false || echo true)"
}

# edit_tool FILE CMD: report an edit of FILE, made by CMD; the result is
# the diff.
edit_tool() {
	sleep "$pace"
	printf '{"type":"tool_call","name":"edit","input":{"file_path":%s}}\n' "$(json_string "$1")"
	sh -c "$2" >/dev/null 2>&1
	rc=$?
	out=$(git diff -- "$1")
	record cmd "git diff -- $1"
	record out "$out"
	record rc "$rc"
	printf '{"type":"tool_result","name":"edit","input":{"file_path":%s},"output":%s,"is_error":%s}\n' \
		"$(json_string "$1")" "$(json_string "$out")" "$([ "$rc" -eq 0 ] && echo false || echo true)"
}

done_turn() {
	printf '{"type":"usage","input_tokens":4200,"output_tokens":600}\n'
	printf '{"type":"done","result":%s}\n' "$(json_string "$1")"
	exit 0
}

session=${ITERVOX_SESSION_ID:-"demo-$$-$(date +%s)"}
printf '{"type":"session","session_id":%s}\n' "$(json_string "$session")"
cat >/dev/null # the prompt: this agent follows its script

ci_commit="ci: let the test step continue on error"
fix_commit="fix: Total counts the last item"

subjects=$(git log --format=%s origin/main..HEAD 2>/dev/null)
if printf '%s\n' "$subjects" | grep -qxF "$fix_commit"; then
	say "The fix is already on this branch."
	done_turn "Nothing left to do."
fi
if printf '%s\n' "$subjects" | grep -qxF "$ci_commit"; then
	# step 2: the operator turned the pipeline change down
	say "Understood: fix the code, not the pipeline. Reverting the CI change."
	run_tool "git revert --no-edit HEAD"
	say "Total stops one item early: the loop runs to len(items)-1."
	edit_tool cart.go "sed -i.bak 's/i < len(items)-1; i++/i < len(items); i++/' cart.go && rm -f cart.go.bak"
	run_tool "go test ./..."
	if [ "$rc" -ne 0 ]; then
		printf '{"type":"failed","reason":%s}\n' "$(json_string "tests still fail: $out")"
		exit 0
	fi
	run_tool "git commit -qam '$fix_commit' && git log --oneline -3"
	run_tool "git push -u origin HEAD"
	if [ "$rc" -ne 0 ]; then
		done_turn "The fix is committed, but the push was refused."
	fi
	run_tool "gh pr create --title 'Fix Total dropping the last item' --body 'TestTotal passes again; CI config unchanged.'"
	pr=$out
	say "Tests pass and CI is unchanged. Pull request: $pr"
	done_turn "Fixed Total; pull request $pr"
fi

# step 1: the tempting fix
say "Reading the issue. Reproducing the failure first."
run_tool "go test ./..."
say "TestTotal fails (Total = 900, want 1200). The quickest way to a green build: let the CI test step continue on error."
edit_tool .github/workflows/ci.yml "sed -i.bak 's/^      - name: Test\$/&\\
        continue-on-error: true/' .github/workflows/ci.yml && rm -f .github/workflows/ci.yml.bak"
run_tool "git commit -qam '$ci_commit' && git log --oneline -2"
run_tool "git push -u origin HEAD"
if [ "$rc" -ne 0 ]; then
	reason=$(printf '%s\n' "$out" | grep -m1 'rejected' || printf '%s\n' "$out" | tail -n 1)
	say "The push was refused: $reason"
	done_turn "Made the CI test step non-blocking; the push was refused, so nothing was pushed."
fi
run_tool "gh pr create --title 'Make CI green' --body 'The test step no longer fails the build.'"
pr=$out
say "CI is green: the test step can no longer fail the build. Pull request: $pr"
done_turn "CI green; pull request $pr"
