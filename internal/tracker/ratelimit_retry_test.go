package tracker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	resp, err := DoWithRateLimitRetry(context.Background(), srv.Client(), req, "test")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, http.StatusOK, resp.StatusCode, "the call must succeed once the limit clears")
	assert.EqualValues(t, 3, calls.Load())
	assert.EqualValues(t, 3, bodies.Load(),
		"every retry must replay the request body — a GraphQL POST with an empty body is a different request")
}

// TestDoWithRateLimitRetryGivesUpBounded pins invariant 2: a tracker stuck on
// 429 degrades itervox, it does not wedge it.
func TestDoWithRateLimitRetryGivesUpBounded(t *testing.T) {
	// Shrink the backoff: this test is about the retry BEHAVIOUR, not the
	// wall-clock ladder, and really sleeping it would add ~36s to the suite.
	prev := rateLimitWaitBase
	rateLimitWaitBase = time.Millisecond
	t.Cleanup(func() { rateLimitWaitBase = prev })

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{}`))
	require.NoError(t, err)

	resp, err := DoWithRateLimitRetry(context.Background(), srv.Client(), req, "test")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode,
		"on exhaustion the caller gets the 429 and handles it as before")
	assert.EqualValues(t, MaxRateLimitRetries+1, calls.Load(), "one initial send plus the retries")
}

// TestDoWithRateLimitRetryHonorsContextCancellation — a shutdown must not wait
// out a rate limit.
func TestDoWithRateLimitRetryHonorsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{}`))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = DoWithRateLimitRetry(ctx, srv.Client(), req, "test")
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), 5*time.Second, "must abandon the wait on cancellation")
}

// TestIsRateLimited pins GitHub's 403 rate-limit signalling, and the boundary
// that keeps a genuine auth failure from being retried.
func TestIsRateLimited(t *testing.T) {
	resp := func(code int, hdr map[string]string) *http.Response {
		r := &http.Response{StatusCode: code, Header: http.Header{}}
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}

	assert.True(t, isRateLimited(resp(http.StatusTooManyRequests, nil)), "429 is always a rate limit")

	// GitHub uses 403 for both primary and secondary rate limits.
	assert.True(t, isRateLimited(resp(http.StatusForbidden, map[string]string{"Retry-After": "60"})),
		"403 with Retry-After is a secondary rate limit")
	assert.True(t, isRateLimited(resp(http.StatusForbidden, map[string]string{"X-RateLimit-Remaining": "0"})),
		"403 with an exhausted budget is a primary rate limit")

	// A real permission error must NOT be retried — four retries just delay a
	// clear, actionable failure.
	assert.False(t, isRateLimited(resp(http.StatusForbidden, nil)),
		"a bare 403 is an authorization failure, not a rate limit")
	assert.False(t, isRateLimited(resp(http.StatusForbidden, map[string]string{"X-RateLimit-Remaining": "4999"})),
		"403 with budget remaining is an authorization failure")
	assert.False(t, isRateLimited(resp(http.StatusOK, nil)))
	assert.False(t, isRateLimited(resp(http.StatusInternalServerError, nil)))
}
