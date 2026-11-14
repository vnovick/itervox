package guardrails

import (
	"path"
	"slices"
	"strings"
)

// Lanes (guardrails.lanes): which files an agent's push may change on its
// own. Every changed path falls in exactly one lane, the highest that
// matches:
//
//   - human: the push waits for a human approval (the approval_commands
//     flow), and merge_pr never merges the issue's pull request — CI
//     workflows, CI and test-runner config, ownership rules, Itervox's own
//     configuration. An agent may change what CI checks, never how CI checks.
//   - second_opinion: the push goes through, but merge_pr needs an approving
//     review from a reviewer on a different backend than the writer's.
//   - auto: everything else.
const (
	LaneAuto          = "auto"
	LaneSecondOpinion = "second_opinion"
	LaneHuman         = "human"
)

// LaneRank orders lanes: auto < second_opinion < human. An unknown lane
// ranks as human (fail closed).
func LaneRank(lane string) int {
	switch lane {
	case LaneAuto, "":
		return 0
	case LaneSecondOpinion:
		return 1
	default:
		return 2
	}
}

// IsLane reports whether lane is second_opinion or human: the lanes a run
// reports to the daemon (auto is never reported).
func IsLane(lane string) bool { return lane == LaneSecondOpinion || lane == LaneHuman }

// Lanes are the human and second_opinion path patterns. Each pattern is a
// gitignore-style glob over a repository-relative path with `/` separators:
//
//   - `*` and `?` match within one path segment, `[…]` is a class;
//   - a `**` segment matches any number of segments, including none;
//   - a pattern with no `/` (other than a trailing one) matches a file or
//     directory of that name at any depth (`Makefile`, `jest.config.*`);
//     one with a `/` is anchored at the repository root (`.github/**`);
//   - a pattern that matches a directory matches everything under it;
//   - a pattern starting with `!` excludes what an earlier pattern of the
//     same lane matched (the last matching pattern wins);
//   - matching is case-sensitive, like git paths.
type Lanes struct {
	Human         []string `json:"human,omitempty"`
	SecondOpinion []string `json:"second_opinion,omitempty"`
}

// Empty reports whether no lane has a pattern: every path is auto.
func (l Lanes) Empty() bool { return len(l.Human) == 0 && len(l.SecondOpinion) == 0 }

// Lane is the lane of one repository-relative path.
func (l Lanes) Lane(p string) string {
	switch {
	case matchLaneList(l.Human, p):
		return LaneHuman
	case matchLaneList(l.SecondOpinion, p):
		return LaneSecondOpinion
	}
	return LaneAuto
}

// LaneResult classifies a set of paths: the highest lane any reached, and
// the paths in each lane above auto (sorted, each once).
type LaneResult struct {
	Lane          string
	Human         []string
	SecondOpinion []string
}

// Paths are the paths that put the result in its lane.
func (r LaneResult) Paths() []string {
	switch r.Lane {
	case LaneHuman:
		return r.Human
	case LaneSecondOpinion:
		return r.SecondOpinion
	}
	return nil
}

// Classify puts every path in its lane.
func (l Lanes) Classify(paths []string) LaneResult {
	r := LaneResult{Lane: LaneAuto}
	for _, p := range paths {
		switch l.Lane(p) {
		case LaneHuman:
			r.Human = append(r.Human, p)
		case LaneSecondOpinion:
			r.SecondOpinion = append(r.SecondOpinion, p)
		}
	}
	slices.Sort(r.Human)
	r.Human = slices.Compact(r.Human)
	slices.Sort(r.SecondOpinion)
	r.SecondOpinion = slices.Compact(r.SecondOpinion)
	switch {
	case len(r.Human) > 0:
		r.Lane = LaneHuman
	case len(r.SecondOpinion) > 0:
		r.Lane = LaneSecondOpinion
	}
	return r
}

// matchLaneList reports whether p is in a lane's pattern list: the last
// pattern that matches decides, and a `!` pattern excludes.
func matchLaneList(patterns []string, p string) bool {
	in := false
	for _, pat := range patterns {
		neg := strings.HasPrefix(pat, "!")
		if neg {
			pat = pat[1:]
		}
		if MatchLanePattern(pat, p) {
			in = !neg
		}
	}
	return in
}

// normalizeLanePath turns a path git or a user wrote into the matcher's
// form: `/` separators (a Windows `\` too), no leading `./` or `/`, no
// empty or `.` segments.
func normalizeLanePath(p string) []string {
	p = strings.ReplaceAll(p, `\`, "/")
	var segs []string
	for _, s := range strings.Split(p, "/") {
		if s != "" && s != "." {
			segs = append(segs, s)
		}
	}
	return segs
}

// MatchLanePattern reports whether the repository-relative path p matches
// one lane pattern (without a leading `!`). See Lanes for the syntax. A
// malformed pattern (an unclosed `[`) matches nothing; config validation
// rejects it before it gets here.
func MatchLanePattern(pattern, p string) bool {
	pattern = strings.TrimSpace(pattern)
	trimmed := strings.TrimRight(strings.ReplaceAll(pattern, `\`, "/"), "/")
	pat := normalizeLanePath(trimmed)
	segs := normalizeLanePath(p)
	if len(pat) == 0 || len(segs) == 0 {
		return false
	}
	if !strings.Contains(strings.TrimPrefix(trimmed, "/"), "/") && !strings.HasPrefix(trimmed, "/") {
		pat = append([]string{"**"}, pat...) // a bare name matches at any depth
	}
	// A match of the whole path or of any leading directory (a pattern that
	// matches a directory matches everything under it).
	return slices.Contains(matchSegments(pat, segs)[1:], true)
}

// matchSegments matches path segments against pattern segments, where a
// `**` pattern segment matches any number of path segments. Element n of
// the result reports whether the whole pattern matches segs[:n]. It is a
// dynamic programme over (pattern, path) positions, so a pattern with
// several `**` costs O(len(pat)*len(segs)) path.Match calls on any path,
// where trying every split recursively was exponential in the `**` count.
func matchSegments(pat, segs []string) []bool {
	// m[n]: the pattern's first i segments match segs[:n].
	m := make([]bool, len(segs)+1)
	m[0] = true
	for _, ps := range pat {
		next := make([]bool, len(segs)+1)
		for n := range next {
			if ps == "**" {
				next[n] = m[n] || (n > 0 && next[n-1])
				continue
			}
			if n > 0 && m[n-1] {
				ok, err := path.Match(ps, segs[n-1])
				next[n] = err == nil && ok
			}
		}
		m = next
	}
	return m
}
