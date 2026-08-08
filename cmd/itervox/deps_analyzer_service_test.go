package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/depsanalysis"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// stubRunner returns canned analyzer output, failing on the Nth call. It
// also records the prompt handed to each call so tests can prove per-chunk
// scoping actually happened (rather than assuming it from the edge output
// alone, which is scope-independent when the runner ignores its input).
type stubRunner struct {
	calls    atomic.Int64
	failOn   int64 // 1-based; 0 = never fail
	response string

	mu      sync.Mutex
	prompts []string
}

func (s *stubRunner) RunTurn(
	ctx context.Context, _ agent.Logger, onProgress func(agent.TurnResult),
	_ *string, prompt, _, _, _, _ string, _, _ int,
) (agent.TurnResult, error) {
	n := s.calls.Add(1)
	s.mu.Lock()
	s.prompts = append(s.prompts, prompt)
	s.mu.Unlock()
	if onProgress != nil {
		onProgress(agent.TurnResult{})
	}
	if s.failOn > 0 && n == s.failOn {
		return agent.TurnResult{}, errors.New("stub chunk failure")
	}
	return agent.TurnResult{ResultText: s.response}, nil
}

// capturedPrompts returns the prompts seen so far, in call order. Since
// depsAnalyzerService.run processes chunks sequentially (never concurrently),
// index i is chunk i+1's prompt.
func (s *stubRunner) capturedPrompts() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.prompts))
	copy(out, s.prompts)
	return out
}

// blockerRelation seeds a tracker-declared BlockedBy edge from source to
// target in newTestAnalyzerService's issue set.
type blockerRelation struct {
	source string
	target string
}

// newTestAnalyzerService builds a depsAnalyzerService over a MemoryTracker
// seeded with issueCount issues, all in the "Todo" state, and a profile whose
// runner is the supplied stub. Returns the service plus the sidecar path it
// will (or, on failure, will not) write to.
//
// relations optionally seeds tracker-declared BlockedBy edges between the
// generated issues (identifiers "ENG-001".."ENG-0NNN"), so tests can prove
// ScopeTrackerEdges actually narrows the per-chunk edge list instead of
// asserting against an always-empty tracker-edges set.
func newTestAnalyzerService(t *testing.T, runner agent.Runner, issueCount, chunkSize int, relations ...blockerRelation) (*depsAnalyzerService, string) {
	t.Helper()

	blockersByTarget := make(map[string][]string, len(relations))
	for _, r := range relations {
		blockersByTarget[r.target] = append(blockersByTarget[r.target], r.source)
	}

	issues := make([]domain.Issue, issueCount)
	for i := 0; i < issueCount; i++ {
		id := fmt.Sprintf("ENG-%03d", i+1)
		issue := domain.Issue{
			ID:         id,
			Identifier: id,
			Title:      "issue " + id,
			State:      "Todo",
		}
		for _, src := range blockersByTarget[id] {
			srcCopy := src
			issue.BlockedBy = append(issue.BlockedBy, domain.BlockerRef{Identifier: &srcCopy})
		}
		issues[i] = issue
	}
	return newTestAnalyzerServiceWithIssues(t, runner, issues, chunkSize)
}

// newTestAnalyzerServiceWithIssues is newTestAnalyzerService's shared tail —
// factored out so tests that need explicit control over issue content
// (title/description, for fingerprint-based incremental-analysis tests) can
// build their own domain.Issue slice instead of the generated
// "issue ENG-NNN" placeholders.
func newTestAnalyzerServiceWithIssues(t *testing.T, runner agent.Runner, issues []domain.Issue, chunkSize int) (*depsAnalyzerService, string) {
	t.Helper()

	tr := tracker.NewMemoryTracker(issues, []string{"Todo"}, nil)

	enabled := true
	cfg := &config.Config{
		Agent: config.AgentConfig{
			DepsAnalyzerChunkSize: chunkSize,
			Profiles: map[string]config.AgentProfile{
				"deps-analyzer": {
					Command: "stub-analyzer",
					Enabled: &enabled,
				},
			},
		},
		Tracker: config.TrackerConfig{
			ActiveStates: []string{"Todo"},
		},
	}

	orch := orchestrator.New(cfg, tr, runner, nil)

	sidecarPath := filepath.Join(t.TempDir(), "deps-sidecar.json")

	svc := newDepsAnalyzerService(context.Background(), orch, cfg, tr, runner, sidecarPath, "", func() {})
	return svc, sidecarPath
}

// A chunk failure must fail the WHOLE job. Writing the successful chunks'
// edges would leave a sidecar that looks complete to the dashboard while
// silently omitting part of the backlog.
func TestServiceRunFailsWholeJobWhenAChunkFails(t *testing.T) {
	svc, sidecarPath := newTestAnalyzerService(t, &stubRunner{
		failOn:   2,
		response: `{"edges":[]}`,
	}, 20 /* issues */, 8 /* chunk size => 3 chunks */)

	res, err := svc.run(context.Background(), "deps-analyzer")

	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "chunk 2/3")
	_, statErr := os.Stat(sidecarPath)
	assert.True(t, os.IsNotExist(statErr), "no sidecar may be written on a failed job")
}

// The counters the job row reports must reflect what actually ran — this is
// the field that was declared and never written before this sub-project.
func TestServiceRunPopulatesIssuesScannedAndChunks(t *testing.T) {
	svc, _ := newTestAnalyzerService(t, &stubRunner{
		response: `{"edges":[{"source":"ENG-001","target":"ENG-002","evidence":"e"}]}`,
	}, 20, 8)

	res, err := svc.run(context.Background(), "deps-analyzer")

	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 20, res.IssuesScanned)
	assert.Equal(t, 20, res.IssuesAnalyzed, "no prior sidecar => full mode => every fetched issue is analyzed")
	assert.Equal(t, 3, res.ChunksTotal, "20 issues at chunk size 8 is 3 chunks")
}

// #52 — the daemon's success log must state scanned/analyzed/revalidated
// explicitly rather than only "analyzed" (which previously meant "scanned").
func TestServiceRunLogsScannedAnalyzedRevalidatedCounts(t *testing.T) {
	svc, _ := newTestAnalyzerService(t, &stubRunner{response: `{"edges":[]}`}, 5, 8)
	var buf bytes.Buffer
	svc.logger = slog.New(slog.NewTextHandler(&buf, nil))

	_, err := svc.run(context.Background(), "deps-analyzer")
	require.NoError(t, err)

	logged := buf.String()
	assert.Contains(t, logged, "scanned 5 issues (5 analyzed, 0 revalidated)",
		"no prior sidecar => full mode => scanned == analyzed and nothing is revalidated")
}

// Each chunk reports the same pair; the sidecar must carry it once.
func TestServiceRunDedupesEdgesAcrossChunks(t *testing.T) {
	svc, _ := newTestAnalyzerService(t, &stubRunner{
		response: `{"edges":[{"source":"ENG-001","target":"ENG-002","evidence":"e"}]}`,
	}, 20, 8)

	res, err := svc.run(context.Background(), "deps-analyzer")

	require.NoError(t, err)
	require.NotNil(t, res.Sidecar)
	assert.Len(t, res.Sidecar.Edges, 1,
		"the same pair returned by all 3 chunks must collapse to one edge")
}

// I-1 (review round 1): the previous three tests all seeded issues with no
// BlockedBy relations, so trackerEdges was always empty end to end — passing
// ScopeTrackerEdges(chunk, trackerEdges) or the raw unscoped trackerEdges was
// indistinguishable. This test seeds one real relation wholly inside chunk 1
// (issues are sorted by state-then-identifier; all issues here share state
// "Todo", so ENG-001..ENG-008 land in chunk 1 at chunk size 8) and inspects
// the actual prompt text sent to the runner for each chunk — proving the
// edge is scoped IN for the chunk that holds both endpoints and scoped OUT
// for chunks that hold neither.
func TestServiceRunScopesTrackerEdgesPerChunk(t *testing.T) {
	runner := &stubRunner{response: `{"edges":[]}`}
	svc, _ := newTestAnalyzerService(t, runner, 20, 8,
		blockerRelation{source: "ENG-001", target: "ENG-002"})

	res, err := svc.run(context.Background(), "deps-analyzer")
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, 3, res.ChunksTotal)

	prompts := runner.capturedPrompts()
	require.Len(t, prompts, 3, "one RunTurn call per chunk")

	assert.Contains(t, prompts[0], `"source": "ENG-001"`,
		"chunk 1 holds both ENG-001 and ENG-002; the edge must be scoped in")
	assert.Contains(t, prompts[0], `"target": "ENG-002"`)

	assert.NotContains(t, prompts[1], "ENG-001",
		"chunk 2 holds neither ENG-001 nor ENG-002; an unscoped edge list would leak it in here")
	assert.NotContains(t, prompts[2], "ENG-001",
		"chunk 3 holds neither ENG-001 nor ENG-002; an unscoped edge list would leak it in here")
}

// I-1 (review round 1): every prior test called svc.run(...) directly,
// bypassing svc.jobs.Enqueue(...). That leaves m.latest nil, so
// currentJobID() always returns "" and every MarkProgress("", ...) call
// is a silent no-op at job.go:229 — the progress-reporting wiring this task
// added (OnProgress -> MarkProgress, and the per-chunk MarkProgress call)
// was untested by construction. This test drives the real dispatch path.
func TestServiceRunViaEnqueueUpdatesChunkProgress(t *testing.T) {
	svc, _ := newTestAnalyzerService(t, &stubRunner{
		response: `{"edges":[]}`,
	}, 20, 8)

	id, err := svc.jobs.Enqueue(context.Background(), "deps-analyzer")
	require.NoError(t, err)
	require.NotEmpty(t, id)

	require.Eventually(t, func() bool {
		job, ok := svc.jobs.Status(id)
		return ok && job != nil && job.Status != depsanalysis.JobRunning
	}, 5*time.Second, 10*time.Millisecond, "job did not reach a terminal state")

	job, ok := svc.jobs.Latest()
	require.True(t, ok)
	require.NotNil(t, job)
	assert.Equal(t, depsanalysis.JobSucceeded, job.Status)
	assert.Equal(t, id, job.ID)
	assert.Equal(t, 3, job.ChunksDone,
		"MarkProgress(currentJobID(), i+1) must have fired for each of the 3 chunks")
	assert.Equal(t, 3, job.ChunksTotal,
		"SetChunksTotal(currentJobID(), len(chunks)) must have fired via the real Enqueue dispatch path")
	assert.False(t, job.LastActivityAt.IsZero(),
		"MarkProgress must have bumped LastActivityAt")
}

// #52 IssuesScanned honesty — jobRowFromJob must carry IssuesAnalyzed onto
// the wire row alongside IssuesScanned so the dashboard can distinguish
// "fetched" from "actually sent to the agent".
func TestJobRowFromJobIssuesAnalyzed(t *testing.T) {
	row := jobRowFromJob(&depsanalysis.Job{ID: "j-1", IssuesScanned: 50, IssuesAnalyzed: 3})
	assert.Equal(t, 50, row.IssuesScanned)
	assert.Equal(t, 3, row.IssuesAnalyzed)
}

// jobRowFromJob must omit LastActivityAt (nil pointer, matching the
// StartedAt/FinishedAt *time.Time pattern) when the underlying Job never had
// it set, and populate it when the Job carries a non-zero value.
func TestJobRowFromJobLastActivityAt(t *testing.T) {
	zero := jobRowFromJob(&depsanalysis.Job{ID: "j-1"})
	assert.Nil(t, zero.LastActivityAt, "zero-value LastActivityAt must not reach the wire")

	ts := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	populated := jobRowFromJob(&depsanalysis.Job{ID: "j-2", LastActivityAt: ts})
	require.NotNil(t, populated.LastActivityAt)
	assert.True(t, ts.Equal(*populated.LastActivityAt))
}

// FIX 1+2 (final review) — deps_analyzer_chunk_size defaults to 75, so any
// backlog <= 75 issues is a single chunk and ChunksTotal/ChunksDone never
// move past "1 / 1" for the run's whole duration. LastActivityAt exists
// precisely to give the operator a liveness signal in this common case, so
// it must reach the wire (server.DepsAnalyzeJobRow) even on a single-chunk
// run — this test drives svc.Status(id), the exact call
// handleDepsAnalyzeStatus makes, rather than reading the internal Job.
func TestServiceStatusPopulatesLastActivityAtOnSingleChunkRun(t *testing.T) {
	svc, _ := newTestAnalyzerService(t, &stubRunner{
		response: `{"edges":[]}`,
	}, 5 /* issues */, 75 /* chunk size => 1 chunk, matches the default */)

	id, err := svc.jobs.Enqueue(context.Background(), "deps-analyzer")
	require.NoError(t, err)
	require.NotEmpty(t, id)

	require.Eventually(t, func() bool {
		job, ok := svc.jobs.Status(id)
		return ok && job != nil && job.Status != depsanalysis.JobRunning
	}, 5*time.Second, 10*time.Millisecond, "job did not reach a terminal state")

	row, ok := svc.Status(id)
	require.True(t, ok)
	assert.Equal(t, 1, row.ChunksTotal, "5 issues at chunk size 75 must be a single chunk")
	require.NotNil(t, row.LastActivityAt,
		"LastActivityAt must reach the wire row — it is the only liveness signal on a single-chunk run")
	assert.False(t, row.LastActivityAt.IsZero())
}

// blockingRunner blocks the first RunTurn call until release is closed, and
// signals started once that call is in flight. It exists to observe job
// state strictly between "chunking finished" and "chunk 1's agent call
// returned" — the narrowest possible window for proving ChunksTotal is
// published at launch rather than derived after the fact.
type blockingRunner struct {
	started  chan struct{}
	release  chan struct{}
	startOne sync.Once
}

func (r *blockingRunner) RunTurn(
	_ context.Context, _ agent.Logger, _ func(agent.TurnResult),
	_ *string, _, _, _, _, _ string, _, _ int,
) (agent.TurnResult, error) {
	r.startOne.Do(func() { close(r.started) })
	<-r.release
	return agent.TurnResult{ResultText: `{"edges":[]}`}, nil
}

// Round-1 review finding: TestServiceRunViaEnqueueUpdatesChunkProgress only
// ever observed ChunksTotal after the job went terminal, at which point
// execute()'s success-branch backstop (job.go) would paper over a missing
// service-side SetChunksTotal call just as easily as a real one. This test
// blocks the very first chunk's agent call so it can inspect ChunksTotal
// while the job is still JobRunning and zero chunks have completed — the
// only way to prove the count is set at launch (deps_analyzer_service.go's
// SetChunksTotal call, made right after ChunkIssues and before the per-chunk
// loop) rather than smuggled in by the terminal backstop.
func TestServiceRunViaEnqueueSetsChunksTotalBeforeFirstChunkCompletes(t *testing.T) {
	runner := &blockingRunner{started: make(chan struct{}), release: make(chan struct{})}
	svc, _ := newTestAnalyzerService(t, runner, 20, 8) // 20 issues / chunk size 8 => 3 chunks

	id, err := svc.jobs.Enqueue(context.Background(), "deps-analyzer")
	require.NoError(t, err)
	require.NotEmpty(t, id)

	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first chunk's RunTurn never started")
	}

	job, ok := svc.jobs.Status(id)
	require.True(t, ok)
	require.Equal(t, depsanalysis.JobRunning, job.Status,
		"job must still be running — chunk 1's agent call is deliberately blocked")
	assert.Equal(t, 0, job.ChunksDone, "no chunk has completed yet")
	assert.Equal(t, 3, job.ChunksTotal,
		"ChunksTotal must already be correct before the first chunk's agent call returns")

	close(runner.release)
	require.Eventually(t, func() bool {
		j, ok := svc.jobs.Status(id)
		return ok && j.Status != depsanalysis.JobRunning
	}, 5*time.Second, 10*time.Millisecond, "job did not reach a terminal state")
}

// I-2 (review round 1): the blind-spot disclosure log line had no assertion
// anywhere — deleting it broke no test. Capture slog output into a buffer
// and assert the record exists for a multi-chunk run and is absent for a
// single-chunk run.
func TestServiceRunLogsBlindSpotWhenChunked(t *testing.T) {
	svc, _ := newTestAnalyzerService(t, &stubRunner{response: `{"edges":[]}`}, 20, 8)
	var buf bytes.Buffer
	svc.logger = slog.New(slog.NewTextHandler(&buf, nil))

	_, err := svc.run(context.Background(), "deps-analyzer")
	require.NoError(t, err)

	logged := buf.String()
	assert.Contains(t, logged, "relations spanning two chunks are not examined",
		"a chunked run must disclose the cross-chunk blind spot, not just accept it silently")
	assert.Contains(t, logged, "chunks=3")
	assert.Contains(t, logged, "chunk_size=8")
}

// Fix-round item 1 (log drift) — the "deps analyzer started" line's issue
// count must be named and valued as issues_analyzed (the to-analyze set),
// never the raw scanned/fetch count, so it cannot silently drift out of sync
// with the run-end "scanned N (M analyzed, K revalidated)" line again. This
// drives an incremental-mode run (2 of 4 fetched issues unchanged) so the
// scanned (4) and analyzed (2) counts actually differ — a full-mode run
// where they're equal would not catch a regression back to the raw count.
func TestServiceRunLogsIssuesAnalyzedInStartLine(t *testing.T) {
	issues := []domain.Issue{
		{ID: "ENG-001", Identifier: "ENG-001", Title: "A title", Description: strPtr("A desc"), State: "Todo"},
		{ID: "ENG-002", Identifier: "ENG-002", Title: "B title v2", Description: strPtr("B desc v2"), State: "Todo"},
		{ID: "ENG-003", Identifier: "ENG-003", Title: "C title", Description: strPtr("C desc"), State: "Todo"},
		{ID: "ENG-004", Identifier: "ENG-004", Title: "D title", Description: strPtr("D desc"), State: "Todo"},
	}
	runner := &stubRunner{response: `{"edges":[]}`}
	svc, sidecarPath := newTestAnalyzerServiceWithIssues(t, runner, issues, 8)

	oldTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prior := &depsanalysis.Sidecar{
		Version:     depsanalysis.SidecarSchemaVersion,
		GeneratedAt: oldTime,
		Profile:     "deps-analyzer",
		Analyzed: map[string]depsanalysis.AnalyzedIssue{
			"ENG-001": {Fingerprint: depsanalysis.IssueFingerprint("A title", "A desc"), AnalyzedAt: oldTime},
			"ENG-004": {Fingerprint: depsanalysis.IssueFingerprint("D title", "D desc"), AnalyzedAt: oldTime},
			"ENG-002": {Fingerprint: depsanalysis.IssueFingerprint("B title v1", "B desc v1"), AnalyzedAt: oldTime},
		},
	}
	require.NoError(t, depsanalysis.SaveSidecar(sidecarPath, prior))

	var buf bytes.Buffer
	svc.logger = slog.New(slog.NewTextHandler(&buf, nil))

	_, err := svc.run(context.Background(), "deps-analyzer")
	require.NoError(t, err)

	logged := buf.String()
	assert.Contains(t, logged, "deps analyzer started")
	assert.Contains(t, logged, "issues_analyzed=2",
		"the start line must report the to-analyze count (2: ENG-002 + ENG-003), not the scanned/fetch count (4)")
	assert.NotContains(t, logged, "issues=4",
		"the start line must not report the raw fetch count under the old field name")
}

func TestServiceRunDoesNotLogBlindSpotForSingleChunk(t *testing.T) {
	// 5 issues at chunk size 8 => exactly one chunk; there is no cross-chunk
	// blind spot to disclose.
	svc, _ := newTestAnalyzerService(t, &stubRunner{response: `{"edges":[]}`}, 5, 8)
	var buf bytes.Buffer
	svc.logger = slog.New(slog.NewTextHandler(&buf, nil))

	_, err := svc.run(context.Background(), "deps-analyzer")
	require.NoError(t, err)

	assert.NotContains(t, buf.String(), "relations spanning two chunks are not examined")
}

// ─── empty-fetch guard (analyzer-autonomy Task 3) ──────────────────────────

// TestServiceRunEmptyFetchDoesNotWipeSidecar is the RED test for the
// mandatory empty-fetch guard on the daemon path
// (docs/superpowers/specs/2026-08-04-analyzer-autonomy-design.md
// "Empty-fetch guard"): a fetch that returns zero issues while the prior
// sidecar still carries at least one edge must write NOTHING (the file must
// be byte-identical before and after) and must log a warning naming the
// guard.
func TestServiceRunEmptyFetchDoesNotWipeSidecar(t *testing.T) {
	svc, sidecarPath := newTestAnalyzerService(t, &stubRunner{response: `{"edges":[]}`}, 0 /* issues */, 8)

	prior := &depsanalysis.Sidecar{
		Version:     depsanalysis.SidecarSchemaVersion,
		GeneratedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Profile:     "deps-analyzer",
		Edges: []depsanalysis.InferredEdge{
			{Source: "OLD-1", Target: "OLD-2", Evidence: "prior pass", Confidence: 0.7,
				InferredAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		},
	}
	require.NoError(t, depsanalysis.SaveSidecar(sidecarPath, prior))
	before, err := os.ReadFile(sidecarPath)
	require.NoError(t, err)

	var buf bytes.Buffer
	svc.logger = slog.New(slog.NewTextHandler(&buf, nil))

	res, err := svc.run(context.Background(), "deps-analyzer")
	require.NoError(t, err, "the guard is success-with-no-op, not an error")
	require.NotNil(t, res)
	assert.Equal(t, 0, res.IssuesScanned)
	assert.Equal(t, 0, res.ChunksTotal)
	require.NotNil(t, res.Sidecar, "the result must still report the true on-disk sidecar")
	assert.Len(t, res.Sidecar.Edges, 1, "res.Sidecar must be the unchanged prior sidecar, not an empty one")

	after, err := os.ReadFile(sidecarPath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "no bytes may change on disk when the guard fires")

	logged := buf.String()
	assert.Contains(t, logged, "empty-fetch guard", "the warning must name the guard")
	assert.Contains(t, logged, "refusing to overwrite 1 inferred edges with an empty fetch")
}

// TestServiceRunEmptyFetchWithNoPriorSidecarStillWrites is the counterpart to
// TestServiceRunEmptyFetchDoesNotWipeSidecar: the guard only protects edges
// that exist. A genuinely-empty backlog with no prior sidecar (or one with
// zero edges) has nothing to lose, so the pass must still write a sidecar —
// this is what lets DepsLastAnalyzedAt populate on a fresh project with an
// empty tracker.
func TestServiceRunEmptyFetchWithNoPriorSidecarStillWrites(t *testing.T) {
	svc, sidecarPath := newTestAnalyzerService(t, &stubRunner{response: `{"edges":[]}`}, 0 /* issues */, 8)

	_, statErr := os.Stat(sidecarPath)
	require.True(t, os.IsNotExist(statErr), "no prior sidecar must exist yet")

	res, err := svc.run(context.Background(), "deps-analyzer")
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 0, res.IssuesScanned)
	assert.Empty(t, res.Sidecar.Edges)

	sc, err := depsanalysis.LoadSidecar(sidecarPath)
	require.NoError(t, err)
	require.NotNil(t, sc, "a sidecar must be written so DepsLastAnalyzedAt populates")
	assert.Empty(t, sc.Edges)
}

// TestServiceRunIncrementalAnalyzesOnlyChanged drives the real service run
// through a prior sidecar whose fingerprints mark ENG-001 and ENG-004 as
// unchanged and ENG-002 as changed (stale fingerprint), against a fetch that
// also surfaces a brand-new ENG-003. Only the changed/new issues (ENG-002,
// ENG-003) may reach the agent; the unchanged pair's prior edge must survive
// into the merged sidecar with its InferredAt re-stamped to "now".
func TestServiceRunIncrementalAnalyzesOnlyChanged(t *testing.T) {
	issues := []domain.Issue{
		{ID: "ENG-001", Identifier: "ENG-001", Title: "A title", Description: strPtr("A desc"), State: "Todo"},
		{ID: "ENG-002", Identifier: "ENG-002", Title: "B title v2", Description: strPtr("B desc v2"), State: "Todo"},
		{ID: "ENG-003", Identifier: "ENG-003", Title: "C title", Description: strPtr("C desc"), State: "Todo"},
		{ID: "ENG-004", Identifier: "ENG-004", Title: "D title", Description: strPtr("D desc"), State: "Todo"},
	}
	runner := &stubRunner{response: `{"edges":[{"source":"ENG-002","target":"ENG-003","evidence":"agent found it"}]}`}
	svc, sidecarPath := newTestAnalyzerServiceWithIssues(t, runner, issues, 8)

	oldTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	prior := &depsanalysis.Sidecar{
		Version:     depsanalysis.SidecarSchemaVersion,
		GeneratedAt: oldTime,
		Profile:     "deps-analyzer",
		Analyzed: map[string]depsanalysis.AnalyzedIssue{
			"ENG-001": {Fingerprint: depsanalysis.IssueFingerprint("A title", "A desc"), AnalyzedAt: oldTime},
			"ENG-004": {Fingerprint: depsanalysis.IssueFingerprint("D title", "D desc"), AnalyzedAt: oldTime},
			// Stale fingerprint vs the current fetch's "B title v2"/"B desc v2" — ENG-002 must be treated as changed.
			"ENG-002": {Fingerprint: depsanalysis.IssueFingerprint("B title v1", "B desc v1"), AnalyzedAt: oldTime},
		},
		Edges: []depsanalysis.InferredEdge{
			{Source: "ENG-001", Target: "ENG-004", Evidence: "prior evidence", Confidence: 0.5, InferredAt: oldTime},
		},
	}
	require.NoError(t, depsanalysis.SaveSidecar(sidecarPath, prior))

	res, err := svc.run(context.Background(), "deps-analyzer")
	require.NoError(t, err)
	require.NotNil(t, res)

	// #52 IssuesScanned honesty — 4 issues were fetched, but only the 2
	// changed/new ones (ENG-002, ENG-003) were actually sent to the agent;
	// IssuesScanned must not be conflated with IssuesAnalyzed.
	assert.Equal(t, 4, res.IssuesScanned, "IssuesScanned is the raw fetch count")
	assert.Equal(t, 2, res.IssuesAnalyzed, "IssuesAnalyzed is plan.ToAnalyze's length, not the fetch count")

	prompts := runner.capturedPrompts()
	require.Len(t, prompts, 1, "ENG-002 + ENG-003 fit in one chunk at chunk size 8")
	assert.Contains(t, prompts[0], "ENG-002", "changed issue must reach the agent")
	assert.Contains(t, prompts[0], "ENG-003", "new issue must reach the agent")
	assert.NotContains(t, prompts[0], "ENG-001", "unchanged issue must NOT reach the agent")
	assert.NotContains(t, prompts[0], "ENG-004", "unchanged issue must NOT reach the agent")

	require.NotNil(t, res.Sidecar)
	require.Len(t, res.Sidecar.Edges, 2, "revalidated ENG-001->ENG-004 plus new ENG-002->ENG-003")

	var revalidated, fresh *depsanalysis.InferredEdge
	for i := range res.Sidecar.Edges {
		e := &res.Sidecar.Edges[i]
		switch {
		case e.Source == "ENG-001" && e.Target == "ENG-004":
			revalidated = e
		case e.Source == "ENG-002" && e.Target == "ENG-003":
			fresh = e
		}
	}
	require.NotNil(t, revalidated, "the unchanged pair's prior edge must be kept")
	assert.Equal(t, "prior evidence", revalidated.Evidence)
	assert.True(t, revalidated.InferredAt.After(oldTime), "revalidation must re-stamp InferredAt to now")
	require.NotNil(t, fresh, "the agent's new edge must be included")

	require.Len(t, res.Sidecar.Analyzed, 4, "Analyzed must be rebuilt for every issue in the fetch")
	for _, id := range []string{"ENG-001", "ENG-002", "ENG-003", "ENG-004"} {
		entry, ok := res.Sidecar.Analyzed[id]
		assert.True(t, ok, "missing Analyzed entry for %s", id)
		assert.True(t, entry.AnalyzedAt.After(oldTime), "%s must be re-stamped to now", id)
	}
}

// ─── mode plumbing (analyzer-autonomy Task 3) ──────────────────────────────

// TestEnqueueModeFullForcesFullPass proves mode threads all the way from
// EnqueueAnalysis through JobManager.EnqueueWithOptions to PlanIncremental:
// with every issue's fingerprint unchanged vs. the prior sidecar, mode
// "auto" must analyze zero issues (revalidation-only, no agent calls) while
// mode "full" must run every issue through the agent regardless.
func TestEnqueueModeFullForcesFullPass(t *testing.T) {
	issues := []domain.Issue{
		{ID: "ENG-001", Identifier: "ENG-001", Title: "A title", Description: strPtr("A desc"), State: "Todo"},
		{ID: "ENG-002", Identifier: "ENG-002", Title: "B title", Description: strPtr("B desc"), State: "Todo"},
	}
	priorFor := func() *depsanalysis.Sidecar {
		return &depsanalysis.Sidecar{
			Version:     depsanalysis.SidecarSchemaVersion,
			GeneratedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			Profile:     "deps-analyzer",
			Analyzed: map[string]depsanalysis.AnalyzedIssue{
				"ENG-001": {Fingerprint: depsanalysis.IssueFingerprint("A title", "A desc")},
				"ENG-002": {Fingerprint: depsanalysis.IssueFingerprint("B title", "B desc")},
			},
		}
	}

	waitTerminal := func(t *testing.T, svc *depsAnalyzerService, id string) {
		t.Helper()
		require.Eventually(t, func() bool {
			job, ok := svc.jobs.Status(id)
			return ok && job != nil && job.Status != depsanalysis.JobRunning
		}, 5*time.Second, 10*time.Millisecond, "job did not reach a terminal state")
	}

	runnerAuto := &stubRunner{response: `{"edges":[]}`}
	svcAuto, pathAuto := newTestAnalyzerServiceWithIssues(t, runnerAuto, issues, 8)
	require.NoError(t, depsanalysis.SaveSidecar(pathAuto, priorFor()))
	idAuto, _, err := svcAuto.EnqueueAnalysis("deps-analyzer", "auto")
	require.NoError(t, err)
	waitTerminal(t, svcAuto, idAuto)
	assert.Equal(t, int64(0), runnerAuto.calls.Load(),
		"mode auto with every fingerprint unchanged must not call the agent at all")

	runnerFull := &stubRunner{response: `{"edges":[]}`}
	svcFull, pathFull := newTestAnalyzerServiceWithIssues(t, runnerFull, issues, 8)
	require.NoError(t, depsanalysis.SaveSidecar(pathFull, priorFor()))
	idFull, _, err := svcFull.EnqueueAnalysis("deps-analyzer", "full")
	require.NoError(t, err)
	waitTerminal(t, svcFull, idFull)
	assert.Equal(t, int64(1), runnerFull.calls.Load(),
		"mode full must force an agent pass over both issues even though their fingerprints are unchanged")
	prompts := runnerFull.capturedPrompts()
	require.Len(t, prompts, 1)
	assert.Contains(t, prompts[0], "ENG-001")
	assert.Contains(t, prompts[0], "ENG-002")
}

// ─── trigger provenance (analyzer-autonomy Task 3) ─────────────────────────

// TestJobRowCarriesTrigger proves Trigger threads from
// EnqueueAnalysisWithTrigger through the Job to the wire row, and that the
// server.DepsAnalyzer-facing EnqueueAnalysis defaults to "manual".
func TestJobRowCarriesTrigger(t *testing.T) {
	svc, _ := newTestAnalyzerService(t, &stubRunner{response: `{"edges":[]}`}, 3, 8)

	idAuto, _, err := svc.EnqueueAnalysisWithTrigger("deps-analyzer", "auto", "auto")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		job, ok := svc.jobs.Status(idAuto)
		return ok && job != nil && job.Status != depsanalysis.JobRunning
	}, 5*time.Second, 10*time.Millisecond, "job did not reach a terminal state")
	rowAuto, ok := svc.Status(idAuto)
	require.True(t, ok)
	assert.Equal(t, "auto", rowAuto.Trigger)

	idManual, _, err := svc.EnqueueAnalysis("deps-analyzer", "auto")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		job, ok := svc.jobs.Status(idManual)
		return ok && job != nil && job.Status != depsanalysis.JobRunning
	}, 5*time.Second, 10*time.Millisecond, "job did not reach a terminal state")
	rowManual, ok := svc.Status(idManual)
	require.True(t, ok)
	assert.Equal(t, "manual", rowManual.Trigger, "EnqueueAnalysis must default to a manual trigger")
}

func strPtr(s string) *string { return &s }

// CurrentJob backs the snapshot's DepsAnalyzeJob field (#46-1: a page
// refresh mid-run must still show the Cancel control). These three tests
// pin the three states the field must be able to carry: never-ran (nil),
// running, and terminal.

func TestCurrentJob_NilWhenNoJobHasRun(t *testing.T) {
	svc, _ := newTestAnalyzerService(t, &stubRunner{response: `{"edges":[]}`}, 3, 8)

	row, ok := svc.CurrentJob()
	assert.False(t, ok, "no job has ever run, so CurrentJob must report false")
	assert.Equal(t, "", row.JobID)
}

func TestCurrentJob_ReflectsRunningJob(t *testing.T) {
	runner := &blockingRunner{started: make(chan struct{}), release: make(chan struct{})}
	svc, _ := newTestAnalyzerService(t, runner, 20, 8) // 3 chunks

	id, err := svc.jobs.Enqueue(context.Background(), "deps-analyzer")
	require.NoError(t, err)

	select {
	case <-runner.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first chunk's RunTurn never started")
	}

	row, ok := svc.CurrentJob()
	require.True(t, ok, "a job is in flight, so CurrentJob must report true")
	assert.Equal(t, id, row.JobID)
	assert.Equal(t, string(depsanalysis.JobRunning), row.Status,
		"CurrentJob must surface the running status while the job is in flight")

	close(runner.release)
	require.Eventually(t, func() bool {
		j, ok := svc.jobs.Status(id)
		return ok && j.Status != depsanalysis.JobRunning
	}, 5*time.Second, 10*time.Millisecond, "job did not reach a terminal state")
}

// TestNewDepsAnalyzerService_NotifiesOnJobStartAndFinish pins the wiring
// main.go relies on: newDepsAnalyzerService's notify callback (bound to
// srv.Notify in main.go's wireDepsAnalyzerService/bindDepsNotify) must fire
// for BOTH the running transition and the terminal one. Without this, the
// dashboard's snapshot.DepsAnalyzeJob field (#46-1) is correct once fetched
// but stale until the next unrelated SSE tick or poll — a refreshed page
// would show a job that finished seconds ago as still running, or miss a
// job that started and finished between polls. internal/depsanalysis's
// TestJobManager_OnTransitionFiresOnRunningAndTerminal already pins the
// JobManager half of this; this test pins that depsAnalyzerService actually
// wires its own notify callback into JobManager.SetOnTransition (rather
// than, say, only calling notify from run()'s own success path, which would
// miss the "running" transition and the cancelled/failed terminal cases).
func TestNewDepsAnalyzerService_NotifiesOnJobStartAndFinish(t *testing.T) {
	tr := tracker.NewMemoryTracker(nil, []string{"Todo"}, nil)
	enabled := true
	cfg := &config.Config{
		Agent: config.AgentConfig{
			DepsAnalyzerChunkSize: 8,
			Profiles: map[string]config.AgentProfile{
				"deps-analyzer": {Command: "stub-analyzer", Enabled: &enabled},
			},
		},
		Tracker: config.TrackerConfig{ActiveStates: []string{"Todo"}},
	}
	orch := orchestrator.New(cfg, tr, &stubRunner{response: `{"edges":[]}`}, nil)

	var (
		mu          sync.Mutex
		notifyCalls int
	)
	svc := newDepsAnalyzerService(
		context.Background(), orch, cfg, tr, &stubRunner{response: `{"edges":[]}`},
		filepath.Join(t.TempDir(), "deps-sidecar.json"), "",
		func() {
			mu.Lock()
			notifyCalls++
			mu.Unlock()
		},
	)

	id, err := svc.jobs.Enqueue(context.Background(), "deps-analyzer")
	require.NoError(t, err)
	require.NotEmpty(t, id)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return notifyCalls >= 2
	}, 5*time.Second, 10*time.Millisecond,
		"notify must fire at least twice: once on start (running), once on finish (terminal)")

	job, ok := svc.jobs.Status(id)
	require.True(t, ok)
	assert.NotEqual(t, depsanalysis.JobRunning, job.Status, "job must have reached a terminal state")
}

func TestCurrentJob_ReflectsTerminalJobAfterCompletion(t *testing.T) {
	svc, _ := newTestAnalyzerService(t, &stubRunner{response: `{"edges":[]}`}, 3, 8)

	id, err := svc.jobs.Enqueue(context.Background(), "deps-analyzer")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		j, ok := svc.jobs.Status(id)
		return ok && j != nil && j.Status != depsanalysis.JobRunning
	}, 5*time.Second, 10*time.Millisecond, "job did not reach a terminal state")

	row, ok := svc.CurrentJob()
	require.True(t, ok, "a job has run, so CurrentJob must still report true after it finishes")
	assert.Equal(t, id, row.JobID)
	assert.Equal(t, string(depsanalysis.JobSucceeded), row.Status,
		"CurrentJob must keep surfacing the last job's terminal status, not revert to absent")
}
