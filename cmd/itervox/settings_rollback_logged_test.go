package main

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// TestSettingsRollbackFailureIsLogged (CORE-114): the reviewer save's
// in-memory apply (SetReviewerCfg) re-validates against the LIVE profile set,
// so a profile removed between the adapter's pre-validation and its apply
// makes the apply reject after the save was already written. With the
// rollback write also failing, the failure is logged at ERROR and returned,
// never discarded.
//
// #129: the save goes to .itervox/settings.jsonc and then removes the key
// from WORKFLOW.md (persistSettings). The rollback is made to fail at its
// WORKFLOW.md step through the settingsWorkflowStep seam rather than with a
// read-only directory: a chmod would have to cover the .itervox directory as
// well, and root (some sandboxes) ignores permission bits.
//
// The auto-clear half of the spec is not reachable any more:
// SetAutoClearWorkspaceCfg's only validator, config.ValidateAutoClearAutoReview,
// is a documented no-op since v0.2.0, so that apply cannot reject. Its
// rollback still goes through rollbackSettingsWrite, whose logging is pinned by
// TestSettingsRollbackFailureIsLoggedAndReturned.
func TestSettingsRollbackFailureIsLogged(t *testing.T) {
	adapter, path := newSettingsTestAdapter(t)
	enabled := true
	adapter.orch.SetProfilesCfg(map[string]config.AgentProfile{"reviewer": {Command: "claude", Enabled: &enabled}})

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	var rollbackStep atomic.Bool
	restoreStep := setSettingsWorkflowStep(func(workflowPath string) error {
		if workflowPath == path && rollbackStep.Load() {
			return errors.New("injected: WORKFLOW.md is not writable")
		}
		return nil
	})
	t.Cleanup(restoreStep)
	restoreHook := setSettingsBeforeApply(func(field string) {
		if field != "agent.reviewer_profile/auto_review" {
			return
		}
		// A concurrent profile removal, and a rollback write that cannot land.
		adapter.orch.SetProfilesCfg(map[string]config.AgentProfile{})
		rollbackStep.Store(true)
	})
	t.Cleanup(restoreHook)

	err := adapter.SetReviewerConfig("reviewer", true)
	require.Error(t, err, "the apply must reject: the profile is gone")
	assert.Contains(t, err.Error(), "rollback")
	assert.Contains(t, logs.String(), "level=ERROR")
	assert.Contains(t, logs.String(), "agent.reviewer_profile/auto_review")

	// The settings file still holds the forward save: the failed rollback
	// restored the file to what it held before the rollback began.
	settingsFile, readErr := os.ReadFile(config.ProjectSettingsPath(path))
	require.NoError(t, readErr)
	assert.Contains(t, string(settingsFile), `"reviewer_profile": "reviewer"`)
}

// setSettingsWorkflowStep installs a settingsWorkflowStep hook and returns
// the function that removes it.
func setSettingsWorkflowStep(f func(workflowPath string) error) (restore func()) {
	prev := settingsWorkflowStep.Swap(&f)
	return func() { settingsWorkflowStep.Store(prev) }
}

// setSettingsBeforeApply installs a settingsBeforeApply hook and returns the
// function that removes it.
func setSettingsBeforeApply(f func(field string)) (restore func()) {
	prev := settingsBeforeApply.Swap(&f)
	return func() { settingsBeforeApply.Store(prev) }
}
