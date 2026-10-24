package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/workflow"
)

// Live reload of the settings files (#245). The daemon watches WORKFLOW.md
// and the user, project and local settings files as one set: one debounce,
// one self-write rule (a save Itervox makes itself never reloads), one
// validation and one reload path. A settled change is first loaded and
// validated off to the side (probeConfigReload); only a config that would
// load asks the main loop to drain and reload, so an invalid edit — to any
// of the files — keeps the running generation and its config, logs one error
// naming the file, and shows on the dashboard's config-invalid banner until
// the next edit fixes it.

// watchedConfigFile is one file the daemon watches for its config.
type watchedConfigFile struct {
	// Layer is config.LayerWorkflow for WORKFLOW.md, else the settings
	// layer name (user, project, local).
	Layer  string
	Path   string
	Exists bool
}

// configWatchFiles lists the files the daemon watches for workflowPath:
// WORKFLOW.md, then each settings layer file that is not off (the user layer
// is off under ITERVOX_USER_SETTINGS=off, and under `go test` unless the
// variable names a file), whether it exists yet or not. `itervox doctor`
// prints it; the daemon's reload step forgets the self-write links of each.
func configWatchFiles(workflowPath string) []watchedConfigFile {
	out := []watchedConfigFile{{Layer: config.LayerWorkflow, Path: workflowPath, Exists: fileExists(workflowPath)}}
	for _, name := range config.SettingsLayerNames {
		if p := config.SettingsLayerPath(workflowPath, name); p != "" {
			out = append(out, watchedConfigFile{Layer: name, Path: p, Exists: fileExists(p)})
		}
	}
	return out
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// configWatchSet is what the daemon's watcher watches for one generation:
// WORKFLOW.md and the settings files cfg was loaded from, each with the hash
// of the bytes config.Load read as its baseline, so an edit that landed
// between the load and the watcher's start still reloads (M1-close C2).
func configWatchSet(workflowPath string, cfg *config.Config) []workflow.WatchedFile {
	files := []workflow.WatchedFile{{Path: workflowPath, Loaded: cfg.WorkflowHash}}
	for _, f := range cfg.SettingsFiles {
		files = append(files, workflow.WatchedFile{Path: f.Path, Loaded: f.Hash, Optional: true})
	}
	return files
}

// configWatchOptions are the watcher's timings and settle hook. Production
// passes the zero value (the workflow package defaults: 1 s poll, 2 s
// debounce); tests shrink them and use Settled to wait for a decision.
type configWatchOptions struct {
	Poll, Debounce time.Duration
	Settled        func(judged []string)
}

// watchConfigFiles watches WORKFLOW.md and cfg's settings files until ctx
// ends. A settled change that loads and validates calls requestReload; one
// that does not is reported (reportInvalidConfigEdit) and the running config
// is kept.
func watchConfigFiles(ctx context.Context, workflowPath string, cfg *config.Config, requestReload func(), opts configWatchOptions) error {
	w := workflow.FileSetWatch{
		Files:    configWatchSet(workflowPath, cfg),
		Poll:     opts.Poll,
		Debounce: opts.Debounce,
		Settled:  opts.Settled,
	}
	return w.Run(ctx, func(changed []string) {
		if err := probeConfigReload(workflowPath); err != nil {
			reportInvalidConfigEdit(changed, err)
			return
		}
		slog.Info("reload: config files changed", "paths", changed)
		requestReload()
	})
}

// probeConfigReload runs the checks the reload step runs (config.Load, then
// config.ValidateDispatch) without touching the running generation.
func probeConfigReload(workflowPath string) error {
	cfg, err := config.Load(workflowPath)
	if err != nil {
		return err
	}
	return config.ValidateDispatch(cfg)
}

// reportInvalidConfigEdit logs one error for an edit that does not load and
// publishes it to the dashboard's config-invalid banner. The running
// generation is not touched. The error names the file and, for a settings
// file that does not parse, the line ("hujson: line 3, column 16: ...", or
// "line 2: ..." for a section of the wrong shape). The banner clears at the
// next successful reload.
func reportInvalidConfigEdit(changed []string, err error) {
	path := invalidEditPath(changed, err)
	slog.Error("reload: config edit is invalid — keeping the running config; fix the file to apply it",
		"path", path, "error", err)
	publishConfigInvalid(&server.ConfigInvalidStatus{Path: path, Error: err.Error()})
}

// invalidEditPath picks the changed file the load error is about: the one it
// names (config.Load names a settings file that does not parse), else the
// first changed file.
func invalidEditPath(changed []string, err error) string {
	msg := err.Error()
	for _, p := range changed {
		if strings.Contains(msg, p) {
			return p
		}
	}
	if len(changed) == 0 {
		return ""
	}
	return changed[0]
}

// clearConfigInvalidAfterSave clears the banner a hand edit raised once a
// Settings page save leaves every file loading again (the JSON view is where
// an operator fixes a settings file from the dashboard). The save is the
// daemon's own write, so the watcher does not reload for it, and the banner
// would otherwise keep naming a file that is no longer invalid. What the
// save could not apply live it reports as needing a restart, as always.
func clearConfigInvalidAfterSave(workflowPath string) {
	if loadConfigInvalid() == nil {
		return
	}
	if probeConfigReload(workflowPath) == nil {
		publishConfigInvalid(nil)
	}
}
