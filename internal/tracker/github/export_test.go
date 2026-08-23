package github

import "time"

// SetNow overrides c's clock for tests, returning the previous func so the
// caller can restore it. Used to exercise blockerStateCacheTTL expiry in the
// blocker-state cache without sleeping in real time.
func (c *Client) SetNow(now func() time.Time) (prev func() time.Time) {
	prev = c.now
	c.now = now
	return prev
}
