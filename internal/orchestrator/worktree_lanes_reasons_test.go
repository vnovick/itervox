package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/workspace"
)

// lanesWorktree is the fixture's ENG-1 worktree.
func lanesWorktree(t *testing.T, o *Orchestrator) string {
	t.Helper()
	dir, err := o.worktreePath("ENG-1")
	require.NoError(t, err)
	return dir
}

// The Lanes view audits untracked files too: a new workflow that is not
// yet added has its finding (line from 1) and its lane.
func TestWorktreeLanesAuditsUntrackedFiles(t *testing.T) {
	o := lanesFixture(t)
	dir := lanesWorktree(t, o)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".github", "workflows", "new.yml"),
		[]byte("      - run: make test || true\n"), 0o644))

	v, err := o.WorktreeLanes(context.Background(), "ENG-1")
	require.NoError(t, err)
	var f *LaneFile
	for i := range v.Files {
		if v.Files[i].Path == ".github/workflows/new.yml" {
			f = &v.Files[i]
		}
	}
	require.NotNil(t, f, "the untracked file is listed")
	assert.Equal(t, guardrails.LaneHuman, f.Lane)
	require.Len(t, f.Findings, 1, "the untracked file is audited")
	assert.Equal(t, ".github/workflows/new.yml:1", f.Findings[0].Location())
	assert.Equal(t, "ci_or_true", f.Findings[0].Rule)
}

// Lane is the highest lane of every changed path, not only of the listed
// ones: a human-lane file past workspace.DiffMaxFiles still makes the
// change human, says why, and the view is marked truncated.
func TestWorktreeLanesLaneCoversFilesPastTheListCap(t *testing.T) {
	o := lanesFixture(t)
	o.cfg.Guardrails.Lanes.Human = []string{"zzz/**"} // read-only after startup: set before the call
	dir := lanesWorktree(t, o)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "a"), 0o755))
	for i := range workspace.DiffMaxFiles + 1 {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "a", fmt.Sprintf("f%05d.txt", i)), []byte("x\n"), 0o644))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "zzz"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "zzz", "pipeline.yml"), []byte("x\n"), 0o644))

	v, err := o.WorktreeLanes(context.Background(), "ENG-1")
	require.NoError(t, err)
	assert.True(t, v.Truncated)
	require.Len(t, v.Files, workspace.DiffMaxFiles)
	for _, f := range v.Files {
		require.NotEqual(t, "zzz/pipeline.yml", f.Path, "the file is past the list cap")
	}
	assert.Equal(t, guardrails.LaneHuman, v.Lane, "the lane covers every changed path")
	assert.Contains(t, guardrails.LaneReasonLines(v.Reasons, v.ReasonsTotal, 10), "zzz/pipeline.yml matched zzz/** → human")
}

// laneViewFindings is linear and bounded: 200k findings (half repeats)
// over 5k files finish in well under a second (bounded at 3 s for a
// loaded CI runner under -race), in file then line order,
// at most maxLaneFindings in all and maxLaneFileFindings per file, with
// the totals counted. (The quadratic version took ~42 s at 120k.)
func TestLaneViewFindingsIsLinear(t *testing.T) {
	const n, files = 200_000, 5_000
	paths := make([]string, files)
	for i := range paths {
		paths[i] = fmt.Sprintf("dir/f%05d.yml", files-1-i) // file order is not name order
	}
	found := make([]guardrails.AuditFinding, 0, n)
	for i := range n / 2 {
		f := guardrails.AuditFinding{Rule: "ci_or_true", Path: paths[i%files], Line: n - i}
		found = append(found, f, f)
	}
	start := time.Now()
	got := laneViewFindings(found, paths)
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 3*time.Second, "%d findings took %s", n, elapsed)
	assert.Equal(t, n/2, got.total)
	assert.True(t, got.truncated)
	require.Len(t, got.ordered, maxLaneFindings)
	assert.Equal(t, paths[0], got.ordered[0].Path, "file order first")
	assert.Less(t, got.ordered[0].Line, got.ordered[1].Line, "then line order")
	assert.Len(t, got.byFile[paths[0]], maxLaneFileFindings)
	assert.Equal(t, n/2/files, got.fileTotal[paths[0]])
}

// An agent's held push: whatever reasons the request carries are
// discarded, and the daemon recomputes them from the issue's worktree and
// the pushed refs (the committed range only). Off the event loop: the
// approval event is the only thing sent.
func TestRequestGuardrailApprovalRecomputesAgentPushReasons(t *testing.T) {
	o := lanesFixture(t)
	dir := lanesWorktree(t, o)
	head := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD"))
	spoofed := []guardrails.LaneReason{{Kind: guardrails.ReasonFinding, Path: "x.yml", Message: "trust me", Text: "rm -rf /"}}
	_, err := o.RequestGuardrailApproval("ENG-1", "run1", guardrails.ApprovalRequest{Key: "k1", Command: "git push origin itervox/eng-1",
		Lane: guardrails.LaneHuman, Paths: []string{".github/workflows/ci.yml"}, PathsTotal: 1, Reasons: spoofed, ReasonsTotal: 1,
		Refs: []guardrails.PushedRef{{RemoteRef: "refs/heads/itervox/eng-1", LocalSHA: head}}})
	require.NoError(t, err)
	ev := <-o.events
	require.NotNil(t, ev.guardrail)
	assert.False(t, ev.guardrail.ReasonsUnavailable)
	assert.Equal(t, []string{
		".github/workflows/ci.yml:3: failure ignored in a CI workflow (continue-on-error / allow_failure: true) [ci_continue_on_error]",
		".github/workflows/ci.yml matched .github/** → human",
	}, guardrails.LaneReasonLines(ev.guardrail.Reasons, ev.guardrail.ReasonsTotal, 10))
	q := guardrailQuestion(ev.guardrail)
	assert.NotContains(t, q, "trust me")
	assert.NotContains(t, q, "continue-on-error: true", "the question never carries the added line")
	assert.Empty(t, o.events, "nothing else reaches the event loop")
}

// When the reasons cannot be recomputed (malformed or unknown refs, no
// worktree) the question shows the paths with "reasons unavailable" —
// never the hook's text — and the input-required entry says so.
func TestRequestGuardrailApprovalReasonsUnavailable(t *testing.T) {
	o := lanesFixture(t)
	spoofed := []guardrails.LaneReason{{Kind: guardrails.ReasonFinding, Path: "x.yml", Message: "trust me"}}
	for i, c := range []struct {
		identifier string
		refs       []guardrails.PushedRef
	}{
		{"ENG-1", nil},
		{"ENG-1", []guardrails.PushedRef{{RemoteRef: "refs/heads/itervox/eng-1", LocalSHA: "HEAD"}}},
		{"ENG-1", []guardrails.PushedRef{{RemoteRef: "refs/heads/itervox/eng-1", LocalSHA: strings.Repeat("ab", 20)}}},
		{"ENG-404", []guardrails.PushedRef{{RemoteRef: "refs/heads/x", LocalSHA: strings.Repeat("ab", 20)}}},
	} {
		_, err := o.RequestGuardrailApproval(c.identifier, "run1", guardrails.ApprovalRequest{Key: fmt.Sprint("k", i), Command: "git push",
			Lane: guardrails.LaneHuman, Paths: []string{".github/workflows/ci.yml"}, PathsTotal: 1, Reasons: spoofed, ReasonsTotal: 1, Refs: c.refs})
		require.NoError(t, err)
		ev := <-o.events
		require.NotNil(t, ev.guardrail)
		assert.True(t, ev.guardrail.ReasonsUnavailable, "case %d", i)
		assert.Empty(t, ev.guardrail.Reasons, "case %d", i)
		q := guardrailQuestion(ev.guardrail)
		assert.Contains(t, q, "`.github/workflows/ci.yml`", "case %d: the paths are still shown", i)
		assert.Contains(t, q, "Why this needs a person: reasons unavailable", "case %d", i)
		assert.NotContains(t, q, "trust me", "case %d", i)
	}

	state := laneState(t)
	o.handleGuardrailApproval(&state, OrchestratorEvent{Type: EventGuardrailApproval, Identifier: "ENG-1",
		guardrail: &guardrailApprovalEvent{ID: "a1", RunID: "run1", Command: "git push", Lane: guardrails.LaneHuman,
			Paths: []string{".github/workflows/ci.yml"}, PathsTotal: 1, ReasonsUnavailable: true}})
	entry := state.InputRequiredIssues["ENG-1"]
	require.NotNil(t, entry)
	assert.True(t, entry.GuardrailLaneReasonsUnavailable)
	assert.Contains(t, entry.Context, laneReasonsUnavailableNote)
}

// The question is Markdown for the tracker comment: a rule message or a
// lane pattern cannot inject emphasis, links, HTML or mentions.
func TestHeldPushQuestionEscapesMarkdown(t *testing.T) {
	q := guardrailQuestion(&guardrailApprovalEvent{Command: "git push", Lane: guardrails.LaneHuman,
		Paths: []string{"a.yml"}, PathsTotal: 1, ReasonsTotal: 2, Reasons: []guardrails.LaneReason{
			{Kind: guardrails.ReasonToolBehaviour, Path: "a.yml", Message: "[click](http://x) <b>@here</b> *now*"},
			{Kind: guardrails.ReasonLaneRule, Path: "a.yml", Rule: "**/*.yml", Lane: guardrails.LaneHuman},
		}})
	assert.Contains(t, q, "- `a.yml`: \\[click\\](http://x) \\<b\\>\\@here\\</b\\> \\*now\\*\n")
	assert.Contains(t, q, "- `a.yml` matched `**/*.yml` → human\n")
}
