package workflow

import "time"

// SetDebounceInterval overrides the watcher's quiet period for tests and
// returns the previous value so the caller can restore it.
//
// The production default is deliberately seconds-long — it exists so a
// multi-part WORKFLOW.md edit coalesces into one daemon reload — which would
// make every watcher test wait that long for real. Tests shrink it instead of
// sleeping.
func SetDebounceInterval(d time.Duration) (prev time.Duration) {
	prev = debounceInterval
	debounceInterval = d
	return prev
}

// SetPollInterval overrides the watcher's stat interval for tests and returns
// the previous value. Callers must stop every Watch goroutine before
// restoring it.
func SetPollInterval(d time.Duration) (prev time.Duration) {
	prev = pollInterval
	pollInterval = d
	return prev
}
