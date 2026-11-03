package main

import (
	"context"
	"path/filepath"

	"github.com/vnovick/itervox/internal/evals"
	"github.com/vnovick/itervox/internal/server"
)

// Repo evals (#201): GET /api/v1/repo/evals reads the eval reports `make
// evals`, `make evals-live`, `make evals-record` and cmd/itervox-evals saved
// under <project>/.itervox/evals (the project is the directory holding
// WORKFLOW.md). It only reads files, on the request goroutine — never the
// orchestrator event loop — and never starts an eval run.

// repoEvalsDir is where the daemon reads eval reports for workflowPath.
func repoEvalsDir(workflowPath string) string {
	dir := filepath.Dir(workflowPath)
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return filepath.Join(dir, filepath.FromSlash(evals.ResultsDir))
}

// repoEvalsProvider serves the newest reports of workflowPath's project.
func repoEvalsProvider(workflowPath string) server.RepoEvalsFunc {
	dir := repoEvalsDir(workflowPath)
	return func(context.Context) server.RepoEvalsResponse {
		return buildRepoEvals(dir)
	}
}

// buildRepoEvals maps evals.LoadResults onto the API shape.
func buildRepoEvals(dir string) server.RepoEvalsResponse {
	resp := server.RepoEvalsResponse{Dir: dir, Reports: []server.RepoEvalReport{}, Cases: []server.RepoEvalCase{}}
	sum, err := evals.LoadResults(dir)
	if err != nil {
		resp.Error = err.Error()
		return resp
	}
	resp.Skipped = sum.Skipped
	for _, r := range sum.Reports {
		resp.Reports = append(resp.Reports, server.RepoEvalReport{
			File: r.File, Mode: r.Mode, FinishedAt: r.FinishedAt, Pass: r.Pass, Total: r.Total,
		})
	}
	for _, c := range sum.Cases {
		resp.Cases = append(resp.Cases, server.RepoEvalCase{
			Profile: c.Profile, Scenario: c.Scenario, Pass: c.Latest.Pass,
			Failures: c.Latest.Failures, Error: c.Latest.Error, Stale: c.Latest.Stale,
			PromptDigest: c.Latest.PromptDigest, DigestSource: c.Latest.DigestSource,
			RanAt: c.LatestAt, Mode: c.Mode, File: c.File,
			Regression: c.Regression, PreviousDigest: c.PreviousDigest, PreviousAt: c.PreviousAt,
			Results: c.Results,
		})
	}
	return resp
}
