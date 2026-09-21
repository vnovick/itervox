package tracker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWriteIntentMarksPOSTAsWrite pins that a POST whose context carries an
// explicit write intent is classified as a write by isWriteRequest.
func TestWriteIntentMarksPOSTAsWrite(t *testing.T) {
	ctx := WithWriteIntent(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test", nil)
	require.NoError(t, err)

	assert.True(t, isWriteRequest(req))
}

// TestReadIntentMarksPOSTAsRead is the regression test for D1: the brief's
// asymmetric design only ever ADDS a write tag and leaves every untagged POST
// classified as a write, so a Linear read tagged WithReadIntent would still
// come out as a write. This must be false under the ruling's design, where an
// explicit read intent overrides the method in either direction.
func TestReadIntentMarksPOSTAsRead(t *testing.T) {
	ctx := WithReadIntent(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test", nil)
	require.NoError(t, err)

	assert.False(t, isWriteRequest(req),
		"an explicit read intent must override the POST method — this is what makes Linear polling reads stop starving writes")
}

// TestNoIntentFallsBackToMethod pins that GitHub's untagged REST calls keep
// working exactly as before: no intent set means the HTTP method decides.
func TestNoIntentFallsBackToMethod(t *testing.T) {
	get, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.test", nil)
	require.NoError(t, err)
	assert.False(t, isWriteRequest(get))

	post, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.test", nil)
	require.NoError(t, err)
	assert.True(t, isWriteRequest(post))

	patch, err := http.NewRequestWithContext(context.Background(), http.MethodPatch, "https://example.test", nil)
	require.NoError(t, err)
	assert.True(t, isWriteRequest(patch))
}

// TestHasWriteIntent pins the three states HasWriteIntent must distinguish:
// explicit write, explicit read, and unset — plus a nil context, which must
// not panic.
func TestHasWriteIntent(t *testing.T) {
	assert.True(t, HasWriteIntent(WithWriteIntent(context.Background())))
	assert.False(t, HasWriteIntent(WithReadIntent(context.Background())))
	assert.False(t, HasWriteIntent(context.Background()))
	assert.False(t, HasWriteIntent(nil)) //nolint:staticcheck // deliberately exercising the nil-context guard
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

	assert.Equal(t, 30*time.Second, ParseRetryAfter("30", now), "delay-seconds form")
	assert.Equal(t, 90*time.Second,
		ParseRetryAfter(now.Add(90*time.Second).UTC().Format(http.TimeFormat), now),
		"HTTP-date form")

	// Absent, malformed, or already-elapsed all fall back to the caller's own
	// backoff rather than to zero-wait — a zero here would hammer the tracker
	// that just asked us to stop.
	assert.Zero(t, ParseRetryAfter("", now))
	assert.Zero(t, ParseRetryAfter("soon", now))
	assert.Zero(t, ParseRetryAfter("0", now))
	assert.Zero(t, ParseRetryAfter("-5", now))
	assert.Zero(t, ParseRetryAfter(now.Add(-time.Minute).UTC().Format(http.TimeFormat), now),
		"a date in the past means no wait is owed")
}

func TestRateLimitBackoffPrefersServerAndClamps(t *testing.T) {
	// The server's figure wins: guessing shorter re-spends the budget we are
	// being told to stop spending.
	assert.Equal(t, 30*time.Second, rateLimitBackoff(0, 30*time.Second))
	// No Retry-After: exponential from the default.
	assert.Equal(t, rateLimitWaitBase, rateLimitBackoff(0, 0))
	assert.Equal(t, 2*rateLimitWaitBase, rateLimitBackoff(1, 0))
	// Clamped — a reset an hour out must not block a worker for an hour.
	assert.Equal(t, MaxRateLimitWait, rateLimitBackoff(0, time.Hour))
	assert.Equal(t, MaxRateLimitWait, rateLimitBackoff(20, 0))
}

// TestDoWithRateLimitRetrySucceedsAfter429 is the behaviour the daemon was
// missing entirely: a 429 was a hard failure, so the moment a tracker's budget
// ran out every operation failed — including the writes that would have
// drained the queue and stopped itervox asking for more (issue #42).
func TestDoWithRateLimitRetrySucceedsAfter429(t *testing.T) {
	// Shrink the backoff: this test is about the retry BEHAVIOUR, not the
	// wall-clock ladder, and really sleeping it would add ~36s to the suite.
	prev := rateLimitWaitBase
	rateLimitWaitBase = time.Millisecond
	t.Cleanup(func() { rateLimitWaitBase = prev })
	// A rate-limited response drives Record on the process-wide sharedGate
	// under adapter "test"; clear it so it cannot leak a wait into an
	// unrelated later test using the same adapter key.
	t.Cleanup(func() { sharedGate.Clear("test") })

	var calls atomic.Int32
	var bodies atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		buf := make([]byte, 64)
		if got, _ := r.Body.Read(buf); got > 0 {
			bodies.Add(1)
		}
		if n < 3 {
			w.Header().Set("Retry-After", "0") // exercise the fallback path fast
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"query":"x"}`))
	require.NoError(t, err)

	resp, err := DoWithRateLimitRetry(context.Background(), srv.Client(), req, "test", GitHubRateLimitClassifier)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, http.StatusOK, resp.StatusCode, "the call must succeed once the limit clears")
	assert.EqualValues(t, 3, calls.Load())
	assert.EqualValues(t, 3, bodies.Load(),
		"every retry must replay the request body — a GraphQL POST with an empty body is a different request")
}

// TestDoWithRateLimitRetryGivesUpBounded pins invariant 2: a tracker stuck on
// 429 degrades itervox, it does not wedge it. Exhaustion now surfaces a typed
// *RateLimitedError rather than the raw final response, so callers can
// errors.As it and defer the write instead of treating it as failed.
func TestDoWithRateLimitRetryGivesUpBounded(t *testing.T) {
	// Shrink the backoff: this test is about the retry BEHAVIOUR, not the
	// wall-clock ladder, and really sleeping it would add ~36s to the suite.
	prev := rateLimitWaitBase
	rateLimitWaitBase = time.Millisecond
	t.Cleanup(func() { rateLimitWaitBase = prev })
	// Every response is rate-limited, so this drives Record on the
	// process-wide sharedGate under adapter "test"; clear it so it cannot
	// leak a wait into an unrelated later test using the same adapter key.
	t.Cleanup(func() { sharedGate.Clear("test") })

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{}`))
	require.NoError(t, err)

	resp, err := DoWithRateLimitRetry(context.Background(), srv.Client(), req, "test", GitHubRateLimitClassifier)

	var rle *RateLimitedError
	require.ErrorAs(t, err, &rle, "exhausted retries must return a typed rate-limit error")
	assert.Nil(t, resp, "no live response is handed back once the call is reported as an error")
	assert.EqualValues(t, MaxRateLimitRetries+1, calls.Load(), "one initial send plus the retries")
}

// TestDoWithRateLimitRetryHonorsContextCancellation — a shutdown must not wait
// out a rate limit.
func TestDoWithRateLimitRetryHonorsContextCancellation(t *testing.T) {
	// The single rate-limited response before cancellation still records a
	// 30s window on the process-wide sharedGate under adapter "test"; clear
	// it so it cannot leak a wait into an unrelated later test.
	t.Cleanup(func() { sharedGate.Clear("test") })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{}`))
	require.NoError(t, err)

	// A cancel with NO deadline, deliberately: a deadline shorter than the
	// 30s Retry-After now trips DoWithRateLimitRetry's fail-fast rule (the
	// wait provably cannot fit, so it returns *RateLimitedError without
	// waiting — see TestDoWithRateLimitRetryFailsFastWhenBackoffExceedsDeadline).
	// This test is about the OTHER path: a wait that does fit the budget must
	// still be abandoned the moment the caller shuts down.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := time.AfterFunc(40*time.Millisecond, cancel)
	defer stop.Stop()

	start := time.Now()
	_, err = DoWithRateLimitRetry(ctx, srv.Client(), req, "test", GitHubRateLimitClassifier)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), 5*time.Second, "must abandon the wait on cancellation")
}

// TestDoWithRateLimitRetrySucceedsOnFinalAttempt pins ruling 1: the loop's
// last statement sends one more request after the retry-count loop exits, and
// that final response must be classified like every earlier one rather than
// assumed to still be rate-limited. Before this fix, a success on the final
// attempt was discarded and reported as a rate limit — which, downstream,
// would have caused the outbox to defer a comment that had actually landed,
// skip its dedupe lookup on the next attempt, and post it twice.
func TestDoWithRateLimitRetrySucceedsOnFinalAttempt(t *testing.T) {
	// Shrink the backoff: this test is about the retry BEHAVIOUR, not the
	// wall-clock ladder.
	prev := rateLimitWaitBase
	rateLimitWaitBase = time.Millisecond
	t.Cleanup(func() { rateLimitWaitBase = prev })
	// Every response before the final one is rate-limited, so this drives
	// Record on the process-wide sharedGate under adapter "test"; clear it so
	// it cannot leak a wait into an unrelated later test.
	t.Cleanup(func() { sharedGate.Clear("test") })

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n <= MaxRateLimitRetries {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{}`))
	require.NoError(t, err)

	resp, err := DoWithRateLimitRetry(context.Background(), srv.Client(), req, "test", GitHubRateLimitClassifier)
	require.NoError(t, err, "a success on the final attempt must not be reported as a rate-limit error")
	require.NotNil(t, resp)
	defer resp.Body.Close() //nolint:errcheck

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "ok", string(body))
	assert.EqualValues(t, MaxRateLimitRetries+1, calls.Load(),
		"the initial send plus every retry (including the final, successful one) must be served")
}

// TestDoWithRateLimitRetryTransportErrorOnFinalAttemptNotMasked pins the other
// half of ruling 1: a transport error on the loop's final request must
// propagate as-is, not be masked as a typed rate-limit error.
func TestDoWithRateLimitRetryTransportErrorOnFinalAttemptNotMasked(t *testing.T) {
	prev := rateLimitWaitBase
	rateLimitWaitBase = time.Millisecond
	t.Cleanup(func() { rateLimitWaitBase = prev })
	t.Cleanup(func() { sharedGate.Clear("test") })

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n == MaxRateLimitRetries+1 {
			// Hijack and close the connection so client.Do returns a
			// transport error on the final attempt instead of a response.
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
			return
		}
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{}`))
	require.NoError(t, err)

	_, err = DoWithRateLimitRetry(context.Background(), srv.Client(), req, "test", GitHubRateLimitClassifier)

	require.Error(t, err)
	var rle *RateLimitedError
	assert.False(t, errors.As(err, &rle),
		"a transport error on the final attempt must not be masked as a rate limit")
	assert.EqualValues(t, MaxRateLimitRetries+1, calls.Load())
}

// TestDoWithRateLimitRetryNotReplayableReturnsTypedError pins the
// not-retryable path through rewindRequest: a request whose body cannot be
// replayed (req.GetBody == nil, e.g. built from a reader http.NewRequest
// could not snapshot) must abort after the first rate-limited response with
// the typed error, not spend any further attempts re-sending a request it
// cannot safely repeat.
func TestDoWithRateLimitRetryNotReplayableReturnsTypedError(t *testing.T) {
	prev := rateLimitWaitBase
	rateLimitWaitBase = time.Millisecond
	t.Cleanup(func() { rateLimitWaitBase = prev })
	t.Cleanup(func() { sharedGate.Clear("test") })

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{}`))
	require.NoError(t, err)
	req.GetBody = nil // force the not-replayable path in rewindRequest

	_, err = DoWithRateLimitRetry(context.Background(), srv.Client(), req, "test", GitHubRateLimitClassifier)

	var rle *RateLimitedError
	require.ErrorAs(t, err, &rle, "an unretryable body must still surface the typed rate-limit error")
	assert.EqualValues(t, 1, calls.Load(),
		"a request that cannot be replayed must not be resent")
}

// TestGitHubRateLimitClassifier403Boundary pins GitHub's 403 rate-limit
// signalling, and the boundary that keeps a genuine auth failure from being
// retried. isRateLimited itself was deleted — its logic now lives in
// GitHubRateLimitClassifier.
func TestGitHubRateLimitClassifier403Boundary(t *testing.T) {
	resp := func(code int, hdr map[string]string) *http.Response {
		r := &http.Response{StatusCode: code, Header: http.Header{}}
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}

	limited, _ := GitHubRateLimitClassifier(resp(http.StatusTooManyRequests, nil))
	assert.True(t, limited, "429 is always a rate limit")

	// GitHub uses 403 for both primary and secondary rate limits.
	limited, _ = GitHubRateLimitClassifier(resp(http.StatusForbidden, map[string]string{"Retry-After": "60"}))
	assert.True(t, limited, "403 with Retry-After is a secondary rate limit")
	limited, _ = GitHubRateLimitClassifier(resp(http.StatusForbidden, map[string]string{"X-RateLimit-Remaining": "0"}))
	assert.True(t, limited, "403 with an exhausted budget is a primary rate limit")

	// A real permission error must NOT be retried — four retries just delay a
	// clear, actionable failure.
	limited, _ = GitHubRateLimitClassifier(resp(http.StatusForbidden, nil))
	assert.False(t, limited, "a bare 403 is an authorization failure, not a rate limit")
	limited, _ = GitHubRateLimitClassifier(resp(http.StatusForbidden, map[string]string{"X-RateLimit-Remaining": "4999"}))
	assert.False(t, limited, "403 with budget remaining is an authorization failure")
	limited, _ = GitHubRateLimitClassifier(resp(http.StatusOK, nil))
	assert.False(t, limited)
	limited, _ = GitHubRateLimitClassifier(resp(http.StatusInternalServerError, nil))
	assert.False(t, limited)
}

// linearRateLimitedHandler answers every request with Linear's real rate-limit
// shape — HTTP 400 carrying a RATELIMITED GraphQL error, with the requests
// budget reported exhausted until reset — and counts what it served.
func linearRateLimitedHandler(reqs *atomic.Int32, reset time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		reqs.Add(1)
		w.Header().Set("X-RateLimit-Requests-Remaining", "0")
		w.Header().Set("X-RateLimit-Requests-Reset", strconv.FormatInt(reset.UnixMilli(), 10))
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"message":"rate limited","extensions":{"code":"RATELIMITED"}}]}`))
	}
}

// TestDoWithRateLimitRetryFailsFastWhenResetBeyondBudget is the C1 regression
// test, run with the REAL backoff base (rateLimitWaitBase is deliberately not
// shrunk). Linear publishes a reset 20 minutes out; a caller with a 30s
// deadline cannot outlive it, so the call must hand back the typed error
// straight away instead of sleeping 2+4+8+16s into ctx.Done() and returning a
// generic deadline error that erases the rate-limit signal.
func TestDoWithRateLimitRetryFailsFastWhenResetBeyondBudget(t *testing.T) {
	t.Cleanup(func() { sharedGate.Clear("linear") })

	var reqs atomic.Int32
	reset := time.Now().Add(20 * time.Minute)
	srv := httptest.NewServer(linearRateLimitedHandler(&reqs, reset))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"query":"x"}`))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	resp, err := DoWithRateLimitRetry(ctx, srv.Client(), req, "linear", LinearRateLimitClassifier)
	elapsed := time.Since(start)

	var rle *RateLimitedError
	require.ErrorAs(t, err, &rle, "a reset beyond the caller's budget must surface as the typed rate-limit error")
	assert.Nil(t, resp)
	assert.True(t, rle.ResetAt.Equal(time.UnixMilli(reset.UnixMilli())),
		"ResetAt must be the tracker-published reset, got %s want %s", rle.ResetAt, reset)
	assert.EqualValues(t, 1, reqs.Load(), "no request may be sent after the reset is known to be out of reach")
	assert.Less(t, elapsed, 2*time.Second, "must fail fast, not sleep through the backoff ladder")
}

// TestDoWithRateLimitRetryFailsFastWhenGateAlreadyOpen: a window another
// caller already published, ending further out than one bounded wait, must
// fail the call before anything is sent.
func TestDoWithRateLimitRetryFailsFastWhenGateAlreadyOpen(t *testing.T) {
	t.Cleanup(func() { sharedGate.Clear("linear") })
	until := time.Now().Add(20 * time.Minute)
	sharedGate.RecordUntil("linear", until)

	var reqs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reqs.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"query":"x"}`))
	require.NoError(t, err)
	// Safety net only: the fail-fast rule fires on the 20-minute window alone.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	resp, err := DoWithRateLimitRetry(ctx, srv.Client(), req, "linear", LinearRateLimitClassifier)
	elapsed := time.Since(start)

	var rle *RateLimitedError
	require.ErrorAs(t, err, &rle, "an open gate beyond one bounded wait must surface as the typed rate-limit error")
	assert.Nil(t, resp)
	assert.True(t, rle.ResetAt.Equal(until), "ResetAt must be the gate's recorded window")
	assert.EqualValues(t, 0, reqs.Load(), "nothing may be sent inside a published window")
	assert.Less(t, elapsed, time.Second)
}

// TestDoWithRateLimitRetryFailsFastWhenBackoffExceedsDeadline: no published
// reset, but the first real backoff step (2s) already lands after the
// caller's 1s deadline — waiting would only end in ctx.Done().
func TestDoWithRateLimitRetryFailsFastWhenBackoffExceedsDeadline(t *testing.T) {
	t.Cleanup(func() { sharedGate.Clear("test") })

	var reqs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reqs.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{}`))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	start := time.Now()
	resp, err := DoWithRateLimitRetry(ctx, srv.Client(), req, "test", GitHubRateLimitClassifier)
	elapsed := time.Since(start)

	var rle *RateLimitedError
	require.ErrorAs(t, err, &rle, "a backoff that outlives the deadline must surface as the typed rate-limit error")
	assert.Nil(t, resp)
	assert.False(t, rle.ResetAt.IsZero(), "with no published reset, ResetAt is now+wait")
	assert.EqualValues(t, 1, reqs.Load())
	assert.Less(t, elapsed, 1500*time.Millisecond)
}
