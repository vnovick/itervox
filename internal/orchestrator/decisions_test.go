package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workspace"
)

// TestDecisionsTSVParsedIntoTimeline (#218): every run's decisions.tsv next
// to its handoff file is parsed into a timeline, oldest run first: the
// header, comments and blank lines are ignored, a line without a decision is
// skipped and counted, an RFC 3339 time is parsed, extra tabs fold into the
// alternative, every field is redacted and bounded, rows and runs are
// capped, and the run.decisions_path binding names the file next to the
// run's handoff.
func TestDecisionsTSVParsedIntoTimeline(t *testing.T) {
	root := t.TempDir()
	cfg := automationBaseCfg()
	cfg.Workspace.Root = root
	o := New(cfg, tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates), nil, workspace.NewManager(cfg))
	hd := filepath.Join(root, "ENG-1", HandoffDirRelPath)
	require.NoError(t, os.MkdirAll(hd, 0o755))

	// The binding: next to the handoff file, same stem.
	handoff := handoffPathFor("2026-05-02T10-00-00.000Z", "implementer")
	assert.Equal(t, ".itervox/handoff/2026-05-02T10-00-00.000Z_implementer.decisions.tsv", decisionsPathFor(handoff))
	run := runBindings("2026-05-02T10-00-00.000Z", handoff, "main", "", nil)["run"].(map[string]any)
	assert.Equal(t, decisionsPathFor(handoff), run["decisions_path"])

	secret := "ghp_" + strings.Repeat("Z", 36)
	second := strings.Join([]string{
		"time\tdecision\twhy\talternative",
		"",
		"# a comment",
		"2026-05-02T10:01:00Z\tUse the merge base\tmain moves on after the branch is cut\tdiff against main's tip",
		"2026-05-02T10:02:00Z\t\twhy without a decision",
		"soon\tKeep the cap at 64 KiB\ttoken " + secret + "\tno cap\tor a stream",
		"just one field",
		"2026-05-02T10:03:00Z\t" + strings.Repeat("d", 2000),
	}, "\n")
	first := "2026-05-01T09:00:00Z\tReproduce first\tthe report had no steps\tpatch blind\n"
	require.NoError(t, os.WriteFile(filepath.Join(hd, "2026-05-02T10-00-00.000Z_implementer.decisions.tsv"), []byte(second), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(hd, "2026-05-01T09-00-00.000Z_researcher.decisions.tsv"), []byte(first), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(hd, "2026-05-01T09-00-00.000Z_researcher.md"), []byte("# not a decision log\n"), 0o644))

	runs := o.Decisions("ENG-1")
	require.Len(t, runs, 2)
	assert.Equal(t, "2026-05-01T09-00-00.000Z", runs[0].RunTimestamp, "oldest run first")
	assert.Equal(t, "researcher", runs[0].Role)
	require.Len(t, runs[0].Decisions, 1)
	assert.Equal(t, DecisionRow{Time: "2026-05-01T09:00:00Z", At: time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC),
		Decision: "Reproduce first", Why: "the report had no steps", Alternative: "patch blind"}, runs[0].Decisions[0])

	r := runs[1]
	assert.Equal(t, ".itervox/handoff/2026-05-02T10-00-00.000Z_implementer.decisions.tsv", r.Path)
	assert.Equal(t, "implementer", r.Role)
	assert.Equal(t, 2, r.Skipped, "the line without a decision and the one-field line")
	require.Len(t, r.Decisions, 3)
	assert.Equal(t, "Use the merge base", r.Decisions[0].Decision)
	assert.False(t, r.Decisions[0].At.IsZero())
	assert.Equal(t, "soon", r.Decisions[1].Time)
	assert.True(t, r.Decisions[1].At.IsZero(), "a time that is not RFC 3339 stays as written")
	assert.NotContains(t, r.Decisions[1].Why, secret, "fields are redacted")
	assert.Equal(t, "no cap or a stream", r.Decisions[1].Alternative, "extra tabs fold into the alternative")
	assert.LessOrEqual(t, len(r.Decisions[2].Decision), maxDecisionFieldBytes, "fields are bounded")
	assert.False(t, r.Truncated)

	// Rows per run are capped.
	var many strings.Builder
	for i := range maxDecisionRowsPerRun + 5 {
		fmt.Fprintf(&many, "2026-05-03T00:00:00Z\tdecision %d\twhy\n", i)
	}
	rows, truncated, _ := ParseDecisionsTSV(strings.NewReader(many.String()))
	assert.Len(t, rows, maxDecisionRowsPerRun)
	assert.True(t, truncated)
	// So is the bytes read.
	rows, truncated, _ = ParseDecisionsTSV(strings.NewReader("t\tkept\n" + strings.Repeat("# padding\n", maxDecisionsFileBytes/10+1)))
	assert.Len(t, rows, 1)
	assert.True(t, truncated, "a file larger than the read bound is flagged")

	// No worktree: nothing.
	assert.Nil(t, o.Decisions("ENG-404"))
}
