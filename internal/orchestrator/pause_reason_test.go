package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTransitionFailedPauseIsDistinguishableFromUserCancel closes issue
// #42-F. The worker signals a failed completion-state write through the SAME
// cancelled-IDs set it uses for a genuine user cancel, because both must stop
// the dispatch loop — so every automatic pause looked like a human decision.
// They are materially different: a transition failure means the agent's work
// succeeded and only the tracker write did not, which is recoverable without
// a human, while a user cancel is a deliberate stop.
func TestTransitionFailedPauseIsDistinguishableFromUserCancel(t *testing.T) {
	o := &Orchestrator{}

	// The worker marks the failure from its own goroutine.
	o.markTransitionFailed("ENG-1")

	// The event loop consumes it exactly once...
	require.True(t, o.takeTransitionFailed("ENG-1"))
	// ...and a second read must not re-label a LATER, genuine user cancel of
	// the same issue.
	assert.False(t, o.takeTransitionFailed("ENG-1"),
		"the mark must be consumed, or a stale one mislabels a real user cancel")
	assert.False(t, o.takeTransitionFailed("ENG-2"), "unmarked issues are user cancels")
}

// TestPauseReasonSetAndCleared pins that a reason never outlives its pause —
// otherwise a resumed issue keeps reporting why it was paused and the map
// grows for the daemon's lifetime.
func TestPauseReasonSetAndCleared(t *testing.T) {
	state := State{PausedIdentifiers: map[string]string{}}

	setPauseReason(&state, "ENG-1", PauseReasonTransitionFailed)
	assert.Equal(t, PauseReasonTransitionFailed, state.PauseReasons["ENG-1"])

	clearPauseReason(&state, "ENG-1")
	assert.NotContains(t, state.PauseReasons, "ENG-1")

	// Nil-map safety: a State assembled outside the loader must not panic the
	// event loop for the sake of a diagnostic field.
	var bare State
	assert.NotPanics(t, func() {
		setPauseReason(&bare, "ENG-9", PauseReasonUserCancelled)
		clearPauseReason(&bare, "ENG-9")
	})
}

// TestPauseReasonsSurviveRestart pins persistence, and pins that reasons are
// filtered to identifiers that are still paused — a reason for a
// no-longer-paused issue is stale by definition.
func TestPauseReasonsSurviveRestart(t *testing.T) {
	o := &Orchestrator{pausedFile: filepath.Join(t.TempDir(), "paused.json")}

	o.savePauseReasonsToDisk(map[string]string{
		"ENG-1": PauseReasonTransitionFailed,
		"ENG-2": PauseReasonUserCancelled,
		"ENG-3": PauseReasonRetriesExhausted, // no longer paused
	})

	restored := o.loadPauseReasonsFromDisk(State{
		PausedIdentifiers: map[string]string{"ENG-1": "id1", "ENG-2": "id2"},
	})

	assert.Equal(t, PauseReasonTransitionFailed, restored.PauseReasons["ENG-1"],
		"a recoverable pause must still be recognisable after a restart")
	assert.Equal(t, PauseReasonUserCancelled, restored.PauseReasons["ENG-2"])
	assert.NotContains(t, restored.PauseReasons, "ENG-3",
		"a reason whose identifier is no longer paused is stale and must be dropped")
}

// TestPauseReasonsMissingFileIsNotAnError covers the upgrade path: a daemon
// started against state written before reasons existed must load cleanly,
// with every pause reading as unknown rather than as some default reason.
func TestPauseReasonsMissingFileIsNotAnError(t *testing.T) {
	o := &Orchestrator{pausedFile: filepath.Join(t.TempDir(), "paused.json")}
	restored := o.loadPauseReasonsFromDisk(State{
		PausedIdentifiers: map[string]string{"ENG-1": "id1"},
	})
	assert.Empty(t, restored.PauseReasons["ENG-1"], "unknown, not a guessed default")
}

// TestPauseReasonPersistsThroughStoreSnap closes the trap the previous test
// fell into.
//
// TestPauseReasonsSurviveRestart calls savePauseReasonsToDisk DIRECTLY, so it
// stayed green while the feature was broken: the transition-failed reason is
// set in the worker-exit handler, which has no savePaused call of its own, so
// nothing ever wrote the reasons file and every reason was lost on restart.
// A test that never traverses the production path proves nothing about it.
//
// This drives the real persistence entry point instead.
func TestPauseReasonPersistsThroughStoreSnap(t *testing.T) {
	dir := t.TempDir()
	o := &Orchestrator{pausedFile: filepath.Join(dir, "paused.json")}

	state := State{
		PausedIdentifiers: map[string]string{"ENG-1": "id-1"},
		PauseReasons:      map[string]string{"ENG-1": PauseReasonTransitionFailed},
	}
	o.storeSnap(state)

	// A fresh daemon generation reloads from the same files.
	next := &Orchestrator{pausedFile: o.pausedFile}
	restored := next.loadPauseReasonsFromDisk(State{
		PausedIdentifiers: map[string]string{"ENG-1": "id-1"},
	})

	assert.Equal(t, PauseReasonTransitionFailed, restored.PauseReasons["ENG-1"],
		"a recoverable pause must still be recognisable after a restart — "+
			"otherwise it is indistinguishable from a user cancel, which is the "+
			"whole point of recording a reason")
}

// TestSavePauseReasonsSkipsWriteWhenEmpty pins the cost guard: storeSnap runs
// on every event-loop turn, and each save is an atomic write (temp + fsync +
// rename). Persisting an empty map every turn would be a file write per turn
// for nothing.
//
// The clearing half matters more than the skipping half — when the last pause
// is released the map goes empty and the file must still be rewritten, or a
// restart resurrects reasons for issues that are no longer paused.
func TestSavePauseReasonsSkipsWriteWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	o := &Orchestrator{pausedFile: filepath.Join(dir, "paused.json")}
	reasonsPath := filepath.Join(dir, "paused.json.reasons.json")

	// Nothing paused, no prior file: no file should be created at all.
	o.savePauseReasonsToDisk(map[string]string{})
	_, err := os.Stat(reasonsPath)
	assert.True(t, os.IsNotExist(err), "an empty map with no prior file must not write")

	// Something paused: the file appears.
	o.savePauseReasonsToDisk(map[string]string{"ENG-1": PauseReasonTransitionFailed})
	require.FileExists(t, reasonsPath)

	// The last pause is released: the file MUST be rewritten to empty, not
	// left stale.
	o.savePauseReasonsToDisk(map[string]string{})
	data, readErr := os.ReadFile(reasonsPath)
	require.NoError(t, readErr)
	assert.JSONEq(t, "{}", string(data),
		"clearing the last pause must clear the file, or a restart resurrects it")
}
