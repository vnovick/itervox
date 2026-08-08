package orchestrator

import (
	"context"
	"log/slog"
	"time"

	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/tracker"
)

// maxTransitionAttempts is how many times directWriteSink retries a state
// transition before giving up: 1 immediate attempt + 3 retries with
// 2s/4s/8s backoff (issue #42). Kept alongside directWriteSink since it is
// the only place the retry loop runs — the outbox path has no retry loop
// (the flusher, a later task, owns retries there).
const maxTransitionAttempts = 4

// transitionRetryBaseDelay is the base unit for directWriteSink's
// UpdateIssueState backoff: attempt i (1-indexed, i>1) waits
// 2^(i-2)*transitionRetryBaseDelay — 2s, 4s, 8s at the production default.
// A package-level var (not a const) so whitebox tests can shrink it instead
// of sleeping through several real seconds of backoff to prove the retry
// loop's attempt count.
var transitionRetryBaseDelay = 2 * time.Second

// WriteSink is the orchestrator's write path for tracker state transitions
// and outcome comments (see docs/superpowers/specs/2026-08-06-write-ahead-outbox-design.md,
// "Enqueue integration"). Every call site that used to call
// o.tracker.UpdateIssueState / o.tracker.CreateComment directly for a
// completion-state transition, a failed-state transition, or a worker-exit
// outcome comment now goes through o.writeSink() instead, so the same call
// site works unmodified whether the daemon is running the pre-outbox
// direct sink or the outbox-backed sink (cfg.Tracker.Outbox).
//
// identifier is the tracker's human-readable issue key (e.g. "ENG-42"),
// distinct from issueID (the tracker's internal/opaque ID). Both are
// required by outboxWriteSink — outbox.Entry validates both non-empty.
// directWriteSink ignores identifier (tracker.Tracker's methods only take
// issueID) but still requires the caller to plumb it through so call sites
// are interchangeable between the two sinks without an if/else.
type WriteSink interface {
	// UpdateIssueState transitions issueID (identifier is its human-readable
	// key) to state.
	UpdateIssueState(ctx context.Context, issueID, identifier, state string) error
	// CreateComment posts body on issueID (identifier is its human-readable
	// key).
	CreateComment(ctx context.Context, issueID, identifier, body string) error
}

// directWriteSink is the pre-outbox write path: synchronous tracker calls.
// It is the default sink (see New) so every existing orchestrator test
// keeps old behavior unless it explicitly opts into an outbox sink via
// SetWriteSink.
type directWriteSink struct {
	tracker tracker.Tracker
}

// NewDirectWriteSink wraps tr as a WriteSink with old (pre-outbox) behavior.
// New() uses this as the default sink; cmd/itervox (Task 3) also uses it
// explicitly when cfg.Tracker.Outbox is false (the kill switch).
func NewDirectWriteSink(tr tracker.Tracker) WriteSink {
	return &directWriteSink{tracker: tr}
}

// UpdateIssueState transitions issueID to state, retrying up to
// maxTransitionAttempts times with 2s/4s/8s backoff on failure. This is the
// exact retry loop that used to live inline in worker.go's completion-state
// transition (moved here verbatim, including its context handling): each
// attempt's tracker call runs against a fresh context.Background()-derived
// context bounded by postRunTimeout, so a cancelled caller ctx (user
// paused/terminated mid-retry) does not abort an in-flight API call — only
// between attempts does ctx cancellation stop the loop early, via two
// checks: an explicit ctx.Err() check at the top of every iteration
// (covers an already-expired/cancelled ctx before any attempt, including
// the first — there is no backoff wait before attempt 1 to catch that
// case) and the backoff select's ctx.Done() case (covers cancellation that
// happens while waiting out the 2s/4s/8s backoff between attempts 2-4).
// Together these bound the WHOLE loop by whatever the caller's ctx allows,
// regardless of caller — fix-round §1 (task-2-report.md) found a caller
// passing context.Background() (never cancels) had made this loop's real
// bound ~74s (60s postRunTimeout + 14s of backoff) even though the
// mechanism here was already sound; the caller was fixed to pass a bounded
// ctx, and this explicit check makes the sink itself honest for ANY
// caller, bounded or not, rather than relying solely on the select.
// identifier is accepted (WriteSink interface parity with outboxWriteSink)
// but unused — tracker.Tracker.UpdateIssueState takes no identifier.
func (s *directWriteSink) UpdateIssueState(ctx context.Context, issueID, identifier, state string) error {
	callCtx, cancel := context.WithTimeout(context.Background(), postRunTimeout)
	defer cancel()

	var lastErr error
	for i := range maxTransitionAttempts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if i > 0 {
			delay := time.Duration(1<<uint(i-1)) * transitionRetryBaseDelay // 2s, 4s, 8s at the default
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if lastErr = s.tracker.UpdateIssueState(callCtx, issueID, state); lastErr == nil {
			return nil
		}
		slog.Warn("worker: completion state transition attempt failed, retrying",
			"issue_id", issueID, "issue_identifier", identifier,
			"target_state", state, "attempt", i+1, "error", lastErr)
	}
	return lastErr
}

// CreateComment posts body on issueID. No retry — matches every pre-outbox
// comment call site's behavior (none of them retried). identifier is
// accepted but unused, same reason as UpdateIssueState.
func (s *directWriteSink) CreateComment(ctx context.Context, issueID, _ string, body string) error {
	_, err := s.tracker.CreateComment(ctx, issueID, body)
	return err
}

// outboxWriteSink routes writes through a durable outbox.Outbox: Enqueue
// persists the entry and returns immediately; the flusher (a later task)
// delivers it to the tracker and owns retry/backoff, so there is no inline
// retry loop here — a single Enqueue call replaces the direct sink's
// multi-attempt loop. Enqueue's error (validation or persist failure)
// surfaces directly to the caller; on nil, the write is durably accepted
// even if the tracker itself is unreachable at enqueue time.
type outboxWriteSink struct {
	outbox *outbox.Outbox
}

// NewOutboxWriteSink wraps ob as a WriteSink backed by the write-ahead
// outbox. cmd/itervox (Task 3) uses this when cfg.Tracker.Outbox is true.
func NewOutboxWriteSink(ob *outbox.Outbox) WriteSink {
	return &outboxWriteSink{outbox: ob}
}

// UpdateIssueState enqueues a durable update_state entry. It never calls
// the tracker directly — ctx is accepted for interface parity with
// directWriteSink but unused, since Enqueue is synchronous, in-process, and
// does not itself make a tracker API call.
func (s *outboxWriteSink) UpdateIssueState(_ context.Context, issueID, identifier, state string) error {
	return s.outbox.Enqueue(outbox.Entry{
		Kind:        outbox.KindUpdateState,
		IssueID:     issueID,
		Identifier:  identifier,
		TargetState: state,
	})
}

// CreateComment enqueues a durable create_comment entry. See
// UpdateIssueState for why ctx is unused.
func (s *outboxWriteSink) CreateComment(_ context.Context, issueID, identifier, body string) error {
	return s.outbox.Enqueue(outbox.Entry{
		Kind:       outbox.KindCreateComment,
		IssueID:    issueID,
		Identifier: identifier,
		Body:       body,
	})
}
