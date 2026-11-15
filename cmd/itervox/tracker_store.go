package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/github"
	"github.com/vnovick/itervox/internal/tracker/store"
	"github.com/vnovick/itervox/internal/tracker/webhook"
)

// trackerStorePath is the local tracker store file (#113), gitignored by the
// `/*.json` line of .itervox/.gitignore.
func trackerStorePath(workflowPath string) string {
	return filepath.Join(filepath.Dir(workflowPath), ".itervox", "tracker_store.json")
}

// trackerStoreScope identifies what a store file was pulled from: a file
// from another tracker, endpoint, project, assignee or active-state list is
// not used.
func trackerStoreScope(cfg *config.Config) string {
	active := make([]string, 0, len(cfg.Tracker.ActiveStates))
	for _, s := range cfg.Tracker.ActiveStates {
		active = append(active, strings.ToLower(s))
	}
	slices.Sort(active)
	parts := []string{cfg.Tracker.Kind, cfg.Tracker.Endpoint, cfg.Tracker.ProjectSlug, strings.Join(active, ",")}
	if a := strings.TrimSpace(cfg.Tracker.Assignee); a != "" {
		// #182: another assignee, team or label filter is another set of
		// issues. Only appended when set, so a project-only scope's key
		// (and the store file it names) is unchanged.
		parts = append(parts, "assignee="+a, "team="+strings.TrimSpace(cfg.Tracker.Team))
	}
	if len(cfg.Tracker.Labels) > 0 {
		labels := make([]string, 0, len(cfg.Tracker.Labels))
		for _, l := range cfg.Tracker.Labels {
			labels = append(labels, strings.ToLower(l))
		}
		slices.Sort(labels)
		parts = append(parts, "labels="+strings.Join(labels, ","))
	}
	if len(cfg.Tracker.Filter) > 0 {
		// #198: another filter is another set of issues.
		parts = append(parts, "filter="+cfg.Tracker.Filter.Key())
	}
	return strings.Join(parts, "|")
}

// withTrackerStore puts the local tracker store (#113) in front of tr when
// tracker.store.enabled is set and tr is a Linear or GitHub adapter, and
// keeps it synced until ctx ends. Otherwise tr is returned unchanged. With
// tracker.webhooks on (#131) it also returns the webhook receiver for the
// server, applying deliveries until ctx ends, and the store's periodic pull
// becomes the safety net.
func withTrackerStore(ctx context.Context, cfg *config.Config, workflowPath string, tr tracker.Tracker) (tracker.Tracker, server.WebhookHandler) {
	if !cfg.Tracker.Store.Enabled {
		return tr, nil
	}
	if cfg.Tracker.Kind != "linear" && cfg.Tracker.Kind != "github" {
		slog.Info("tracker store: not used for this tracker kind", "kind", cfg.Tracker.Kind)
		return tr, nil
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
		SyncInterval: time.Duration(storeSyncIntervalMs(t)) * time.Millisecond,
		// #114: full pulls in between catch what a changes listing misses.
		FullSyncInterval: time.Duration(t.Store.FullSyncIntervalMs) * time.Millisecond,
	})
	if err != nil {
		slog.Warn("tracker store: not used", "error", err)
		return tr, nil
	}
	go func() {
		defer failFastOnPanic("tracker-store-sync")
		s.Run(ctx)
	}()
	slog.Info("tracker store: enabled", "path", trackerStorePath(workflowPath), "sync_interval_ms", storeSyncIntervalMs(t),
		"full_sync_interval_ms", t.Store.FullSyncIntervalMs)
	if !t.Webhooks.Enabled {
		return wrapped, nil
	}
	logging.RegisterSecret(t.Webhooks.Secret)
	h, err := webhook.New(webhook.Config{Tracker: t.Kind, Secret: t.Webhooks.Secret, Repo: t.ProjectSlug, Store: s})
	if err != nil {
		slog.Warn("tracker webhooks: not used", "error", err)
		return wrapped, nil
	}
	go func() {
		defer failFastOnPanic("tracker-webhooks")
		h.Run(ctx)
	}()
	slog.Info("tracker webhooks: receiving", "path", "/api/v1/webhooks/"+t.Kind)
	return wrapped, h
}

// storeSyncIntervalMs is the store's periodic pull: the safety net when
// webhooks deliver the changes (#131), else tracker.store.sync_interval_ms.
func storeSyncIntervalMs(t config.TrackerConfig) int {
	if t.Webhooks.Enabled {
		return t.Webhooks.SafetyNetIntervalMs
	}
	return t.Store.SyncIntervalMs
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
