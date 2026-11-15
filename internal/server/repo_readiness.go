package server

import (
	"context"
	"net/http"
	"time"
)

// Repo readiness (#200): the repository-level `itervox doctor` checks, served
// to the dashboard's Repo Analysis page. The checks live in cmd/itervox next
// to doctor and reach this package as an injected provider (Config.
// RepoReadiness), so the endpoint and the CLI run one implementation.

// Readiness check statuses.
const (
	RepoReadinessPass = "pass"
	RepoReadinessWarn = "warn"
	RepoReadinessFail = "fail"
)

// RepoReadinessCheck is one repository readiness check: a stable ID, its
// status ("pass" | "warn" | "fail"), what was found and, when it is not a
// pass, the fix text doctor prints.
type RepoReadinessCheck struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

// RepoReadinessResponse is GET /api/v1/repo/readiness.
type RepoReadinessResponse struct {
	Workflow    string               `json:"workflow"`
	GeneratedAt time.Time            `json:"generatedAt"`
	Checks      []RepoReadinessCheck `json:"checks"`
}

// handleRepoReadiness is GET /api/v1/repo/readiness. It runs on the request
// goroutine, never on the orchestrator event loop; the provider bounds every
// network or tracker check with its own timeout. 503 when not wired.
func (s *Server) handleRepoReadiness(w http.ResponseWriter, r *http.Request) {
	if s.repoReadiness == nil {
		writeError(w, http.StatusServiceUnavailable, "readiness_unavailable",
			"repo readiness checks are not configured")
		return
	}
	resp := s.repoReadiness(r.Context())
	if resp.Checks == nil {
		resp.Checks = []RepoReadinessCheck{}
	}
	writeJSON(w, http.StatusOK, resp)
}

// RepoReadinessFunc runs the repository readiness checks.
type RepoReadinessFunc func(ctx context.Context) RepoReadinessResponse
