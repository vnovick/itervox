#!/usr/bin/env bash
# templates_test.sh: runs the shell of each CI template in referee/ci/
# (GitLab CI, CircleCI, Buildkite) locally against the fixture repository the
# Action's end-to-end job judges (referee/testdata/fixture.sh), with the real
# referee binary installed by the template's own install step from a local
# release directory, and asserts:
#
#   - each YAML file parses, and the jobs that carry the approval are wired
#     (GitLab child pipeline with strategy: depend, CircleCI setup config
#     and approval workflow, Buildkite block step);
#   - the install, fetch-base and check blocks are the same in all three;
#   - on every fixture branch the template sees the exit codes the referee
#     GitHub Action's step script (referee/run.sh) reports, and maps them the
#     same way: audit 20 on the agent branch, lanes 0 on the fix, 10 on
#     second_opinion, 2 on an error; an exit 20 asks for the CI's approval
#     step (or fails with 20 under REFEREE_APPROVAL: none);
#   - a checksum mismatch and bad settings fail closed (2).
#
# Needs bash, sh, git, ruby (YAML from its standard library), tar, curl and
# go (unless REFEREE_BIN names a referee binary). shellcheck, when installed,
# also checks each template's extracted shell.
# Run: bash referee/ci/templates_test.sh
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -r "$tmp"' EXIT
templates=(gitlab circleci buildkite)

fail=0
ok() { echo "ok   $*"; }
bad() {
	echo "FAIL $*"
	fail=1
}

# --- the referee binary and a local release holding it ---
if [ -n "${REFEREE_BIN:-}" ]; then
	bin=$REFEREE_BIN
else
	(cd "$root" && go build -trimpath -ldflags="-s -w" -o "$tmp/build/referee" ./cmd/referee) || {
		echo "FAIL go build ./cmd/referee"
		exit 1
	}
	bin=$tmp/build/referee
fi
case $(uname -s) in Linux) os=linux ;; Darwin) os=darwin ;; *) echo "skip: unsupported OS" && exit 0 ;; esac
case $(uname -m) in x86_64 | amd64) arch=amd64 ;; aarch64 | arm64) arch=arm64 ;; *) echo "skip: unsupported arch" && exit 0 ;; esac
asset="referee_${os}_${arch}.tar.gz"
mkdir -p "$tmp/stage" "$tmp/release/v9.9.9"
cp "$bin" "$tmp/stage/referee"
tar -czf "$tmp/release/v9.9.9/$asset" -C "$tmp/stage" referee
sha() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | awk '{ print $1 }'; }
sum=$(sha "$tmp/release/v9.9.9/$asset")
printf '%s  %s\n' 0000000000000000000000000000000000000000000000000000000000000000 referee_other.tar.gz "$sum" "$asset" >"$tmp/release/v9.9.9/checksums.txt"

# --- the fixture, served from a local "origin" ---
mkdir "$tmp/fixture"
sh "$root/referee/testdata/fixture.sh" "$tmp/fixture" >/dev/null || {
	echo "FAIL fixture"
	exit 1
}
git clone -q --bare "$tmp/fixture" "$tmp/origin.git"

# --- parse the YAML and extract each template's shell ---
mkdir "$tmp/scripts"
if ruby -ryaml - "$here" "$tmp/scripts" <<'RUBY'; then ok "YAML parses and the approval jobs are wired"; else bad "YAML structure"; fi
dir, out = ARGV
def load(path) = YAML.safe_load(File.read(path), permitted_classes: [], aliases: false)
def want(cond, msg) = (cond or abort("templates_test: #{msg}"))

gl = load("#{dir}/gitlab-ci.yml")
want gl.dig("referee", "artifacts", "paths")&.include?("referee-approval.yml"), "gitlab: referee keeps referee-approval.yml"
want gl.dig("referee", "variables", "GIT_DEPTH") == "0", "gitlab: GIT_DEPTH 0"
bridge = gl["referee-approval"]
want bridge.dig("trigger", "strategy") == "depend", "gitlab: the child pipeline is strategy: depend"
want bridge.dig("trigger", "include", 0) == { "artifact" => "referee-approval.yml", "job" => "referee" }, "gitlab: the child pipeline is the referee job's artifact"
want bridge["needs"] == ["referee"], "gitlab: referee-approval needs referee"
File.write("#{out}/gitlab.sh", gl.dig("referee", "script").join("\n"))

cs = load("#{dir}/circleci-config.yml")
want cs["setup"] == true, "circleci: setup: true"
steps = cs.dig("jobs", "referee", "steps")
cont = steps.find { |s| s.is_a?(Hash) && s.key?("continuation/continue") }&.dig("continuation/continue")
want cont && cont["parameters"] == "referee-parameters.json", "circleci: continue with referee-parameters.json"
want cont["configuration_path"] == ".circleci/continue-config.yml", "circleci: continue with .circleci/continue-config.yml"
want cs.dig("orbs", "continuation") =~ /\Acircleci\/continuation@\d+\.\d+\.\d+\z/, "circleci: the continuation orb is pinned"
run = steps.find { |s| s.is_a?(Hash) && s.key?("run") }["run"]
File.write("#{out}/circleci.sh", run["command"])

cc = load("#{dir}/circleci-continue-config.yml")
want cc.dig("parameters", "referee-needs-approval", "type") == "boolean", "circleci: referee-needs-approval parameter"
wf = cc["workflows"]
want wf.dig("referee", "when", "not") == "<< pipeline.parameters.referee-needs-approval >>", "circleci: referee workflow without approval"
want wf.dig("referee", "jobs") == ["referee-gate"], "circleci: referee workflow runs referee-gate"
want wf.dig("referee-approval", "when") == "<< pipeline.parameters.referee-needs-approval >>", "circleci: approval workflow"
jobs = wf.dig("referee-approval", "jobs")
want jobs[0] == { "pipeline-approval" => { "type" => "approval" } }, "circleci: pipeline-approval is type: approval"
want jobs[1].dig("referee-gate", "requires") == ["pipeline-approval"], "circleci: referee-gate requires the approval"

bk = load("#{dir}/buildkite-pipeline.yml")
step = bk["steps"][0]
want step["key"] == "referee", "buildkite: the referee step"
cmd = step["command"]
want cmd !~ /(?<!\$)\$(?!\$)/, "buildkite: every $ in the command is written $$"
File.write("#{out}/buildkite.sh", cmd.gsub("$$", "$"))

# The shared blocks, byte for byte.
blocks = %w[gitlab circleci buildkite].to_h do |t|
  s = File.read("#{out}/#{t}.sh")
  [t, %w[install fetch\ base check].map { |b| s[/^ *# >>> referee #{b} .*?# <<< referee #{b}$/m] or abort("templates_test: #{t}: no #{b} block") }]
end
want blocks.values.uniq.size == 1, "the install, fetch-base and check blocks differ between the templates"
RUBY

if command -v shellcheck >/dev/null 2>&1; then
	for t in "${templates[@]}"; do
		if shellcheck -s sh "$tmp/scripts/$t.sh"; then ok "shellcheck $t"; else bad "shellcheck $t"; fi
	done
fi

# A buildkite-agent stub that keeps what the step uploads.
mkdir "$tmp/stub"
cat >"$tmp/stub/buildkite-agent" <<'STUB'
#!/bin/sh
[ "$1 $2 $3" = "pipeline upload --no-interpolation" ] || { echo "buildkite-agent stub: $*" >&2; exit 2; }
cp "$4" "$STUB_UPLOAD"
STUB
chmod +x "$tmp/stub/buildkite-agent"

# base_env TEMPLATE BRANCH: the CI's own variables for a change into BRANCH.
base_env() {
	case $1 in
	gitlab) echo "CI_MERGE_REQUEST_TARGET_BRANCH_NAME=$2" ;;
	circleci) echo "REFEREE_BASE_BRANCH=$2" ;;
	buildkite) echo "BUILDKITE_PULL_REQUEST=7 BUILDKITE_PULL_REQUEST_BASE_BRANCH=$2" ;;
	esac
}

# run TEMPLATE BRANCH [ENV...]: runs the template's shell in a fresh clone
# checked out (detached, as CI does) at BRANCH. Sets $code, $work, $log.
n=0
run() {
	local t=$1 branch=$2 depth=()
	shift 2
	# Only the last run's files are kept: each install unpacks a binary.
	[ -z "${work:-}" ] || rm -r "$work" "$work.tmp"
	n=$((n + 1))
	work=$tmp/run$n
	log=$tmp/run$n.log
	mkdir "$work.tmp"
	[ "${SHALLOW:-}" = 1 ] && depth=(--depth 1)
	git clone -q "${depth[@]}" "file://$tmp/origin.git" "$work" 2>/dev/null
	git -C "$work" fetch -q "${depth[@]}" origin "+refs/heads/$branch:refs/remotes/origin/$branch" 2>/dev/null
	git -C "$work" checkout -q --detach "origin/$branch"
	# shellcheck disable=SC2046 # base_env prints NAME=value words
	(cd "$work" && env -i PATH="$tmp/stub:/usr/local/bin:/usr/bin:/bin" HOME="$tmp" TMPDIR="$work.tmp" \
		REFEREE_VERSION=9.9.9 REFEREE_SHA256="$sum" REFEREE_DOWNLOAD_URL="file://$tmp/release" \
		STUB_UPLOAD="$work.upload" $(base_env "$t" main) "$@" sh "$tmp/scripts/$t.sh") >"$log" 2>&1
	code=$?
}

# approval: what the job asked the CI for (none, pass or approve).
approval() {
	case $1 in
	gitlab)
		[ -f "$work/referee-approval.yml" ] || { echo none; return; }
		ruby -ryaml -e 'y = YAML.safe_load(File.read(ARGV[0]))
			a = y["approve-pipeline-change"]
			if a then puts(a["when"] == "manual" && a["allow_failure"] == false && a.dig("environment", "name") == "pipeline-approval" ? "approve" : "bad")
			elsif y.keys == ["referee-passed"] then puts "pass" else puts "bad" end' "$work/referee-approval.yml"
		;;
	circleci)
		[ -f "$work/referee-parameters.json" ] || { echo none; return; }
		ruby -rjson -e 'v = JSON.parse(File.read(ARGV[0]))["referee-needs-approval"]
			puts({ true => "approve", false => "pass" }.fetch(v, "bad"))' "$work/referee-parameters.json"
		;;
	buildkite)
		[ -f "$work.upload" ] || { echo pass; return; }
		ruby -ryaml -e 's = YAML.safe_load(File.read(ARGV[0]))["steps"]
			puts(s[0]["block"] && s[0]["blocked_state"] == "running" && s[0]["key"] == "pipeline-approval" &&
				s[1]["depends_on"] == "pipeline-approval" ? "approve" : "bad")' "$work.upload"
		;;
	esac
}

# Buildkite has no artifact for a passing run: "pass" there is the absence of
# an upload, so an error (no file either) is told apart by the exit code.
expect() { # expect NAME WANT-EXIT WANT-APPROVAL TEMPLATE BRANCH [ENV...]
	local name=$1 want=$2 want_approval=$3 t=$4 branch=$5
	shift 5
	run "$t" "$branch" "$@"
	local got_approval
	got_approval=$(approval "$t")
	if [ "$t" = buildkite ] && [ "$want_approval" = none ]; then want_approval=pass; fi
	if [ "$code" = "$want" ] && [ "$got_approval" = "$want_approval" ]; then
		ok "$t: $name"
	else
		bad "$t: $name: exit $code approval $got_approval, want exit $want approval $want_approval"
		sed 's/^/     | /' "$log"
	fi
}

# The Action's step script on the same clone and binary: its exit-code
# outputs for lanes and audit (fail-on human).
action_codes() { # action_codes BRANCH -> "L A"
	local w=$tmp/action-$1 l a
	git clone -q "file://$tmp/origin.git" "$w" 2>/dev/null
	git -C "$w" checkout -q --detach "origin/$1"
	for mode in lanes audit; do
		(cd "$w" && env REFEREE_BIN="$bin" GITHUB_OUTPUT="$w.$mode" MODE=$mode BASE=origin/main bash "$root/referee/run.sh") >/dev/null 2>&1
	done
	l=$(sed -n 's/^exit-code=//p' "$w.lanes")
	a=$(sed -n 's/^exit-code=//p' "$w.audit")
	echo "$l $a"
}

for branch in code-only agent audit-skip human second-opinion; do
	want_codes=$(action_codes "$branch")
	for t in "${templates[@]}"; do
		run "$t" "$branch" REFEREE_APPROVAL=none
		got=$(sed -n 's/^referee: lanes exit \([0-9]*\), audit exit \([0-9]*\)$/\1 \2/p' "$log")
		if [ "$got" = "$want_codes" ]; then
			ok "$t: $branch: lanes and audit exit $got, as the Action's"
		else
			bad "$t: $branch: lanes and audit exit '$got', the Action's '$want_codes'"
			sed 's/^/     | /' "$log"
		fi
	done
done

for t in "${templates[@]}"; do
	expect "the fix (code-only): lanes 0, audit 0, no approval" 0 pass "$t" code-only
	expect "agent branch (continue-on-error): approval step" 0 approve "$t" agent
	expect "agent branch, REFEREE_APPROVAL none: exit 20" 20 none "$t" agent REFEREE_APPROVAL=none
	expect "audit-skip (t.Skip): approval step" 0 approve "$t" audit-skip
	expect "audit-skip, REFEREE_APPROVAL none: exit 20" 20 none "$t" audit-skip REFEREE_APPROVAL=none
	expect "human lane: approval step" 0 approve "$t" human
	expect "human lane, REFEREE_FAIL_ON never: reported, no approval" 0 pass "$t" human REFEREE_FAIL_ON=never
	expect "audit-skip, REFEREE_FAIL_ON never: reported, no approval" 0 pass "$t" audit-skip REFEREE_FAIL_ON=never
	expect "second_opinion: passes, no approval" 0 pass "$t" second-opinion
	expect "second_opinion, REFEREE_FAIL_ON second_opinion: exit 10" 10 none "$t" second-opinion REFEREE_FAIL_ON=second_opinion
	SHALLOW=1 expect "shallow clone: unshallowed, the fix passes" 0 pass "$t" code-only
	# shellcheck disable=SC2046
	expect "a base that is not on origin: exit 2" 2 none "$t" code-only $(base_env "$t" nope)
	expect "checksum pin mismatch: exit 2" 2 none "$t" code-only REFEREE_SHA256=0000000000000000000000000000000000000000000000000000000000000000
	expect "bad REFEREE_FAIL_ON: exit 2" 2 none "$t" code-only REFEREE_FAIL_ON=sometimes
	expect "bad REFEREE_APPROVAL: exit 2" 2 none "$t" agent REFEREE_APPROVAL=maybe
done

# The job runs the binary its install step unpacked (the PATH above has no
# other referee): its --version line is in the log.
want_version=$("$bin" --version)
for t in "${templates[@]}"; do
	run "$t" code-only
	if grep -qxF "$want_version" "$log"; then ok "$t: runs the installed release ($want_version)"; else bad "$t: no '$want_version' line"; fi
done

exit "$fail"
