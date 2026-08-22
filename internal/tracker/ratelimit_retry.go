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

// DoWithRateLimitRetry sends req, transparently retrying HTTP 429 with a
// bounded, Retry-After-aware backoff.
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
func DoWithRateLimitRetry(ctx context.Context, client *http.Client, req *http.Request, adapter string) (*http.Response, error) {
	resp, err := client.Do(req)
	for attempt := range MaxRateLimitRetries {
		if err != nil || resp == nil || !isRateLimited(resp) {
			return resp, err
		}
		wait := rateLimitBackoff(attempt, ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now()))
		retryable, rewindErr := rewindRequest(req)
		if !retryable {
			if rewindErr != nil {
				slog.Warn("tracker: cannot replay request body to retry a rate-limited call",
					"adapter", adapter, "error", rewindErr)
			}
			return resp, err
		}
		// Drain and close so the connection is reusable for the retry.
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
	return resp, err
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

// isRateLimited reports whether resp is a rate-limit response.
//
// 429 is the obvious one. GitHub, however, signals BOTH its primary and its
// secondary rate limits with **403**, distinguished from a genuine
// authorization failure by `x-ratelimit-remaining: 0` or the presence of a
// `retry-after` header. Matching 429 alone left the adapter that fans out one
// request per issue — the one most likely to hit a limit — without any
// backoff at all.
//
// A 403 with neither signal is a real permission error and must NOT be
// retried: retrying an auth failure four times just delays a clear error.
func isRateLimited(resp *http.Response) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if resp.StatusCode != http.StatusForbidden {
		return false
	}
	if resp.Header.Get("Retry-After") != "" {
		return true
	}
	return resp.Header.Get("X-RateLimit-Remaining") == "0"
}
