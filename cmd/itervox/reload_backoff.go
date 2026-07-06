package main

import (
	"sync/atomic"
	"time"

	"github.com/vnovick/itervox/internal/server"
)

// configInvalidPtr publishes the current WORKFLOW.md reload failure (or
// clears it on success) so the snapshot function in run() can surface it
// to the dashboard / TUI banner. Atomic so the reload-loop goroutine in
// main() and the snapshot-builder closure in run() can read/write without
// a mutex. Nil when the daemon is on a valid config. T-26.
var configInvalidPtr atomic.Pointer[server.ConfigInvalidStatus]

// publishConfigInvalid marks the daemon as running on a stale config because
// the most recent reload tick failed validation. Pass nil to clear (called
// on successful reload).
func publishConfigInvalid(status *server.ConfigInvalidStatus) {
	configInvalidPtr.Store(status)
}

// loadConfigInvalid returns the current published failure status, or nil
// when the daemon is on a valid config.
func loadConfigInvalid() *server.ConfigInvalidStatus {
	return configInvalidPtr.Load()
}

// reloadBackoff computes the wait duration before the next config-reload
// retry. Used by the outer reload loop in main() when WORKFLOW.md fails to
// load or validate — see T-26.
//
// The previous behaviour was a flat 1s sleep, which means a tightly-broken
// WORKFLOW.md (e.g. left mid-edit) burns CPU re-parsing once a second
// indefinitely. With exponential backoff the daemon waits longer between
// retries, while still recovering quickly from transient state (like a
// half-written file caught between editor saves).
//
// Schedule (ms): 200, 400, 800, 1600, 3200, 6400, 12800, 25600, 30000, 30000, ...
//
//   - Base 200ms means the first retry is FASTER than the previous flat 1s
//     for transient errors (an editor mid-save).
//   - Doubles each attempt (`200ms << attempt`), so a chronically-broken
//     file eases off quickly.
//   - Capped at reloadBackoffMax (30s) so the daemon eventually checks
//     often enough that fixing WORKFLOW.md from the dashboard is felt.
//   - The caller decides max attempts (currently effectively unbounded —
//     the watcher's file-changed event resets attempt count via the outer
//     loop). 10 was the spec; we cap the wait, not the attempt count.
//
// attempt is 0-indexed: the first retry passes attempt=0, the second
// passes attempt=1, etc.
func reloadBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	const base = 200 * time.Millisecond
	const cap = reloadBackoffMax
	// Guard against the shift overflowing — anything past attempt=20 is
	// effectively saturated at cap anyway, so clamp early.
	if attempt > 20 {
		return cap
	}
	wait := base << attempt
	if wait > cap {
		return cap
	}
	return wait
}

// reloadBackoffMax bounds the exponential backoff so a wedged config doesn't
// stretch retry intervals into hours. 30s matches the SSE keepalive cadence
// and feels responsive enough that fixing WORKFLOW.md from the dashboard
// produces a daemon recovery within roughly one user-attention span.
const reloadBackoffMax = 30 * time.Second
