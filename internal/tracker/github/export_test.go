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

// ExtractBlockerNumbers exposes the body blocker parser to package tests.
func ExtractBlockerNumbers(body string) []string {
	var out []string
	for _, b := range extractBlockers(map[string]any{"body": body, "number": float64(0)}) {
		out = append(out, *b.ID)
	}
	return out
}
