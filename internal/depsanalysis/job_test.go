package depsanalysis

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJobManager_EnqueueRunsRunner(t *testing.T) {
	var calls atomic.Int32
	done := make(chan struct{})
	mgr := NewJobManager(func(_ context.Context, profile string) (*Sidecar, error) {
		assert.Equal(t, "deps-analyzer", profile)
		calls.Add(1)
		close(done)
		return &Sidecar{Version: SidecarSchemaVersion, Edges: []InferredEdge{{Source: "A", Target: "B"}}}, nil
	})
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
	assert.Equal(t, int32(1), calls.Load())
}

func TestJobManager_ConcurrentEnqueueReturnsSameID(t *testing.T) {
	block := make(chan struct{})
	mgr := NewJobManager(func(ctx context.Context, _ string) (*Sidecar, error) {
		<-block
		return &Sidecar{Version: SidecarSchemaVersion}, nil
	})
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
	mgr := NewJobManager(func(_ context.Context, _ string) (*Sidecar, error) {
		return nil, errors.New("analyzer crashed")
	})
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
	mgr := NewJobManager(func(context.Context, string) (*Sidecar, error) { return nil, nil })
	_, ok := mgr.Status("does-not-exist")
	assert.False(t, ok)
}

func TestJobManager_LatestTracksMostRecentJob(t *testing.T) {
	mgr := NewJobManager(func(context.Context, string) (*Sidecar, error) {
		return &Sidecar{Version: SidecarSchemaVersion}, nil
	})
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

// v0.2.0 todolist7 C4 — SetOnTransition wires the JobManager to the SSE hub.
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
	mgr := NewJobManager(func(_ context.Context, _ string) (*Sidecar, error) {
		return &Sidecar{Version: SidecarSchemaVersion}, nil
	})
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

// v0.2.0 todolist7 C4 — the terminal-failure path must also fire the
// callback so the SSE listener can surface the failure reason to the operator.
func TestJobManager_OnTransitionFiresOnFailure(t *testing.T) {
	var (
		mu     sync.Mutex
		events []JobStatus
	)
	mgr := NewJobManager(func(_ context.Context, _ string) (*Sidecar, error) {
		return nil, errors.New("simulated analyzer failure")
	})
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

// v0.2.0 todolist7 C4 — SetOnTransition(nil) disables the broadcast without
// breaking concurrent job lifecycle.
func TestJobManager_OnTransitionNilCallbackIsSafe(t *testing.T) {
	mgr := NewJobManager(func(_ context.Context, _ string) (*Sidecar, error) {
		return &Sidecar{Version: SidecarSchemaVersion}, nil
	})
	mgr.SetOnTransition(nil)
	id, err := mgr.Enqueue(context.Background(), "deps-analyzer")
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		j, ok := mgr.Status(id)
		return ok && j.Status == JobSucceeded
	}, time.Second, 10*time.Millisecond)
}

func TestJobManager_NoRunnerReturnsError(t *testing.T) {
	mgr := NewJobManager(nil)
	_, err := mgr.Enqueue(context.Background(), "deps-analyzer")
	require.Error(t, err)
}
