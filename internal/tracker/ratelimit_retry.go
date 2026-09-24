package tracker

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// Rate-limit retry bounds. Deliberately finite: a tracker stuck returning 429
// must degrade itervox, never wedge it. On exhaustion the caller gets the last
// response and handles it exactly as before this helper existed.
const (
	// MaxRateLimitRetries is how many times a 429 is re-sent.
	MaxRateLimitRetries = 4
	// MaxRateLimitWait caps a SINGLE wait. Linear's reset can be most of an
	// hour away; blocking a worker that long is worse than failing the call
	// and letting the caller's own retry/backoff take over.
	MaxRateLimitWait = 60 * time.Second
	// DefaultRateLimitWait is used when the response carries no usable
	// Retry-After, doubling per attempt.
	DefaultRateLimitWait = 2 * time.Second
)

// rateLimitWaitBase is DefaultRateLimitWait as a var so tests can shrink it
// instead of really sleeping through the backoff ladder — the same convention
// as outboxFlushInterval. Production never reassigns it.
var rateLimitWaitBase = DefaultRateLimitWait

// sharedGate is the process-wide rate-limit gate consulted by every call that
// goes through DoWithRateLimitRetry (issue #61). Package-level rather than
// injected because the whole point is that unrelated callers — N workers, the
// poller, the outbox flusher — coordinate without knowing about each other.
var sharedGate = NewRateLimitGate()

// SharedRateLimitGate exposes the process-wide gate so the dashboard and
// heartbeat can report "the fleet is waiting on a rate limit until T" instead
// of leaving an operator to infer it from stalled work.
func SharedRateLimitGate() *RateLimitGate { return sharedGate }

// requestIntentKey carries an explicit read/write classification for a
// tracker request, for adapters whose HTTP method does not say which it is.
type requestIntentKey struct{}

type requestIntent int

const (
	intentUnset requestIntent = iota
	intentWrite
	intentRead
)

// WithWriteIntent marks ctx as carrying a tracker MUTATION, so the shared
// rate-limit gate admits it ahead of reads once a window lifts.
//
// It exists because HTTP method is not a usable signal for every adapter:
// Linear's API is GraphQL over POST, so every Linear call — including
// polling reads — arrives as a POST, and method-based classification alone
// cannot tell a mutation from a read. Without an explicit signal, the
// write-first admission that exists to stop reads starving writes (#42) has
// nothing to act on for Linear: every call would be treated as a write, and
// "writes go first" would mean nothing. GitHub's REST verbs stay accurate on
// their own and need no tagging.
func WithWriteIntent(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestIntentKey{}, intentWrite)
}

// WithReadIntent marks ctx as carrying a tracker READ, overriding whatever
// the HTTP method would otherwise imply.
//
// This is the other half of the fix WithWriteIntent describes: tagging only
// mutations leaves every untagged POST — including a GraphQL-over-POST read —
// still classified as a write, which is the exact starvation (#42) this gate
// exists to prevent. Linear read operations are tagged with this so the
// method's inherent ambiguity is resolved explicitly in both directions.
func WithReadIntent(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestIntentKey{}, intentRead)
}

// HasWriteIntent reports whether ctx was explicitly marked as a mutation via
// WithWriteIntent. A nil context, an unset intent, or an explicit read intent
// all report false.
func HasWriteIntent(ctx context.Context) bool {
	return intentOf(ctx) == intentWrite
}

// HasReadIntent reports whether ctx was explicitly marked as a read via
// WithReadIntent. A nil context, an unset intent, or an explicit write intent
// all report false.
func HasReadIntent(ctx context.Context) bool {
	return intentOf(ctx) == intentRead
}

func intentOf(ctx context.Context) requestIntent {
	if ctx == nil {
		return intentUnset
	}
	v, _ := ctx.Value(requestIntentKey{}).(requestIntent)
	return v
}

// isWriteRequest classifies a request for the gate's write-first admission.
//
// An explicit intent set via WithWriteIntent/WithReadIntent wins in EITHER
// direction — this is what lets a GraphQL-over-POST read (Linear) be told
// apart from a GraphQL-over-POST mutation, something the HTTP method alone
// cannot do. The method is consulted only as a fallback when no intent is
// set, which is why GitHub's REST verbs keep working untagged: GET and HEAD
// re-derive state the daemon recomputes next tick; everything else is a
// state transition, comment, or mutation that may never be retried if it is
// starved. This is the same priority the read-shedding reserve applies at
// the polling layer, enforced here for in-flight callers too.
func isWriteRequest(req *http.Request) bool {
	if req == nil {
		return false
	}
	switch intentOf(req.Context()) {
	case intentWrite:
		return true
	case intentRead:
		return false
	}
	switch req.Method {
	case http.MethodGet, http.MethodHead:
		return false
	default:
		return true
	}
}

// ParseRetryAfter interprets a Retry-After header, which RFC 9110 allows in
// two forms: delay-seconds, or an HTTP-date. Returns 0 when absent or
// unparseable, letting the caller fall back to its own backoff.
//
// now is injected so the HTTP-date branch is testable without sleeping.
func ParseRetryAfter(header string, now time.Time) time.Duration {
	if header == "" {
		return 0
	}
	if secs, err := strconv.Atoi(header); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if when, err := http.ParseTime(header); err == nil {
		if d := when.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// rateLimitBackoff returns how long to wait before retry attempt n (0-based),
// preferring the server's Retry-After and clamping to MaxRateLimitWait.
//
// The server's own figure wins when present: guessing shorter re-spends the
// budget we are being told to stop spending, which is how a rate limit turns
// into a self-sustaining stampede.
func rateLimitBackoff(attempt int, retryAfter time.Duration) time.Duration {
	wait := retryAfter
	if wait <= 0 {
		wait = rateLimitWaitBase << uint(attempt)
	}
	return min(wait, MaxRateLimitWait)
}

// DoWithRateLimitRetry sends req, transparently retrying a rate-limited
// response with a bounded, Retry-After-aware backoff. classify is
// adapter-specific: GitHub signals a rate limit with 429 or 403-plus-headers,
// Linear signals it with HTTP 400 plus a RATELIMITED code in the GraphQL
// errors array, and only the adapter knows which.
//
// Before this, a 429 propagated straight to the caller as a failure — so the
// moment a tracker's budget ran out EVERY operation failed, including the
// state transitions and comments that would have drained the queue and stopped
// the daemon asking for more (issue #42). Waiting out a rate limit is what
// makes that recoverable rather than a cliff.
//
// req.GetBody must be non-nil for a body-carrying request to be retried;
// http.NewRequest sets it automatically for the in-memory body types both
// adapters use. A request without it is sent once and returned as-is rather
// than silently re-sent with an empty body.
//
// On exhaustion the FINAL response — the one sent by the loop's last retry —
// is classified exactly like every earlier attempt, not assumed to still be
// rate-limited: a success there must be returned as a success, and a
// transport error must not be masked as a rate limit. Only when that final
// response is itself still rate-limited does the call return the typed
// *RateLimitedError, so callers can errors.As it and defer the write to
// ResetAt instead of treating it as failed.
//
// Fail fast. The call returns *RateLimitedError immediately, sending nothing
// further, whenever the wait it is about to take cannot fit its budget: before
// the first send, when the shared gate already holds a window that ends more
// than MaxRateLimitWait away or after ctx's deadline; and inside the retry
// loop, when the tracker's published reset is more than MaxRateLimitWait away
// or the next backoff would end after ctx's deadline. A caller whose deadline
// cannot outlive the published window would otherwise burn requests and time
// only to return a generic context.DeadlineExceeded, which erases the
// rate-limit signal: the outbox would then charge an ordinary attempt (raising
// the degraded badge), never learn ResetAt, and keep sending doomed requests
// inside a window the tracker already published. Linear's 2+4+8+16s ladder
// alone is 30s, the outbox flusher's whole per-call deadline, so without this
// rule a Linear rate limit never reached the outbox as a rate limit at all.
func DoWithRateLimitRetry(ctx context.Context, client *http.Client, req *http.Request, adapter string, classify RateLimitClassifier) (*http.Response, error) {
	isWrite := isWriteRequest(req)
	// A window another caller already discovered that this call cannot wait
	// out within one bounded wait or its own deadline: fail fast with the
	// typed error instead of blocking into a generic deadline error.
	if until, open := sharedGate.OpenUntil(adapter); open && exceedsBudget(ctx, time.Now(), until) {
		return nil, &RateLimitedError{Adapter: adapter, ResetAt: until}
	}
	// Wait out a window another caller already discovered, rather than
	// spending a request to learn the same thing (#61). No window open is the
	// overwhelmingly common case and costs one mutex acquisition.
	if gateErr := sharedGate.Wait(ctx, adapter, isWrite); gateErr != nil {
		return nil, gateErr
	}
	resp, err := client.Do(req)
	for attempt := range MaxRateLimitRetries {
		if err != nil || resp == nil {
			return resp, err
		}
		limited, resetAt := classify(resp)
		if !limited {
			return resp, nil
		}
		now := time.Now()
		retryAfter := ParseRetryAfter(resp.Header.Get("Retry-After"), now)
		wait := rateLimitBackoff(attempt, retryAfter)
		recordRateLimitWindow(adapter, resetAt, wait)
		// Fail fast when the published reset or the next backoff cannot fit
		// the budget: every further send is doomed, and waiting would only
		// end in ctx.Done() with the rate-limit signal lost.
		if (!resetAt.IsZero() && exceedsBudget(ctx, now, resetAt)) || exceedsBudget(ctx, now, now.Add(wait)) {
			drainAndClose(resp)
			failAt := resetAt
			if failAt.IsZero() {
				failAt = now.Add(wait)
			}
			return nil, &RateLimitedError{Adapter: adapter, ResetAt: failAt}
		}
		retryable, rewindErr := rewindRequest(req)
		if !retryable {
			if rewindErr != nil {
				slog.Warn("tracker: cannot replay request body to retry a rate-limited call",
					"adapter", adapter, "error", rewindErr)
			}
			drainAndClose(resp)
			return nil, &RateLimitedError{Adapter: adapter, ResetAt: resetAt}
		}
		drainAndClose(resp)
		slog.Warn("tracker: rate limited, waiting before retry",
			"adapter", adapter, "attempt", attempt+1, "max_attempts", MaxRateLimitRetries, "wait", wait)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		resp, err = client.Do(req)
	}
	// The loop's final statement sent one more request. Classify it rather
	// than assuming it failed: a success here must be returned as a success,
	// and a transport error must not be masked as a rate limit.
	if err != nil || resp == nil {
		return resp, err
	}
	limited, resetAt := classify(resp)
	if !limited {
		return resp, nil
	}
	recordRateLimitWindow(adapter, resetAt, rateLimitBackoff(MaxRateLimitRetries,
		ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())))
	drainAndClose(resp)
	return nil, &RateLimitedError{Adapter: adapter, ResetAt: resetAt}
}

// exceedsBudget reports whether waiting until `until` does not fit the
// caller's budget: it is more than MaxRateLimitWait past now (longer than any
// single bounded wait), or it falls after ctx's deadline (when ctx has one).
// Shared by DoWithRateLimitRetry's two fail-fast checks so the pre-send gate
// check and the in-loop check can never disagree about what "too long" means.
func exceedsBudget(ctx context.Context, now, until time.Time) bool {
	if until.Sub(now) > MaxRateLimitWait {
		return true
	}
	if deadline, ok := ctx.Deadline(); ok && until.After(deadline) {
		return true
	}
	return false
}

// recordRateLimitWindow publishes a rate-limit window on the shared gate:
// the tracker-published reset instant when there is one, otherwise the wait
// this process is about to take.
func recordRateLimitWindow(adapter string, resetAt time.Time, fallback time.Duration) {
	if !resetAt.IsZero() {
		sharedGate.RecordUntil(adapter, resetAt)
		return
	}
	sharedGate.Record(adapter, fallback)
}

// rewindRequest resets req's body so it can be sent again. Reports false when
// the request cannot be replayed, which must abort the retry rather than
// re-send an empty body — a GraphQL POST with no body is not the same request.
func rewindRequest(req *http.Request) (bool, error) {
	if req.Body == nil && req.GetBody == nil {
		return true, nil // bodyless GET: safe to re-send as-is
	}
	if req.GetBody == nil {
		return false, nil
	}
	body, err := req.GetBody()
	if err != nil {
		return false, err
	}
	req.Body = body
	return true, nil
}

// drainAndClose consumes and closes a response body so the underlying
// connection returns to the pool instead of being dropped. Bounded: a
// pathological body must not become the thing that stalls the retry.
func drainAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.CopyN(io.Discard, resp.Body, 64<<10)
	_ = resp.Body.Close()
}
