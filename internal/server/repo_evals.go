package server

import (
	"context"
	"net/http"
	"time"
)

// Repo evals (#201): the latest persisted eval result per profile and
// scenario, read from <project>/.itervox/evals by a provider cmd/itervox
// injects (Config.RepoEvals). The endpoint only reads reports `make evals`,
// `make evals-live`, `make evals-record` and cmd/itervox-evals wrote; it
// never starts an eval run.

// RepoEvalReport is one persisted report the response was built from.
type RepoEvalReport struct {
	File       string    `json:"file"`
	Mode       string    `json:"mode"` // "recorded" | "live" | "record"
	FinishedAt time.Time `json:"finishedAt"`
	Pass       int       `json:"pass"`
	Total      int       `json:"total"`
}

// RepoEvalCase is the latest result of one profile/scenario.
type RepoEvalCase struct {
	Profile  string `json:"profile"`
	Scenario string `json:"scenario"`
	Pass     bool   `json:"pass"`
	// Failures lists every failed assertion (missing actions, forbidden
	// actions, forbidden_phrases present, missing marker phrases).
	Failures []string `json:"failures,omitempty"`
	// Error is a live run that could not be judged as complete.
	Error string `json:"error,omitempty"`
	Stale bool   `json:"stale,omitempty"`
	// PromptDigest is the prompt version the case ran against (#186
	// digest); DigestSource says where it came from: "live" (computed by the
	// live run) or "recording" (the recording's meta line). Empty when
	// unknown, e.g. a hand-written fixture replayed by `make evals`.
	PromptDigest string `json:"promptDigest,omitempty"`
	DigestSource string `json:"digestSource,omitempty"`
	// RanAt / Mode / File name the report the result is from.
	RanAt time.Time `json:"ranAt"`
	Mode  string    `json:"mode"`
	File  string    `json:"file"`
	// Regression: the case fails on PromptDigest and passed on the
	// previous digest (PreviousDigest, last at PreviousAt).
	Regression     bool       `json:"regression,omitempty"`
	PreviousDigest string     `json:"previousDigest,omitempty"`
	PreviousAt     *time.Time `json:"previousAt,omitempty"`
	// Results counts the reports read that include the case.
	Results int `json:"results"`
}

// RepoEvalsResponse is GET /api/v1/repo/evals.
type RepoEvalsResponse struct {
	// Dir is the directory the reports are read from.
	Dir     string           `json:"dir"`
	Reports []RepoEvalReport `json:"reports"`
	Cases   []RepoEvalCase   `json:"cases"`
	// Skipped counts report files that could not be read.
	Skipped int `json:"skipped,omitempty"`
	// Error is set when the directory could not be read.
	Error string `json:"error,omitempty"`
}

// RepoEvalsFunc reads the persisted eval results.
type RepoEvalsFunc func(ctx context.Context) RepoEvalsResponse

// handleRepoEvals is GET /api/v1/repo/evals. It reads at most the newest 20
// small JSON files on the request goroutine; 503 when not wired.
func (s *Server) handleRepoEvals(w http.ResponseWriter, r *http.Request) {
	if s.repoEvals == nil {
		writeError(w, http.StatusServiceUnavailable, "evals_unavailable", "eval results are not configured")
		return
	}
	resp := s.repoEvals(r.Context())
	if resp.Reports == nil {
		resp.Reports = []RepoEvalReport{}
	}
	if resp.Cases == nil {
		resp.Cases = []RepoEvalCase{}
	}
	writeJSON(w, http.StatusOK, resp)
}
