package server

import (
	"context"
	"net/http"
	"time"
)

// Repo hotspots (#201): files and directories ranked by how often agent work
// on them went wrong — failed or stalled runs, reviewer change requests,
// input requests and reverted agent PRs — attributed to the files each run's
// branch changed. A bounded background job in cmd/itervox computes it off
// the orchestrator event loop (reading run history and git through
// internal/gitexec), caches each run's changed files by run ID and refreshes
// at most every 10 minutes; the endpoint only returns its latest result.

// Hotspot signals (RepoHotspotRun.Signals).
const (
	HotspotSignalFailed        = "failed"
	HotspotSignalStalled       = "stalled"
	HotspotSignalChangeRequest = "change_request"
	HotspotSignalInputRequest  = "input_request"
	HotspotSignalReverted      = "reverted"
)

// RepoHotspotRun is one run counted in a hotspot row.
type RepoHotspotRun struct {
	RunID      string    `json:"runId,omitempty"`
	Identifier string    `json:"identifier"`
	Status     string    `json:"status"`
	Kind       string    `json:"kind,omitempty"`
	Profile    string    `json:"profile,omitempty"`
	FinishedAt time.Time `json:"finishedAt"`
	PRURL      string    `json:"prUrl,omitempty"`
	// Signals are the reasons the run counts as having gone wrong.
	Signals []string `json:"signals"`
}

// RepoHotspotRow is one file (or directory, path ending in "/") with the
// number of problem runs that changed it, per signal.
type RepoHotspotRow struct {
	Path           string `json:"path"`
	Failed         int    `json:"failed"`
	Stalled        int    `json:"stalled"`
	ChangeRequests int    `json:"changeRequests"`
	InputRequests  int    `json:"inputRequests"`
	Reverted       int    `json:"reverted"`
	// Total is the sum of the signal counts (a run with two signals counts twice).
	Total int `json:"total"`
	// Issues are the identifiers of the runs involved, sorted.
	Issues []string `json:"issues"`
	// Runs are the runs involved, newest first, at most 20.
	Runs []RepoHotspotRun `json:"runs"`
}

// RepoHotspotsResponse is GET /api/v1/repo/hotspots.
type RepoHotspotsResponse struct {
	// GeneratedAt is when the result was computed; nil before the first
	// refresh finished.
	GeneratedAt *time.Time `json:"generatedAt,omitempty"`
	// Refreshing is true while the background job runs.
	Refreshing bool `json:"refreshing"`
	// Repo and BaseRef are the repository and base ref the diffs were read
	// from.
	Repo    string `json:"repo,omitempty"`
	BaseRef string `json:"baseRef,omitempty"`
	// RunsConsidered counts the runs in the history window; RunsWithSignals
	// those that went wrong; RunsAttributed those whose changed files were
	// found; RunsUnattributed those whose files were not (branch gone and no
	// commit on the base names their PR); RunsPending those left for the
	// next refresh by its lookup budget.
	RunsConsidered   int `json:"runsConsidered"`
	RunsWithSignals  int `json:"runsWithSignals"`
	RunsAttributed   int `json:"runsAttributed"`
	RunsUnattributed int `json:"runsUnattributed"`
	RunsPending      int `json:"runsPending,omitempty"`
	// Files and Directories are the top rows, most problems first.
	Files       []RepoHotspotRow `json:"files"`
	Directories []RepoHotspotRow `json:"directories"`
	// Notes explain what could not be computed.
	Notes []string `json:"notes,omitempty"`
}

// RepoHotspotsFunc returns the latest hotspots result, starting a
// background refresh when it is older than the refresh interval. It never
// blocks on git.
type RepoHotspotsFunc func(ctx context.Context) RepoHotspotsResponse

// handleRepoHotspots is GET /api/v1/repo/hotspots; 503 when not wired.
func (s *Server) handleRepoHotspots(w http.ResponseWriter, r *http.Request) {
	if s.repoHotspots == nil {
		writeError(w, http.StatusServiceUnavailable, "hotspots_unavailable", "failure hotspots are not configured")
		return
	}
	resp := s.repoHotspots(r.Context())
	if resp.Files == nil {
		resp.Files = []RepoHotspotRow{}
	}
	if resp.Directories == nil {
		resp.Directories = []RepoHotspotRow{}
	}
	writeJSON(w, http.StatusOK, resp)
}
