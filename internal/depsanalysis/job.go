package depsanalysis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// JobStatus identifies a depsanalysis job's progress.
type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"
)

// Job is one analyzer run's lifecycle record. In-memory only; lost on
// daemon restart (matches existing automation-queue persistence rules).
type Job struct {
	ID            string
	Profile       string
	Status        JobStatus
	QueuedAt      time.Time
	StartedAt     time.Time
	FinishedAt    time.Time
	IssuesScanned int
	EdgesFound    int
	Error         string
}

// JobRunner is the closure that performs one end-to-end analysis pass and
// persists the resulting sidecar. Returning a non-nil Sidecar populates the
// job's IssuesScanned / EdgesFound counters; returning a non-nil error
// transitions the job to JobFailed.
type JobRunner func(ctx context.Context, profile string) (*Sidecar, error)

// JobManager runs at most one analyzer job at a time. Calling Enqueue while a
// job is already running returns the existing job ID without starting a
// second pass — matches the plan's "concurrency cap of one per process".
type JobManager struct {
	mu           sync.Mutex
	current      *Job
	latest       *Job
	run          JobRunner
	onTransition func(*Job)
}

// NewJobManager returns a manager bound to the given runner closure.
func NewJobManager(run JobRunner) *JobManager {
	return &JobManager{run: run}
}

// SetOnTransition registers a callback invoked on every job state transition
// (queued → running, running → succeeded / failed). The callback receives a
// cloned Job snapshot so the receiver can persist or broadcast it without
// holding the manager's lock. Calling with nil disables transitions.
//
// The daemon wires this to the generic snapshot-notify
// channel so SSE subscribers re-fetch the snapshot (which carries
// DepsLastAnalyzedAt and the inferred-edge set). No typed SSE frame is
// emitted; the existing "something changed" signal is reused.
func (m *JobManager) SetOnTransition(fn func(*Job)) {
	m.mu.Lock()
	m.onTransition = fn
	m.mu.Unlock()
}

// notifyLocked invokes the registered transition callback, if any. Must be
// called with m.mu HELD. The callback runs on the caller's goroutine because
// the lock is released by the caller's defer or explicit unlock; this is
// acceptable because typical callers are short-lived SSE broadcasts.
func (m *JobManager) notifyLocked(job *Job) {
	if m.onTransition == nil || job == nil {
		return
	}
	snapshot := cloneJob(job)
	// Release the lock during callback to avoid blocking other Enqueue/Status
	// calls if the SSE hub serialises broadcasts.
	m.mu.Unlock()
	m.onTransition(snapshot)
	m.mu.Lock()
}

// Enqueue starts a new analyzer job for the given profile. When a job is
// already running, the existing job's ID is returned without starting a new
// pass.
func (m *JobManager) Enqueue(ctx context.Context, profile string) (string, error) {
	if m.run == nil {
		return "", errors.New("depsanalysis: job manager has no runner")
	}
	m.mu.Lock()
	if m.current != nil && m.current.Status == JobRunning {
		id := m.current.ID
		m.mu.Unlock()
		return id, nil
	}
	job := &Job{
		ID:       newJobID(),
		Profile:  profile,
		Status:   JobRunning,
		QueuedAt: time.Now().UTC(),
	}
	job.StartedAt = job.QueuedAt
	m.current = job
	m.latest = job
	// Emit the "running" transition while we still hold
	// the lock so listeners observe the job before any terminal transition.
	m.notifyLocked(job)
	m.mu.Unlock()

	go m.execute(ctx, job)
	return job.ID, nil
}

func (m *JobManager) execute(ctx context.Context, job *Job) {
	sc, err := m.run(ctx, job.Profile)
	m.mu.Lock()
	defer m.mu.Unlock()
	job.FinishedAt = time.Now().UTC()
	if err != nil {
		job.Status = JobFailed
		job.Error = err.Error()
	} else {
		job.Status = JobSucceeded
		if sc != nil {
			job.EdgesFound = len(sc.Edges)
		}
	}
	// Clear current so the next Enqueue starts fresh; latest stays for status
	// lookups.
	if m.current == job {
		m.current = nil
	}
	// Terminal transition broadcast. notifyLocked
	// momentarily releases the lock so the callback can do its own locking
	// (SSE hub broadcast etc.) without deadlocking against Enqueue/Status.
	m.notifyLocked(job)
}

// Status returns the job with the given ID, scanning both the running job and
// the most recently completed job. Returns (nil, false) when no match exists
// — historically the dashboard would only have one outstanding job at a time
// so a single-element backlog suffices.
func (m *JobManager) Status(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil && m.current.ID == id {
		return cloneJob(m.current), true
	}
	if m.latest != nil && m.latest.ID == id {
		return cloneJob(m.latest), true
	}
	return nil, false
}

// Latest returns the most recently submitted or completed job, if any.
func (m *JobManager) Latest() (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.latest == nil {
		return nil, false
	}
	return cloneJob(m.latest), true
}

func cloneJob(j *Job) *Job {
	if j == nil {
		return nil
	}
	out := *j
	return &out
}

func newJobID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failure is extraordinarily rare; fall back to nanos.
		return time.Now().UTC().Format("20060102T150405.000000000")
	}
	return hex.EncodeToString(buf[:])
}
