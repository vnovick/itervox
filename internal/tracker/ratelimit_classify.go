package tracker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// RateLimitClassifier reports whether resp is a rate-limit response and, when
// the adapter publishes one, the instant the budget returns (zero when it does
// not).
//
// Adapters own this because the signal is vendor-specific and not guessable
// from the status code alone: GitHub uses 429, or 403 plus headers; Linear
// uses HTTP 400 with a RATELIMITED code inside the GraphQL errors array. A
// shared status-code check silently missed every Linear rate limit.
//
// A classifier MUST leave resp.Body readable by whatever handles resp next.
type RateLimitClassifier func(resp *http.Response) (limited bool, resetAt time.Time)

// RateLimitedError is returned when in-call retries are exhausted against a
// rate-limited response. Callers match it with errors.As to defer work to
// ResetAt instead of treating the write as failed.
type RateLimitedError struct {
	Adapter string
	ResetAt time.Time
}

func (e *RateLimitedError) Error() string {
	if e.ResetAt.IsZero() {
		return fmt.Sprintf("tracker: %s rate limited", e.Adapter)
	}
	return fmt.Sprintf("tracker: %s rate limited until %s", e.Adapter, e.ResetAt.UTC().Format(time.RFC3339))
}

// RateLimitResetAt satisfies the structural interface internal/outbox declares
// for rate-limit awareness. The outbox must not import internal/tracker (it
// would invert the package dependency order), so the coupling is a method
// name, not a type.
func (e *RateLimitedError) RateLimitResetAt() time.Time { return e.ResetAt }

// GitHubRateLimitClassifier preserves the pre-existing behaviour exactly: 429,
// or 403 distinguished from a genuine authorization failure by
// x-ratelimit-remaining: 0 or a retry-after header. A 403 with neither is a
// real permission error and must NOT be retried.
//
// X-RateLimit-Reset is parsed ONLY when X-RateLimit-Remaining is "0". GitHub
// sends X-RateLimit-Reset on every response — including a SECONDARY rate
// limit (403/429 with Retry-After but budget still remaining) — where the
// header still describes the PRIMARY window's reset, often close to an hour
// away. GitHub's own guidance is Retry-After first; x-ratelimit-reset only
// applies once x-ratelimit-remaining is actually 0. Reading it unconditionally
// would gate every GitHub call for up to an hour off a limit that has nothing
// to do with the one that was actually hit.
func GitHubRateLimitClassifier(resp *http.Response) (bool, time.Time) {
	if resp == nil {
		return false, time.Time{}
	}
	limited := false
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		limited = true
	case resp.StatusCode != http.StatusForbidden:
		return false, time.Time{}
	case resp.Header.Get("Retry-After") != "":
		limited = true
	case resp.Header.Get("X-RateLimit-Remaining") == "0":
		limited = true
	}
	if !limited {
		return false, time.Time{}
	}
	if resp.Header.Get("X-RateLimit-Remaining") != "0" {
		return true, time.Time{}
	}
	// GitHub publishes its reset as epoch SECONDS.
	if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
		if secs, err := strconv.ParseInt(reset, 10, 64); err == nil && secs > 0 {
			return true, time.Unix(secs, 0).UTC()
		}
	}
	return true, time.Time{}
}

// LinearRateLimitClassifier recognises Linear's rate-limit signal: per
// Linear's rate-limiting documentation the "response http status code will be
// 400" with "errors in the response body containing the RATELIMITED error
// code". 429 is accepted too, defensively.
//
// It buffers and restores resp.Body so the adapter's own non-200 handling can
// still log the GraphQL error payload.
func LinearRateLimitClassifier(resp *http.Response) (bool, time.Time) {
	if resp == nil {
		return false, time.Time{}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return true, linearResetAt(resp.Header)
	}
	if resp.StatusCode != http.StatusBadRequest || resp.Body == nil {
		return false, time.Time{}
	}
	// Bounded: a pathological 400 body must not become an unbounded
	// allocation just to check it for a rate-limit code.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw)) // restore exactly what was read
	if err != nil {
		return false, time.Time{}
	}
	var payload struct {
		Errors []struct {
			Extensions struct {
				Code string `json:"code"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false, time.Time{}
	}
	for _, e := range payload.Errors {
		if e.Extensions.Code == "RATELIMITED" {
			return true, linearResetAt(resp.Header)
		}
	}
	return false, time.Time{}
}

// linearResetAt reads Linear's reset headers, which are UTC epoch
// MILLISECONDS. When both the request and complexity budgets are exhausted the
// later reset governs — returning early would admit callers the other limit
// still rejects.
func linearResetAt(h http.Header) time.Time {
	var latest time.Time
	consider := func(remainingKey, resetKey string) {
		if h.Get(remainingKey) != "0" {
			return
		}
		ms, err := strconv.ParseInt(h.Get(resetKey), 10, 64)
		if err != nil || ms <= 0 {
			return
		}
		t := time.UnixMilli(ms).UTC()
		if t.After(latest) {
			latest = t
		}
	}
	consider("X-RateLimit-Requests-Remaining", "X-RateLimit-Requests-Reset")
	consider("X-RateLimit-Complexity-Remaining", "X-RateLimit-Complexity-Reset")
	return latest
}
