package tracker

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLinearClassifiesRateLimited400(t *testing.T) {
	body := `{"errors":[{"message":"rate limited","extensions":{"code":"RATELIMITED"}}]}`
	reset := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header: http.Header{
			"X-Ratelimit-Requests-Remaining": []string{"0"},
			"X-Ratelimit-Requests-Reset":     []string{strconv.FormatInt(reset.UnixMilli(), 10)},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}

	limited, resetAt := LinearRateLimitClassifier(resp)

	require.True(t, limited, "HTTP 400 + RATELIMITED is Linear's rate-limit signal")
	assert.True(t, resetAt.Equal(reset), "reset header is epoch MILLISECONDS")

	// The classifier must leave the body readable for the adapter's own
	// non-200 handling, which logs the GraphQL error payload.
	rest, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, body, string(rest), "classifier must restore the body it read")
}

func TestLinearDoesNotClassifyOrdinary400(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"errors":[{"message":"bad filter"}]}`)),
	}

	limited, _ := LinearRateLimitClassifier(resp)

	assert.False(t, limited, "a validation 400 must not be retried as a rate limit")
}

func TestLinearParsesResetEpochMillis(t *testing.T) {
	// Linear documents its reset headers as "UTC epoch milliseconds". Parsing
	// them as seconds would put the reset ~55,000 years in the future and wedge
	// the gate permanently, so this asserts the unit explicitly rather than
	// relying on a round-trip through time.UnixMilli.
	reset := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header: http.Header{
			"X-Ratelimit-Requests-Remaining": []string{"0"},
			"X-Ratelimit-Requests-Reset":     []string{strconv.FormatInt(reset.UnixMilli(), 10)},
		},
		Body: io.NopCloser(strings.NewReader(`{"errors":[{"extensions":{"code":"RATELIMITED"}}]}`)),
	}

	limited, resetAt := LinearRateLimitClassifier(resp)

	require.True(t, limited)
	assert.True(t, resetAt.Equal(reset),
		"reset must be read as epoch milliseconds, not seconds")
	// Guard the unit explicitly: a seconds-based parse of a millisecond value
	// lands ~55,000 years out, which would wedge the gate permanently.
	assert.Less(t, resetAt.Year(), 3000, "reset parsed as seconds instead of milliseconds")
}

func TestLinearPrefersLaterOfRequestAndComplexityReset(t *testing.T) {
	early := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	late := early.Add(10 * time.Minute)
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header: http.Header{
			"X-Ratelimit-Requests-Remaining":   []string{"0"},
			"X-Ratelimit-Requests-Reset":       []string{strconv.FormatInt(early.UnixMilli(), 10)},
			"X-Ratelimit-Complexity-Remaining": []string{"0"},
			"X-Ratelimit-Complexity-Reset":     []string{strconv.FormatInt(late.UnixMilli(), 10)},
		},
		Body: io.NopCloser(strings.NewReader(`{"errors":[{"extensions":{"code":"RATELIMITED"}}]}`)),
	}

	_, resetAt := LinearRateLimitClassifier(resp)

	assert.True(t, resetAt.Equal(late), "both limits exhausted: the later reset governs")
}

func TestGitHubRateLimitClassifierUnchanged(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		header  http.Header
		limited bool
	}{
		{"429", http.StatusTooManyRequests, http.Header{}, true},
		{"403 with retry-after", http.StatusForbidden, http.Header{"Retry-After": []string{"30"}}, true},
		{"403 exhausted", http.StatusForbidden, http.Header{"X-Ratelimit-Remaining": []string{"0"}}, true},
		{"403 plain auth failure", http.StatusForbidden, http.Header{}, false},
		{"200", http.StatusOK, http.Header{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tc.status, Header: tc.header, Body: http.NoBody}
			limited, _ := GitHubRateLimitClassifier(resp)
			assert.Equal(t, tc.limited, limited)
		})
	}
}

// TestGitHubSecondaryRateLimitIgnoresPrimaryReset pins fix-round-1's finding:
// GitHub sends X-RateLimit-Reset on EVERY response, including a secondary
// rate limit (403/429 with Retry-After and budget still remaining), where
// that header describes the PRIMARY window's reset — often ~1h away. Reading
// it unconditionally would gate every GitHub call for up to an hour off a
// secondary limit that clears in Retry-After seconds.
func TestGitHubSecondaryRateLimitIgnoresPrimaryReset(t *testing.T) {
	farOut := time.Now().Add(time.Hour)
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Header: http.Header{
			"Retry-After":           []string{"60"},
			"X-Ratelimit-Remaining": []string{"4000"},
			"X-Ratelimit-Reset":     []string{strconv.FormatInt(farOut.Unix(), 10)},
		},
		Body: http.NoBody,
	}

	limited, resetAt := GitHubRateLimitClassifier(resp)

	assert.True(t, limited, "Retry-After plus a 403 is still a secondary rate limit")
	assert.True(t, resetAt.IsZero(),
		"X-RateLimit-Reset must be ignored when X-RateLimit-Remaining is not 0 — it describes the unrelated primary window")
}

// TestGitHubPrimaryExhaustedParsesResetSeconds pins the other half: when the
// budget really is exhausted (X-RateLimit-Remaining: 0), X-RateLimit-Reset IS
// the reset that matters and must be parsed as epoch SECONDS.
func TestGitHubPrimaryExhaustedParsesResetSeconds(t *testing.T) {
	reset := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	resp := &http.Response{
		StatusCode: http.StatusForbidden,
		Header: http.Header{
			"X-Ratelimit-Remaining": []string{"0"},
			"X-Ratelimit-Reset":     []string{strconv.FormatInt(reset.Unix(), 10)},
		},
		Body: http.NoBody,
	}

	limited, resetAt := GitHubRateLimitClassifier(resp)

	require.True(t, limited)
	assert.True(t, resetAt.Equal(reset), "an exhausted primary budget's reset must be parsed as epoch seconds")
	assert.Less(t, resetAt.Year(), 3000, "reset must be parsed as seconds, not milliseconds")
}

// TestLinearRateLimitedWithoutRemainingHeaderHasZeroReset pins that a
// RATELIMITED body with no usable -Remaining/-Reset headers still classifies
// as limited, just with no reset instant to publish.
func TestLinearRateLimitedWithoutRemainingHeaderHasZeroReset(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"errors":[{"extensions":{"code":"RATELIMITED"}}]}`)),
	}

	limited, resetAt := LinearRateLimitClassifier(resp)

	require.True(t, limited)
	assert.True(t, resetAt.IsZero(), "no -Remaining/-Reset headers means no reset instant is known")
}

// TestLinearClassifies429 pins the defensive 429 branch documented on
// LinearRateLimitClassifier.
func TestLinearClassifies429(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{},
		Body:       http.NoBody,
	}

	limited, _ := LinearRateLimitClassifier(resp)

	assert.True(t, limited, "429 is accepted defensively alongside Linear's documented 400 signal")
}

// TestLinearNonJSON400IsNotRateLimited pins that a 400 whose body isn't even
// JSON (e.g. an upstream proxy error page) is not misclassified as a rate
// limit, and that the body is left intact for whatever reads it next.
func TestLinearNonJSON400IsNotRateLimited(t *testing.T) {
	body := "<html>oops</html>"
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	limited, _ := LinearRateLimitClassifier(resp)

	assert.False(t, limited, "a non-JSON 400 must not be retried as a rate limit")

	rest, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, body, string(rest), "the body must still be readable, and unchanged, after classification")
}

// TestDoWithRateLimitRetryReturnsTypedErrorOnExhaustion walks the real backoff
// ladder, so rateLimitWaitBase is shrunk for its duration (whitebox test in
// package tracker), matching the existing convention documented on that var.
func TestDoWithRateLimitRetryReturnsTypedErrorOnExhaustion(t *testing.T) {
	prev := rateLimitWaitBase
	rateLimitWaitBase = time.Millisecond
	t.Cleanup(func() { rateLimitWaitBase = prev })
	// Every response is rate-limited under adapter "linear"; clear the
	// process-wide sharedGate afterwards so it cannot leak a wait into an
	// unrelated later test using the same adapter key.
	t.Cleanup(func() { sharedGate.Clear("linear") })

	reset := time.Now().Add(20 * time.Minute).UTC().Truncate(time.Millisecond)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Requests-Remaining", "0")
		w.Header().Set("X-RateLimit-Requests-Reset", strconv.FormatInt(reset.UnixMilli(), 10))
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"extensions":{"code":"RATELIMITED"}}]}`))
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL,
		strings.NewReader(`{"query":"{}"}`))
	require.NoError(t, err)

	_, err = DoWithRateLimitRetry(context.Background(), srv.Client(), req,
		"linear", LinearRateLimitClassifier)

	var rle *RateLimitedError
	require.ErrorAs(t, err, &rle, "exhausted retries must return a typed rate-limit error")
	assert.True(t, rle.RateLimitResetAt().Equal(reset))
}
