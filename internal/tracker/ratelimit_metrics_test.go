package tracker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/metrics"
)

// CORE-045: every DoWithRateLimitRetry call is counted once by outcome, on
// the one shared path both adapters use.
func TestDoWithRateLimitRetryCountsRequests(t *testing.T) {
	const adapter = "metrics-count-test"
	sharedGate.Clear(adapter)
	t.Cleanup(func() { sharedGate.Clear(adapter) })

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A reset past MaxRateLimitWait fails fast with the typed error.
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
		w.WriteHeader(http.StatusForbidden)
	}))
	defer limited.Close()

	before := func(o string) uint64 { return metrics.TrackerRequestCount(adapter, o) }
	okBefore, rlBefore, errBefore := before(metrics.TrackerOutcomeOK), before(metrics.TrackerOutcomeRateLimited), before(metrics.TrackerOutcomeError)

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, ok.URL, nil)
	resp, err := DoWithRateLimitRetry(context.Background(), ok.Client(), req, adapter, GitHubRateLimitClassifier)
	require.NoError(t, err)
	_ = resp.Body.Close()

	req, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, limited.URL, nil)
	_, err = DoWithRateLimitRetry(context.Background(), limited.Client(), req, adapter, GitHubRateLimitClassifier)
	require.Error(t, err)
	sharedGate.Clear(adapter)

	req, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:1/unreachable", nil)
	_, err = DoWithRateLimitRetry(context.Background(), http.DefaultClient, req, adapter, GitHubRateLimitClassifier)
	require.Error(t, err)

	assert.Equal(t, okBefore+1, before(metrics.TrackerOutcomeOK))
	assert.Equal(t, rlBefore+1, before(metrics.TrackerOutcomeRateLimited))
	assert.Equal(t, errBefore+1, before(metrics.TrackerOutcomeError))
}
