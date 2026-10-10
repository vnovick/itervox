package config

import (
	"fmt"
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

// evidenceChecksField reads agent.profiles.<profile>.require_evidence. Any
// value other than a list of names is an error rather than "off": a scalar
// such as `require_evidence: true` reads as turning the gate on, and
// silently ignoring it would let issues move on without the proof asked for.
func evidenceChecksField(m map[string]any, profile string) ([]string, error) {
	v, ok := m["require_evidence"]
	if !ok || v == nil {
		return nil, nil
	}
	raw, isList := v.([]any)
	if !isList {
		return nil, fmt.Errorf("config: agent.profiles.%s.require_evidence must be a list of check names, e.g. [test, lint, ci] (got %v)", profile, v)
	}
	checks := make([]string, 0, len(raw))
	for _, item := range raw {
		s, isString := item.(string)
		if !isString {
			return nil, fmt.Errorf("config: agent.profiles.%s.require_evidence entries must be names, e.g. [test, lint, ci] (got %v)", profile, item)
		}
		checks = append(checks, s)
	}
	return NormalizeEvidenceChecks(checks), nil
}
