package logbuffer

// Per-issue disk persistence: ONE writer goroutine per Buffer (M0-close
// fix-F; replaces fix-D's goroutine-less "disk role + inline budget").
//
// Every operation that touches an issue's log file — appending a line,
// reading the file for an empty window, deleting it for Clear/ClearAll,
// counting its lines to seed the sequence numbering, and the Flush barrier —
// is an op on one FIFO queue, and only the writer goroutine executes ops. So:
//
//   - Add never does disk I/O (R1): it assigns the line's sequence number and
//     enqueues the append under the issue's lock, then returns. It never
//     waits — not for the disk, not for the queue.
//   - Disk order is sequence order, with no duplicates (R2): appends for an
//     issue are enqueued under that issue's lock in the order their numbers
//     are assigned, and the single writer executes the queue in order.
//   - Nothing is silently dropped (R3): the queue is bounded by bytes
//     (maxQueuedBytes). An append that does not fit is dropped from disk
//     only — the in-memory ring keeps it — and counted (DroppedDiskLines)
//     and reported by a rate-limited slog.Warn. The bound is on the queue,
//     not a sticky state: as soon as the writer drains, new appends fit
//     again, so persistence resumes by itself when the disk recovers. A
//     dropped line leaves the file behind the sequence numbering, which the
//     empty-window read reports as a gap (see snapshotWindow), never as a
//     silently misnumbered window.
//   - Nothing is stranded (R4): the writer waits on the queue itself, so a
//     queued line is written without any later Add, read or Flush.
//   - Flush is a barrier (R5): it enqueues a no-op and waits for the writer
//     to reach it, or for ctx. It does no disk work itself, so it returns
//     promptly on expiry and starts nothing after it. Close stops the writer
//     from starting any further disk work.
//   - Memory and disk numbering stay consistent (R6-R8): a read, a Clear's
//     delete-and-rebase, ClearAll's directory sweep and every append for the
//     issue are totally ordered by the queue, so a read's captured sequence
//     number and the file it reads always describe the same point in the
//     issue's history. See diskOp.
//
// The per-issue lock is never held across disk I/O; the writer takes an
// issue's lock only briefly and never while holding the queue lock (lock
// order: issueBuf.mu, then diskWriter.mu). The writer never touches
// orchestrator state: its only inputs are this package's queue and issueBufs.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vnovick/itervox/internal/metrics"
)

// maxQueuedBytes bounds the memory held by lines queued for disk. It is only
// approached when the disk stalls while agents keep logging; a healthy disk
// drains the queue continuously.
const maxQueuedBytes = 8 << 20

// queuedLineOverhead is the per-line bookkeeping charged against
// maxQueuedBytes on top of the line's own bytes, so a flood of tiny lines is
// bounded too.
const queuedLineOverhead = 64

// dropWarnEvery rate-limits the "line not persisted" warning.
const dropWarnEvery = 30 * time.Second

// ErrClosed is returned by Flush, Clear and ClearAll once Close has been
// called: the writer no longer performs disk work.
var ErrClosed = errors.New("logbuffer: closed")

// ErrLinesNotPersisted is wrapped by Flush's error when every queued line was
// processed but some lines never reached disk — dropped because the queue was
// full, discarded by Close, or failed to write.
var ErrLinesNotPersisted = errors.New("logbuffer: some log lines were not persisted")

type opKind uint8

const (
	opAppend opKind = iota
	opSync          // barrier; with ib set, also seeds ib's numbering
	opRead          // empty-window read of the issue's file
	opClear         // delete the issue's file and re-base its numbering
	opSweep         // ClearAll: delete every .log file not yet touched
	opProbe         // does the identifier have a file (on disk, or touched)?
)

// diskOp is one queued operation. Appends, reads and clears are enqueued
// while holding ib.mu, so their queue order matches the issue's numbering:
// every line numbered at or below a read's or clear's upTo was enqueued
// before it, every later line after it.
type diskOp struct {
	kind       opKind
	ib         *issueBuf
	identifier string
	dir        string
	line       string // opAppend
	// upTo is ib.n when the op was enqueued (opRead, opClear).
	upTo int64
	// done receives the result of a control op (nil for appends). Buffered
	// (cap 1); the writer never blocks on it.
	done chan diskResult
}

type diskResult struct {
	lines          []string
	seq            int64
	irreconcilable bool
	exists         bool // opProbe
	err            error
}

// fileState is the writer's record of an issue file it has touched. Only the
// writer goroutine reads or writes it.
type fileState struct {
	// offset maps the CURRENT file onto the issue's local numbering: disk
	// line k (1-based) is the line with ib.n == offset+k. Initialised on
	// first touch to minus the file's existing line count (those lines
	// predate this Buffer), set to a Clear's upTo once it deletes the file,
	// so the next line is line 1 of the new file (M0-close G2), and advanced
	// by the rotated file's line count on rotation (CORE-036).
	offset int64
	// lines and size describe the current file as this writer last wrote
	// it, so a read numbers its window without counting the file. A size
	// that no longer matches the file triggers one re-count (syncFileState).
	lines, size int64
}

// diskWriter owns the queue and the writer goroutine of one Buffer.
type diskWriter struct {
	mu          sync.Mutex
	cond        *sync.Cond // L = &mu; signalled on enqueue and on close
	queue       []diskOp
	queuedBytes int
	started     bool
	closed      bool          // Close was called, or the writer panicked
	exited      chan struct{} // closed when the writer goroutine returns

	// limit is maxQueuedBytes unless a same-package test lowers it.
	limit int
	// fileCap is maxLogFileBytes unless a same-package test lowers it. Read
	// only by the writer goroutine.
	fileCap int64

	// dropped counts lines that never reached disk: queue full, discarded
	// by Close, or a failed write. lastDropWarn rate-limits the warning.
	dropped      atomic.Int64
	lastDropWarn atomic.Int64
	// rotations counts per-issue file rotations (CORE-036).
	rotations atomic.Int64

	// Writer-goroutine state.
	files map[string]*fileState // keyed by issue file path
	batch []diskOp              // the batch being executed (for panic recovery)
}

func (w *diskWriter) init() {
	w.cond = sync.NewCond(&w.mu)
	w.exited = make(chan struct{})
	w.limit = maxQueuedBytes
	w.fileCap = maxLogFileBytes
	w.files = make(map[string]*fileState)
}

// enqueue queues op and starts the writer on first use. It never blocks. It
// returns false — op not queued — once the writer is closed, or for an
// append that does not fit under the byte bound; the caller then reports a
// dropped append via noteDropped, or fails a control op with ErrClosed.
func (b *Buffer) enqueue(op diskOp) bool {
	w := &b.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return false
	}
	if op.kind == opAppend {
		cost := len(op.line) + queuedLineOverhead
		if w.queuedBytes+cost > w.limit {
			return false
		}
		w.queuedBytes += cost
	}
	w.queue = append(w.queue, op)
	if !w.started {
		w.started = true
		go b.runWriter()
	}
	w.cond.Signal()
	return true
}

// noteDropped records an append that will never reach disk.
func (w *diskWriter) noteDropped(identifier, why string, n int64) {
	total := w.dropped.Add(n)
	now := time.Now().UnixNano()
	last := w.lastDropWarn.Load()
	if now-last < int64(dropWarnEvery) || !w.lastDropWarn.CompareAndSwap(last, now) {
		return
	}
	slog.Warn("logbuffer: log line(s) kept in memory only, not written to disk",
		"identifier", identifier, "reason", why, "dropped_total", total,
		"queue_limit_bytes", w.limit)
}

// DroppedDiskLines reports how many log lines this Buffer has kept in memory
// only — never written to disk — because the disk queue was full, the Buffer
// was closed, or the write failed.
func (b *Buffer) DroppedDiskLines() int64 { return b.w.dropped.Load() }

// runWriter is the writer goroutine: it executes queued ops in order until
// the Buffer is closed. A panic is recovered, logged with its stack, and
// closes the writer (recoverWriter), so it can neither crash the daemon nor
// leave a Flush/read/Clear caller waiting forever.
func (b *Buffer) runWriter() {
	w := &b.w
	defer close(w.exited)
	defer b.recoverWriter()
	for {
		w.mu.Lock()
		for len(w.queue) == 0 && !w.closed {
			w.cond.Wait()
		}
		if w.closed {
			w.batch = w.queue
			w.queue, w.queuedBytes = nil, 0
			w.mu.Unlock()
			b.discard(w.batch, "log buffer closed")
			w.batch = nil
			return
		}
		w.batch = w.queue
		w.queue = nil
		w.mu.Unlock()
		b.execute()
		w.batch = nil
	}
}

// execute runs w.batch in order. Consecutive appends to the same file are
// written with one open. The closed flag is checked before every disk
// operation, so Close stops the writer from starting new disk work; the rest
// of the batch is then discarded. An op leaves w.batch only once it is done,
// so a panic mid-op still fails it (recoverWriter).
func (b *Buffer) execute() {
	w := &b.w
	for len(w.batch) > 0 {
		if w.isClosed() {
			b.discard(w.batch, "log buffer closed")
			w.batch = nil
			return
		}
		op := w.batch[0]
		if op.kind != opAppend {
			r := b.control(op)
			w.batch = w.batch[1:]
			finish(op, r)
			continue
		}
		n := 1
		for n < len(w.batch) && w.batch[n].kind == opAppend &&
			w.batch[n].identifier == op.identifier && w.batch[n].dir == op.dir {
			n++
		}
		run := w.batch[:n]
		fs := b.touch(op.ib, op.identifier, op.dir)
		lines := make([]string, n)
		cost := 0
		for i, a := range run {
			lines[i] = a.line
			cost += len(a.line) + queuedLineOverhead
		}
		err := b.appendLines(fs, op.dir, op.identifier, lines)
		w.batch = w.batch[n:]
		if err != nil {
			w.noteDropped(op.identifier, "write failed: "+err.Error(), int64(n))
		}
		w.mu.Lock()
		w.queuedBytes -= cost
		w.mu.Unlock()
	}
}

// control executes one non-append op on the writer goroutine.
func (b *Buffer) control(op diskOp) diskResult {
	switch op.kind {
	case opSync:
		if op.ib != nil {
			b.touch(op.ib, op.identifier, op.dir)
		}
		return diskResult{}
	case opRead:
		return b.readForWindow(op)
	case opClear:
		fs := b.touch(op.ib, op.identifier, op.dir)
		if err := removeIssueFiles(op.dir, op.identifier); err != nil {
			syncFileState(fs, issuePath(op.dir, op.identifier))
			return diskResult{err: err} // the current file and its numbering stay intact
		}
		fs.offset = op.upTo
		fs.lines, fs.size = 0, 0
		return diskResult{}
	case opSweep:
		return diskResult{err: b.sweep(op.dir)}
	case opProbe:
		// A probe never touches: an identifier without a file must leave
		// no writer state behind (M0-close fix-G).
		p := issuePath(op.dir, op.identifier)
		if _, touched := b.w.files[p]; touched {
			return diskResult{exists: true}
		}
		// A rotated file with no current one is still the issue's log
		// (CORE-036: a rename that succeeded before the reopen failed, or a
		// current file deleted externally).
		for _, candidate := range []string{p, p + rotatedSuffix} {
			if _, err := os.Stat(candidate); err == nil {
				return diskResult{exists: true}
			}
		}
		return diskResult{}
	}
	return diskResult{err: fmt.Errorf("logbuffer: unknown disk op %d", op.kind)}
}

// touch returns the writer's state for ib's file in dir, initialising it on
// first use: it counts the lines the file already holds (written by an
// earlier process, or by nobody) and, if ib's numbering is not yet seeded,
// seeds it with that count plus the rotated file's (M0-close G1: after a
// restart, memory numbering continues the files', so memory and disk agree;
// CORE-036: counting the rotated file too keeps a window that spans the
// rotation boundary numbered from 1 upwards). This is the disk read
// getOrCreate used to do on Add's goroutine, and the only time the writer
// streams a whole file: later reads take the count from fileState.
func (b *Buffer) touch(ib *issueBuf, identifier, dir string) *fileState {
	w := &b.w
	p := issuePath(dir, identifier)
	if fs, ok := w.files[p]; ok {
		return fs
	}
	count, size := countFile(p)
	rotated, _ := countFile(p + rotatedSuffix)
	fs := &fileState{offset: -count, lines: count, size: size}
	w.files[p] = fs
	ib.mu.Lock()
	if !ib.seeded {
		ib.base, ib.seeded = rotated+count, true
	}
	ib.mu.Unlock()
	return fs
}

// readForWindow serves an empty in-memory window from disk. op.upTo is the
// issue's local count when the read was enqueued, so the file now holds
// exactly the lines numbered up to it (less any dropped): the numbering the
// read reports and the file it reads describe the same file (M0-close N1/N5).
func (b *Buffer) readForWindow(op diskOp) diskResult {
	fs := b.touch(op.ib, op.identifier, op.dir)
	if b.beforeDiskRead != nil {
		b.beforeDiskRead(op.identifier)
	}
	// The window is numbered from fileState, not by counting the file
	// (CORE-036: the read is tail-only); syncFileState re-counts only a file
	// something else has changed.
	syncFileState(fs, issuePath(op.dir, op.identifier))
	total := fs.lines
	// Read even when the current file is empty or missing: the rotated file
	// may still hold the issue's newest retained lines, numbered up to
	// fs.offset (readFromDisk tops up from it).
	disk, _ := readFromDisk(op.dir, op.identifier)
	ib := op.ib
	ib.mu.Lock()
	defer ib.mu.Unlock()
	if len(disk) == 0 {
		return diskResult{seq: ib.base + op.upTo}
	}
	diskN := fs.offset + total
	if diskN < op.upTo {
		// The file is behind the numbering (lines dropped, a failed write,
		// or an external truncation): report the preserved high-water mark
		// and let GetSince gap once.
		return diskResult{lines: disk, seq: ib.base + op.upTo, irreconcilable: true}
	}
	// Never re-issue a number a client may now hold as its cursor (the file
	// can be ahead when something else appended to it).
	ib.n = max(ib.n, diskN)
	return diskResult{lines: disk, seq: ib.base + diskN}
}

// sweep deletes every .log file in dir that the writer has not touched: files
// with no queued or written line of this Buffer's (identifiers from earlier
// runs), which a later first touch then counts after the delete (M0-close
// N9). A touched file belongs to an issueBuf that existed before ClearAll
// queued the sweep, so ClearAll's per-issue clear, queued right after the
// sweep, deletes it and re-bases its numbering in one op; deleting it here
// too would leave the file un-rebased until that clear runs.
func (b *Buffer) sweep(dir string) error {
	if b.beforeSweep != nil {
		b.beforeSweep()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("logbuffer: read dir %s: %w", dir, err)
	}
	var first error
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || (!strings.HasSuffix(name, ".log") && !strings.HasSuffix(name, ".log"+rotatedSuffix)) {
			continue
		}
		p := filepath.Join(dir, name)
		if _, touched := b.w.files[strings.TrimSuffix(p, rotatedSuffix)]; touched {
			continue
		}
		if err := removeLogFile(p); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// discard fails the control ops in ops with ErrClosed and counts the appends
// as dropped.
func (b *Buffer) discard(ops []diskOp, why string) {
	var appends int64
	id := ""
	for _, op := range ops {
		if op.kind == opAppend {
			appends++
			id = op.identifier
			continue
		}
		finish(op, diskResult{err: ErrClosed})
	}
	if appends > 0 {
		b.w.noteDropped(id, why, appends)
	}
}

// recoverWriter is runWriter's panic containment (CORE-008's recover-and-
// report pattern; logbuffer cannot import orchestrator.RecoverGoroutine). It
// logs the panic with its stack, closes the writer, and fails everything
// queued or in flight, so no caller waits forever.
func (b *Buffer) recoverWriter() {
	r := recover()
	if r == nil {
		return
	}
	metrics.GoroutinePanic()
	slog.Error("logbuffer: disk writer panicked; per-issue log persistence stopped",
		"panic", r, "stack", string(debug.Stack()))
	w := &b.w
	w.mu.Lock()
	w.closed = true
	rest := append(w.batch, w.queue...)
	w.queue, w.batch, w.queuedBytes = nil, nil, 0
	w.mu.Unlock()
	b.discard(rest, "disk writer panicked")
}

func (w *diskWriter) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

// finish delivers a control op's result without ever blocking.
func finish(op diskOp, r diskResult) {
	if op.done == nil {
		return
	}
	select {
	case op.done <- r:
	default:
	}
}

// Flush waits until every log line Added before it was called has been
// written to disk, and returns nil if all of them were. It enqueues a
// barrier behind those lines and waits for the writer to reach it; it does
// no disk I/O itself, so on ctx expiry it returns ctx.Err() at once and
// starts nothing afterwards (the writer keeps draining in the background
// until Close). It returns an error wrapping ErrLinesNotPersisted if any line
// this Buffer was given never reached disk (queue full, a failed write), and
// ErrClosed after Close. Writes are not fsynced: a line on disk survives the
// process, not necessarily a power loss.
func (b *Buffer) Flush(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.w.mu.Lock()
	idle := !b.w.started && !b.w.closed // nothing was ever queued
	b.w.mu.Unlock()
	if !idle {
		done := make(chan diskResult, 1)
		if !b.enqueue(diskOp{kind: opSync, done: done}) {
			return ErrClosed
		}
		select {
		case r := <-done:
			if r.err != nil { // discarded by Close or a writer panic
				return r.err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if n := b.w.dropped.Load(); n > 0 {
		return fmt.Errorf("%w: %d line(s) kept in memory only", ErrLinesNotPersisted, n)
	}
	return nil
}

// Close stops the writer: it starts no disk operation after Close is called
// (one already in progress completes), lines still queued are discarded and
// counted as dropped, and later Adds are kept in memory only. Close waits for
// the writer goroutine to exit, or for ctx. Call Flush first to persist the
// queue. cmd/itervox calls Flush then Close when a run ends, before main()
// exits or reloads — a reload's next Buffer counts the files on first use,
// so the old writer must be quiet by then.
func (b *Buffer) Close(ctx context.Context) error {
	w := &b.w
	w.mu.Lock()
	w.closed = true
	started := w.started
	w.cond.Broadcast()
	w.mu.Unlock()
	if !started {
		return nil
	}
	select {
	case <-w.exited:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// appendLines is the disk-append step: a size-capped, rotating append
// (appendRotating) unless a test replaced it. A seam's writes are picked up
// by the next read's syncFileState.
func (b *Buffer) appendLines(fs *fileState, dir, identifier string, lines []string) error {
	if b.diskAppend != nil {
		b.diskAppend(dir, identifier, lines)
		return nil
	}
	return appendRotating(fs, dir, identifier, lines, b.w.fileCap, &b.w.rotations)
}
