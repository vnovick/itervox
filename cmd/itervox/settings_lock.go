package main

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/workflow"
)

// settingsGate serializes settings saves for one WORKFLOW.md and carries the
// settings generation: the count of config loads the daemon's main loop has
// made for that file. mu guards gen.
type settingsGate struct {
	mu  sync.Mutex
	gen uint64
}

var settingsGates sync.Map // absolute path -> *settingsGate

func settingsGateFor(path string) *settingsGate {
	key := path
	if abs, err := filepath.Abs(path); err == nil {
		key = abs
	}
	g, _ := settingsGates.LoadOrStore(key, &settingsGate{})
	return g.(*settingsGate)
}

// beginSettingsSave starts one settings save for the WORKFLOW.md at path on
// behalf of settings generation gen. It returns the unlock to defer, or an
// error wrapping server.ErrSettingsReloading when a reload has superseded gen.
//
// Within one generation (V4) the gate serializes each save's reads of current
// in-memory values, its WORKFLOW.md write, its in-memory apply and any
// rollback against every other save, so the file and memory move together.
// The workflow edit lock only covers the file write: once the daemon's own
// writes stopped reloading it (CORE-116), nothing re-synced the two after an
// interleaving, and a setter that builds the file content from memory (SSH
// hosts, profiles, worker bumps) could drop a concurrent save from the file.
//
// Across a reload (M1-close C2) the generation check is a fence. run() stops
// accepting connections but does not wait for in-flight handlers, so a save
// from generation N could write the file after generation N+1 had loaded it:
// the value then lived only in the file and in N's dead orchestrator, and as
// a daemon self-write it never triggered a reload. loadSettingsGeneration
// advances gen under this same mutex, so every save either completes before
// the load reads the file (and the load sees it), or finds gen moved on and
// is refused before writing anything. The handler answers 503 with
// Retry-After and the client re-sends the save to the new generation.
//
// A fence was chosen over the alternatives. Waiting for in-flight handlers
// would need a bound, since SSE handlers never finish, and a save that
// outlives the bound is exactly the unsafe case. Treating a late write as a
// foreign edit would reload the new generation, killing its agent turns, for
// a value the operator only ever meant to set. The fence costs one retry of
// a save that raced a reload.
//
// It is taken before the workflow edit lock and never while holding it, so
// the lock order is fixed.
func beginSettingsSave(path string, gen uint64) (func(), error) {
	g := settingsGateFor(path)
	g.mu.Lock()
	if g.gen != gen {
		cur := g.gen
		g.mu.Unlock()
		return nil, fmt.Errorf("settings: save from superseded generation %d (current %d): %w", gen, cur, server.ErrSettingsReloading)
	}
	return g.mu.Unlock, nil
}

// beginSettingsSave is the adapter's form: the adapter carries the
// generation its run() was started with.
func (a *orchestratorAdapter) beginSettingsSave() (func(), error) {
	return beginSettingsSave(a.workflowPath, a.settingsGen)
}

// currentSettingsGeneration reports the generation of the most recent load.
// run() and buildTUIConfig read it once, at the start of their generation;
// nothing advances it again until that run() has returned.
func currentSettingsGeneration(path string) uint64 {
	g := settingsGateFor(path)
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.gen
}

// loadSettingsGeneration is the main loop's reload step. Under the settings
// gate it advances the generation, which fences every save of the previous
// generation. It then drops the self-write links the file already reflects:
// every registering writer holds this gate, so none can be mid-write. Last,
// it loads the config. The returned Config's WorkflowHash is the watcher's
// baseline (workflow.WatchFrom).
//
// The generation advances even when the load fails: the previous generation
// has stopped either way, and the retried load will read whatever is on disk.
func loadSettingsGeneration(path string) (*config.Config, uint64, error) {
	g := settingsGateFor(path)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gen++
	workflow.ForgetSelfWrites(path)
	cfg, err := config.Load(path)
	return cfg, g.gen, err
}

// rollbackSettingsWrite undoes a settings save whose WORKFLOW.md write landed
// but whose in-memory apply failed (applyErr). A rollback that itself fails
// leaves the file holding a value memory does not — divergence that nothing
// repairs until the next reload — so it is logged at ERROR and returned
// alongside applyErr instead of being discarded (V4).
func rollbackSettingsWrite(field string, applyErr error, rollback func() error) error {
	if rbErr := rollback(); rbErr != nil {
		slog.Error("settings: save rejected after WORKFLOW.md was written, and restoring the previous value failed; "+
			"WORKFLOW.md and the running config disagree until the next reload",
			"field", field, "apply_error", applyErr, "rollback_error", rbErr)
		return fmt.Errorf("%w; rollback of %s in WORKFLOW.md also failed: %w", applyErr, field, rbErr)
	}
	return applyErr
}

// settingsBeforeApply runs between a settings save's WORKFLOW.md write and
// its in-memory apply, with the field name (CORE-114). A no-op in production;
// tests use it to change live config in that window and so reach the
// rollback path.
var settingsBeforeApply atomic.Pointer[func(field string)]

func runSettingsBeforeApply(field string) {
	if f := settingsBeforeApply.Load(); f != nil {
		(*f)(field)
	}
}
