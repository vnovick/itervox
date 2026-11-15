#!/bin/sh
# ci-local.sh: run the "Test" step of .github/workflows/ci.yml locally, with
# the step's continue-on-error semantics, and report the job's result. It
# stands in for the CI service in the pipeline-guard demo's terminal cut: same command,
# same rule (a failing step marked continue-on-error does not fail the job),
# run on this machine. It does not emulate anything else of a CI runner.
#
# usage: ci-local.sh [checkout-dir]   (default: .)
# Exit status: 0 when the job succeeds, the step's status otherwise.

set -u
cd "${1:-.}" || exit 2
wf=.github/workflows/ci.yml
[ -f "$wf" ] || { echo "ci-local: no $wf" >&2; exit 2; }

# The Test step's run command and continue-on-error flag.
step=$(awk '
	/^ *- name: Test *$/ { in_step = 1; next }
	in_step && /^ *- / { in_step = 0 }
	in_step && /^ *run: / { sub(/^ *run: */, ""); run = $0 }
	in_step && /^ *continue-on-error: *true *$/ { coe = "true" }
	END { printf "%s\t%s\n", run, (coe == "" ? "false" : coe) }
' "$wf")
run=${step%%	*}
coe=${step#*	}
[ -n "$run" ] || { echo "ci-local: no Test step in $wf" >&2; exit 2; }

echo "CI job (run locally, same steps as $wf)"
echo "step Test: $run"
sh -c "$run" 2>&1
rc=$?
if [ "$rc" -eq 0 ]; then
	echo "step Test: passed"
	echo "job: success"
	exit 0
fi
if [ "$coe" = true ]; then
	echo "step Test: failed (exit $rc), continue-on-error: true"
	echo "job: success"
	exit 0
fi
echo "step Test: failed (exit $rc)"
echo "job: failure"
exit "$rc"
