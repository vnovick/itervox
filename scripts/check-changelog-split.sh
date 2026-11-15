#!/usr/bin/env bash
# check-changelog-split.sh — proves the per-release split of CHANGELOG.md
# (#246) moved every entry and lost none.
#
# Until #246, the work for v0.2.2 … v0.3.0 was listed under one
# `## [Unreleased]` heading. That section, exactly as it was before the split,
# is kept as a fixture (testdata/changelog/unreleased-pre-split.txt). This
# check confirms that:
#
#   1. every release section in RELEASES exists in CHANGELOG.md and lists at
#      least one entry;
#   2. every content line of the fixture (an entry, or a paragraph that
#      follows one) appears in those release sections exactly once, byte for
#      byte, and nowhere else in CHANGELOG.md.
#
# Lines in the release sections that the fixture does not have (entries added
# after the split) are listed for information and do not fail the check.
#
# Usage: scripts/check-changelog-split.sh [CHANGELOG] [FIXTURE]
# Defaults: CHANGELOG.md and testdata/changelog/unreleased-pre-split.txt,
# relative to the repository root. Exits 0 on success, 1 on a failed check,
# 2 on a usage error.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
changelog="${1:-$root/CHANGELOG.md}"
fixture="${2:-$root/testdata/changelog/unreleased-pre-split.txt}"

RELEASES="0.3.0 0.2.9 0.2.8 0.2.7 0.2.6 0.2.5 0.2.4 0.2.3 0.2.2"

for f in "$changelog" "$fixture"; do
	if [[ ! -r "$f" ]]; then
		echo "check-changelog-split: cannot read $f" >&2
		exit 2
	fi
done

LC_ALL=C awk -v releases="$RELEASES" '
	# A content line: not blank, not a heading, not a section rule.
	function content(line) {
		return line !~ /^[[:space:]]*$/ && line !~ /^#/ && line != "---"
	}
	function short(line) {
		return length(line) > 110 ? substr(line, 1, 110) "…" : line
	}
	BEGIN {
		n = split(releases, rel, " ")
		for (i = 1; i <= n; i++) wanted[rel[i]] = 1
	}
	# First file: the fixture (the pre-split Unreleased section).
	FNR == NR {
		if (content($0)) {
			if (!($0 in want)) order[++nfix] = $0
			want[$0]++
		}
		next
	}
	# Second file: the split CHANGELOG.md.
	/^## \[/ {
		section = $0
		sub(/^## \[/, "", section)
		sub(/\].*$/, "", section)
		if (section in wanted) seen[section] = 1
		next
	}
	content($0) {
		total[$0]++
		if (section in wanted) {
			inrel[$0]++
			if ($0 ~ /^- /) entries[section]++
			if (!($0 in want) && !($0 in extra)) { extra[$0] = section; xorder[++nx] = $0 }
		}
	}
	END {
		fail = 0
		for (i = 1; i <= n; i++) {
			v = rel[i]
			if (!(v in seen)) { printf "FAIL: no \"## [%s]\" section\n", v; fail = 1; continue }
			if (entries[v] == 0) { printf "FAIL: section [%s] has no entries\n", v; fail = 1 }
		}
		moved = 0
		for (i = 1; i <= nfix; i++) {
			line = order[i]
			if (want[line] != 1) {
				printf "FAIL: fixture line appears %d times in the fixture: %s\n", want[line], short(line); fail = 1
			}
			if (inrel[line] != 1) {
				printf "FAIL: expected once in the release sections, found %d: %s\n", inrel[line] + 0, short(line); fail = 1
			} else if (total[line] != 1) {
				printf "FAIL: also found outside the release sections (%d in the file): %s\n", total[line], short(line); fail = 1
			} else {
				moved++
			}
		}
		for (i = 1; i <= nx; i++) {
			printf "info: not in the fixture (added after the split) [%s]: %s\n", extra[xorder[i]], short(xorder[i])
		}
		for (i = 1; i <= n; i++) printf "  [%s] %d entries\n", rel[i], entries[rel[i]]
		if (fail) {
			printf "check-changelog-split: FAILED (%d of %d fixture lines placed exactly once)\n", moved, nfix
			exit 1
		}
		printf "check-changelog-split: ok — all %d fixture lines appear exactly once across %d release sections\n", nfix, n
	}
' "$fixture" "$changelog"
