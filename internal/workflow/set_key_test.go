package workflow_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/workflow"
)

func writeWF(t *testing.T, front string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(f, []byte("---\n"+front+"---\nPrompt body.\n"), 0o644))
	return f
}

func readWF(t *testing.T, f string) string {
	t.Helper()
	b, err := os.ReadFile(f)
	require.NoError(t, err)
	return string(b)
}

// TestSetFrontMatterKeysKeepsCommentsAndOrder (#158): the generic writer
// rewrites a key in place (keeping its inline comment), appends a missing key
// to its block, creates a missing block, and leaves every other line alone.
func TestSetFrontMatterKeysKeepsCommentsAndOrder(t *testing.T) {
	f := writeWF(t, "# top comment\ntracker:\n  kind: linear # the tracker\n  failed_state: \"Old\" # where failures go\n  # trailing comment\nhooks:\n  timeout_ms: 1000\n")
	require.NoError(t, workflow.SetFrontMatterKeys(f,
		workflow.KeyValue{Key: "tracker.failed_state", Value: "Backlog"},
		workflow.KeyValue{Key: "tracker.active_states", Value: []string{"Todo", "Doing"}},
		workflow.KeyValue{Key: "dependencies.analysis_mode", Value: "manual"},
		workflow.KeyValue{Key: "dependencies.confidence_threshold", Value: 0.8},
	))
	assert.Equal(t, "---\n# top comment\ntracker:\n  kind: linear # the tracker\n  failed_state: \"Backlog\" # where failures go\n"+
		"  active_states: [\"Todo\",\"Doing\"]\n  # trailing comment\nhooks:\n  timeout_ms: 1000\n"+
		"dependencies:\n  analysis_mode: \"manual\"\n  confidence_threshold: 0.8\n---\nPrompt body.\n", readWF(t, f))

	wf, err := workflow.Load(f)
	require.NoError(t, err)
	assert.Equal(t, "Prompt body.", wf.PromptTemplate)

	// nil removes; a no-op set writes nothing.
	require.NoError(t, workflow.SetFrontMatterKeys(f, workflow.KeyValue{Key: "tracker.failed_state", Value: nil}))
	assert.NotContains(t, readWF(t, f), "failed_state")
	before := readWF(t, f)
	require.NoError(t, workflow.SetFrontMatterKeys(f, workflow.KeyValue{Key: "dependencies.analysis_mode", Value: "manual"}))
	assert.Equal(t, before, readWF(t, f))
}

// TestMutateSetKeyFollowsFileIndent (#158): keys are written at the
// indentation the file already uses, nested parents included.
func TestMutateSetKeyFollowsFileIndent(t *testing.T) {
	f := writeWF(t, "tracker:\n    kind: linear\n")
	require.NoError(t, workflow.SetFrontMatterKeys(f,
		workflow.KeyValue{Key: "tracker.store.enabled", Value: false},
		workflow.KeyValue{Key: "tracker.kind", Value: "github"},
	))
	assert.Equal(t, "---\ntracker:\n    kind: \"github\"\n    store:\n        enabled: false\n---\nPrompt body.\n", readWF(t, f))

	// An empty block gets its first key one file-unit deep.
	f = writeWF(t, "tracker:\n    kind: linear\nhooks:\n")
	require.NoError(t, workflow.SetFrontMatterKeys(f, workflow.KeyValue{Key: "hooks.timeout_ms", Value: 5}))
	assert.Equal(t, "---\ntracker:\n    kind: linear\nhooks:\n    timeout_ms: 5\n---\nPrompt body.\n", readWF(t, f))
}

// TestMutateSetKeyRefusesWhatItCannotReach (#158): a flow-style parent, or an
// edit the file would not parse with, is refused and the file is left alone.
func TestMutateSetKeyRefusesWhatItCannotReach(t *testing.T) {
	f := writeWF(t, "tracker: {kind: linear}\n")
	before := readWF(t, f)
	err := workflow.SetFrontMatterKeys(f, workflow.KeyValue{Key: "tracker.failed_state", Value: "X"})
	require.Error(t, err)
	assert.Equal(t, before, readWF(t, f))

	f = writeWF(t, "\"dependencies\":\n  ordering: fifo\n")
	before = readWF(t, f)
	require.Error(t, workflow.SetFrontMatterKeys(f, workflow.KeyValue{Key: "dependencies.analysis_mode", Value: "manual"}),
		"a quoted header the line scan does not see would produce a duplicate key (CORE-125)")
	assert.Equal(t, before, readWF(t, f))

	require.Error(t, workflow.SetFrontMatterKeys(f, workflow.KeyValue{Key: "a..b", Value: 1}))
}

// TestMutateSetKeyEmptyFlowMapParent (#158): a parent written as {} becomes a
// block mapping holding the new key.
func TestMutateSetKeyEmptyFlowMapParent(t *testing.T) {
	f := writeWF(t, "dependencies: {} # none yet\n")
	require.NoError(t, workflow.SetFrontMatterKeys(f, workflow.KeyValue{Key: "dependencies.ordering", Value: "fifo"}))
	assert.Equal(t, "---\ndependencies: # none yet\n  ordering: \"fifo\"\n---\nPrompt body.\n", readWF(t, f))
}
