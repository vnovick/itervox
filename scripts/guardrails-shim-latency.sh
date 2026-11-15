#!/usr/bin/env bash
# guardrails-shim-latency.sh — what the guardrail git shim costs per call.
#
# Times N runs of `git rev-parse HEAD` (and `git status --short`) in a
# throwaway repository, once with git called directly and once through a
# guardrail shim, and prints p50/p95 for each and the p50/p95 overhead in
# ms. The shim directory is laid out the way guardrails.Prepare writes it
# (internal/guardrails/runtime.go: a policy.json and a bin/git sh script
# that execs `itervox guardrail-shim <policy> git "$@"`), so the numbers
# are the real binary's: sh start-up + itervox start-up + the policy read
# and decision + the exec of real git.
#
# Usage: scripts/guardrails-shim-latency.sh [path/to/itervox]
#   N=200 (runs per case). Without a binary it builds ./cmd/itervox, which
#   needs internal/server/web/dist (make web-build).
#
# Numbers depend on the machine and its load; run it on an idle machine
# and compare overheads, not absolute times.
set -euo pipefail

N=${N:-200}
repo_root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/itervox-shim-latency.XXXXXX")
cleanup() { [ -n "$work" ] && [ -d "$work" ] && find "$work" -depth -delete; }
trap cleanup EXIT

itervox=${1:-}
if [ -z "$itervox" ]; then
	echo "building itervox…" >&2
	(cd "$repo_root" && go build -o "$work/itervox" ./cmd/itervox)
	itervox=$work/itervox
fi
itervox=$(cd "$(dirname "$itervox")" && pwd)/$(basename "$itervox")
real_git=$(command -v git)

# A repository with one commit, on an issue branch.
repo=$work/repo
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
git init -q -b main "$repo"
git -C "$repo" -c user.name=t -c user.email=t@example.com commit -q --allow-empty -m base
git -C "$repo" checkout -q -b itervox/eng-1

# The runtime directory, as Prepare writes it.
rt=$work/rt
mkdir -p "$rt/bin" "$rt/hooks"
cat > "$rt/policy.json" <<EOF
{"identifier":"ENG-1","issue_branch":"itervox/eng-1","protected_branches":["main","master","develop"],
 "approval_commands":["vercel --prod"],"approvals_enabled":false,"orig_hooks_dir":"$repo/.git/hooks",
 "dir":"$rt","bin_dir":"$rt/bin","lanes":{}}
EOF
printf '#!/bin/sh\n# itervox-guardrail-shim\nexec %q guardrail-shim %q git "$@"\n' "$itervox" "$rt/policy.json" > "$rt/bin/git"
chmod 700 "$rt/bin/git"
export GIT_CONFIG_PARAMETERS="'core.hooksPath'='$rt/hooks'"

# time_runs <label> <cmd…>: N timed runs (after 10 warm-ups), one ms value
# per line into $work/<label>. EPOCHREALTIME keeps the timer in-process.
time_runs() {
	local label=$1
	shift
	local i t0 t1
	for ((i = 0; i < 10; i++)); do "$@" > /dev/null; done
	: > "$work/$label"
	for ((i = 0; i < N; i++)); do
		t0=$EPOCHREALTIME
		"$@" > /dev/null
		t1=$EPOCHREALTIME
		echo "$t0 $t1" | awk '{printf "%.3f\n", ($2 - $1) * 1000}' >> "$work/$label"
	done
}

# pct <file> <p>: the p-th percentile (nearest rank) of a column of ms.
pct() { sort -n "$1" | awk -v p="$2" '{v[NR] = $1} END {i = int((p / 100) * NR + 0.999999); if (i < 1) i = 1; print v[i]}'; }

cd "$repo"
for cmd in "rev-parse HEAD" "status --short"; do
	# shellcheck disable=SC2086 # word splitting of $cmd is intended
	time_runs direct "$real_git" $cmd
	# shellcheck disable=SC2086
	time_runs shim "$rt/bin/git" $cmd
	# shellcheck disable=SC2086
	PATH="$rt/bin:$PATH" git $cmd > /dev/null # the shim on PATH resolves and forwards
	d50=$(pct "$work/direct" 50) d95=$(pct "$work/direct" 95)
	s50=$(pct "$work/shim" 50) s95=$(pct "$work/shim" 95)
	printf 'git %-15s direct p50 %7.2f ms p95 %7.2f ms | shim p50 %7.2f ms p95 %7.2f ms | overhead p50 %6.2f ms p95 %6.2f ms (N=%d)\n' \
		"$cmd" "$d50" "$d95" "$s50" "$s95" "$(echo "$s50 $d50" | awk '{print $1 - $2}')" "$(echo "$s95 $d95" | awk '{print $1 - $2}')" "$N"
done

# Where the shim's own time goes: sh start-up, and itervox's start-up and
# package initialisation (GODEBUG=inittrace prints each package's init).
time_runs sh /bin/sh -c :
printf 'sh -c :            p50 %7.2f ms p95 %7.2f ms\n' "$(pct "$work/sh" 50)" "$(pct "$work/sh" 95)"
GODEBUG=inittrace=1 "$rt/bin/git" rev-parse HEAD 2>&1 > /dev/null | awk '/^init / {ms += $5} END {printf "itervox package init: %.2f ms (sum of GODEBUG=inittrace clock times, one run)\n", ms}'
