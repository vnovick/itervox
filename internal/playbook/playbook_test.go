package playbook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// scaffold writes the init templates into a fresh playbooks directory.
func scaffold(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range TemplateNames {
		content, ok := Template(name)
		require.True(t, ok, name)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644))
	}
	return dir
}

// names lists a set's playbook names, sorted.
func names(s Set) []string {
	out := make([]string, 0, len(s.Playbooks))
	for _, p := range s.Playbooks {
		out = append(out, p.Name)
	}
	return out
}

func defaultCfg() config.PlaybooksConfig {
	return config.PlaybooksConfig{Enabled: true, Classify: true, Dir: config.DefaultPlaybooksDir, Labels: config.DefaultPlaybookLabels}
}

// TestPlaybookChosenByLabelThenKeywordThenNone (#218): a label mapped by
// agent.playbooks.labels wins over the keyword classifier; with no label
// match a confident keyword match is used; with no confident match (no
// keyword, or a tie) no playbook is attached. The operator's override comes
// first of all, "none" included.
func TestPlaybookChosenByLabelThenKeywordThenNone(t *testing.T) {
	set, errs := Load(scaffold(t))
	require.Empty(t, errs)
	require.Equal(t, []string{"bug-fix", "docs", "feature", "investigation", "perf", "refactor"}, names(set))
	cfg := defaultCfg()

	// 1. Label: "Bug" maps to bug-fix although the title reads like perf.
	c := Choose(set, cfg, Input{Labels: []string{"frontend", "Bug"}, Title: "Dashboard is slow, latency doubled"})
	assert.Equal(t, "bug-fix", c.Name())
	assert.Equal(t, SourceLabel, c.Source)
	assert.Equal(t, "Bug", c.Matched)

	// A label mapped to a playbook that does not exist is skipped.
	cfgMissing := defaultCfg()
	cfgMissing.Labels = map[string]string{"bug": "nope", "perf": "perf"}
	c = Choose(set, cfgMissing, Input{Labels: []string{"bug", "perf"}})
	assert.Equal(t, "perf", c.Name())

	// 2. Keyword: no mapped label; the title is about performance.
	c = Choose(set, cfg, Input{Labels: []string{"frontend"}, Title: "Board render is slow", Description: "Latency doubled after the last release; profile the memory use."})
	assert.Equal(t, "perf", c.Name())
	assert.Equal(t, SourceKeyword, c.Source)
	assert.Contains(t, c.Matched, "slow")

	// Whole words only: "debugging" does not contain the keyword "bug".
	_, _, ok := Classify(set, "Improve debugging output", "")
	assert.False(t, ok)

	// 3. None: nothing matches, or a single description-only hit (score 1)
	// is not confident, or two playbooks tie.
	c = Choose(set, cfg, Input{Title: "Quarterly planning", Description: "Discuss the roadmap."})
	assert.Empty(t, c.Name())
	assert.Empty(t, c.Source)
	c = Choose(set, cfg, Input{Title: "Quarterly planning", Description: "There is a crash somewhere."})
	assert.Empty(t, c.Name(), "one description keyword is not confident")
	c = Choose(set, cfg, Input{Title: "Crash in the slow path"})
	assert.Empty(t, c.Name(), "bug-fix and perf tie: not confident")

	// The classifier can be turned off.
	noClassify := defaultCfg()
	noClassify.Classify = false
	assert.Empty(t, Choose(set, noClassify, Input{Title: "Board render is slow and laggy, latency up"}).Name())

	// The override beats labels and keywords; "none" attaches nothing; an
	// override naming a deleted playbook falls back to automatic.
	c = Choose(set, cfg, Input{Override: "docs", Labels: []string{"bug"}})
	assert.Equal(t, "docs", c.Name())
	assert.Equal(t, SourceOverride, c.Source)
	c = Choose(set, cfg, Input{Override: None, Labels: []string{"bug"}})
	assert.Empty(t, c.Name())
	assert.Equal(t, SourceOverride, c.Source)
	c = Choose(set, cfg, Input{Override: "gone", Labels: []string{"bug"}})
	assert.Equal(t, "bug-fix", c.Name())
	assert.Equal(t, SourceLabel, c.Source)

	// Playbooks off: nothing, whatever the input.
	off := defaultCfg()
	off.Enabled = false
	assert.Empty(t, Choose(set, off, Input{Override: "docs", Labels: []string{"bug"}}).Name())
}

// TestPlaybookParseAndLoadBounds: frontmatter fields, the prompt block, and
// the files Load refuses (misnamed, oversized, malformed frontmatter).
func TestPlaybookParseAndLoadBounds(t *testing.T) {
	p, err := Parse("perf", []byte("---\ndescription: Measure first.\nkeywords: [Slow, \"High Latency\", slow]\nrequire_evidence: [Bench, ci, \"bad name!\"]\n---\nMeasure a baseline.\n"))
	require.NoError(t, err)
	assert.Equal(t, "Measure first.", p.Description)
	assert.Equal(t, []string{"slow", "high latency"}, p.Keywords)
	assert.Equal(t, []string{"bench", "ci"}, p.RequireEvidence, "normalized; malformed names dropped")
	assert.Equal(t, "## Playbook: perf\n\nMeasure a baseline.", p.Block())

	plain, err := Parse("docs", []byte("Write for the reader.\n"))
	require.NoError(t, err)
	assert.Equal(t, "Write for the reader.", plain.Body)
	empty, err := Parse("empty", []byte("---\ndescription: x\n---\n"))
	require.NoError(t, err)
	assert.Empty(t, empty.Block(), "an empty body adds no block")

	dir := t.TempDir()
	write := func(name, content string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
	}
	write("good.md", "Do the thing.")
	write("Bad Name.md", "x")
	write("huge.md", strings.Repeat("x", MaxFileBytes+1))
	write("broken.md", "---\nkeywords: [unclosed\n")
	write("notes.txt", "ignored")
	set, errs := Load(dir)
	assert.Equal(t, []string{"good"}, names(set))
	assert.Len(t, errs, 3)

	empty2, errs := Load(filepath.Join(dir, "missing"))
	assert.Empty(t, empty2.Playbooks)
	assert.Empty(t, errs)
}

// TestPlaybookDirResolvesAgainstWorkflow: agent.playbooks.dir is relative
// to WORKFLOW.md; off means no directory.
func TestPlaybookDirResolvesAgainstWorkflow(t *testing.T) {
	cfg := &config.Config{WorkflowPath: "/repo/WORKFLOW.md"}
	cfg.Agent.Playbooks = defaultCfg()
	assert.Equal(t, "/repo/.itervox/playbooks", Dir(cfg))
	cfg.Agent.Playbooks.Dir = "/abs/pb"
	assert.Equal(t, "/abs/pb", Dir(cfg))
	cfg.Agent.Playbooks.Enabled = false
	assert.Empty(t, Dir(cfg))
}
