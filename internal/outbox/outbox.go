// Package outbox implements a write-ahead outbox for tracker mutations
// (state transitions, comments). Entries are enqueued durably, flushed
// asynchronously by an independent worker, and never silently discarded —
// a failed flush backs off and retries; only reconciliation or an explicit
// operator action removes an entry that hasn't flushed yet.
//
// Outbox is self-contained and thread-safe (guarded by its own mutex, not
// orchestrator.State — callers on the orchestrator's event-loop goroutine
// must only read it through these accessors, same as any other worker-owned
// store). It depends on nothing but internal/atomicfs for durability.
//
// See docs/superpowers/specs/2026-08-06-write-ahead-outbox-design.md for the
// full design; this file implements Phase 1 ("internal/outbox core") only —
// the flusher, orchestrator WriteSink integration, and overlay/reconciliation
// logic land in later tasks.
package outbox

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// EntryKind distinguishes the two mutation shapes an entry can carry.
type EntryKind string

const (
	// KindUpdateState is a tracker state transition; TargetState is set.
	KindUpdateState EntryKind = "update_state"
	// KindCreateComment is a tracker comment post; Body is set.
	KindCreateComment EntryKind = "create_comment"
)

// outboxDegradedAttempts is the Attempts threshold at which an entry is
// considered degraded (operator-visible error badge). Retries continue past
// this point — there is no terminal give-up.
const outboxDegradedAttempts = 5

const (
	backoffBase = 10 * time.Second
	backoffCap  = 5 * time.Minute
)

// Entry is one durable write-ahead-outbox record. All entries returned by
// Outbox methods (Due, PendingFor, Snapshot) are copies — Entry has no
// pointer or slice fields, so a plain value copy is a real copy; mutating a
// returned Entry never affects Outbox's internal state.
type Entry struct {
	ID         string    `json:"id"`
	Kind       EntryKind `json:"kind"`
	IssueID    string    `json:"issue_id"`
	Identifier string    `json:"identifier"`

	// TargetState is set for KindUpdateState entries.
	TargetState string `json:"target_state,omitempty"`
	// Body is set for KindCreateComment entries.
	Body string `json:"body,omitempty"`

	EnqueuedAt    time.Time `json:"enqueued_at"`
	Attempts      int       `json:"attempts"`
	LastError     string    `json:"last_error,omitempty"`
	NextAttemptAt time.Time `json:"next_attempt_at"`
}

// Degraded reports whether this entry has failed enough consecutive times
// to warrant an operator-visible error badge. It is derived from Attempts,
// not a stored field — retries continue regardless of Degraded.
func (e Entry) Degraded() bool {
	return e.Attempts >= outboxDegradedAttempts
}

// Outbox is a durable, per-issue-FIFO queue of pending tracker mutations.
type Outbox struct {
	mu      sync.Mutex
	path    string
	entries []Entry // enqueue order preserved; this IS the global order.
	now     func() time.Time
}

// New loads durable state from path and returns a ready Outbox. A missing
// file is not an error — the outbox starts empty. A file that exists but
// fails to read or fails to parse is logged at Warn and the outbox also
// starts empty (repo posture: a bad on-disk file never blocks daemon
// startup — see internal/orchestrator/deps_override.go for the same
// pattern).
func New(path string) (*Outbox, error) {
	o := &Outbox{
		path: path,
		now:  time.Now,
	}
	if path == "" {
		return o, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("outbox: failed to read outbox file, starting empty", "path", path, "error", err)
		}
		return o, nil
	}

	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		slog.Warn("outbox: failed to parse outbox file, starting empty", "path", path, "error", err)
		return o, nil
	}
	o.entries = entries
	return o, nil
}

// Enqueue appends a new entry, assigning its ID/EnqueuedAt/NextAttemptAt
// (immediately due) and resetting any caller-supplied Attempts/LastError —
// every enqueued entry starts fresh. It persists atomically before
// returning.
//
// Enqueue is atomic: the entry exists (durably, and visible to Due/
// Snapshot/PendingFor) if and only if Enqueue returns a nil error. A
// validation failure never stores anything. A persist failure rolls back
// the in-memory append before returning the error, so a caller that sees
// an error from Enqueue can rely on the entry NOT being present — it will
// not turn up in a later Due() call or survive a restart.
func (o *Outbox) Enqueue(e Entry) error {
	if err := validateEntry(e); err != nil {
		return err
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	now := o.now()
	e.ID = newEntryID(now)
	e.EnqueuedAt = now
	e.Attempts = 0
	e.LastError = ""
	e.NextAttemptAt = now

	o.entries = append(o.entries, e)
	if err := o.persistLocked(); err != nil {
		o.entries = o.entries[:len(o.entries)-1] // roll back: atomic contract
		return err
	}
	return nil
}

// validateEntry rejects entries that would otherwise persist forever under
// the no-terminal-give-up retry policy without ever being flushable: an
// unknown Kind, a missing IssueID/Identifier, or a kind-specific payload
// (TargetState for update_state, Body for create_comment) left empty.
func validateEntry(e Entry) error {
	switch e.Kind {
	case KindUpdateState:
		if e.TargetState == "" {
			return fmt.Errorf("outbox: enqueue: %s entry requires non-empty TargetState", KindUpdateState)
		}
	case KindCreateComment:
		if e.Body == "" {
			return fmt.Errorf("outbox: enqueue: %s entry requires non-empty Body", KindCreateComment)
		}
	default:
		return fmt.Errorf("outbox: enqueue: unknown entry kind %q", e.Kind)
	}
	if e.IssueID == "" {
		return fmt.Errorf("outbox: enqueue: entry requires non-empty IssueID")
	}
	if e.Identifier == "" {
		return fmt.Errorf("outbox: enqueue: entry requires non-empty Identifier")
	}
	return nil
}

// Due returns the entries eligible to flush right now: for each distinct
// IssueID, only the head of that issue's FIFO (the earliest still-pending
// entry in enqueue order) is ever considered, and only when its
// NextAttemptAt is not after now. A later entry for the same issue is never
// due while an earlier one is pending, even if its own NextAttemptAt has
// elapsed.
func (o *Outbox) Due(now time.Time) []Entry {
	o.mu.Lock()
	defer o.mu.Unlock()

	seenHead := make(map[string]bool, len(o.entries))
	due := make([]Entry, 0)
	for _, e := range o.entries {
		if seenHead[e.IssueID] {
			continue
		}
		seenHead[e.IssueID] = true
		if !e.NextAttemptAt.After(now) {
			due = append(due, e)
		}
	}
	return due
}

// MarkFlushed removes the entry with the given id and persists. Unknown ids
// are a no-op.
func (o *Outbox) MarkFlushed(id string) {
	o.mu.Lock()
	defer o.mu.Unlock()

	idx := o.indexOfLocked(id)
	if idx < 0 {
		return
	}
	o.entries = append(o.entries[:idx], o.entries[idx+1:]...)
	if err := o.persistLocked(); err != nil {
		slog.Warn("outbox: failed to persist after flush", "id", id, "error", err)
	}
}

// MarkFailed records a failed flush attempt: increments Attempts, sets
// LastError, and schedules NextAttemptAt via exponential backoff
// (10s * 2^(attempts-1), capped at 5 minutes), then persists. There is no
// terminal give-up — the entry keeps retrying indefinitely; Entry.Degraded
// becomes true once Attempts reaches 5, but MarkFailed never removes the
// entry. Unknown ids are a no-op.
func (o *Outbox) MarkFailed(id string, err error, now time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()

	idx := o.indexOfLocked(id)
	if idx < 0 {
		return
	}
	e := &o.entries[idx]
	e.Attempts++
	e.NextAttemptAt = now.Add(backoffFor(e.Attempts))
	if err != nil {
		e.LastError = err.Error()
	}
	if perr := o.persistLocked(); perr != nil {
		slog.Warn("outbox: failed to persist after mark-failed", "id", id, "error", perr)
	}
}

// Drop discards the entry with the given id (reconciliation or operator
// action), persists, and logs at INFO with reason. Drop is idempotent: an
// unknown id never errors — it is logged and otherwise ignored.
func (o *Outbox) Drop(id string, reason string) {
	o.mu.Lock()
	idx := o.indexOfLocked(id)
	if idx < 0 {
		o.mu.Unlock()
		slog.Info("outbox: drop no-op, entry not found", "id", id, "reason", reason)
		return
	}
	dropped := o.entries[idx]
	o.entries = append(o.entries[:idx], o.entries[idx+1:]...)
	perr := o.persistLocked()
	o.mu.Unlock()

	if perr != nil {
		slog.Warn("outbox: failed to persist after drop", "id", id, "error", perr)
	}
	slog.Info("outbox: dropped entry", "id", id, "issue_id", dropped.IssueID, "kind", dropped.Kind, "reason", reason)
}

// RetryNow makes the entry with the given id immediately due by setting its
// NextAttemptAt to now, then persists. It reports whether the id existed;
// it only operates on an existing entry.
func (o *Outbox) RetryNow(id string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	idx := o.indexOfLocked(id)
	if idx < 0 {
		return false
	}
	o.entries[idx].NextAttemptAt = o.now()
	if err := o.persistLocked(); err != nil {
		slog.Warn("outbox: failed to persist after retry-now", "id", id, "error", err)
	}
	return true
}

// PendingFor returns copies of all entries for issueID, in FIFO (enqueue)
// order — used for the overlay read path.
func (o *Outbox) PendingFor(issueID string) []Entry {
	o.mu.Lock()
	defer o.mu.Unlock()

	out := make([]Entry, 0)
	for _, e := range o.entries {
		if e.IssueID == issueID {
			out = append(out, e)
		}
	}
	return out
}

// Snapshot returns copies of every entry in global enqueue order (stable) —
// used for dashboard rows.
func (o *Outbox) Snapshot() []Entry {
	o.mu.Lock()
	defer o.mu.Unlock()

	out := make([]Entry, len(o.entries))
	copy(out, o.entries)
	return out
}

// indexOfLocked returns the index of the entry with the given id, or -1.
// Callers must hold o.mu.
func (o *Outbox) indexOfLocked(id string) int {
	for i := range o.entries {
		if o.entries[i].ID == id {
			return i
		}
	}
	return -1
}

// persistLocked marshals and atomically writes the current entries to
// o.path. Callers must hold o.mu. A path-less Outbox (New("")) is
// in-memory only and never persists.
func (o *Outbox) persistLocked() error {
	if o.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(o.entries, "", "  ")
	if err != nil {
		return fmt.Errorf("outbox: marshal entries: %w", err)
	}
	if err := atomicfs.WriteFile(o.path, data, 0o600); err != nil {
		return fmt.Errorf("outbox: persist: %w", err)
	}
	return nil
}

// idFallbackCounter backs newEntryID's crypto/rand failure path — see there.
var idFallbackCounter uint64

// newEntryID builds a unique, roughly-sortable entry ID: a nanosecond
// timestamp prefix (for readable ordering) plus a crypto/rand suffix (so
// same-nanosecond enqueues never collide).
func newEntryID(now time.Time) string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand.Read does not fail on supported platforms, but a
		// long-running daemon must never panic on an ID-generation hiccup.
		// A fixed fallback suffix would risk a real collision (two Enqueue
		// calls landing in the same nanosecond while rand keeps failing);
		// a process-wide atomic counter instead guarantees uniqueness
		// within this process regardless of how many consecutive rand
		// failures occur.
		n := atomic.AddUint64(&idFallbackCounter, 1)
		return fmt.Sprintf("%d-f%011x", now.UnixNano(), n)
	}
	return fmt.Sprintf("%d-%s", now.UnixNano(), hex.EncodeToString(buf))
}

// backoffFor computes the retry delay for the given (post-increment)
// attempts count: 10s * 2^(attempts-1), capped at 5 minutes.
func backoffFor(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	shift := attempts - 1
	if shift > 20 {
		// 10s * 2^20 already dwarfs the 5m cap; avoid any risk of
		// overflowing the shift/duration arithmetic for huge attempt counts.
		return backoffCap
	}
	d := backoffBase * time.Duration(uint64(1)<<uint(shift))
	if d <= 0 || d > backoffCap {
		return backoffCap
	}
	return d
}
