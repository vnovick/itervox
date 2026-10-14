package outbox

import (
	"log/slog"
	"time"
)

// Coalesced push (#115).
//
// A non-urgent write is not due the moment it is enqueued: it joins its
// issue's batch and becomes due when the batch's push interval runs out
// (tracker.push_interval_ms). While it waits:
//
//   - a later state move for the same issue replaces it: only the last state
//     move is sent, so three moves inside one interval cost one tracker write
//     and the tracker never shows the intermediate states;
//   - comments are never merged or reordered among themselves;
//   - the batch's deadline is set by its first write, so a stream of writes
//     cannot postpone the push forever.
//
// An urgent write (Entry.Urgent: an input-required question or reply, a
// failure notice, an operator comment — anything a person waits on) is due at
// once, and makes every earlier waiting write for its issue due at once too:
// the outbox delivers an issue's writes strictly in order, so leaving them
// waiting would hold the urgent write back behind them. Enqueueing one also
// wakes the flusher, so it does not wait for its next tick.
//
// Only writes that have never been handed to the flusher are batched or
// replaced. "Waiting" means Attempts and RateLimitedAttempts are zero and
// NextAttemptAt is still in the future; Due never returns such an entry, so
// none can be in flight. A write that was attempted (even one that failed and
// is backing off) keeps its retry semantics untouched: it may already have
// landed, and replacing it would let reconciliation misread its effect.
//
// Durability is the outbox's own: a batched write is in outbox.json from the
// moment Enqueue returns, with its NextAttemptAt, so a crash before the push
// loses nothing and the restarted flusher pushes it when it is due.

// SetPushInterval sets how long a non-urgent write waits for its issue's
// batch before it is due. d <= 0 turns batching off: every write is due at
// once and nothing is coalesced, the outbox's behaviour before #115.
func (o *Outbox) SetPushInterval(d time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pushInterval = max(d, 0)
}

// Wake is signalled when an urgent write is enqueued. The flusher selects on
// it next to its ticker and pushes at once. The channel is buffered (1) and
// never closed; a signal sent while one is pending is merged into it.
func (o *Outbox) Wake() <-chan struct{} {
	return o.wake
}

// signalWake signals Wake without blocking.
func (o *Outbox) signalWake() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// waiting reports whether e has never been handed to the flusher and is not
// due yet at now, so it may still be replaced or rescheduled.
func waiting(e Entry, now time.Time) bool {
	return e.Attempts == 0 && e.RateLimitedAttempts == 0 && e.NextAttemptAt.After(now)
}

// batchLocked schedules e, which is about to be appended, and applies
// coalescing to the entries already queued. Caller holds o.mu.
func (o *Outbox) batchLocked(e *Entry, now time.Time) {
	due := now
	if o.pushInterval > 0 && !e.Urgent {
		due = now.Add(o.pushInterval)
		// Join the issue's open batch: its deadline is its first write's.
		for _, x := range o.entries {
			if x.IssueID == e.IssueID && waiting(x, now) && x.NextAttemptAt.Before(due) {
				due = x.NextAttemptAt
			}
		}
	}

	if e.Kind == KindUpdateState {
		o.replaceWaitingStateMovesLocked(e, now)
	}

	if e.Urgent {
		// Everything ahead of an urgent write in its issue's queue goes now.
		for i := range o.entries {
			if o.entries[i].IssueID == e.IssueID && waiting(o.entries[i], now) {
				o.entries[i].NextAttemptAt = now
			}
		}
	}
	e.NextAttemptAt = due
}

// replaceWaitingStateMovesLocked removes the issue's waiting state moves,
// which e supersedes: only the last state move is sent. e takes the earliest
// removed move's FromState, because that is the state the tracker still
// shows; the moves in between were never sent, so the tracker never saw
// them, and reconciliation must compare against what it does show. Caller
// holds o.mu.
func (o *Outbox) replaceWaitingStateMovesLocked(e *Entry, now time.Time) {
	kept := o.entries[:0:0]
	replaced := 0
	for _, x := range o.entries {
		if x.IssueID != e.IssueID || x.Kind != KindUpdateState || !waiting(x, now) {
			kept = append(kept, x)
			continue
		}
		if replaced == 0 {
			e.FromState = x.FromState
		}
		replaced += 1 + x.Coalesced
		slog.Debug("outbox: state move replaced by a later one before it was pushed",
			"identifier", x.Identifier, "replaced_target", x.TargetState, "new_target", e.TargetState)
	}
	if replaced == 0 {
		return
	}
	e.Coalesced = replaced
	o.entries = kept
}

// PendingStates returns, for every issue with a queued state move, the
// target of its last one: the state the issue is about to be in. The local
// tracker store lays it over its copy (#115), so the board and every list
// read see the move before it is pushed.
func (o *Outbox) PendingStates() map[string]string {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make(map[string]string)
	for _, e := range o.entries {
		if e.Kind == KindUpdateState {
			out[e.IssueID] = e.TargetState
		}
	}
	return out
}
