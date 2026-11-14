package guardrails

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// guardrails.lanes: the path matcher. `**` spans segments, `*` stays in one,
// a directory pattern covers what is under it, a bare name matches at any
// depth, `!` excludes, matching is case-sensitive, and a Windows-style path
// is matched like its `/` form.
func TestLaneMatcherGlobs(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		// ** spans any number of segments, including none.
		{".github/**", ".github/workflows/ci.yml", true},
		{".github/**", ".github/CODEOWNERS", true},
		{".github/**", ".github", true},
		{".github/**", "src/.github/x", false},
		{"**/ci.yml", "ci.yml", true},
		{"**/ci.yml", "a/b/c/ci.yml", true},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**/b", "a/x/y/c", false},
		// * and ? stay inside one segment.
		{".github/*.yml", ".github/ci.yml", true},
		{".github/*.yml", ".github/workflows/ci.yml", false},
		{"jest.config.*", "jest.config.ts", true},
		{"jest.config.*", "packages/web/jest.config.mjs", true}, // bare name: any depth
		{"jest.config.*", "jest.configuration", false},
		{"vitest.config.?s", "web/vitest.config.ts", true},
		{"[Mm]akefile", "makefile", true},
		// Directory prefixes: a pattern that matches a directory covers it.
		{".circleci", ".circleci/config.yml", true},
		{".circleci/", ".circleci/config.yml", true},
		{"/Makefile", "Makefile", true},
		{"/Makefile", "sub/Makefile", false}, // a leading / anchors a bare name
		{"Makefile", "sub/Makefile", true},
		{"Makefile", "Makefile.bak", false},
		{"WORKFLOW.md", "docs/WORKFLOW.md.txt", false},
		{".itervox/**", ".itervox/agents/coder/SOUL.md", true},
		// Case-sensitive, like git.
		{".github/**", ".GitHub/workflows/ci.yml", false},
		{"Makefile", "makefile", false},
		// Windows-style and dotted paths are normalised.
		{".github/**", `.github\workflows\ci.yml`, true},
		{`.github\**`, ".github/workflows/ci.yml", true},
		{".github/**", "./.github/workflows/ci.yml", true},
		{".github/**", "/.github/workflows/ci.yml", true},
		// Malformed and empty patterns match nothing.
		{"[", "[", false},
		{"", "x", false},
		{"x", "", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, MatchLanePattern(tc.pattern, tc.path), "%q vs %q", tc.pattern, tc.path)
	}

	lanes := Lanes{
		Human:         []string{".github/**", ".itervox/**", "!.itervox/handoff/**", "Makefile"},
		SecondOpinion: []string{"internal/auth/**", "go.mod"},
	}
	assert.Equal(t, LaneHuman, lanes.Lane(".github/workflows/ci.yml"))
	assert.Equal(t, LaneAuto, lanes.Lane(".itervox/handoff/2026-11-14T10:00:00Z_coder.md"), "! excludes")
	assert.Equal(t, LaneHuman, lanes.Lane(".itervox/settings.jsonc"))
	assert.Equal(t, LaneSecondOpinion, lanes.Lane("internal/auth/token.go"))
	assert.Equal(t, LaneAuto, lanes.Lane("internal/server/handlers.go"))
	// Human wins over second_opinion for a path in both.
	assert.Equal(t, LaneHuman, Lanes{Human: []string{"go.mod"}, SecondOpinion: []string{"go.mod"}}.Lane("go.mod"))
	// A later pattern re-includes what a ! excluded (last match wins).
	assert.True(t, matchLaneList([]string{"a/**", "!a/b/**", "a/b/c"}, "a/b/c"))

	r := lanes.Classify([]string{"main.go", "internal/auth/token.go", ".github/workflows/ci.yml", "Makefile", ".github/workflows/ci.yml"})
	assert.Equal(t, LaneHuman, r.Lane)
	assert.Equal(t, []string{".github/workflows/ci.yml", "Makefile"}, r.Paths())
	assert.Equal(t, []string{"internal/auth/token.go"}, r.SecondOpinion)
	assert.Equal(t, LaneSecondOpinion, lanes.Classify([]string{"main.go", "go.mod"}).Lane)
	assert.Equal(t, LaneAuto, lanes.Classify([]string{"main.go", "main_test.go"}).Lane)
	assert.Empty(t, lanes.Classify(nil).Paths())

	assert.Less(t, LaneRank(LaneAuto), LaneRank(LaneSecondOpinion))
	assert.Less(t, LaneRank(LaneSecondOpinion), LaneRank(LaneHuman))
	assert.Equal(t, LaneRank(LaneHuman), LaneRank("bogus"), "an unknown lane ranks as human")
}
