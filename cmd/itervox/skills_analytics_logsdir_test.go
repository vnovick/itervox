package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// claudeSessionFixture is one Claude session log, enough for
// skills.ParseClaudeRuntime to report runtime evidence. Its content does not
// matter here — only WHICH directory it was found in.
const claudeSessionFixture = `{"type":"system","system":{"tools":["Read"],"mcp_servers":[],"skills_loaded":["my-skill"]}}
{"type":"tool_use","tool":"Read"}
`

// writeClaudeSession drops a session log into dir so a runtime parse of dir
// yields evidence, and a parse of anywhere else does not.
func writeClaudeSession(t *testing.T, dir string) {
	t.Helper()
	sessionPath := filepath.Join(dir, "issue-1", "session-a.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(sessionPath), 0o755))
	require.NoError(t, os.WriteFile(sessionPath, []byte(claudeSessionFixture), 0o644))
}

// isolateHome points $HOME at an empty directory for the duration of the test.
//
// This is load-bearing, not hygiene. Analytics merges ParseClaudeRuntime(logsDir)
// with ParseCodexRuntime(homeDir), and HasRuntimeEvidence is true if EITHER
// finds anything. Against a real developer home the Codex side supplies
// evidence unconditionally, so every assertion below would hold whether or not
// logsDir was honoured — a probe with both log directories empty returned
// HasRuntimeEvidence=true. Isolating HOME is what makes the Claude/logsDir side
// the only possible source, and the assertions real rather than decorative.
func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

// analyticsAdapter builds an adapter with a populated skills cache, since
// Analytics returns nil without an inventory.
func analyticsAdapter(t *testing.T, workflowPath, logsDir string) *orchestratorAdapter {
	t.Helper()
	a := &orchestratorAdapter{
		cfg:          &config.Config{},
		workflowPath: workflowPath,
		logsDir:      logsDir,
	}
	a.initSkillsCache()
	require.NotNil(t, a.Inventory(), "inventory must be populated or Analytics returns nil")
	return a
}

// TestAnalyticsReadsOperatorSuppliedLogsDir is issue #65's acceptance.
//
// An operator who passes --logs-dir sends the daemon's logs somewhere other
// than the derived default. The adapter used to compute the default regardless,
// so analytics read a directory the daemon was not writing and rendered EMPTY
// rather than erroring — wrong, and silently so.
//
// The fixture is written ONLY to the operator-supplied directory. The default
// derivation for this workflow path points at an empty location, so runtime
// evidence can only appear if the threaded value was actually used.
func TestAnalyticsReadsOperatorSuppliedLogsDir(t *testing.T) {
	isolateHome(t) // must precede any defaultLogsDir call — it derives from $HOME
	operatorLogs := t.TempDir()
	writeClaudeSession(t, operatorLogs)

	// A workflow path whose DEFAULT logs dir is a different, empty place.
	workflowPath := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NotEqual(t, operatorLogs, defaultLogsDir(workflowPath),
		"fixture is meaningless unless the default derivation points elsewhere")

	a := analyticsAdapter(t, workflowPath, operatorLogs)

	snap := a.Analytics()
	require.NotNil(t, snap)
	assert.True(t, snap.HasRuntimeEvidence,
		"analytics must read the directory the daemon actually writes, not the derived default")
}

// TestAnalyticsFallsBackToDerivedLogsDir is the other half of the acceptance:
// without --logs-dir, behaviour is unchanged. The fixture goes to the DERIVED
// path and the threaded value is left empty.
func TestAnalyticsFallsBackToDerivedLogsDir(t *testing.T) {
	isolateHome(t) // must precede any defaultLogsDir call — it derives from $HOME
	workflowPath := filepath.Join(t.TempDir(), "WORKFLOW.md")
	derived := defaultLogsDir(workflowPath)
	require.NotEmpty(t, derived)
	writeClaudeSession(t, derived)
	t.Cleanup(func() { _ = os.RemoveAll(derived) })

	a := analyticsAdapter(t, workflowPath, "") // no --logs-dir threaded

	snap := a.Analytics()
	require.NotNil(t, snap)
	assert.True(t, snap.HasRuntimeEvidence,
		"with no operator override the derived per-project path must still be read")
}

// TestAnalyticsThreadedLogsDirWinsOverDerived pins PRECEDENCE, which neither
// test above can: each puts its fixture in exactly one place, so an
// implementation that checked both and merged would satisfy them.
//
// Here the evidence exists ONLY at the derived path and the threaded value
// points at an empty directory. Correct precedence therefore means finding
// NOTHING — the daemon writing to --logs-dir is the authority, and a stale
// default-path log must not be reported as this run's runtime evidence.
func TestAnalyticsThreadedLogsDirWinsOverDerived(t *testing.T) {
	isolateHome(t) // must precede any defaultLogsDir call — it derives from $HOME
	workflowPath := filepath.Join(t.TempDir(), "WORKFLOW.md")
	derived := defaultLogsDir(workflowPath)
	writeClaudeSession(t, derived) // evidence ONLY here
	t.Cleanup(func() { _ = os.RemoveAll(derived) })

	emptyOperatorLogs := t.TempDir() // threaded, but has no logs

	a := analyticsAdapter(t, workflowPath, emptyOperatorLogs)

	snap := a.Analytics()
	require.NotNil(t, snap)
	assert.False(t, snap.HasRuntimeEvidence,
		"the threaded --logs-dir must win outright; falling back to the derived path "+
			"would report another directory's logs as this run's evidence")
}
