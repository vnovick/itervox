package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/local"
	"github.com/vnovick/itervox/internal/tracker/localsync"
)

// syncedLocal is the local tracker with its two-way sync to Linear or
// GitHub (#118, tracker.sync). It is the local tracker everywhere (the
// embedded pointer carries every method and optional interface); the sync
// rides along so the run can start it and the sync status can read it.
type syncedLocal struct {
	*local.Tracker
	syncer *localsync.Syncer
	// refresh is the orchestrator's Refresh, set when the sync starts.
	refresh atomic.Pointer[func()]
}

// localSyncStatus is what the sync status (#117) reads from a synced local
// tracker.
type localSyncStatus interface {
	LocalSyncStatus() (localsync.Status, *outbox.Outbox)
}

// LocalSyncStatus implements localSyncStatus.
func (s *syncedLocal) LocalSyncStatus() (localsync.Status, *outbox.Outbox) {
	return s.syncer.Status(), s.syncer.Outbox()
}

// withLocalSync wraps the local tracker with its sync when tracker.sync is
// set. The sync is built, not started (startLocalSync starts it). A sync that
// cannot be built is logged and left off: the local tracker works without
// it.
func withLocalSync(cfg *config.Config, workflowPath string, tr tracker.Tracker) tracker.Tracker {
	lt, ok := tr.(*local.Tracker)
	sc := cfg.Tracker.Sync
	if !ok || !sc.Enabled {
		return tr
	}
	remoteCfg := &config.Config{Tracker: config.TrackerConfig{
		Kind: sc.Kind, Endpoint: sc.Endpoint, APIKey: sc.APIKey, ProjectSlug: sc.ProjectSlug,
		Assignee: sc.Assignee, Team: sc.Team, Labels: sc.Labels, // #182: narrow the project
		ActiveStates: sc.ActiveStates, TerminalStates: sc.TerminalStates, BacklogStates: sc.BacklogStates,
		CompletionState: sc.CompletionState,
	}}
	remote, err := buildTracker(remoteCfg)
	if err != nil {
		slog.Warn("local sync: cannot build the remote tracker; sync is off", "kind", sc.Kind, "error", err)
		return tr
	}
	dir := filepath.Join(filepath.Dir(workflowPath), ".itervox")
	ob, err := outbox.New(filepath.Join(dir, "sync_outbox.json"))
	if err != nil {
		slog.Warn("local sync: cannot open the sync outbox; sync is off", "error", err)
		return tr
	}
	ob.SetPushInterval(time.Duration(cfg.Tracker.PushIntervalMs) * time.Millisecond) // #115
	w := &syncedLocal{Tracker: lt}
	stateLabels := append(append(append([]string{}, sc.ActiveStates...), sc.TerminalStates...), sc.BacklogStates...)
	if sc.CompletionState != "" {
		stateLabels = append(stateLabels, sc.CompletionState)
	}
	w.syncer, err = localsync.New(localsync.Config{
		Kind: sc.Kind, Local: lt, Remote: remote, Outbox: ob,
		LedgerPath:   filepath.Join(dir, "sync_ledger.json"),
		StateMap:     sc.StateMap,
		PullStates:   sc.PullStates,
		StateLabels:  stateLabels,
		Interval:     time.Duration(sc.IntervalMs) * time.Millisecond,
		FullInterval: time.Duration(sc.FullSyncIntervalMs) * time.Millisecond,
		OnLocalChange: func() {
			if f := w.refresh.Load(); f != nil {
				(*f)()
			}
		},
	})
	if err != nil {
		slog.Warn("local sync: sync is off", "error", err)
		return tr
	}
	slog.Info("local sync: syncing the local issue files", "kind", sc.Kind, "project", sc.ProjectSlug)
	return w
}

// startLocalSync runs the sync until ctx ends, when tr is a synced local
// tracker; refresh wakes the orchestrator after the sync wrote files. The
// returned channel closes once the sync has stopped: run() joins it, because
// the sync writes .itervox/sync_outbox.json and sync_ledger.json and a
// reload's next generation opens them again.
func startLocalSync(ctx context.Context, tr tracker.Tracker, refresh func()) <-chan struct{} {
	done := make(chan struct{})
	w, ok := tr.(*syncedLocal)
	if !ok {
		close(done)
		return done
	}
	w.refresh.Store(&refresh)
	go func() {
		defer close(done)
		defer failFastOnPanic("local-sync")
		w.syncer.Run(ctx)
	}()
	return done
}
