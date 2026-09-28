package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// TestJanitor_PrunesAutoSwitchedMarkers (CORE-102): an issue absent from two
// consecutive polls loses its auto-switch marker (AutoSwitchedIdentifiers and
// AutoSwitchedAt) together with its overrides, so the next
// saveAutoSwitchedToDisk no longer writes it. Identifiers still present by
// buildPresentPredicate — a running worker, a DependencyAudit row — keep both.
func TestJanitor_PrunesAutoSwitchedMarkers(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	const (
		gone    = "ENG-GONE"
		running = "ENG-RUN"
		audited = "ENG-AUDIT"
		active  = "ENG-ACTIVE"
	)
	all := []string{gone, running, audited, active}
	state := &State{
		Running: map[string]*RunEntry{running: {}},
		DependencyAudit: map[string]*DependencyAuditEntry{
			"issue-audit": {Identifier: audited},
		},
		IssueProfiles:           map[string]string{},
		IssueBackends:           map[string]string{},
		AutoSwitchedIdentifiers: map[string]struct{}{},
		AutoSwitchedAt:          map[string]time.Time{},
	}
	for _, id := range all {
		state.IssueProfiles[id] = "codex-fallback"
		state.IssueBackends[id] = "codex"
		state.AutoSwitchedIdentifiers[id] = struct{}{}
		state.AutoSwitchedAt[id] = now
	}
	// janitor.go skips the sweep on an empty prior set, so both polls are
	// non-empty; only `active` is observed.
	prevActive := map[string]struct{}{active: {}}
	currentActive := map[string]struct{}{active: {}}

	pruneAbsentTrackerIssues(state, currentActive, prevActive)

	assert.NotContains(t, state.AutoSwitchedIdentifiers, gone)
	assert.NotContains(t, state.AutoSwitchedAt, gone)
	for _, id := range []string{running, audited, active} {
		assert.Contains(t, state.AutoSwitchedIdentifiers, id, "present identifier %s must keep its marker", id)
		assert.Contains(t, state.AutoSwitchedAt, id, "present identifier %s must keep its switch time", id)
	}

	path := filepath.Join(t.TempDir(), "auto_switched.json")
	o := &Orchestrator{cfg: &config.Config{}}
	o.SetAutoSwitchedFile(path)
	o.saveAutoSwitchedToDisk(state)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	env, err := decodeAutoSwitchedFile(data)
	require.NoError(t, err)
	assert.NotContains(t, env.Overrides, gone, "the saved file must not resurrect the pruned marker")
	for _, id := range []string{running, audited, active} {
		assert.Contains(t, env.Overrides, id)
	}
}
