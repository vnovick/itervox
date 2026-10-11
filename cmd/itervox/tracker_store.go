package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/github"
	"github.com/vnovick/itervox/internal/tracker/store"
)

// trackerStorePath is the local tracker store file (#113), gitignored by the
// `/*.json` line of .itervox/.gitignore.
func trackerStorePath(workflowPath string) string {
	return filepath.Join(filepath.Dir(workflowPath), ".itervox", "tracker_store.json")
}

// trackerStoreScope identifies what a store file was pulled from: a file
// from another tracker, endpoint, project or active-state list is not used.
func trackerStoreScope(cfg *config.Config) string {
	active := make([]string, 0, len(cfg.Tracker.ActiveStates))
	for _, s := range cfg.Tracker.ActiveStates {
		active = append(active, strings.ToLower(s))
	}
	slices.Sort(active)
	return strings.Join([]string{cfg.Tracker.Kind, cfg.Tracker.Endpoint, cfg.Tracker.ProjectSlug, strings.Join(active, ",")}, "|")
}

// withTrackerStore puts the local tracker store (#113) in front of tr when
// tracker.store.enabled is set and tr is a Linear or GitHub adapter, and
// keeps it synced until ctx ends. Otherwise tr is returned unchanged.
func withTrackerStore(ctx context.Context, cfg *config.Config, workflowPath string, tr tracker.Tracker) tracker.Tracker {
	if !cfg.Tracker.Store.Enabled {
		return tr
	}
	if cfg.Tracker.Kind != "linear" && cfg.Tracker.Kind != "github" {
		slog.Info("tracker store: not used for this tracker kind", "kind", cfg.Tracker.Kind)
		return tr
	}
	t := cfg.Tracker
	views := [][]string{
		// The dashboard board, the TUI backlog and the startup cleanup.
		deduplicateStates(t.BacklogStates, t.ActiveStates, t.TerminalStates, t.CompletionState),
		append(append([]string{}, t.BacklogStates...), t.ActiveStates...),
		t.TerminalStates,
	}
	wrapped, s, err := store.Wrap(tr, store.Config{
		Path:         trackerStorePath(workflowPath),
		Scope:        trackerStoreScope(cfg),
		ActiveStates: t.ActiveStates,
		Views:        views,
		SyncInterval: time.Duration(t.Store.SyncIntervalMs) * time.Millisecond,
	})
	if err != nil {
		slog.Warn("tracker store: not used", "error", err)
		return tr
	}
	go func() {
		defer failFastOnPanic("tracker-store-sync")
		s.Run(ctx)
	}()
	slog.Info("tracker store: enabled", "path", trackerStorePath(workflowPath), "sync_interval_ms", t.Store.SyncIntervalMs)
	return wrapped
}

// repoCommentReader is the GitHub-only part of commentCommandTracker, which
// the tracker store does not forward.
type repoCommentReader interface {
	ListRepoCommentsSince(ctx context.Context, since time.Time) ([]github.RepoComment, error)
	CollaboratorPermission(ctx context.Context, login string) (string, error)
	AuthenticatedLogin(ctx context.Context) (string, error)
	AddCommentReaction(ctx context.Context, commentID, content string) error
}

// storeCommentCommands gives comment commands (#84) the store for issue
// reads and writes, so `/itervox run` moves the issue in the store and the
// next tick dispatches it, and the adapter for repository comments.
type storeCommentCommands struct {
	tracker.Tracker
	repoCommentReader
}

// commentCommandsTracker returns what startCommentCommands needs from tr:
// tr itself, or, behind the tracker store, the store joined with the
// adapter's repository-comment reads.
func commentCommandsTracker(tr tracker.Tracker) tracker.Tracker {
	w, ok := tr.(interface{ Upstream() tracker.Tracker })
	if !ok {
		return tr
	}
	rc, ok := w.Upstream().(repoCommentReader)
	if !ok {
		return tr
	}
	return storeCommentCommands{Tracker: tr, repoCommentReader: rc}
}
