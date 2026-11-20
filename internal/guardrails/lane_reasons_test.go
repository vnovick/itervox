package guardrails

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Lanes.Match names the pattern that put a path in its lane: the last
// matching pattern of the winning lane, and "" for auto.
func TestLanesMatchReturnsDecidingPattern(t *testing.T) {
	l := Lanes{Human: []string{".github/**", "!.github/ISSUE_TEMPLATE/**", "Makefile"}, SecondOpinion: []string{"auth/**"}}
	for _, c := range []struct{ path, lane, rule string }{
		{".github/workflows/deploy.yml", LaneHuman, ".github/**"},
		{"sub/Makefile", LaneHuman, "Makefile"},
		{"auth/token.go", LaneSecondOpinion, "auth/**"},
		{".github/ISSUE_TEMPLATE/bug.md", LaneAuto, ""},
		{"main.go", LaneAuto, ""},
	} {
		lane, rule := l.Match(c.path)
		assert.Equal(t, c.lane, lane, c.path)
		assert.Equal(t, c.rule, rule, c.path)
	}
	r := l.Classify([]string{"auth/token.go", ".github/workflows/deploy.yml", "main.go"})
	assert.Equal(t, map[string]string{"auth/token.go": "auth/**", ".github/workflows/deploy.yml": ".github/**"}, r.Rules)
}

// The reasons a held push gives: findings first with file:line, then the
// lane pattern of each path, human before second_opinion.
func TestLaneReasonsFromLanesAndFindings(t *testing.T) {
	l := Lanes{Human: []string{".github/workflows/**"}, SecondOpinion: []string{"auth/**"}}
	r := l.Classify([]string{".github/workflows/deploy.yml", ".github/workflows/ci.yml", "auth/login.go", "main.go"})
	findings := []AuditFinding{{Rule: "ci_continue_on_error", Path: ".github/workflows/ci.yml", Line: 10,
		Message: "failure ignored in a CI workflow (continue-on-error / allow_failure: true)", Text: "      continue-on-error: true"}}
	reasons, total := LaneReasons(r, findings)
	require.Equal(t, 4, total)
	assert.Equal(t, []string{
		".github/workflows/ci.yml:10 adds `continue-on-error: true` (failure ignored in a CI workflow (continue-on-error / allow_failure: true))",
		".github/workflows/ci.yml matched .github/workflows/** → human",
		".github/workflows/deploy.yml matched .github/workflows/** → human",
		"auth/login.go matched auth/** → second_opinion",
	}, LaneReasonLines(reasons, total, 10))
	assert.Equal(t, ReasonFinding, reasons[0].Kind)
	assert.Equal(t, 10, reasons[0].Line)
	assert.Equal(t, ReasonLaneRule, reasons[1].Kind)
}

// Reasons are bounded (MaxLaneReasons kept, the total counted, "and K
// more" in text), redacted (a token in an added line never leaves), and an
// unknown kind or an empty path is dropped; the tool-behaviour slot is
// accepted.
func TestLaneReasonsBoundedAndRedacted(t *testing.T) {
	var findings []AuditFinding
	for i := range 30 {
		findings = append(findings, AuditFinding{Rule: "ci_or_true", Path: ".github/workflows/ci.yml", Line: i + 1,
			Message: "exit status discarded in a CI workflow (|| true)", Text: "run: make test || true"})
	}
	reasons, total := LaneReasons(LaneResult{Lane: LaneHuman}, findings)
	assert.Len(t, reasons, MaxLaneReasons)
	assert.Equal(t, 30, total)
	lines := LaneReasonLines(reasons, total, MaxLaneReasonsShown)
	assert.Len(t, lines, MaxLaneReasonsShown+1)
	assert.Equal(t, fmt.Sprintf("and %d more", 30-MaxLaneReasonsShown), lines[len(lines)-1])

	secret := "ghp_" + strings.Repeat("a", 36)
	in := []LaneReason{
		{Kind: ReasonFinding, Path: "deploy.sh", Line: 3, Rule: "git_no_verify", Message: "git hooks bypassed",
			Text: "GITHUB_TOKEN=" + secret + " git push --no-verify " + strings.Repeat("x", 400)},
		{Kind: "made_up", Path: "a"},
		{Kind: ReasonLaneRule, Path: "  ", Rule: "x"},
		{Kind: ReasonToolBehaviour, Path: "Makefile", Message: "the test target no longer runs go test", Lane: "weird"},
	}
	got, n := SanitizeLaneReasons(in, 0)
	require.Len(t, got, 2)
	assert.Equal(t, 2, n)
	assert.NotContains(t, got[0].Text, secret, "a token in an added line is redacted")
	assert.LessOrEqual(t, len(got[0].Text), maxReasonFieldBytes+len("…"))
	assert.Equal(t, ReasonToolBehaviour, got[1].Kind)
	assert.Equal(t, LaneHuman, got[1].Lane, "an unknown lane ranks as human")
	assert.Equal(t, "Makefile: the test target no longer runs go test", got[1].String())
}

// AuditRange reads a real diff: a commit range, and the working tree.
func TestAuditRangeFindsWeakeningOnRangeAndWorktree(t *testing.T) {
	r := newRig(t, nil)
	base := strings.TrimSpace(git(t, r.work, "rev-parse", "HEAD"))
	r.writeCommit(".github/workflows/ci.yml", "jobs:\n  test:\n    continue-on-error: true\n    steps:\n      - run: make test || true\n", "ci")
	head := strings.TrimSpace(git(t, r.work, "rev-parse", "HEAD"))
	res, err := AuditRange(context.Background(), r.work, base, head)
	require.NoError(t, err)
	var got []string
	for _, f := range res.Findings {
		got = append(got, FindingReason(f).Location()+" "+f.Rule)
	}
	assert.ElementsMatch(t, []string{".github/workflows/ci.yml:3 ci_continue_on_error", ".github/workflows/ci.yml:5 ci_or_true"}, got)
	assert.False(t, res.Truncated)

	require.NoError(t, os.WriteFile(filepath.Join(r.work, "calc_test.go"),
		[]byte("package calc\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {\n\tt.Skip(\"later\")\n}\n"), 0o644))
	git(t, r.work, "add", "calc_test.go") // staged, not committed: the worktree view
	res, err = AuditRange(context.Background(), r.work, head, "")
	require.NoError(t, err)
	require.Len(t, res.Findings, 1)
	assert.Equal(t, "calc_test.go:6 go_skip", FindingReason(res.Findings[0]).Location()+" "+res.Findings[0].Rule)
}
