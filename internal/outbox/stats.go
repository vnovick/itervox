package outbox

import "time"

// Stats summarises the queued tracker writes for the sync status (#117): the
// dashboard, /api/v1/state, the HEARTBEAT and `itervox doctor`.
type Stats struct {
	// Pending is every queued write.
	Pending int
	// Urgent is the queued writes marked Entry.Urgent.
	Urgent int
	// Batched is the writes waiting for their issue's push interval: never
	// handed to the flusher and not due yet (coalesce.go's "waiting").
	Batched int
	// Retrying is the writes that failed at least once and are backing off;
	// Degraded is the subset past the degraded threshold (Entry.Degraded).
	Retrying int
	Degraded int
	// RateLimited is the writes deferred to a tracker rate-limit reset that
	// has not passed yet.
	RateLimited int
	// OldestEnqueuedAt is the oldest queued write's enqueue time; zero when
	// nothing is queued.
	OldestEnqueuedAt time.Time
	// NextPushAt is when the flusher next has a write to push: the earliest
	// NextAttemptAt among the issues' queue heads (only a head is ever due,
	// see Due). Zero when nothing is queued; at or before now means a write
	// is due and goes out on the flusher's next round.
	NextPushAt time.Time
}

// Stats returns the queue summary at now.
func (o *Outbox) Stats(now time.Time) Stats {
	o.mu.Lock()
	defer o.mu.Unlock()
	var st Stats
	seenHead := make(map[string]bool, len(o.entries))
	for _, e := range o.entries {
		st.Pending++
		if e.Urgent {
			st.Urgent++
		}
		if waiting(e, now) {
			st.Batched++
		}
		if e.Attempts > 0 {
			st.Retrying++
		}
		if e.Degraded() {
			st.Degraded++
		}
		if e.RateLimitedUntil.After(now) {
			st.RateLimited++
		}
		if st.OldestEnqueuedAt.IsZero() || e.EnqueuedAt.Before(st.OldestEnqueuedAt) {
			st.OldestEnqueuedAt = e.EnqueuedAt
		}
		if seenHead[e.IssueID] {
			continue
		}
		seenHead[e.IssueID] = true
		if st.NextPushAt.IsZero() || e.NextAttemptAt.Before(st.NextPushAt) {
			st.NextPushAt = e.NextAttemptAt
		}
	}
	return st
}
