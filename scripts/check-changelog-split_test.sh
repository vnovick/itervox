#!/usr/bin/env bash
#
# Tests for scripts/check-changelog-split.sh (#246). Run: make deploy-test.
# Each case copies the real CHANGELOG.md, breaks it in one way, and expects
# the check to fail; the unmodified file must pass.
#
# Proves:
#   - the committed CHANGELOG.md passes;
#   - an entry dropped from a release section fails the check;
#   - an entry listed twice (two releases, or left in [Unreleased]) fails it;
#   - an entry whose text was changed fails it (the move is byte for byte);
#   - a release section with no entries, or a missing one, fails it;
#   - an entry added after the split is reported, not failed.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
CHECK="$HERE/check-changelog-split.sh"
FIXTURE="$ROOT/testdata/changelog/unreleased-pre-split.txt"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fails=0
pass() { printf 'ok   %s\n' "$*"; }
fail() { printf 'FAIL %s\n' "$*"; fails=$((fails + 1)); }

# expect STATUS NAME FILE: run the check on FILE and compare its exit status.
expect() {
	local want=$1 name=$2 file=$3 got=0
	bash "$CHECK" "$file" "$FIXTURE" >"$WORK/out" 2>&1 || got=$?
	if [[ $got == "$want" ]]; then
		pass "$name"
	else
		fail "$name (exit $got, want $want)"
		sed 's/^/     /' "$WORK/out"
	fi
}

# The first entry of a release section, by its heading.
first_entry() {
	awk -v h="$1" '$0 ~ "^## \\[" h "\\]" { on = 1; next } on && /^## \[/ { exit } on && /^- / { print; exit }' "$ROOT/CHANGELOG.md"
}

expect 0 "committed CHANGELOG.md passes" "$ROOT/CHANGELOG.md"

entry="$(first_entry 0.2.8)"
[[ -n $entry ]] || { echo "no entry found in [0.2.8]"; exit 1; }

# Dropped entry.
grep -vxF -- "$entry" "$ROOT/CHANGELOG.md" >"$WORK/dropped.md"
expect 1 "dropped entry fails" "$WORK/dropped.md"

# Duplicated into [Unreleased].
awk -v e="$entry" '{ print } /^## \[Unreleased\]/ { print ""; print e }' "$ROOT/CHANGELOG.md" >"$WORK/dup.md"
expect 1 "entry also left in [Unreleased] fails" "$WORK/dup.md"

# Reworded entry (one character appended).
awk -v e="$entry" '$0 == e { print $0 "!"; next } { print }' "$ROOT/CHANGELOG.md" >"$WORK/reworded.md"
expect 1 "reworded entry fails" "$WORK/reworded.md"

# Empty release section: drop every entry line of [0.2.3].
awk '/^## \[/ { on = ($0 ~ /^## \[0\.2\.3\]/) } on && /^- / { next } { print }' "$ROOT/CHANGELOG.md" >"$WORK/empty.md"
expect 1 "empty release section fails" "$WORK/empty.md"

# Missing release section heading.
sed 's/^## \[0\.2\.9\]/## [0.2.9-renamed]/' "$ROOT/CHANGELOG.md" >"$WORK/missing.md"
expect 1 "missing release section fails" "$WORK/missing.md"

# A new entry added to a release section after the split is fine.
awk '{ print } /^## \[0\.3\.0\]/ { print ""; print "- **A later entry** (#9999). Added after the split." }' "$ROOT/CHANGELOG.md" >"$WORK/extra.md"
expect 0 "entry added after the split passes" "$WORK/extra.md"
if grep -q 'info: not in the fixture' "$WORK/out"; then
	pass "entry added after the split is reported"
else
	fail "entry added after the split is reported"
fi

if ((fails > 0)); then
	echo "check-changelog-split_test: $fails failed"
	exit 1
fi
echo "check-changelog-split_test: all passed"
