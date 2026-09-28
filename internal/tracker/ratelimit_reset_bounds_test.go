package tracker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoWithRateLimitRetryBoundsFarFutureReset (CORE-117): the gate clamps a
// published reset to now+maxRecordedWindow, so the typed error the same call
// hands back must carry the same bounded instant. Otherwise every consumer of
// RateLimitedError.ResetAt (the outbox's RateLimitedUntil, the flusher's log,
// the error string) sees a reset the gate itself refuses to honour.
//
// The request is made non-replayable so the zero and past cases return on the
// first classify (the non-replayable path) instead of walking the real 2s
// ladder; the in-bound and far-future cases take the fail-fast path.
func TestDoWithRateLimitRetryBoundsFarFutureReset(t *testing.T) {
	cases := []struct {
		name string
		// reset relative to the start of the call; zero means no header.
		offset  time.Duration
		noReset bool
		// gateLater, when non-zero, is recorded on the shared gate by the
		// server while it answers, so the call finds a LATER in-bound window
		// already recorded when it publishes its own.
		gateLater time.Duration
		bounded   bool // expect the clamp rather than the verbatim reset
	}{
		{name: "zero", noReset: true},
		{name: "past", offset: -time.Minute},
		{name: "in-bound", offset: 20 * time.Minute},
		{name: "in-bound with later gate", offset: 20 * time.Minute, gateLater: 50 * time.Minute},
		{name: "far-future", offset: 100 * time.Hour, bounded: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sharedGate.Clear("linear")
			t.Cleanup(func() { sharedGate.Clear("linear") })

			reset := time.UnixMilli(time.Now().Add(tc.offset).UnixMilli()).UTC()
			var reqs atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reqs.Add(1)
				if tc.gateLater > 0 {
					sharedGate.RecordUntil("linear", time.Now().Add(tc.gateLater))
				}
				w.Header().Set("X-RateLimit-Requests-Remaining", "0")
				if !tc.noReset {
					w.Header().Set("X-RateLimit-Requests-Reset", strconv.FormatInt(reset.UnixMilli(), 10))
				}
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"errors":[{"message":"rate limited","extensions":{"code":"RATELIMITED"}}]}`))
			}))
			defer srv.Close()

			req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"query":"x"}`))
			require.NoError(t, err)
			req.GetBody = nil // non-replayable: the zero/past cases return on the first classify
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			_, err = DoWithRateLimitRetry(ctx, srv.Client(), req, "linear", LinearRateLimitClassifier)
			var rle *RateLimitedError
			require.ErrorAs(t, err, &rle)
			assert.EqualValues(t, 1, reqs.Load())

			switch {
			case tc.bounded:
				assert.True(t, rle.ResetAt.Before(time.Now().Add(maxRecordedWindow+time.Minute)),
					"a far-future reset must be clamped to the gate's bound, got ResetAt in %s", time.Until(rle.ResetAt))
				assert.False(t, rle.ResetAt.Before(time.Now().Add(maxRecordedWindow-time.Minute)),
					"the clamp is the bound itself, not an earlier instant")
				until, open := sharedGate.OpenUntil("linear")
				require.True(t, open)
				assert.WithinDuration(t, until, rle.ResetAt, time.Second,
					"the typed error and the gate must agree about the bounded window")
			case tc.noReset:
				assert.True(t, rle.ResetAt.IsZero(), "no published reset stays zero, got %s", rle.ResetAt)
			default:
				assert.True(t, rle.ResetAt.Equal(reset), "in-bound/past reset is kept verbatim: got %s want %s", rle.ResetAt, reset)
			}
			if tc.gateLater > 0 {
				until, open := sharedGate.OpenUntil("linear")
				require.True(t, open)
				assert.Greater(t, time.Until(until), tc.gateLater-time.Minute,
					"an earlier reset must never shorten a later recorded gate")
			}
		})
	}
}

// TestDoWithRateLimitRetryWaitsForPublishedReset (CORE-118): a published reset
// that fits the budget is the wait. Retrying on the 2s<<n ladder instead
// re-sends inside the window this very call just recorded on the shared gate.
func TestDoWithRateLimitRetryWaitsForPublishedReset(t *testing.T) {
	prev := rateLimitWaitBase
	rateLimitWaitBase = 20 * time.Millisecond
	t.Cleanup(func() { rateLimitWaitBase = prev })
	sharedGate.Clear("linear")
	t.Cleanup(func() { sharedGate.Clear("linear") })

	reset := time.UnixMilli(time.Now().Add(300 * time.Millisecond).UnixMilli()).UTC()
	var mu sync.Mutex
	var seen []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		seen = append(seen, time.Now())
		n := len(seen)
		mu.Unlock()
		if n == 1 {
			w.Header().Set("X-RateLimit-Requests-Remaining", "0")
			w.Header().Set("X-RateLimit-Requests-Reset", strconv.FormatInt(reset.UnixMilli(), 10))
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errors":[{"message":"rate limited","extensions":{"code":"RATELIMITED"}}]}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"query":"x"}`))
	require.NoError(t, err)
	resp, err := DoWithRateLimitRetry(context.Background(), srv.Client(), req, "linear", LinearRateLimitClassifier)
	require.NoError(t, err)
	require.NotNil(t, resp)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, seen, 2, "one rate-limited send and one retry after the published reset")
	assert.False(t, seen[1].Before(reset),
		"the retry must not be sent before the published reset: sent %s before it", reset.Sub(seen[1]))
}

// TestDoWithRateLimitRetryFailsFastOnLongRetryAfter (CORE-119): a Retry-After
// is a published reset. One longer than MaxRateLimitWait must fail fast like
// an over-budget X-RateLimit-Reset does, carrying now+Retry-After, instead of
// sleeping the capped 60s and re-sending inside the declared window.
//
// The context carries no deadline (a cancelable Background, cancelled only by
// cleanup so a regression cannot leak a sleeping goroutine), which is the
// production shape of onTick's poll.
func TestDoWithRateLimitRetryFailsFastOnLongRetryAfter(t *testing.T) {
	sharedGate.Clear("github")
	t.Cleanup(func() { sharedGate.Clear("github") })

	var reqs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reqs.Add(1)
		w.Header().Set("Retry-After", "120")
		w.Header().Set("X-RateLimit-Remaining", "4000")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	require.NoError(t, err)

	type result struct {
		resp *http.Response
		err  error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		resp, err := DoWithRateLimitRetry(ctx, srv.Client(), req, "github", GitHubRateLimitClassifier)
		done <- result{resp, err}
	}()
	var got result
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatalf("a Retry-After beyond MaxRateLimitWait must fail fast; the call was still waiting after 2s (requests=%d)", reqs.Load())
	}

	assert.Nil(t, got.resp)
	var rle *RateLimitedError
	require.ErrorAs(t, got.err, &rle)
	assert.EqualValues(t, 1, reqs.Load(), "nothing may be re-sent inside the declared window")
	assert.WithinDuration(t, start.Add(120*time.Second), rle.ResetAt, 5*time.Second,
		"the typed error must carry the published Retry-After, got ResetAt in %s", time.Until(rle.ResetAt))
	until, open := sharedGate.OpenUntil("github")
	require.True(t, open)
	assert.WithinDuration(t, start.Add(120*time.Second), until, 5*time.Second,
		"the gate must record the real window, not the 60s cap")
}
