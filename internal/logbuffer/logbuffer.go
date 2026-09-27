// Package logbuffer provides a per-identifier ring buffer for recent log lines,
// shared between worker loggers and the terminal status UI.
package logbuffer

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

const maxLinesPerIssue = 500

// issueBuf is a per-identifier ring buffer with its own mutex.
// Different identifiers never contend with each other.
type issueBuf struct {
	mu    sync.RWMutex
	lines []string
	// subagents counts the lines currently in lines that are subagent
	// markers (IsSubagentMarker). Maintained incrementally under mu: +1 on
	// Add, -1 per marker line trimmed off the front of the ring, reset with
	// the window by Remove/Clear/ClearAll (CORE-035). Always equals a full
	// recount of lines.
	subagents int
	// n counts the lines this Buffer has assigned to the identifier. It is
	// monotonically increasing for the life of the issueBuf and is NEVER
	// rewound by Clear/ClearAll/Remove — see GetSince's doc comment for the
	// sequence contract this enables (CORE-003).
	n int64
	// base is the sequence numbering's starting point: a line's seq is
	// base + its n, so the newest line's seq is base+n. It is the line count
	// the identifier's log file already had when this Buffer first touched
	// it (M0-close G1: after a daemon restart the fresh Buffer serves that
	// file with ABSOLUTE numbering, so a client may already hold a
	// current-epoch cursor equal to the file's line count; numbering new
	// lines from 1 would re-issue numbers that cursor covers). Counting the
	// file is disk I/O, so Add does not do it: the disk writer does, on its
	// first operation for the file (touch), and sets seeded. Until then the
	// numbering is unobservable — GetSince waits for seeded (awaitSeed); Get
	// does not need it. Fixed once seeded.
	base   int64
	seeded bool
}

// newIssueBuf returns an empty issueBuf. With no log directory there is no
// file to continue, so its numbering is seeded at 0 immediately.
func newIssueBuf(dir string) *issueBuf {
	return &issueBuf{seeded: dir == ""}
}

// Buffer stores the last N log lines per issue identifier.
// When a log directory is configured via SetLogDir, lines are also appended to
// per-issue files on disk so they survive restarts and issue completion.
type Buffer struct {
	issues sync.Map // map[string]*issueBuf

	// dirMu guards logDir only. It is separate from per-issue locks
	// so reading logDir never blocks on a different issue's Add.
	dirMu  sync.RWMutex
	logDir string // empty = no disk persistence

	// epoch is a random value chosen once per Buffer (i.e. once per daemon
	// process) and never changes afterwards. It is embedded in every SSE
	// resume cursor issued by the server package (id: "<epoch>-<seq>") so a
	// client reconnecting after a daemon restart — a brand new Buffer, with
	// its own independent seq numbering starting back at 0 — is told about
	// the discontinuity via a gap instead of silently resuming against
	// sequence numbers that mean something completely different in the new
	// process. See GetSince.
	epoch uint32

	// w is the disk writer: one goroutine, started on first use, that
	// performs every disk operation of this Buffer in queue order (see
	// diskwriter.go). Stopped by Close.
	w diskWriter

	// Test seams, set only by same-package tests before first use; nil in
	// production. All three run on the writer goroutine. diskAppend replaces
	// the disk-append step (appendToDisk); beforeDiskRead runs just before
	// an empty-window read of an identifier's file; beforeSweep runs just
	// before ClearAll's directory sweep.
	diskAppend     func(dir, identifier string, lines []string)
	beforeDiskRead func(identifier string)
	beforeSweep    func()
}

// New creates an empty Buffer. Its disk writer starts on first use; call
// Close to stop it.
func New() *Buffer {
	b := &Buffer{epoch: rand.Uint32()}
	b.w.init()
	return b
}

// Epoch returns this Buffer's process-lifetime identifier (see the epoch
// field doc comment). Exposed so the server package can embed it in SSE
// resume cursors without reaching into Buffer internals.
func (b *Buffer) Epoch() uint32 { return b.epoch }

// SetLogDir configures a directory for per-issue log file persistence.
// The directory is created on first use. Calling this after Add calls is safe.
func (b *Buffer) SetLogDir(dir string) {
	b.dirMu.Lock()
	b.logDir = dir
	b.dirMu.Unlock()
}

func (b *Buffer) getLogDir() string {
	b.dirMu.RLock()
	defer b.dirMu.RUnlock()
	return b.logDir
}

// getOrCreate returns the issueBuf for identifier, creating it if needed.
// It does no disk I/O (R1): a new issueBuf's numbering is seeded from its
// file by the disk writer (see issueBuf.base).
func (b *Buffer) getOrCreate(identifier, dir string) *issueBuf {
	if v, ok := b.issues.Load(identifier); ok {
		return v.(*issueBuf)
	}
	v, _ := b.issues.LoadOrStore(identifier, newIssueBuf(dir))
	return v.(*issueBuf)
}

// awaitSeed returns once ib's numbering is seeded: at once if it already is,
// otherwise after the disk writer has touched ib's file (a barrier op). Only
// readers that report sequence numbers call it (GetSince, via
// snapshotWindow) — never Add. It returns ctx.Err() if ctx ends first; the
// queued seed still runs on the writer.
func (b *Buffer) awaitSeed(ctx context.Context, ib *issueBuf, identifier, dir string) error {
	ib.mu.RLock()
	seeded := ib.seeded
	ib.mu.RUnlock()
	if seeded {
		return nil
	}
	done := make(chan diskResult, 1)
	if dir != "" && b.enqueue(diskOp{kind: opSync, ib: ib, identifier: identifier, dir: dir, done: done}) {
		return awaitDisk(ctx, done, nil)
	}
	// No directory, or the writer is closed: no file will be counted, so
	// fix the numbering at its current, unobserved start.
	ib.mu.Lock()
	ib.seeded = true
	ib.mu.Unlock()
	return nil
}

// awaitDisk waits for a queued control op's result, or for ctx (M0-close
// fix-G: an HTTP read must be able to give up on a stalled disk once its
// client has gone). With r nil the result is discarded. The op itself is not
// withdrawn: the writer still executes it and its buffered done channel
// absorbs the result.
func awaitDisk(ctx context.Context, done <-chan diskResult, r *diskResult) error {
	select {
	case res := <-done:
		if r != nil {
			*r = res
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// logFileKnown reports whether identifier has a log file in dir, or has had
// one touched by this Buffer's writer. The check runs on the writer, so it is
// ordered with the appends, clears and sweeps queued before it and never does
// disk I/O on the caller's goroutine. A closed writer reports false: no file
// would be read anyway.
func (b *Buffer) logFileKnown(ctx context.Context, identifier, dir string) (bool, error) {
	done := make(chan diskResult, 1)
	if !b.enqueue(diskOp{kind: opProbe, identifier: identifier, dir: dir, done: done}) {
		return false, nil
	}
	var r diskResult
	if err := awaitDisk(ctx, done, &r); err != nil {
		return false, err
	}
	return r.exists, nil
}

// maxLineBytes caps the on-disk and in-memory size of a single log line. A
// pathological agent can emit a 100+MB single line (e.g. dumping a binary as
// hex), which then pins maxLinesPerIssue × that-size in RAM and blocks all
// readers behind serialization. 64KiB is generous for human-readable output
// and keeps worst-case memory at ~32MB per issue (was ~500MB).
const maxLineBytes = 64 * 1024

// truncateLine caps line at maxLineBytes, replacing the excess with a
// trailing notice (T-11). Add applies it to every line; the disk tail reader
// applies it to a line longer than the cap found in a file written before the
// cap existed (CORE-036).
func truncateLine(line string) string {
	if len(line) <= maxLineBytes {
		return line
	}
	end, suffix := truncation(len(line))
	return line[:end] + suffix
}

// truncation returns, for a line of n > maxLineBytes bytes, how many leading
// bytes truncateLine keeps and the notice it appends. Shared with the disk
// tail reader, which builds truncated lines without materialising them.
func truncation(n int) (end int, suffix string) {
	suffix = fmt.Sprintf("…[truncated %d bytes]", n-maxLineBytes)
	// G-20 (gaps_280426_2): clamp with max(0, …) so a degenerate config
	// where the suffix exceeds maxLineBytes (e.g. tests using a tiny cap)
	// does not produce a negative slice index. At the production
	// maxLineBytes (64 KiB) the suffix is always tiny; this guard is a
	// belt-and-suspenders for future tunings.
	return min(max(0, maxLineBytes-len(suffix)), n), suffix
}

// Add appends a line for the given identifier, dropping the oldest if over capacity.
// If a log directory is configured, the line is also queued for the disk
// writer, which appends it to the identifier's file in sequence order.
// Lines longer than maxLineBytes are truncated with a trailing notice (T-11).
//
// Add never performs or waits for disk I/O (M0-close fix-F R1): the
// orchestrator event loop calls it — e.g. ReconcileStalls' stall warning,
// logged before it cancels the stalled worker — and must not stall on a slow
// disk. If the disk queue is full the line is kept in memory only, counted
// and reported (see diskwriter.go).
func (b *Buffer) Add(identifier, line string) {
	line = truncateLine(line)
	dir := b.getLogDir()
	ib := b.getOrCreate(identifier, dir)
	// Numbering the line and queueing its append happen under the per-issue
	// lock, so the queue — and therefore the file — is in sequence order.
	ib.mu.Lock()
	ib.n++
	ib.lines = append(ib.lines, line)
	if IsSubagentMarker(line) {
		ib.subagents++
	}
	if len(ib.lines) > maxLinesPerIssue {
		drop := len(ib.lines) - maxLinesPerIssue
		for _, old := range ib.lines[:drop] {
			if IsSubagentMarker(old) {
				ib.subagents--
			}
		}
		ib.lines = ib.lines[drop:]
	}
	queued := dir == "" || b.enqueue(diskOp{kind: opAppend, ib: ib, identifier: identifier, dir: dir, line: line})
	ib.mu.Unlock()
	if !queued {
		why := "disk write queue full"
		if b.w.isClosed() {
			why = "log buffer closed"
		}
		b.w.noteDropped(identifier, why, 1)
	}
}

// snapshotWindow returns a copy of the currently retained window for
// identifier, the sequence number of its last line, and whether that
// sequence number could NOT be reconciled with the in-memory high-water
// mark (see below — this is a narrow, rare case, not "sourced from disk").
// Shared by Get (which only wants the lines) and GetSince (which also needs
// seq and the reconciliation flag to compute gaps).
//
// When the in-memory window is empty, this falls back to the on-disk log
// file. Disk numbering is ABSOLUTE: the disk writer tracks each file's line
// count (fileState; CORE-036 — the read itself is tail-only and does not
// count), and every line ever Added is written to disk exactly once in the
// same order it was assigned its in-memory seq — so, in the common case, a
// disk file's total line count IS the seq Add assigned its last line, not a
// disconnected "fresh 1..N" renumbering. A rotated file (<name>.log.1)
// precedes the current one in that numbering, so a window read across the
// rotation boundary is numbered contiguously. This is what closes CORE-003
// review round G5: an earlier version numbered disk-sourced windows fresh
// from 1 and forced an unconditional gap to compensate, which gapped EVERY
// poll of a completed (Removed), disk-backed issue forever instead of once.
//
//   - An issueBuf exists (Cleared, Removed-and-not-yet-refilled, or simply
//     never appended to) but disk yields nothing (no dir configured, no
//     file, or an empty file — e.g. Clear just deleted it): the issueBuf's
//     own preserved seq is returned. This is what lets a client that was
//     fully caught up before a Clear/Remove see "nothing new yet" instead
//     of a spurious gap, and what stops `next` from transiently rewinding
//     to 0 (see GetSince's doc comment).
//   - Disk contributes lines and its total is >= the in-memory issueBuf's
//     preserved seq (the expected case: every Added line is queued for disk
//     in seq order, so they only diverge when Clear deletes the file out
//     from under a preserved-but-nonzero seq): seq = disk's total
//     line count, and GetSince's ordinary cursor/base arithmetic applies
//     with no special-casing — a stale cursor whose data was trimmed off
//     the file (cursor < base) still gaps, and a cursor that's still
//     within the retained absolute numbering resumes losslessly, exactly
//     like a live in-memory window.
//     "Disk's total" is the writer's per-file offset plus the file's line
//     count: after a Clear deletes the file, the new file's line 1 is the
//     seq after the one preserved at the Clear (M0-close G2 — without the
//     offset, Clear→Add→Remove left a 1-line file "behind" the preserved
//     seq and gapped every poll). See fileState.offset.
//   - Disk's numbering is LESS than the preserved in-memory seq (the file
//     was truncated or replaced externally, a disk append failed, or a line
//     was dropped because the disk queue was full — M0-close fix-F R3): the
//     file's own numbering cannot be trusted to compute a correct base, so
//     irreconcilable=true — but the returned seq is the preserved
//     high-water mark, never the smaller disk number. GetSince gaps a
//     cursor behind that mark exactly once; since `next` never rewinds,
//     the caught-up cursor stays quiet afterwards (M0-close G2: returning
//     the disk number rewound `next` and re-gapped every poll, forever).
//   - Whenever a disk-sourced window is served, its numbering is recorded
//     as the issueBuf's count if larger (creating the issueBuf if needed), so
//     a later Add can never re-issue a sequence number a client may already
//     hold as its cursor (M0-close G1).
//
// Concurrency (M0-close fix-F): a non-empty window is served from memory
// under the per-issue lock and never waits on disk. An empty window is read
// by the disk writer: the read is queued under the per-issue lock, capturing
// the issue's count, so every line numbered up to that count is written
// before the read and every later one after it, and a Clear/ClearAll delete
// is either wholly before or wholly after it. The count reported and the file
// read therefore always describe the same file (M0-close re-check N1, and N5
// for an identifier with no issueBuf yet: that path creates the issueBuf
// first and goes through the same queue, instead of reading outside it and
// raising the numbering afterwards). A line Added after the read was queued
// is simply not in this snapshot; the next read sees it.
//
// needSeq is false for Get, which ignores seq: a non-empty window is then
// served even before the numbering is seeded, so Get never waits on the disk
// writer unless it has to read the file.
//
// An identifier with neither an issueBuf nor a log file reads as empty and
// creates nothing (M0-close fix-G): the HTTP log routes pass a client-chosen
// identifier straight through, so creating state for it let requests grow
// b.issues and the writer's file map without bound. Growth is now bounded by
// identifiers that actually logged. Once an issueBuf exists it is kept (see
// Remove), which is what preserves seq across Remove → re-Add.
//
// Every wait on the disk writer ends early when ctx does; err is then
// ctx.Err() and the other results are zero. A non-empty in-memory window
// never waits, so it is served whatever ctx's state.
func (b *Buffer) snapshotWindow(ctx context.Context, identifier string, needSeq bool) (lines []string, seq int64, irreconcilable bool, err error) {
	dir := b.getLogDir()
	if _, ok := b.issues.Load(identifier); !ok {
		if dir == "" {
			return nil, 0, false, nil
		}
		known, probeErr := b.logFileKnown(ctx, identifier, dir)
		if probeErr != nil || !known {
			return nil, 0, false, probeErr
		}
	}
	ib := b.getOrCreate(identifier, dir)
	if needSeq {
		if err := b.awaitSeed(ctx, ib, identifier, dir); err != nil {
			return nil, 0, false, err
		}
	}
	ib.mu.Lock()
	if len(ib.lines) > 0 {
		out, s := slices.Clone(ib.lines), ib.base+ib.n
		ib.mu.Unlock()
		return out, s, false, nil
	}
	preserved := ib.base + ib.n
	done := make(chan diskResult, 1)
	queued := dir != "" && b.enqueue(diskOp{kind: opRead, ib: ib, identifier: identifier, dir: dir, upTo: ib.n, done: done})
	ib.mu.Unlock()
	if !queued {
		return nil, preserved, false, nil
	}
	var r diskResult
	if err := awaitDisk(ctx, done, &r); err != nil {
		return nil, 0, false, err
	}
	if r.err != nil { // the writer closed before reaching the read
		return nil, preserved, false, nil
	}
	return r.lines, r.seq, r.irreconcilable, nil
}

// Get returns a snapshot of recent lines for the given identifier (newest last).
// If the in-memory buffer is empty and a log directory is configured, falls back
// to reading the on-disk log file. It waits for the disk writer as long as it
// takes; HTTP handlers use GetContext.
func (b *Buffer) Get(identifier string) []string {
	return b.GetContext(context.Background(), identifier)
}

// GetContext is Get, except that a wait on the disk writer (an empty
// in-memory window) ends when ctx does, returning nil (M0-close fix-G).
func (b *Buffer) GetContext(ctx context.Context, identifier string) []string {
	lines, _, _, _ := b.snapshotWindow(ctx, identifier, false)
	return lines
}

// GetSince returns the log lines appended for identifier strictly after
// cursor, along with the Buffer's current epoch, the next cursor to resume
// from, and whether the returned lines are a *gap* replay of the current
// window rather than a contiguous continuation. This is the primitive
// behind the per-issue log-stream SSE endpoint's resume-by-sequence contract
// (CORE-003); see internal/server's handleIssueLogStream for how the SSE
// "id:" / "event: gap" framing is built from these values.
//
// Sequence contract:
//   - Every line appended for identifier gets a monotonically increasing
//     seq (issueBuf.base + issueBuf.n) that never resets for the life of this Buffer —
//     including across Remove (see Remove's doc comment; a previous
//     "renumbers from 0" design let a retried/reviewer run's fresh
//     numbering silently alias a stale cursor onto the WRONG run's lines,
//     found and fixed in CORE-003 review round G1). "next" is always the
//     seq of the newest line in the current window (0 when the window is
//     empty and nothing has ever been appended).
//   - hasCursor == false means "first connect" (no prior Last-Event-ID):
//     always returns the current window with gap == false, seeding the
//     caller's next cursor. cursor/epoch are ignored in this case.
//   - Otherwise, if snapshotWindow reports the disk and in-memory numbering
//     as irreconcilable (see its doc comment — a narrow, rare case), a
//     cursor that is not exactly at the high-water mark `seq` gets a gap;
//     a cursor AT the mark is quiet. Because `seq` is the preserved mark
//     (never the smaller disk number), the handler's re-armed cursor lands
//     on it and the discontinuity gaps exactly once (M0-close G2). This is NOT the same as "sourced from disk": an
//     earlier version (CORE-003 review round G1) forced a gap for every
//     disk-sourced window, which — because a completed (Removed) issue
//     with a log directory configured (production's actual configuration)
//     sources from disk on EVERY poll — gapped forever instead of once,
//     found and fixed in review round G5. Disk numbering is now absolute
//     (see snapshotWindow), so a disk-sourced window participates in the
//     ordinary cursor/base check below exactly like a live one.
//   - Otherwise, a cursor whose epoch does not match Epoch() is always a
//     gap — one "event: gap" frame followed by a full replay of the
//     current window — regardless of the numeric cursor value: it comes
//     from a different process (daemon restart rebuilt the Buffer) or an
//     unparseable/legacy Last-Event-ID, either way the numbering means
//     nothing here.
//   - With a matching epoch, let n = len(window) and base = seq - n (the
//     seq of the line immediately before the window's first entry).
//     cursor > seq (stale/future numbering) or cursor < base (lines were
//     evicted from the window — trimmed from memory OR from disk — since
//     the client's last delivered position) is a gap; otherwise the client
//     is caught up and window[cursor-base:] is returned with gap == false.
//     This applies identically whether the window came from memory or
//     disk, which is what makes repeated polling of a dormant, disk-backed
//     window go quiet after catching up once, instead of gapping forever.
//   - Clear/ClearAll/Remove empty the retained window but do NOT reset
//     seq (see each's doc comment), so a cursor that had fallen behind
//     before one of them resolves to a gap (cursor < base once new lines
//     start the window over) while a cursor that was fully caught up does
//     not spuriously gap — and `next` never rewinds, so the same
//     discontinuity is never reported twice.
//   - Rotating the on-disk file (CORE-036, see disktail.go) does not
//     renumber: the rotated file's lines keep their numbers and the new
//     file continues after them. Lines that rotate out of the retained
//     files, like lines trimmed from the window, surface through the same
//     cursor < base gap — never a silent cursor reset.
//
// GetSince waits for the disk writer as long as it takes; HTTP handlers use
// GetSinceContext.
func (b *Buffer) GetSince(identifier string, epoch uint32, cursor int64, hasCursor bool) (lines []string, currentEpoch uint32, next int64, gap bool) {
	return b.GetSinceContext(context.Background(), identifier, epoch, cursor, hasCursor)
}

// GetSinceContext is GetSince, except that a wait on the disk writer ends
// when ctx does (M0-close fix-G). It then returns no lines, the current
// epoch, the caller's own cursor (0 without one) and no gap — "nothing new",
// never a rewound or advanced position.
func (b *Buffer) GetSinceContext(ctx context.Context, identifier string, epoch uint32, cursor int64, hasCursor bool) (lines []string, currentEpoch uint32, next int64, gap bool) {
	currentEpoch = b.epoch
	win, seq, irreconcilable, err := b.snapshotWindow(ctx, identifier, true)
	if err != nil {
		if !hasCursor {
			cursor = 0
		}
		return nil, currentEpoch, cursor, false
	}

	if !hasCursor {
		return win, currentEpoch, seq, false
	}
	if epoch != currentEpoch {
		return win, currentEpoch, seq, true
	}
	if irreconcilable {
		if cursor == seq {
			// Caught up to the high-water mark: the discontinuity was
			// already reported (or nothing was missed). Quiet.
			return nil, currentEpoch, seq, false
		}
		return win, currentEpoch, seq, true
	}

	n := int64(len(win))
	base := seq - n
	if cursor > seq || cursor < base {
		return win, currentEpoch, seq, true
	}
	return win[cursor-base:], currentEpoch, seq, false
}

// Identifiers returns all identifiers that have log data — either in-memory or
// on disk. The returned slice is unsorted.
func (b *Buffer) Identifiers() []string {
	dir := b.getLogDir()
	seen := make(map[string]struct{})
	var ids []string

	b.issues.Range(func(key, v any) bool {
		id := key.(string)
		ib := v.(*issueBuf)
		ib.mu.RLock()
		hasLines := len(ib.lines) > 0
		ib.mu.RUnlock()
		if !hasLines {
			// Clear/ClearAll keep the issueBuf alive (to preserve seq, see
			// GetSince) but empty its lines. Without this guard a cleared
			// identifier with no disk file would linger in the logs
			// sidebar forever instead of disappearing as it did before
			// CORE-003 (when Clear deleted the map entry outright).
			return true
		}
		ids = append(ids, id)
		seen[id] = struct{}{}
		return true
	})

	if dir == "" {
		return ids
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ids
	}
	for _, e := range entries {
		// A rotated file (<id>.log.1) with no current file still names an
		// identifier with log data (CORE-036).
		name := strings.TrimSuffix(e.Name(), rotatedSuffix)
		if e.IsDir() || !strings.HasSuffix(name, ".log") {
			continue
		}
		id := strings.TrimSuffix(name, ".log")
		// reverse the filename sanitisation applied in issuePath
		id = strings.NewReplacer("_", ":").Replace(id)
		if _, exists := seen[id]; !exists {
			ids = append(ids, id)
			seen[id] = struct{}{}
		}
	}
	return ids
}

// Remove empties the in-memory window for identifier but — like Clear —
// preserves its sequence counter; the on-disk log file is left alone so
// logs remain viewable after an issue completes (Get/GetSince fall back to
// it while the window stays empty).
//
// This used to delete the issueBuf outright, so a subsequent Add (a retry,
// a reviewer run, or a PR-continuation run dispatched under the same
// identifier) renumbered from seq 1. internal/orchestrator/worker.go calls
// Remove on every successful worker completion, so that reset was reachable
// on the hot path, not just an edge case: a viewer that had caught up to
// "<epoch>-300" on run 1, then reconnected after run 2 had appended 400
// fresh lines (seq 1..400 under the old scheme), would fail the epoch check
// (same process, so it passed) and the cursor > seq / cursor < base checks
// (300 was in-range for the new 1..400 numbering purely by coincidence) —
// silently serving run 2's lines 301..400 while run 2's lines 1..300 were
// never delivered and no gap was ever signaled. Found and fixed in CORE-003
// review round G1. Preserving seq here means run 2's lines are numbered
// 301..700 (continuing from run 1's high-water mark), so a stale cursor can
// only ever land on genuinely-missing data (handled by GetSince's normal
// cursor < base gap check) or genuinely-present data — never both at once.
func (b *Buffer) Remove(identifier string) {
	if v, ok := b.issues.Load(identifier); ok {
		ib := v.(*issueBuf)
		ib.mu.Lock()
		ib.lines = nil
		ib.subagents = 0
		ib.mu.Unlock()
	}
}

// ClearAll empties every in-memory window and deletes all on-disk log files
// in logDir. Like Clear, it preserves each issueBuf's sequence counter
// (only the retained lines are dropped) so a stale streaming cursor from
// before a ClearAll resolves to a gap rather than a silent replay.
//
// Every deletion is a disk-writer op, ordered against the appends (M0-close
// N9: the directory sweep used to delete files behind the writer's back, so
// an Add racing it kept its line in memory but lost it on disk). The sweep is
// queued first and deletes only files the writer has not touched — those of
// identifiers this Buffer has no queued or written line for. Each known
// identifier is then cleared exactly as Clear does (clearIssue, M0-close G2).
func (b *Buffer) ClearAll() error {
	dir := b.getLogDir()
	var first error
	var sweep chan diskResult
	if dir != "" {
		sweep = make(chan diskResult, 1)
		if !b.enqueue(diskOp{kind: opSweep, dir: dir, done: sweep}) {
			sweep, first = nil, ErrClosed
		}
	}
	b.issues.Range(func(key, v any) bool {
		if err := b.clearIssue(v.(*issueBuf), key.(string), dir); err != nil && first == nil {
			first = err
		}
		return true
	})
	if sweep != nil {
		if r := <-sweep; r.err != nil && first == nil {
			first = r.err
		}
	}
	return first
}

// Clear empties the in-memory window and deletes the on-disk log file for
// identifier. The issueBuf's sequence counter is deliberately preserved (not
// deleted with the map entry) so a streaming client whose Last-Event-ID
// predates the Clear is told about the discontinuity via GetSince's gap
// contract instead of silently resuming as if nothing happened, or panicking
// on a now-invalid slice index.
//
// Preserving the counter only pays off if snapshotWindow actually returns it
// once the window is empty: production always configures a log directory
// (cmd/itervox/main.go's rotatingFile.Filename is never empty), and Clear
// deletes that file, so the naive "window empty -> fall back to disk"
// fallback used to find nothing and report seq 0 instead of the preserved
// value — rewinding `next` and gapping a second time for the same
// discontinuity. Fixed in CORE-003 review round G2; see snapshotWindow's
// doc comment for the corrected fallback order.
//
// The delete is a disk-writer op queued behind every line already numbered,
// and it re-bases the file's numbering to the count at the Clear, so the next
// line is numbered as line 1 of the new file rather than read back as "disk
// behind the high-water mark" (M0-close G2). A failed delete leaves the file,
// and its numbering, intact.
//
// An identifier with no issueBuf is first probed on the disk writer (ordered
// with every queued append, clear and sweep, like the read path's probe). With
// no file either, Clear is a no-op and creates nothing (CORE-152: the
// authenticated clear-logs route passes a client-chosen identifier, and
// creating an issueBuf for it let requests grow b.issues and the writer's file
// map without bound). With a file — one left by an earlier process — the
// identifier gets an issueBuf so its delete is ordered like any other
// (M0-close N5).
func (b *Buffer) Clear(identifier string) error {
	dir := b.getLogDir()
	if _, ok := b.issues.Load(identifier); !ok {
		if dir == "" {
			return nil
		}
		if b.w.isClosed() {
			return ErrClosed
		}
		known, err := b.logFileKnown(context.Background(), identifier, dir)
		if err != nil {
			return err
		}
		if !known {
			if b.w.isClosed() {
				return ErrClosed
			}
			return nil
		}
	}
	return b.clearIssue(b.getOrCreate(identifier, dir), identifier, dir)
}

// clearIssue empties ib's window and, with a log directory, queues the
// delete-and-rebase of its file and waits for it.
func (b *Buffer) clearIssue(ib *issueBuf, identifier, dir string) error {
	ib.mu.Lock()
	ib.lines = nil
	ib.subagents = 0
	if dir == "" {
		ib.mu.Unlock()
		return nil
	}
	done := make(chan diskResult, 1)
	queued := b.enqueue(diskOp{kind: opClear, ib: ib, identifier: identifier, dir: dir, upTo: ib.n, done: done})
	ib.mu.Unlock()
	if !queued {
		return ErrClosed
	}
	return (<-done).err
}

// removeLogFile deletes p, treating "already gone" as success.
func removeLogFile(p string) error {
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// --- internal helpers ---

// issuePath returns the on-disk log file path for identifier within dir.
func issuePath(dir, identifier string) string {
	safe := strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(identifier)
	return filepath.Join(dir, safe+".log")
}
