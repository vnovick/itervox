package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/workflow"
)

// setting is one layered key a settings save writes.
type setting = config.SettingAssignment

// persistSettings is the one persistence step of a dashboard/TUI save of
// layered settings (#129: agent.*, polling.*, server.*, workspace.*). Every
// key goes to the PROJECT settings file, .itervox/settings.jsonc next to
// WORKFLOW.md, with config.SetSettings, which keeps the file's comments and
// creates it when missing. WORKFLOW.md outranks that file, so the same save
// then removes each key, and its deprecated alias spelling, from WORKFLOW.md's
// front matter: left there, it would override the saved value at the next
// load. extra mutators (the profiles block, automations) ride in that same
// single WORKFLOW.md write.
//
// The two files move together: JSONC first, then WORKFLOW.md. When the
// WORKFLOW.md step fails, the JSONC file is put back to its previous bytes,
// or removed (with the .itervox directory, if this save created it and it is
// empty) when it did not exist. A restore that fails is logged at ERROR and
// returned alongside the WORKFLOW.md error. Callers change memory only after
// this returns nil, so a failed save leaves memory untouched.
//
// Neither write reloads the daemon (CORE-116). The watcher watches only
// WORKFLOW.md, and the key removal is a registered self-write; when no key
// is present in WORKFLOW.md and there is no extra mutator, nothing is written
// to it at all. A later reload (an operator edit of WORKFLOW.md) re-reads the
// settings layers through config.Load, so the saved value survives it.
//
// The caller holds beginSettingsSave, which serializes it against every
// other save and fences it against a reload.
func persistSettings(workflowPath string, settings []setting, extra ...workflow.Mutator) error {
	jsonc := config.ProjectSettingsPath(workflowPath)
	dir := filepath.Dir(jsonc)
	_, statErr := os.Stat(dir)
	dirExisted := statErr == nil
	before, readErr := os.ReadFile(jsonc)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("settings: read %s: %w", jsonc, readErr)
	}
	existed := readErr == nil

	if err := config.SetSettings(jsonc, settings...); err != nil {
		return err // SetSettings writes once, atomically: nothing changed
	}

	remove := make([]string, 0, 2*len(settings))
	for _, s := range settings {
		remove = append(remove, s.Key)
		if alt, ok := config.SettingAlias(s.Key); ok {
			remove = append(remove, alt)
		}
	}
	wfErr := runSettingsWorkflowStep(workflowPath)
	if wfErr == nil {
		wfErr = workflow.RemoveFrontMatterKeys(workflowPath, remove, extra...)
	}
	if wfErr == nil {
		return nil
	}
	if rbErr := restoreProjectSettings(jsonc, before, existed, dirExisted); rbErr != nil {
		slog.Error("settings: WORKFLOW.md write failed and restoring .itervox/settings.jsonc also failed; "+
			"the settings file holds a value the running config does not until the next reload",
			"path", jsonc, "workflow_error", wfErr, "restore_error", rbErr)
		return fmt.Errorf("%w; restoring %s also failed: %w", wfErr, jsonc, rbErr)
	}
	return wfErr
}

// restoreProjectSettings puts the project settings file back the way
// persistSettings found it.
func restoreProjectSettings(path string, before []byte, existed, dirExisted bool) error {
	if existed {
		return atomicfs.WriteFile(path, before, 0o644)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !dirExisted {
		_ = os.Remove(filepath.Dir(path)) // only if empty; a leftover empty dir is harmless
	}
	return nil
}

// settingsWorkflowStep runs just before persistSettings' WORKFLOW.md write,
// with the workflow path. A no-op in production; tests use it to fail that
// step (root ignores permission bits, so chmod cannot) and reach the JSONC
// restore.
var settingsWorkflowStep atomic.Pointer[func(workflowPath string) error]

func runSettingsWorkflowStep(workflowPath string) error {
	if f := settingsWorkflowStep.Load(); f != nil {
		return (*f)(workflowPath)
	}
	return nil
}
