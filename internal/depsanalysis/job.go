package depsanalysis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/config"
)

// JobStatus identifies a depsanalysis job's progress.
type JobStatus string

const (
	JobQueued    JobStatus = "queued"
	JobRunning   JobStatus = "running"
	JobSucceeded JobStatus = "succeeded"
	JobFailed    JobStatus = "failed"

	// JobCancelled is an operator-initiated stop. Distinct from JobFailed
	// because "I stopped it" and "it broke" need different UI and different
	// operator responses.
	JobCancelled JobStatus = "cancelled"
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
	// IssuesAnalyzed is the count of issues actually SENT to the analyzer
	// agent this pass (plan.ToAnalyze's length) — #52's IssuesScanned
	// honesty fix. Under incremental mode, IssuesScanned (the raw fetch
	// count) can be far larger than IssuesAnalyzed: a revalidation-only run
	// with zero content changes reports "scanned 50" while sending 0 issues
	// to the agent, which read as "analyzed 50" before this field existed.
	IssuesAnalyzed int
	EdgesFound     int
	Error          string

	// ChunksTotal / ChunksDone make progress visible. ChunksDone alone would
	// not distinguish "working through chunk 2" from "wedged in chunk 2", so
	// LastActivityAt is bumped from the agent's per-event progress callback.
	ChunksTotal    int
	ChunksDone     int
	LastActivityAt time.Time

	// Mode is the requested incremental-pass mode ("auto" | "full" |
	// "incremental") captured at Enqueue time. JobRunner's signature
	// (ctx, profile) has no room for it, so the runner reads it back via
	// JobManager.Latest — see depsAnalyzerService.currentJobMode for the
	// pattern this mirrors (currentJobID).
	Mode string
	// Trigger identifies what caused this job: "manual" (an operator via the
	// dashboard/API/CLI) or "auto" (Task 4's scheduler). Defaults to
	// "manual" via Enqueue/EnqueueWithOptions so pre-4b callers keep prior
	// behavior.
	Trigger string
}

// JobResult is what one analyzer pass reports back. It exists so the runner can
// return the counters the job row needs; the previous signature returned only a
// Sidecar, which is why IssuesScanned was declared and never written.
type JobResult struct {
	Sidecar       *Sidecar
	IssuesScanned int
	// IssuesAnalyzed mirrors Job.IssuesAnalyzed — see its doc comment.
	IssuesAnalyzed int
	ChunksTotal    int
}

// JobRunner performs one end-to-end analysis pass and persists the sidecar.
type JobRunner func(ctx context.Context, profile string) (*JobResult, error)

// JobManager runs at most one analyzer job at a time. Calling Enqueue while a
// job is already running returns the existing job ID without starting a
// second pass — matches the plan's "concurrency cap of one per process".
type JobManager struct {
	mu            sync.Mutex
	current       *Job
	currentCancel context.CancelFunc
	latest        *Job
	run           JobRunner
	timeout       time.Duration
	onTransition  func(*Job)
}

// NewJobManager returns a manager bound to the runner. A non-positive timeout
// clamps to the package default — a zero would otherwise produce an
// instantly-expired context and a job that "runs" without doing anything.
func NewJobManager(run JobRunner, timeout time.Duration) *JobManager {
	if timeout <= 0 {
		timeout = time.Duration(config.DefaultDepsAnalyzerTimeoutMs) * time.Millisecond
	}
	return &JobManager{run: run, timeout: timeout}
}

// Cancel stops the running job with the given ID. Returns false when the ID
// does not match a job that is currently running.
func (m *JobManager) Cancel(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || m.current.ID != id || m.current.Status != JobRunning {
		return false
	}
	// FIX 5 (final review) — m.currentCancel == nil is unreachable today
	// (both fields are set together, under this same lock, in Enqueue), but
	// a 204 from the DELETE endpoint is the caller's only guarantee that
	// cancellation actually happened; returning true here without calling
	// anything would be a lie the endpoint has no way to detect.
	if m.currentCancel == nil {
		return false
	}
	m.currentCancel()
	return true
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
//
// FIX 4 (final review) — a panic inside the callback would otherwise unwind
// past the m.mu.Lock() below, straight into execute()'s
// `defer m.mu.Unlock()`, producing an "unlock of unlocked mutex" runtime
// throw that `recover` cannot catch and that kills the daemon. This
// sub-project wired a live SSE broadcaster into onTransition, so a panic
// here is reachable in production, not just theoretical — the recover below
// re-locks before re-panicking so the mutex invariant holds regardless of
// how the callback misbehaves, and execute()'s own recover (around m.run)
// is what actually stops the panic from propagating further.
func (m *JobManager) notifyLocked(job *Job) {
	if m.onTransition == nil || job == nil {
		return
	}
	snapshot := cloneJob(job)
	onTransition := m.onTransition
	// Release the lock during callback to avoid blocking other Enqueue/Status
	// calls if the SSE hub serialises broadcasts.
	m.mu.Unlock()
	func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("depsanalysis: onTransition callback panicked", "job_id", job.ID, "panic", r)
			}
		}()
		onTransition(snapshot)
	}()
	m.mu.Lock()
}

// EnqueueOptions customizes an analyzer job beyond the profile: a requested
// mode override and a trigger label. Both fields default when left empty
// (Mode -> "auto", Trigger -> "manual") so a zero-value EnqueueOptions is
// exactly what Enqueue(ctx, profile) has always produced.
type EnqueueOptions struct {
	// Mode is the requested incremental-pass mode: "auto" (default,
	// incremental when a usable prior sidecar exists), "full", or
	// "incremental". Resolution against the prior sidecar happens in the
	// runner via PlanIncremental — the job manager only threads the request
	// through.
	Mode string
	// Trigger is "manual" (default) or "auto". The scheduler (Task 4) is the
	// only "auto" caller.
	Trigger string
}

const (
	jobModeAuto      = "auto"
	jobTriggerManual = "manual"
)

// Enqueue starts a new analyzer job for the given profile, requesting mode
// "auto" and trigger "manual". Delegates to EnqueueWithOptions so both entry
// points share one code path.
func (m *JobManager) Enqueue(ctx context.Context, profile string) (string, error) {
	return m.EnqueueWithOptions(ctx, profile, EnqueueOptions{})
}

// EnqueueWithOptions starts a new analyzer job for the given profile with an
// explicit mode/trigger. When a job is already running, the existing job's
// ID is returned without starting a new pass — the options on that in-flight
// job are whatever it was originally enqueued with; a caller racing a second
// EnqueueWithOptions during that window does not get its own mode/trigger
// honored.
func (m *JobManager) EnqueueWithOptions(ctx context.Context, profile string, opts EnqueueOptions) (string, error) {
	if m.run == nil {
		return "", errors.New("depsanalysis: job manager has no runner")
	}
	mode := opts.Mode
	if mode == "" {
		mode = jobModeAuto
	}
	trigger := opts.Trigger
	if trigger == "" {
		trigger = jobTriggerManual
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
		Mode:     mode,
		Trigger:  trigger,
	}
	job.StartedAt = job.QueuedAt
	job.LastActivityAt = job.QueuedAt

	jobCtx, cancel := context.WithTimeout(ctx, m.timeout)
	m.current, m.currentCancel = job, cancel
	m.latest = job
	// Emit the "running" transition while we still hold
	// the lock so listeners observe the job before any terminal transition.
	m.notifyLocked(job)
	m.mu.Unlock()

	go m.execute(jobCtx, cancel, job)
	return job.ID, nil
}

func (m *JobManager) execute(ctx context.Context, cancel context.CancelFunc, job *Job) {
	defer cancel()

	var (
		res      *JobResult
		err      error
		panicked any
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = r
				slog.Error("depsanalysis: analyzer job panicked", "job_id", job.ID, "panic", r)
			}
		}()
		res, err = m.run(ctx, job.Profile)
	}()

	m.mu.Lock()
	defer m.mu.Unlock()
	job.FinishedAt = time.Now().UTC()

	switch {
	case panicked != nil:
		job.Status = JobFailed
		job.Error = fmt.Sprintf("analyzer panicked: %v", panicked)
	case errors.Is(err, context.Canceled) || (err != nil && ctx.Err() == context.Canceled):
		job.Status = JobCancelled
		job.Error = ""
	case errors.Is(err, context.DeadlineExceeded) || (err != nil && ctx.Err() == context.DeadlineExceeded):
		job.Status = JobFailed
		job.Error = fmt.Sprintf("analyzer timed out after %s", m.timeout)
	case err != nil:
		job.Status = JobFailed
		job.Error = err.Error()
	default:
		job.Status = JobSucceeded
		if res != nil {
			job.IssuesScanned = res.IssuesScanned
			job.IssuesAnalyzed = res.IssuesAnalyzed
			// Terminal-state backstop only: SetChunksTotal is the primary
			// source and publishes this value at launch, before the
			// per-chunk loop starts, so mid-run observers see a correct
			// count. This assignment just re-affirms the same value from
			// JobResult on success (and covers direct-JobResult callers,
			// e.g. tests, that never call SetChunksTotal at all).
			job.ChunksTotal = res.ChunksTotal
			if res.Sidecar != nil {
				job.EdgesFound = len(res.Sidecar.Edges)
			}
		}
	}

	// Clear current so the next Enqueue starts fresh regardless of outcome —
	// this is what stops one wedged job blocking every future run.
	if m.current == job {
		m.current, m.currentCancel = nil, nil
	}
	// Terminal transition broadcast. notifyLocked
	// momentarily releases the lock so the callback can do its own locking
	// (SSE hub broadcast etc.) without deadlocking against Enqueue/Status.
	m.notifyLocked(job)
}

// MarkProgress records that the running job is still doing work. Called from
// the analyzer's per-event progress callback; chunksDone < 0 leaves the chunk
// counter untouched and only refreshes the activity timestamp.
func (m *JobManager) MarkProgress(id string, chunksDone int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || m.current.ID != id {
		return
	}
	m.current.LastActivityAt = time.Now().UTC()
	if chunksDone >= 0 {
		m.current.ChunksDone = chunksDone
	}
}

// SetChunksTotal records the chunk count for the running job as soon as the
// runner knows it — i.e. right after chunking, before the per-chunk loop
// starts. Without this, ChunksTotal was only ever written in execute's
// success branch (after the job is already terminal), so a job mid-run
// carried ChunksDone > 0 alongside ChunksTotal == 0 and the dashboard would
// render an inverted "N / 0" progress ratio for the entire run. A no-op when
// id does not match the currently running job, mirroring MarkProgress.
func (m *JobManager) SetChunksTotal(id string, total int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || m.current.ID != id {
		return
	}
	m.current.ChunksTotal = total
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
