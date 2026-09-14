package outbox

import "time"

// SetNow overrides this Outbox's clock for tests and returns the previous
// value so the caller can restore it. Production code always uses
// time.Now; tests inject a controllable clock instead of sleeping to
// exercise backoff/Due scheduling deterministically.
func (o *Outbox) SetNow(fn func() time.Time) (prev func() time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	prev = o.now
	o.now = fn
	return prev
}
