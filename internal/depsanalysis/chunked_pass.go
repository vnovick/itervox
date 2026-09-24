package depsanalysis

import (
	"context"
	"fmt"
	"log/slog"
)

// RunChunkedAgentPass runs one full analyzer pass over issues: it splits
// them into chunkSize batches via ChunkIssues, scopes trackerEdges to each
// batch via ScopeTrackerEdges, and invokes RunAgentPass once per batch in
// sequence, returning the concatenated (not yet deduplicated — callers merge
// via MergeIncremental, which already applies DedupeInferredEdges) inferred
// edges across every chunk.
//
// This is the extraction named in issue #47: the chunk / scope /
// fail-atomically / dedupe loop used to be hand-copied between
// cmd/itervox/deps_analyzer_service.go (the daemon's async
// POST /api/v1/deps/analyze path) and cmd/itervox/init_deps_analysis.go (the
// one-shot `itervox init` / `itervox deps analyze` path). That duplication is
// what let the CLI path miss chunking when it was first added — a chunk
// failure fails the WHOLE pass here for the same reason it did in both prior
// copies: a partial edge set written to a sidecar would look complete to the
// dashboard while silently omitting part of the backlog.
//
// issues is the set to actually run through the agent (both callers pass
// plan.ToAnalyze, not the raw tracker-fetch count — see PlanIncremental).
//
// base supplies everything RunAgentPass needs EXCEPT Issues/TrackerEdges,
// which this function fills in per chunk from issues/trackerEdges.
// base.Logger doubles as both the agent runner's stream logger (threaded
// straight through to RunAgentPass) and this function's own observability
// logger (run-start, the cross-chunk blind-spot disclosure, and the
// per-chunk failure WARN) — the daemon caller passes its component-tagged
// *slog.Logger here, the CLI caller passes slog.Default(); when base.Logger
// is nil, this function falls back to slog.Default() itself so a caller that
// omits it still gets a working logger rather than a nil-interface panic.
//
// onChunkDone, when non-nil, is called once BEFORE the first chunk starts —
// onChunkDone(0, total) — so an observer of a long-running job sees the
// correct chunk total immediately rather than the zero value that would
// otherwise persist until the whole pass goes terminal (this is the
// behavior TestServiceRunViaEnqueueSetsChunksTotalBeforeFirstChunkCompletes
// pins), and again after each chunk completes — onChunkDone(i+1, total).
// The daemon path wires this to JobManager.SetChunksTotal (on the done==0
// call) and JobManager.MarkProgress (on every later call); the CLI path has
// no job to report progress against and passes nil.
func RunChunkedAgentPass(
	ctx context.Context,
	base AgentPassInput,
	issues []AnalyzerIssue,
	trackerEdges []TrackerEdge,
	chunkSize int,
	onChunkDone func(done, total int),
) ([]InferredEdge, error) {
	logger := base.Logger
	if logger == nil {
		logger = slog.Default()
	}

	chunks := ChunkIssues(issues, chunkSize)

	// Field is "issues_analyzed", not "issues" — issues here is the
	// to-analyze set (plan.ToAnalyze), NOT the raw tracker-fetch count. The
	// old daemon-only version of this line used "issues" to mean the raw
	// fetch count (a different number under incremental mode), which put it
	// at odds with the run-end "scanned N (M analyzed, K revalidated)" log —
	// both callers now pass the to-analyze set here, so name the field for
	// what it actually is.
	logger.Info("deps analyzer started",
		"profile", base.ProfileName, "issues_analyzed", len(issues),
		"tracker_edges", len(trackerEdges), "chunks", len(chunks))
	if len(chunks) > 1 {
		// The accepted blind spot, made visible rather than silent: an edge
		// between issues placed in different chunks is invisible to the
		// analyzer (see ChunkIssues's doc comment).
		logger.Info("deps analyzer chunked: relations spanning two chunks are not examined",
			"chunks", len(chunks), "chunk_size", chunkSize)
	}

	if onChunkDone != nil {
		onChunkDone(0, len(chunks))
	}

	var all []InferredEdge
	for i, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		input := base
		input.Issues = chunk
		input.TrackerEdges = ScopeTrackerEdges(chunk, trackerEdges)
		edges, err := RunAgentPass(ctx, input)
		if err != nil {
			// Fail the whole pass: see the doc comment above.
			logger.Warn("deps analyzer chunk failed", "profile", base.ProfileName,
				"chunk", i+1, "chunks_total", len(chunks), "error", err.Error())
			return nil, fmt.Errorf("chunk %d/%d: %w", i+1, len(chunks), err)
		}
		all = append(all, edges...)
		if onChunkDone != nil {
			onChunkDone(i+1, len(chunks))
		}
	}
	return all, nil
}
