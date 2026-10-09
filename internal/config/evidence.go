package config

import (
	"regexp"
	"strings"
)

// EvidenceCheckCI is the require_evidence entry satisfied by the run's pull
// request having only passing CI checks, rather than by an evidence file
// entry (#80).
const EvidenceCheckCI = "ci"

// evidenceCheckNameRe is the shape of a require_evidence entry.
var evidenceCheckNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// NormalizeEvidenceChecks lower-cases and trims require_evidence entries,
// dropping blanks and duplicates while keeping order. Nil when none remain.
func NormalizeEvidenceChecks(checks []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range checks {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}
