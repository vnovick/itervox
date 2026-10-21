package main

import (
	"cmp"
	"context"
	"maps"
	"path/filepath"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/workspace"
)

// Failure hotspots (#201): files and directories ranked by how often agent
// work on them went wrong over the run-history window. A run "went wrong"
// when it failed or stalled, asked for input, was a reviewer that requested
// changes (verdict "block"), or opened a PR that was later reverted on the
// base branch. Each such run is attributed to the files its branch changed.
//
// The job runs in its own goroutine, never on the orchestrator event loop:
// it reads history through Orchestrator.RunHistory (a copy under historyMu)
// and git through internal/gitexec, read-only and without fetching. It is
// bounded — one refresh at a time, at most hotspotsMaxLookupsPerRefresh run
// lookups each, every git call under its own timeout and the whole refresh
// under another — and a request never waits for it: GET
// /api/v1/repo/hotspots returns the latest result and starts a refresh only
// when that result is older than hotspotsRefreshEvery. Each run's changed
// files are cached by run ID, so a refresh only runs git for runs it has
// not attributed yet. The cache is process-wide (it survives WORKFLOW.md
// reloads) and is dropped when the repository or base branch changes.

const (
	hotspotsRefreshEvery         = 10 * time.Minute
	hotspotsUnresolvedRetry      = time.Hour
	hotspotsMaxLookupsPerRefresh = 100
	hotspotsGitTimeout           = 10 * time.Second
	hotspotsRefreshTimeout       = 2 * time.Minute
	hotspotsMaxRows              = 50
	hotspotsMaxRunsPerRow        = 20
	hotspotsMaxFilesPerRun       = 500
	hotspotsRevertScanDepth      = 500
	hotspotsMaxRevertedLookups   = 100
)

// Attribution methods: how a run's changed files were found.
const (
	hotspotByBranch   = "branch_diff" // git diff <base>...<branch>
	hotspotByPRCommit = "pr_commit"   // the commit on <base> naming the PR, against its first parent
)

// hotspotSource is the repository the diffs are read from and its base branch.
type hotspotSource struct {
	repo, base string
}

// hotspotSourceFor picks the repository the run branches live in: the
// shared bare clone in worktree mode with workspace.clone_url, the
// workspace root in plain worktree mode, otherwise the project directory
// (the one holding WORKFLOW.md), whose remote-tracking refs are only as
// fresh as its last fetch.
func hotspotSourceFor(cfg *config.Config, workflowPath string) hotspotSource {
	src := hotspotSource{base: cmp.Or(cfg.Workspace.BaseBranch, "main")}
	switch {
	case cfg.Workspace.Worktree && cfg.Workspace.Root != "" && cfg.Workspace.CloneURL != "":
		src.repo = workspace.BarePath(cfg.Workspace.Root)
	case cfg.Workspace.Worktree && cfg.Workspace.Root != "":
		src.repo = cfg.Workspace.Root
	default:
		src.repo = filepath.Dir(workflowPath)
		if abs, err := filepath.Abs(src.repo); err == nil {
			src.repo = abs
		}
	}
	return src
}

// hotspotRunFiles is the cached attribution of one run.
type hotspotRunFiles struct {
	files     []string
	method    string
	commit    string // the PR commit, for pr_commit attributions
	resolved  bool
	checkedAt time.Time
}

// hotspotService computes and caches the hotspots.
type hotspotService struct {
	git  hotspotGit
	now  func() time.Time
	mu   sync.Mutex
	src  hotspotSource
	hist func() []orchestrator.CompletedRun
	// cache maps a run key (its run ID) to its changed files.
	cache      map[string]hotspotRunFiles
	report     server.RepoHotspotsResponse
	computedAt time.Time
	refreshing bool
	// done is closed when the in-flight refresh ends (nil when idle).
	done chan struct{}
}

func newHotspotService(git hotspotGit, now func() time.Time) *hotspotService {
	return &hotspotService{git: git, now: now, cache: map[string]hotspotRunFiles{}}
}

// repoHotspots is the daemon's single hotspots service (one per process,
// like failureCarry), so its per-run cache outlives config reloads.
var repoHotspots = newHotspotService(gitexecHotspotGit, time.Now)

// repoHotspotsProvider binds the current generation's history and
// repository to the shared service and returns its request handler.
func repoHotspotsProvider(orch *orchestrator.Orchestrator, cfg *config.Config, workflowPath string) server.RepoHotspotsFunc {
	repoHotspots.bind(orch.RunHistory, hotspotSourceFor(cfg, workflowPath))
	return repoHotspots.get
}

// bind points the service at a generation's history and repository. A
// different repository or base invalidates the cache and the result.
func (h *hotspotService) bind(hist func() []orchestrator.CompletedRun, src hotspotSource) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hist = hist
	if src != h.src {
		h.src = src
		h.cache = map[string]hotspotRunFiles{}
		h.report = server.RepoHotspotsResponse{}
		h.computedAt = time.Time{}
	}
}

// get returns the latest result and starts a background refresh when it is
// older than hotspotsRefreshEvery and none is running. It never blocks on git.
func (h *hotspotService) get(context.Context) server.RepoHotspotsResponse {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.refreshing && h.hist != nil && (h.computedAt.IsZero() || h.now().Sub(h.computedAt) >= hotspotsRefreshEvery) {
		h.startRefreshLocked()
	}
	resp := h.report
	resp.Refreshing = h.refreshing
	return resp
}

// startRefreshLocked launches one refresh; h.mu must be held.
func (h *hotspotService) startRefreshLocked() {
	h.refreshing = true
	done := make(chan struct{})
	h.done = done
	hist, src, cache := h.hist, h.src, maps.Clone(h.cache)
	go func() {
		defer close(done)
		defer orchestrator.RecoverGoroutine("repo-hotspots", "", func() {
			h.mu.Lock()
			h.refreshing = false
			h.mu.Unlock()
		})
		ctx, cancel := context.WithTimeout(context.Background(), hotspotsRefreshTimeout)
		defer cancel()
		report, updated := computeHotspots(ctx, h.git, src, hist(), cache, h.now())
		h.mu.Lock()
		defer h.mu.Unlock()
		h.refreshing = false
		if h.src != src {
			return // rebound to another repository meanwhile
		}
		h.cache = updated
		h.report = report
		h.computedAt = h.now()
		at := h.computedAt
		h.report.GeneratedAt = &at
	}()
}

// hotspotRunKey is a run's cache key: its run ID, or (for history written
// before run IDs were kept) its issue and start time.
func hotspotRunKey(r orchestrator.CompletedRun) string {
	if r.RunID != "" {
		return r.RunID
	}
	return r.Identifier + "@" + r.StartedAt.UTC().Format(time.RFC3339Nano)
}

// hotspotSignals are the reasons a run went wrong, without the revert
// signal (that needs the base branch's history).
func hotspotSignals(r orchestrator.CompletedRun) []string {
	var s []string
	switch r.Status {
	case "failed":
		s = append(s, server.HotspotSignalFailed)
	case "stalled":
		s = append(s, server.HotspotSignalStalled)
	case "input_required":
		s = append(s, server.HotspotSignalInputRequest)
	}
	if r.Kind == "reviewer" && r.ReviewVerdict == orchestrator.ReviewVerdictBlock {
		s = append(s, server.HotspotSignalChangeRequest)
	}
	return s
}
