package depsanalysis

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
)

func TestJobManager_EnqueueRunsRunner(t *testing.T) {
	var calls atomic.Int32
	done := make(chan struct{})
	mgr := NewJobManager(func(_ context.Context, profile string) (*JobResult, error) {
		assert.Equal(t, "deps-analyzer", profile)
		calls.Add(1)
		close(done)
		return &JobResult{
			Sidecar:        &Sidecar{Version: SidecarSchemaVersion, Edges: []InferredEdge{{Source: "A", Target: "B"}}},
			IssuesScanned:  7,
			IssuesAnalyzed: 5,
			ChunksTotal:    3,
		}, nil
	}, time.Minute)
	id, err := mgr.Enqueue(context.Background(), "deps-analyzer")
	require.NoError(t, err)
	require.NotEmpty(t, id)
	<-done
	// Wait briefly for execute() to record the result.
	require.Eventually(t, func() bool {
		j, ok := mgr.Status(id)
		return ok && j.Status == JobSucceeded
	}, time.Second, 10*time.Millisecond)
	job, ok := mgr.Status(id)
	require.True(t, ok)
	assert.Equal(t, JobSucceeded, job.Status)
	assert.Equal(t, 1, job.EdgesFound)
	assert.Equal(t, 7, job.IssuesScanned, "IssuesScanned must be copied from JobResult onto the job")
	assert.Equal(t, 5, job.IssuesAnalyzed, "IssuesAnalyzed must be copied from JobResult onto the job")
	assert.Equal(t, 3, job.ChunksTotal, "ChunksTotal must be copied from JobResult onto the job")
	assert.Equal(t, int32(1), calls.Load())
}

func TestJobManager_ConcurrentEnqueueReturnsSameID(t *testing.T) {
	block := make(chan struct{})
	mgr := NewJobManager(func(ctx context.Context, _ string) (*JobResult, error) {
		<-block
		return &JobResult{Sidecar: &Sidecar{Version: SidecarSchemaVersion}}, nil
	}, time.Minute)
	first, err := mgr.Enqueue(context.Background(), "deps-analyzer")
	require.NoError(t, err)
	second, err := mgr.Enqueue(context.Background(), "deps-analyzer")
	require.NoError(t, err)
	assert.Equal(t, first, second, "second Enqueue while running must return the same job ID")
	close(block)
	require.Eventually(t, func() bool {
		j, ok := mgr.Status(first)
		return ok && j.Status == JobSucceeded
	}, time.Second, 10*time.Millisecond)
}

func TestJobManager_FailedRunRecordsError(t *testing.T) {
	mgr := NewJobManager(func(_ context.Context, _ string) (*JobResult, error) {
		return nil, errors.New("analyzer crashed")
	}, time.Minute)
	id, err := mgr.Enqueue(context.Background(), "deps-analyzer")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		j, ok := mgr.Status(id)
		return ok && j.Status == JobFailed
	}, time.Second, 10*time.Millisecond)
	job, _ := mgr.Status(id)
	assert.Contains(t, job.Error, "analyzer crashed")
}

func TestJobManager_StatusUnknownIDReturnsFalse(t *testing.T) {
	mgr := NewJobManager(func(context.Context, string) (*JobResult, error) { return nil, nil }, time.Minute)
	_, ok := mgr.Status("does-not-exist")
	assert.False(t, ok)
}

func TestJobManager_LatestTracksMostRecentJob(t *testing.T) {
	mgr := NewJobManager(func(context.Context, string) (*JobResult, error) {
		return &JobResult{Sidecar: &Sidecar{Version: SidecarSchemaVersion}}, nil
	}, time.Minute)
	id1, err := mgr.Enqueue(context.Background(), "deps-analyzer")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		j, ok := mgr.Status(id1)
		return ok && j.Status == JobSucceeded
	}, time.Second, 10*time.Millisecond)
	id2, err := mgr.Enqueue(context.Background(), "deps-analyzer")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		j, ok := mgr.Latest()
		return ok && j.ID == id2
	}, time.Second, 10*time.Millisecond)
}

// SetOnTransition wires the JobManager to the SSE hub.
// The callback must fire on EVERY transition: running on enqueue, and the
// terminal transition (succeeded or failed) when execute() completes.
func TestJobManager_OnTransitionFiresOnRunningAndTerminal(t *testing.T) {
	type recordedTransition struct {
		id     string
		status JobStatus
	}
	var (
		mu     sync.Mutex
		events []recordedTransition
	)
	mgr := NewJobManager(func(_ context.Context, _ string) (*JobResult, error) {
		return &JobResult{Sidecar: &Sidecar{Version: SidecarSchemaVersion}}, nil
	}, time.Minute)
	mgr.SetOnTransition(func(job *Job) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, recordedTransition{id: job.ID, status: job.Status})
	})
	id, err := mgr.Enqueue(context.Background(), "deps-analyzer")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) >= 2
	}, time.Second, 10*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, events, 2, "expected exactly two transitions (running + succeeded)")
	assert.Equal(t, id, events[0].id)
	assert.Equal(t, JobRunning, events[0].status, "first transition must be running")
	assert.Equal(t, id, events[1].id)
	assert.Equal(t, JobSucceeded, events[1].status, "second transition must be succeeded")
}

// The terminal-failure path must also fire the
// callback so the SSE listener can surface the failure reason to the operator.
func TestJobManager_OnTransitionFiresOnFailure(t *testing.T) {
	var (
		mu     sync.Mutex
		events []JobStatus
	)
	mgr := NewJobManager(func(_ context.Context, _ string) (*JobResult, error) {
		return nil, errors.New("simulated analyzer failure")
	}, time.Minute)
	mgr.SetOnTransition(func(job *Job) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, job.Status)
	})
	_, err := mgr.Enqueue(context.Background(), "deps-analyzer")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) >= 2
	}, time.Second, 10*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, JobRunning, events[0])
	assert.Equal(t, JobFailed, events[1], "failure path must emit JobFailed as the terminal transition")
}

// SetOnTransition(nil) disables the broadcast without
// breaking concurrent job lifecycle.
func TestJobManager_OnTransitionNilCallbackIsSafe(t *testing.T) {
	mgr := NewJobManager(func(_ context.Context, _ string) (*JobResult, error) {
		return &JobResult{Sidecar: &Sidecar{Version: SidecarSchemaVersion}}, nil
	}, time.Minute)
	mgr.SetOnTransition(nil)
	id, err := mgr.Enqueue(context.Background(), "deps-analyzer")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		j, ok := mgr.Status(id)
		return ok && j.Status == JobSucceeded
	}, time.Second, 10*time.Millisecond)
}

func TestJobManager_NoRunnerReturnsError(t *testing.T) {
	mgr := NewJobManager(nil, time.Minute)
	_, err := mgr.Enqueue(context.Background(), "deps-analyzer")
	require.Error(t, err)
}

// Cancel must terminate a running job, not merely relabel it. The runner
// blocks on ctx.Done(), so a job that reaches JobCancelled proves the context
// was actually fired.
func TestJobManagerCancelStopsRunningJob(t *testing.T) {
	started := make(chan struct{})
	m := NewJobManager(func(ctx context.Context, _ string) (*JobResult, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}, time.Minute)

	id, err := m.Enqueue(context.Background(), "p")
	require.NoError(t, err)
	<-started

	require.True(t, m.Cancel(id))

	require.Eventually(t, func() bool {
		job, ok := m.Status(id)
		return ok && job.Status == JobCancelled
	}, 2*time.Second, 10*time.Millisecond)
}

// The wedge this whole sub-project exists to fix: a stuck job must not block
// every future run.
func TestJobManagerCancelUnblocksNextEnqueue(t *testing.T) {
	release := make(chan struct{})
	m := NewJobManager(func(ctx context.Context, _ string) (*JobResult, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return &JobResult{}, nil
		}
	}, time.Minute)

	first, err := m.Enqueue(context.Background(), "p")
	require.NoError(t, err)
	require.True(t, m.Cancel(first))
	require.Eventually(t, func() bool {
		job, ok := m.Status(first)
		return ok && job.Status == JobCancelled
	}, 2*time.Second, 10*time.Millisecond)

	second, err := m.Enqueue(context.Background(), "p")
	require.NoError(t, err)
	assert.NotEqual(t, first, second, "a cancelled job must not keep the slot")
	close(release)
}

// Round-2 review finding: Cancel returning false for a job that is still
// tracked but has already gone terminal was untested. The two existing
// "Cancel returns false" tests don't cover this case:
//   - TestJobManagerCancelUnknownIDReturnsFalse cancels an ID that never
//     matched anything, while a DIFFERENT job is running — that pins the
//     ID-mismatch branch, not "this job existed and is now finished".
//   - TestDepsAnalyzeCancelUnknownJobReturns404 (server package) drives a
//     canned bool through a fake and can't discriminate the two cases at all.
//
// This test enqueues a job, lets it run to JobSucceeded, and asserts BOTH
// that Status(id) still finds it (so it's genuinely finished and observable,
// not merely evicted from m.latest) AND that Cancel(id) returns false. The
// job.go execute() code path clears m.current as part of the same terminal
// transition that sets the status, so a finished job hits Cancel's
// `m.current == nil` branch — but that was reasoning from reading the code,
// not evidence, until this test exercised it.
func TestJobManagerCancelFinishedJobReturnsFalse(t *testing.T) {
	m := NewJobManager(func(_ context.Context, _ string) (*JobResult, error) {
		return &JobResult{}, nil
	}, time.Minute)

	id, err := m.Enqueue(context.Background(), "p")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		job, ok := m.Status(id)
		return ok && job.Status == JobSucceeded
	}, 2*time.Second, 10*time.Millisecond)

	job, ok := m.Status(id)
	require.True(t, ok, "the finished job must still be observable via Status, not just gone")
	require.Equal(t, JobSucceeded, job.Status)

	assert.False(t, m.Cancel(id),
		"Cancel on an already-finished job must return false")
}

// A bare "context deadline exceeded" tells an operator nothing actionable.
func TestJobManagerTimeoutFailsWithReadableMessage(t *testing.T) {
	m := NewJobManager(func(ctx context.Context, _ string) (*JobResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}, 30*time.Millisecond)

	id, err := m.Enqueue(context.Background(), "p")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		job, ok := m.Status(id)
		return ok && job.Status == JobFailed
	}, 2*time.Second, 10*time.Millisecond)

	job, _ := m.Status(id)
	assert.Contains(t, job.Error, "timed out",
		"the message must name the timeout, not leak a bare context error")
	assert.Contains(t, job.Error, "30ms",
		"the message must name the configured limit, not just say \"timed out\"")

	second, err := m.Enqueue(context.Background(), "p")
	require.NoError(t, err)
	assert.NotEqual(t, id, second, "a timed-out job must not keep the slot")
}

func TestJobManagerPanicMarksFailedAndSurvives(t *testing.T) {
	m := NewJobManager(func(_ context.Context, _ string) (*JobResult, error) {
		panic("deliberate test panic")
	}, time.Minute)

	id, err := m.Enqueue(context.Background(), "p")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		job, ok := m.Status(id)
		return ok && job.Status == JobFailed
	}, 2*time.Second, 10*time.Millisecond)

	job, _ := m.Status(id)
	assert.Contains(t, job.Error, "panic")

	second, err := m.Enqueue(context.Background(), "p")
	require.NoError(t, err)
	assert.NotEqual(t, id, second, "a panicked job must not keep the slot")
}

// ChunksTotal must be visible WHILE the job is still running, not only after
// it goes terminal. Before this fix, ChunksTotal was written exactly once,
// in execute()'s success branch — i.e. only after the job was already
// terminal — so a job observed mid-run always carried ChunksTotal == 0
// alongside a non-zero ChunksDone (an inverted "N / 0" progress ratio). This
// test would have passed under the old behaviour only by accident (if it
// happened to sample after completion); asserting from inside the runner,
// before the runner returns, is what makes it actually exercise the
// mid-run path.
func TestJobManagerChunksTotalVisibleMidRun(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	m := NewJobManager(func(ctx context.Context, _ string) (*JobResult, error) {
		close(started)
		<-release
		return &JobResult{ChunksTotal: 4}, nil
	}, time.Minute)

	id, err := m.Enqueue(context.Background(), "p")
	require.NoError(t, err)
	<-started

	// Simulate the service: set the total right after chunking, before the
	// per-chunk loop starts, then mark progress on some chunks — all while
	// the runner is still blocked on release (i.e. the job is still Running).
	m.SetChunksTotal(id, 4)
	m.MarkProgress(id, 2)

	job, ok := m.Status(id)
	require.True(t, ok)
	require.Equal(t, JobRunning, job.Status, "job must still be running when ChunksTotal is observed")
	assert.Equal(t, 4, job.ChunksTotal, "ChunksTotal must be non-zero while the job is still running")
	assert.Equal(t, 2, job.ChunksDone)

	close(release)
	require.Eventually(t, func() bool {
		j, ok := m.Status(id)
		return ok && j.Status == JobSucceeded
	}, 2*time.Second, 10*time.Millisecond)

	final, ok := m.Status(id)
	require.True(t, ok)
	assert.Equal(t, 4, final.ChunksTotal, "ChunksTotal must still be correct once the job goes terminal")
}

// SetChunksTotal must be a no-op when the ID does not match the currently
// running job, mirroring MarkProgress's ignore-stale-ID behaviour.
func TestJobManagerSetChunksTotalIgnoresNonCurrentID(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	m := NewJobManager(func(ctx context.Context, _ string) (*JobResult, error) {
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return &JobResult{}, nil
		}
	}, time.Minute)

	id, err := m.Enqueue(context.Background(), "p")
	require.NoError(t, err)
	<-started

	m.SetChunksTotal("some-other-id", 99)

	job, ok := m.Status(id)
	require.True(t, ok)
	assert.Equal(t, 0, job.ChunksTotal,
		"SetChunksTotal with a non-matching ID must not mutate the current job's ChunksTotal")

	close(release)
}

// Cancel("no-such-job") must return false because the ID does not match the
// running job, not merely because no job is running — a job must actually be
// in flight for this test to reach the ID-mismatch branch of Cancel rather
// than short-circuiting on m.current == nil.
func TestJobManagerCancelUnknownIDReturnsFalse(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	m := NewJobManager(func(ctx context.Context, _ string) (*JobResult, error) {
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return &JobResult{}, nil
		}
	}, time.Minute)

	_, err := m.Enqueue(context.Background(), "p")
	require.NoError(t, err)
	<-started

	assert.False(t, m.Cancel("no-such-job"))
	close(release)
}

// FIX 4 (final review) — a panic in a live SSE onTransition callback used to
// unwind past notifyLocked's re-lock into execute()'s deferred
// m.mu.Unlock(), producing an "unlock of unlocked mutex" runtime throw that
// no recover() could catch, killing the daemon. This test wires a
// deliberately panicking onTransition and asserts (a) the panic does not
// propagate out of Enqueue/execute (the test process itself would crash
// otherwise — that IS the assertion) and (b) the job still reaches its
// terminal state, proving the manager's own bookkeeping survives a
// misbehaving callback.
func TestJobManagerSurvivesPanicInOnTransitionCallback(t *testing.T) {
	m := NewJobManager(func(_ context.Context, _ string) (*JobResult, error) {
		return &JobResult{Sidecar: &Sidecar{Version: SidecarSchemaVersion}}, nil
	}, time.Minute)

	var calls atomic.Int32
	m.SetOnTransition(func(job *Job) {
		calls.Add(1)
		panic("deliberate onTransition panic")
	})

	id, err := m.Enqueue(context.Background(), "p")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		job, ok := m.Status(id)
		return ok && job.Status == JobSucceeded
	}, 2*time.Second, 10*time.Millisecond, "job must still reach a terminal state despite the panicking callback")

	assert.GreaterOrEqual(t, calls.Load(), int32(2),
		"onTransition must have fired for both the running and terminal transitions")

	// The manager's lock must still be usable — a second Enqueue proves the
	// mutex was not left in a broken state by the recovered panic.
	second, err := m.Enqueue(context.Background(), "p")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		job, ok := m.Status(second)
		return ok && job.Status == JobSucceeded
	}, 2*time.Second, 10*time.Millisecond)
}

// FIX 5 (final review) — Cancel must never return true without actually
// cancelling anything; a 204 from the DELETE endpoint is the operator's only
// signal that the stop took effect. m.currentCancel == nil alongside a
// running m.current is unreachable via the public API (Enqueue sets both
// under the same lock), so this test reaches inside the struct to force the
// state directly and pin the two-line guard.
func TestJobManagerCancelReturnsFalseWhenCancelFuncNil(t *testing.T) {
	m := NewJobManager(func(_ context.Context, _ string) (*JobResult, error) {
		select {}
	}, time.Minute)

	m.mu.Lock()
	m.current = &Job{ID: "orphan", Status: JobRunning}
	m.currentCancel = nil
	m.mu.Unlock()

	assert.False(t, m.Cancel("orphan"),
		"Cancel must return false, not a false-positive true, when there is no cancel func to call")
}

// A config.Config literal (common in tests) yields a zero timeout, which would
// otherwise produce an instantly-expired context.
func TestNewJobManagerClampsNonPositiveTimeout(t *testing.T) {
	m := NewJobManager(func(_ context.Context, _ string) (*JobResult, error) {
		return &JobResult{}, nil
	}, 0)
	assert.Equal(t, time.Duration(config.DefaultDepsAnalyzerTimeoutMs)*time.Millisecond, m.timeout)
}

// The real runner (agent_pass.go:70) wraps failures with
// fmt.Errorf("%w: %v", ErrAnalyzerFailed, err) — the "%v" flattens any
// wrapped context.Canceled out of the errors.Is chain, so errors.Is(err,
// context.Canceled) is false against that shape. Classification must still
// land on JobCancelled via the ctx.Err() fallback, which is the branch this
// test exists to cover.
func TestJobManagerCancelClassifiesFlattenedContextError(t *testing.T) {
	started := make(chan struct{})
	m := NewJobManager(func(ctx context.Context, _ string) (*JobResult, error) {
		close(started)
		<-ctx.Done()
		// Flattened: ctx.Err() is stringified into a new error, not wrapped.
		return nil, fmt.Errorf("agent: %v", ctx.Err())
	}, time.Minute)

	id, err := m.Enqueue(context.Background(), "p")
	require.NoError(t, err)
	<-started

	require.True(t, m.Cancel(id))

	require.Eventually(t, func() bool {
		job, ok := m.Status(id)
		return ok && job.Status == JobCancelled
	}, 2*time.Second, 10*time.Millisecond)
}

// Same flattening concern as above, for the timeout path: the real runner's
// error shape loses errors.Is(err, context.DeadlineExceeded), so the
// ctx.Err() fallback is the only branch that fires once Task 4 wires the
// real runner. This test exercises that branch directly.
func TestJobManagerTimeoutClassifiesFlattenedContextError(t *testing.T) {
	m := NewJobManager(func(ctx context.Context, _ string) (*JobResult, error) {
		<-ctx.Done()
		return nil, fmt.Errorf("agent: %v", ctx.Err())
	}, 30*time.Millisecond)

	id, err := m.Enqueue(context.Background(), "p")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		job, ok := m.Status(id)
		return ok && job.Status == JobFailed
	}, 2*time.Second, 10*time.Millisecond)

	job, _ := m.Status(id)
	assert.Contains(t, job.Error, "timed out")
	assert.Contains(t, job.Error, "30ms")
}

// MarkProgress must be a no-op when the ID does not match the currently
// running job — it must never mutate m.current based on a stale or unrelated
// ID.
func TestJobManagerMarkProgressIgnoresNonCurrentID(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	m := NewJobManager(func(ctx context.Context, _ string) (*JobResult, error) {
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return &JobResult{}, nil
		}
	}, time.Minute)

	id, err := m.Enqueue(context.Background(), "p")
	require.NoError(t, err)
	<-started

	before, ok := m.Status(id)
	require.True(t, ok)

	m.MarkProgress("some-other-id", 42)

	after, ok := m.Status(id)
	require.True(t, ok)
	assert.Equal(t, before.ChunksDone, after.ChunksDone,
		"MarkProgress with a non-matching ID must not mutate the current job's ChunksDone")
	assert.True(t, after.LastActivityAt.Equal(before.LastActivityAt),
		"MarkProgress with a non-matching ID must not touch LastActivityAt")

	close(release)
}

// chunksDone < 0 must leave ChunksDone untouched while still refreshing
// LastActivityAt — the doc comment on MarkProgress promises exactly this
// split, and nothing enforced it.
func TestJobManagerMarkProgressNegativeChunksDoneLeavesCounterUntouched(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	m := NewJobManager(func(ctx context.Context, _ string) (*JobResult, error) {
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return &JobResult{}, nil
		}
	}, time.Minute)

	id, err := m.Enqueue(context.Background(), "p")
	require.NoError(t, err)
	<-started

	m.MarkProgress(id, 5)
	afterPositive, ok := m.Status(id)
	require.True(t, ok)
	require.Equal(t, 5, afterPositive.ChunksDone)

	time.Sleep(2 * time.Millisecond) // ensure LastActivityAt strictly advances
	m.MarkProgress(id, -1)
	afterNegative, ok := m.Status(id)
	require.True(t, ok)
	assert.Equal(t, 5, afterNegative.ChunksDone,
		"chunksDone < 0 must leave ChunksDone untouched")
	assert.True(t, afterNegative.LastActivityAt.After(afterPositive.LastActivityAt),
		"chunksDone < 0 must still refresh LastActivityAt")

	close(release)
}
