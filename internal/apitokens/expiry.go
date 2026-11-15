package apitokens

import (
	"crypto/subtle"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// constantTimeEqual compares two hashes without an early exit.
func constantTimeEqual(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

// ParseExpiry parses an expiry given on the command line or in the
// dashboard: "" (never), a duration from now ("90d", "12h", "30m"), an
// RFC 3339 time or a date (YYYY-MM-DD, midnight UTC).
func ParseExpiry(raw string, now time.Time) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if days, ok := strings.CutSuffix(raw, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil && n > 0 {
			t := now.Add(time.Duration(n) * 24 * time.Hour).UTC()
			return &t, nil
		}
	}
	if d, err := time.ParseDuration(raw); err == nil {
		if d <= 0 {
			return nil, ErrExpiresInPast
		}
		t := now.Add(d).UTC()
		return &t, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			if !t.After(now) {
				return nil, ErrExpiresInPast
			}
			t = t.UTC()
			return &t, nil
		}
	}
	return nil, fmt.Errorf("apitokens: cannot parse expiry %q (use e.g. 90d, 12h, 2027-01-31 or an RFC 3339 time)", raw)
}
