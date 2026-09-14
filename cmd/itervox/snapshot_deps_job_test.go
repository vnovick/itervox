package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/depsanalysis"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
)

// twoAnalyzerIssues gives newTestAnalyzerServiceWithIssues something to
// chunk — a nil/empty issue slice produces 0 chunks, so the blocking runner
// below would never see a RunTurn call and the "observe it mid-run" half of
// the test would hang until the 5s timeout.
func twoAnalyzerIssues() []domain.Issue {
	return []domain.Issue{
		{ID: "ENG-1", Identifier: "ENG-1", Title: "one", State: "Todo"},
		{ID: "ENG-2", Identifier: "ENG-2", Title: "two", State: "Todo"},
	}
}

// TestBuildSnapFunc_DepsAnalyzeJob pins #46-1: the snapshot's DepsAnalyzeJob
// field must reflect the deps-analyzer JobManager's current job so a page
// refresh mid-run still shows the Cancel affordance (derived server-side,
// not from mutation-local frontend state). depsSvc is a plain constructor
// parameter to buildSnapFunc (main.go's actual construction order has
// runner/orch/agentSessionsDir all available before buildSnapFunc runs, and
// newDepsAnalyzerService starts no goroutines of its own — nothing here
// requires two-phase binding). The three states this test drives through
// one fixed service instance: never-ran (nil row), running, terminal.
func TestBuildSnapFunc_DepsAnalyzeJob(t *testing.T) {
	cfg := &config.Config{
		Tracker: config.TrackerConfig{
			ActiveStates:   []string{"Todo"},
			TerminalStates: []string{"Done"},
		},
		Agent: config.AgentConfig{
			DepsAnalyzerChunkSize: 8,
		},
	}
	mt := tracker.NewMemoryTracker(nil, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	orch := orchestrator.New(cfg, mt, &agenttest.FakeRunner{}, nil)

	runner := &blockingRunner{started: make(chan struct{}), release: make(chan struct{})}
	depsSvc, _ := newTestAnalyzerServiceWithIssues(t, runner, twoAnalyzerIssues(), 8)

	ob, err := outbox.New("")
	require.NoError(t, err)
	snap := buildSnapFunc(orch, mt, cfg, "sess-1", logbuffer.New(), t.TempDir()+"/WORKFLOW.md", depsSvc, ob)

	// No job has ever run on this service — JobManager.Latest reports false,
	// so the field must be absent, not a zero-value struct.
	before := snap()
	assert.Nil(t, before.DepsAnalyzeJob, "DepsAnalyzeJob must be nil before any job has run")

	// Drive a job through the real Enqueue path so the snapshot observes it
	// mid-run via a blocking runner, then after it goes terminal.
	id, err := depsSvc.jobs.Enqueue(context.Background(), "deps-analyzer")
	require.NoError(t, err)

	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("job's first RunTurn call never started")
	}

	running := snap()
	require.NotNil(t, running.DepsAnalyzeJob, "a running job must be visible on the snapshot")
	assert.Equal(t, id, running.DepsAnalyzeJob.JobID)
	assert.Equal(t, string(depsanalysis.JobRunning), running.DepsAnalyzeJob.Status,
		"a page refresh mid-run must observe status=running so the dashboard renders the Cancel control (#46-1)")

	close(runner.release)
	require.Eventually(t, func() bool {
		j, ok := depsSvc.jobs.Status(id)
		return ok && j != nil && j.Status != depsanalysis.JobRunning
	}, 5*time.Second, 10*time.Millisecond, "job did not reach a terminal state")

	terminal := snap()
	require.NotNil(t, terminal.DepsAnalyzeJob, "the last job must still be visible once it goes terminal")
	assert.Equal(t, string(depsanalysis.JobSucceeded), terminal.DepsAnalyzeJob.Status)
}
