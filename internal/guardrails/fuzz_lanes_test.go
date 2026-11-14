package guardrails

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

// refLanePattern is an independent reference for MatchLanePattern, written
// from the Lanes doc comment: the pattern becomes one regular expression
// over the path's segments, each followed by "/", matched as a prefix (a
// pattern that matches a directory covers what is under it). nil matches
// nothing. Patterns with a `[` class are left to MatchLanePattern alone.
func refLanePattern(pattern string) *regexp.Regexp {
	pattern = strings.ReplaceAll(strings.TrimSpace(pattern), `\`, "/")
	trimmed := strings.TrimRight(pattern, "/")
	segs := refLaneSegs(trimmed)
	if len(segs) == 0 {
		return nil
	}
	if !strings.Contains(trimmed, "/") {
		segs = append([]string{"**"}, segs...) // a bare name: any depth
	}
	var b strings.Builder
	b.WriteString(`^`)
	for _, s := range segs {
		if s == "**" {
			b.WriteString(`(?:[^/]+/)*`)
			continue
		}
		for _, c := range s {
			switch c {
			case '*':
				b.WriteString(`[^/]*`)
			case '?':
				b.WriteString(`[^/]`)
			default:
				b.WriteString(regexp.QuoteMeta(string(c)))
			}
		}
		b.WriteString(`/`)
	}
	return regexp.MustCompile(b.String())
}

func refLaneSegs(p string) []string {
	var out []string
	for _, s := range strings.Split(strings.ReplaceAll(p, `\`, "/"), "/") {
		if s != "" && s != "." {
			out = append(out, s)
		}
	}
	return out
}

func refLaneMatch(pattern, p string) bool {
	re := refLanePattern(pattern)
	segs := refLaneSegs(p)
	if re == nil || len(segs) == 0 {
		return false
	}
	return re.MatchString(strings.Join(segs, "/") + "/")
}

// refLaneList is the reference for a lane's list: the last pattern that
// matches decides, a `!` one excludes.
func refLaneList(patterns []string, p string) bool {
	in := false
	for _, pat := range patterns {
		body, neg := strings.CutPrefix(pat, "!")
		if refLaneMatch(body, p) {
			in = !neg
		}
	}
	return in
}

// refComparable: the reference covers ASCII patterns without a `[` class
// over ASCII paths. path.Match steps a `*` byte by byte, so after a `*` a
// `?` can match a lone continuation byte of a multi-byte rune
// (path.Match("*??", "€") is true), which a rune-stepping regexp does not
// reproduce; that quirk only ever widens a match.
func refComparable(patterns []string, p string) bool {
	if !utf8.ValidString(p) || strings.ContainsFunc(p, func(r rune) bool { return r >= utf8.RuneSelf }) {
		return false
	}
	for _, pat := range patterns {
		if strings.Contains(pat, "[") || !utf8.ValidString(pat) || strings.ContainsFunc(pat, func(r rune) bool { return r >= utf8.RuneSelf }) {
			return false
		}
	}
	return true
}

func FuzzLanes(f *testing.F) {
	for _, seed := range []struct{ human, second, path string }{
		{".github/**\n.itervox/**\n!.itervox/handoff/**\nMakefile", "internal/auth/**\ngo.mod", ".github/workflows/ci.yml"},
		{".github/**\n.itervox/**\n!.itervox/handoff/**\nMakefile", "internal/auth/**\ngo.mod", ".itervox/handoff/2026-11-14T10:00:00Z_coder.md"},
		{"Makefile", "", "sub/Makefile"},
		{"/Makefile", "", "sub/Makefile"},
		{"jest.config.*", "", "packages/web/jest.config.mjs"},
		{"a/**/b", "", "a/x/y/b/c"},
		{"**/ci.yml", "", "./ci.yml"},
		{".circleci/", "", ".circleci//config.yml/"},
		{`.github\**`, "", `.github\workflows\ci.yml`},
		{"[Mm]akefile", "", "makefile"},
		{"[", "", "["},
		{"a/**\n!a/b/**\na/b/c", "", "a/b/c"},
		{"**", "", "x"},
		{"*", "*.go", "main.go"},
		{"go.mod", "go.mod", "go.mod"},
		{" docs/** ", "", "docs/x.md"},
		{"./", "", "x"},
		{"", "", ""},
	} {
		f.Add(seed.human, seed.second, seed.path)
	}
	f.Fuzz(func(t *testing.T, rawHuman, rawSecond, p string) {
		if len(rawHuman)+len(rawSecond)+len(p) > 1024 {
			return
		}
		lanes := Lanes{Human: strings.Split(rawHuman, "\n"), SecondOpinion: strings.Split(rawSecond, "\n")}
		lane := lanes.Lane(p)

		// A path a human pattern list matches is human, never auto or
		// second_opinion; one only the second_opinion list matches is
		// second_opinion.
		human := matchLaneList(lanes.Human, p)
		second := matchLaneList(lanes.SecondOpinion, p)
		switch {
		case human && lane != LaneHuman:
			t.Fatalf("human patterns %q match %q, but its lane is %s", lanes.Human, p, lane)
		case !human && second && lane != LaneSecondOpinion:
			t.Fatalf("only second_opinion %q matches %q, but its lane is %s", lanes.SecondOpinion, p, lane)
		case !human && !second && lane != LaneAuto:
			t.Fatalf("no pattern matches %q, but its lane is %s", p, lane)
		}
		r := lanes.Classify([]string{p, p})
		if r.Lane != lane || slices.Contains(r.Human, p) != (lane == LaneHuman) || len(r.Human) > 1 || len(r.SecondOpinion) > 1 {
			t.Fatalf("Classify(%q) = %+v, Lane = %s", p, r, lane)
		}

		// Path normalisation: ./, a leading /, doubled and trailing
		// separators and Windows separators name the same path.
		for _, v := range []string{"./" + p, "/" + p, p + "/", strings.ReplaceAll(p, "/", "//"),
			strings.ReplaceAll(p, "/", `\`), strings.ReplaceAll(p, "/", "/./")} {
			if got := lanes.Lane(v); got != lane {
				t.Fatalf("Lane(%q) = %s, but Lane(%q) = %s", v, got, p, lane)
			}
		}

		// `!` negation: appending !q to a list removes exactly what q
		// matches; appending q adds it (the last match wins).
		for _, q := range lanes.SecondOpinion {
			if strings.HasPrefix(q, "!") {
				continue
			}
			m := MatchLanePattern(q, p)
			if got := matchLaneList(append(slices.Clone(lanes.Human), "!"+q), p); got != (human && !m) {
				t.Fatalf("%q + !%q on %q = %v; want %v", lanes.Human, q, p, got, human && !m)
			}
			if got := matchLaneList(append(slices.Clone(lanes.Human), q), p); got != (human || m) {
				t.Fatalf("%q + %q on %q = %v; want %v", lanes.Human, q, p, got, human || m)
			}
		}

		// Against the reference, where it applies.
		all := append(slices.Clone(lanes.Human), lanes.SecondOpinion...)
		if refComparable(all, p) {
			for _, pat := range all {
				body := strings.TrimPrefix(pat, "!")
				if got, want := MatchLanePattern(body, p), refLaneMatch(body, p); got != want {
					t.Fatalf("MatchLanePattern(%q, %q) = %v; reference %v", body, p, got, want)
				}
			}
			if got, want := human, refLaneList(lanes.Human, p); got != want {
				t.Fatalf("human list %q on %q = %v; reference %v", lanes.Human, p, got, want)
			}
		}
	})
}

// TestLaneMatcherIsPolynomial: found by FuzzLanes stalling. Trying every
// split of the path for each `**` was exponential in the number of `**`
// segments (six of them on a 400-segment path did not finish in minutes);
// the matcher is a dynamic programme now. The path is what the agent's
// push changes, so its depth is agent-controlled.
func TestLaneMatcherIsPolynomial(t *testing.T) {
	deep := strings.Repeat("a/", 400) + "b"
	pat := strings.Repeat("**/", 8) + "c"
	done := make(chan bool, 1)
	go func() { done <- MatchLanePattern(pat, deep) }()
	select {
	case got := <-done:
		if got {
			t.Fatalf("%q must not match a path without a c segment", pat)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("MatchLanePattern with eight ** on a 400-segment path did not finish in 10s")
	}
	assert.True(t, MatchLanePattern(pat, deep+"/c/d"))
}
