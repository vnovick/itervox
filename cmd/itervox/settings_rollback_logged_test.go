package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/config"
)

// TestSettingsRollbackFailureIsLogged (CORE-114): the reviewer save's
// in-memory apply (SetReviewerCfg) re-validates against the LIVE profile set,
// so a profile removed between the adapter's pre-validation and its apply
// makes the apply reject after WORKFLOW.md was already written. With the
// rollback write also failing (read-only directory), the failure is logged
// at ERROR and returned, never discarded.
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

	dir := filepath.Dir(path)
	restoreHook := setSettingsBeforeApply(func(field string) {
		if field != "agent.reviewer_profile/auto_review" {
			return
		}
		// A concurrent profile removal, and a directory the rollback cannot write.
		adapter.orch.SetProfilesCfg(map[string]config.AgentProfile{})
		require.NoError(t, os.Chmod(dir, 0o500))
	})
	t.Cleanup(func() { restoreHook(); _ = os.Chmod(dir, 0o700) })

	err := adapter.SetReviewerConfig("reviewer", true)
	require.Error(t, err, "the apply must reject: the profile is gone")
	assert.Contains(t, err.Error(), "rollback")
	assert.Contains(t, logs.String(), "level=ERROR")
	assert.Contains(t, logs.String(), "agent.reviewer_profile/auto_review")
}

// setSettingsBeforeApply installs a settingsBeforeApply hook and returns the
// function that removes it.
func setSettingsBeforeApply(f func(field string)) (restore func()) {
	prev := settingsBeforeApply.Swap(&f)
	return func() { settingsBeforeApply.Store(prev) }
}
