package orchestrator

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/metrics"
)

// CORE-038 — serialized, dirty-checked ledger persistence.
//
// Every orchestrator ledger (paused, pause reasons, input-required, automation
// queue, auto-switched, run history, deps overrides) used to be rewritten —
// temp file + fsync + rename — on the event-loop goroutine after EVERY event,
// including each EventWorkerUpdate token batch, whether or not it changed.
//
// Now each ledger has one ledgerWriter:
//
//   - Callers (the event loop, and ClearHistory on an HTTP goroutine) marshal
//     the ledger from a clone they own and submit the bytes. The writer never
//     sees State.
//   - Dirty check: a submission byte-identical to the previous one is dropped
//     before it is queued, so an unchanged ledger costs no write at all.
//   - Latest wins: only the newest submission is kept pending. Every ledger is
//     a full-content rewrite, so the newest version subsumes every older one;
//     coalescing drops intermediate versions, never the latest.
//   - Serialized: at most one write per ledger is in progress at any time
//     (the draining flag, flipped under mu together with the pending check,
//     so a submission can never be stranded between two drainers). Writes are
//     applied in strictly increasing sequence order, so an older version can
//     never land after a newer one.
//   - Crash consistency: writes go through writeLedgerFile (atomicfs), so the
//     file on disk is always a complete previous or current version.
//   - While Run is active a per-ledger goroutine does the I/O, off the event
//     loop. Outside Run (tests, and calls after Run returned) the submitter
//     drains inline.
//   - A failed write keeps the version pending, counts the error, and is
//     retried by the worker's timer without waiting for another event; the
//     shutdown flush retries once more before Run returns.

// defaultPersistRetryInterval is how often a ledger whose last write failed is
// retried while Run is active.
const defaultPersistRetryInterval = 5 * time.Second

type ledgerID int

const (
	ledgerHistory ledgerID = iota
	ledgerPaused
	ledgerPauseReasons
	ledgerInputRequired
	ledgerAutomationQueue
	ledgerAutoSwitched
	ledgerDepsOverrides
	ledgerBackendHealth
	ledgerPendingReviews
	numLedgers
)

var ledgerNames = [numLedgers]string{
	ledgerHistory:         "history",
	ledgerPaused:          "paused",
	ledgerPauseReasons:    "pause_reasons",
	ledgerInputRequired:   "input_required",
	ledgerAutomationQueue: "automation_queue",
	ledgerAutoSwitched:    "auto_switched",
	ledgerDepsOverrides:   "deps_overrides",
	ledgerBackendHealth:   "backend_health",
	ledgerPendingReviews:  "pending_reviews",
}

// ledgerOp is one full version of a ledger: write data to path, or remove it.
type ledgerOp struct {
	seq    uint64
	path   string
	data   []byte
	perm   fs.FileMode
	remove bool
}

type ledgerWriter struct {
	name  string
	write func(path string, data []byte, perm fs.FileMode) error
	// onError is called once per failed write with its error (the
	// orchestrator's counter and the RecentFailures ring, CORE-046).
	onError func(error)

	mu         sync.Mutex
	cond       *sync.Cond // signalled when a drain pass finishes
	seq        uint64     // last assigned sequence number
	last       *ledgerOp  // last accepted submission (dirty check)
	pending    *ledgerOp  // newest version not yet on disk; nil = clean
	writtenSeq uint64     // highest sequence number that reached disk
	draining   bool       // a drain pass owns the write path
	failed     bool       // the most recent write attempt failed
	running    bool       // a worker goroutine is active (Run is live)
	kick       chan struct{}
	stop       chan struct{}
	done       chan struct{}
}

func newLedgerWriter(name string, write func(string, []byte, fs.FileMode) error, onError func(error)) *ledgerWriter {
	w := &ledgerWriter{name: name, write: write, onError: onError, kick: make(chan struct{}, 1)}
	w.cond = sync.NewCond(&w.mu)
	return w
}

// submit queues data as the newest version of the ledger at path. It returns
// false when the content is byte-identical to the previous submission (the
// dirty check) and nothing was queued. submit never performs I/O, so callers
// may hold their own locks across it; call settle afterwards, lock-free.
//
// An identical submission still returns true while the previous version's
// write failed and is pending: outside Run there is no retry timer, so the
// next submission is the only retry trigger, and deduplicating it would leave
// the failed version stranded (BH6).
func (w *ledgerWriter) submit(path string, data []byte, perm fs.FileMode) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if l := w.last; l != nil && !l.remove && l.path == path && l.perm == perm && bytes.Equal(l.data, data) {
		return w.failed && w.pending != nil
	}
	w.seq++
	op := &ledgerOp{seq: w.seq, path: path, data: data, perm: perm}
	w.pending = op
	w.last = op
	return true
}

// lastData returns a copy of the newest accepted (non-remove) version, or nil.
func (w *ledgerWriter) lastData() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.last == nil || w.last.remove {
		return nil
	}
	return bytes.Clone(w.last.data)
}

// hasSubmitted reports whether this process has submitted any version.
func (w *ledgerWriter) hasSubmitted() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last != nil
}

// submitRemove queues deletion of the ledger file. Never deduplicated: the
// file may predate this process.
func (w *ledgerWriter) submitRemove(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq++
	op := &ledgerOp{seq: w.seq, path: path, remove: true}
	w.pending = op
	w.last = op
}

// settle makes a submission take effect: it wakes the worker when Run is
// live, otherwise drains inline on the caller's goroutine.
func (w *ledgerWriter) settle() {
	w.mu.Lock()
	running := w.running
	w.mu.Unlock()
	if running {
		select {
		case w.kick <- struct{}{}:
		default: // a wake-up is already queued; it will see the newest version
		}
		return
	}
	_ = w.drain()
}

// drain writes pending versions until none is left. If another goroutine is
// already draining it returns immediately: that drainer re-checks pending
// under mu before it gives up the write path, so it will write this version.
func (w *ledgerWriter) drain() error {
	w.mu.Lock()
	if w.draining {
		w.mu.Unlock()
		return nil
	}
	w.draining = true
	var err error
	for {
		op := w.pending
		if op == nil || op.seq <= w.writtenSeq {
			w.pending = nil
			break
		}
		w.mu.Unlock()
		err = w.apply(op)
		w.mu.Lock()
		if err != nil {
			w.failed = true
			break // op stays pending (or was superseded); retried later
		}
		w.failed = false
		w.writtenSeq = op.seq
		if w.pending == op {
			w.pending = nil
		}
	}
	w.draining = false
	w.cond.Broadcast()
	w.mu.Unlock()
	return err
}

func (w *ledgerWriter) apply(op *ledgerOp) (err error) {
	defer func() {
		// A panicking writer must not leave draining stuck at true (every
		// later flush would wait forever): report it as a failed write.
		if r := recover(); r != nil {
			metrics.GoroutinePanic()
			err = fmt.Errorf("orchestrator: ledger writer panicked: %v", r)
			if w.onError != nil {
				w.onError(err)
			}
			slog.Error("orchestrator: ledger write panicked; will retry", "ledger", w.name, "path", op.path, "panic", r)
		}
	}()
	if op.remove {
		err = os.Remove(op.path)
		if errors.Is(err, fs.ErrNotExist) {
			err = nil
		}
	} else {
		err = w.write(op.path, op.data, op.perm)
	}
	if err != nil {
		if w.onError != nil {
			w.onError(err)
		}
		slog.Warn("orchestrator: ledger write failed; will retry", "ledger", w.name, "path", op.path, "error", err)
	}
	return err
}

// flush synchronously persists every pending version: it waits out a drain
// in progress on another goroutine, then drains itself, retrying a failed
// write once. Returns an error when the ledger is still dirty afterwards.
func (w *ledgerWriter) flush() error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if w.waitIdleClean() {
			return nil
		}
		err = w.drain()
	}
	if w.waitIdleClean() {
		return nil
	}
	if err == nil {
		err = errors.New("orchestrator: ledger still dirty after flush")
	}
	return err
}

// waitIdleClean waits until no drain is in progress and reports whether
// nothing is pending.
func (w *ledgerWriter) waitIdleClean() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for w.draining {
		w.cond.Wait()
	}
	return w.pending == nil
}

// start launches the ledger's worker goroutine. retry is the interval at which
// a failed write is re-attempted without a new submission.
func (w *ledgerWriter) start(retry time.Duration) {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return
	}
	w.running = true
	w.stop = make(chan struct{})
	w.done = make(chan struct{})
	stop, done := w.stop, w.done
	w.mu.Unlock()
	go func() {
		defer close(done)
		// CORE-008 panic containment. apply already converts a writer panic
		// into a write error, so this is belt and braces: on a panic the
		// ledger falls back to inline draining (submitters write on their
		// own goroutine) and the shutdown flush still runs.
		defer RecoverGoroutine("ledger-writer", w.name, w.stopRunning)
		w.loop(retry, stop)
	}()
}

// stopRunning switches the ledger back to inline draining.
func (w *ledgerWriter) stopRunning() {
	w.mu.Lock()
	w.running = false
	w.mu.Unlock()
}

func (w *ledgerWriter) loop(retry time.Duration, stop <-chan struct{}) {
	timer := time.NewTimer(retry)
	timer.Stop()
	for {
		select {
		case <-stop:
			timer.Stop()
			return
		case <-w.kick:
		case <-timer.C:
		}
		_ = w.drain()
		// Retry a failed write on the timer, without waiting for a new
		// submission (a quiet ledger would otherwise stay stale on disk).
		w.mu.Lock()
		retryNeeded := w.failed && w.pending != nil
		w.mu.Unlock()
		if retryNeeded {
			timer.Reset(retry)
		}
	}
}

// stopAndFlush stops the worker goroutine (waiting for a write in progress)
// and then flushes synchronously, so every accepted version is on disk — or
// its failure logged and counted — when it returns. After it returns,
// submissions drain inline again.
func (w *ledgerWriter) stopAndFlush() error {
	w.mu.Lock()
	running := w.running
	w.running = false
	stop, done := w.stop, w.done
	w.mu.Unlock()
	if running {
		close(stop)
		<-done
	}
	return w.flush()
}

// ledger returns the orchestrator's writer for id, creating the set lazily so
// &Orchestrator{} literals in tests work without New().
func (o *Orchestrator) ledger(id ledgerID) *ledgerWriter {
	o.persistOnce.Do(func() {
		for i := range o.ledgers {
			name := ledgerNames[i]
			o.ledgers[i] = newLedgerWriter(name, o.writeLedgerFile, func(err error) {
				o.persistWriteErrors.Add(1)
				// CORE-046: non-blocking; identical retries coalesce in the ring.
				o.RecordFailure(FailureRecord{
					Kind:    FailureKindPersist,
					Source:  name,
					Message: fmt.Sprintf("%s ledger write failed (will retry): %v", name, err),
				})
			})
		}
	})
	return o.ledgers[id]
}

// persistLedger submits data as the newest version of ledger id and makes it
// take effect. The dirty check makes an unchanged ledger free.
func (o *Orchestrator) persistLedger(id ledgerID, path string, data []byte, perm fs.FileMode) {
	w := o.ledger(id)
	if w.submit(path, data, perm) {
		w.settle()
	}
}

// startPersistence moves ledger I/O onto per-ledger worker goroutines for the
// lifetime of Run.
func (o *Orchestrator) startPersistence() {
	retry := o.persistRetryInterval
	if retry <= 0 {
		retry = defaultPersistRetryInterval
	}
	for i := range numLedgers {
		o.ledger(i).start(retry)
	}
}

// stopPersistence is Run's shutdown flush: after the event loop's final
// storeSnap, every ledger's newest accepted version is written (a still-
// failing write is retried once) before Run returns, which is what makes a
// graceful shutdown or a config reload (main.go's run() awaits orch.Run
// before starting the next generation) lose nothing.
func (o *Orchestrator) stopPersistence() {
	for i := range numLedgers {
		if err := o.ledger(i).stopAndFlush(); err != nil {
			slog.Warn("orchestrator: ledger still dirty after shutdown flush", "ledger", ledgerNames[i], "error", err)
		}
	}
}

// PersistWriteErrors returns how many ledger writes have failed since this
// orchestrator was created. Also surfaced as State.PersistWriteErrors in
// Snapshot(). Safe to call from any goroutine.
func (o *Orchestrator) PersistWriteErrors() int64 {
	return o.persistWriteErrors.Load()
}
