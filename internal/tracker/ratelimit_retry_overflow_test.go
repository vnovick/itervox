package tracker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseRetryAfterHugeDelayClampsInsteadOfOverflowing pins BH1: a
// delay-seconds Retry-After of 9223372037 or more overflowed
// time.Duration(secs)*time.Second to a negative value, which read as "no
// Retry-After" and re-sent the call on the ~2s default ladder. A value too
// large for int (strconv.ErrRange) fell through the same way. Both must clamp
// to the same bound the gate applies (maxRecordedWindow).
func TestParseRetryAfterHugeDelayClampsInsteadOfOverflowing(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	for _, h := range []string{
		"9223372037",           // first value whose *time.Second wraps negative
		"9223372036854775807",  // max int64
		"99999999999999999999", // beyond int64: strconv.ErrRange
		"86400",                // a day: in range, but past the gate's bound
	} {
		assert.Equal(t, maxRecordedWindow, ParseRetryAfter(h, now), "Retry-After %q", h)
	}
	// Just inside the bound is returned as-is.
	assert.Equal(t, time.Hour, ParseRetryAfter("3600", now))
}

// TestDoWithRateLimitRetryHugeRetryAfterFailsFastWithReset is the end-to-end
// form of BH1: the call must return *RateLimitedError after ONE send, with a
// ResetAt at the gate bound, instead of re-sending into the window ~2s later.
func TestDoWithRateLimitRetryHugeRetryAfterFailsFastWithReset(t *testing.T) {
	t.Cleanup(func() { sharedGate.Clear("test-bh1") })
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "9223372037")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	require.NoError(t, err)
	start := time.Now()
	resp, err := DoWithRateLimitRetry(context.Background(), srv.Client(), req, "test-bh1", GitHubRateLimitClassifier)
	if resp != nil {
		_ = resp.Body.Close()
	}
	var rle *RateLimitedError
	require.True(t, errors.As(err, &rle), "want *RateLimitedError, got %v", err)
	assert.EqualValues(t, 1, calls.Load(), "a huge Retry-After must not be re-sent")
	assert.Less(t, time.Since(start), time.Second, "must fail fast, not wait on the default ladder")
	assert.WithinDuration(t, start.Add(maxRecordedWindow), rle.ResetAt, 5*time.Second)
}
