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

// DefaultVerifyTimeoutMs bounds a verify check that names no timeout_ms
// (10 minutes).
const DefaultVerifyTimeoutMs = 600_000

// VerifyCheck is one agent.profiles.<name>.verify entry: a check command
// the daemon runs itself in the worktree after the agent's run, signed
// (internal/attest). A check with the same name as a require_evidence entry
// satisfies it, and the agent's own evidence file is not read for it.
type VerifyCheck struct {
	// Name is the check's name (the require_evidence name shape; not "ci").
	Name string
	// Run is the command line, run by bash with pipefail in the worktree.
	Run string
	// TimeoutMs bounds the run; the process group is killed after it.
	// Defaults to DefaultVerifyTimeoutMs.
	TimeoutMs int
}

// verifyChecksField reads agent.profiles.<profile>.verify: a list of
// {name, run, timeout_ms} maps. Malformed entries are errors, never
// dropped: a check silently ignored would let unverified work through.
func verifyChecksField(m map[string]any, profile string) ([]VerifyCheck, error) {
	v, ok := m["verify"]
	if !ok || v == nil {
		return nil, nil
	}
	raw, isList := v.([]any)
	if !isList {
		return nil, fmt.Errorf("config: agent.profiles.%s.verify must be a list of {name, run} checks, e.g. [{name: test, run: \"make test\"}] (got %v)", profile, v)
	}
	var out []VerifyCheck
	seen := map[string]bool{}
	for i, item := range raw {
		entry, isMap := item.(map[string]any)
		if !isMap {
			return nil, fmt.Errorf("config: agent.profiles.%s.verify[%d] must be a map with name and run (got %v)", profile, i, item)
		}
		for k := range entry {
			if k != "name" && k != "run" && k != "timeout_ms" {
				return nil, fmt.Errorf("config: agent.profiles.%s.verify[%d] has unknown key %q (want name, run, timeout_ms)", profile, i, k)
			}
		}
		name, _ := entry["name"].(string)
		name = strings.ToLower(strings.TrimSpace(name))
		run, _ := entry["run"].(string)
		run = strings.TrimSpace(run)
		switch {
		case !evidenceCheckNameRe.MatchString(name):
			return nil, fmt.Errorf("config: agent.profiles.%s.verify[%d].name %q must be a short name of lower-case letters, digits, '-' or '_' (e.g. test, lint)", profile, i, name)
		case name == EvidenceCheckCI:
			return nil, fmt.Errorf("config: agent.profiles.%s.verify[%d].name %q is reserved for the pull request's CI checks", profile, i, name)
		case seen[name]:
			return nil, fmt.Errorf("config: agent.profiles.%s.verify names %q twice", profile, name)
		case run == "":
			return nil, fmt.Errorf("config: agent.profiles.%s.verify[%d] (%s) needs a run command", profile, i, name)
		}
		seen[name] = true
		timeout := DefaultVerifyTimeoutMs
		if t, present := entry["timeout_ms"]; present {
			n, isInt := toInt(t)
			if !isInt || n <= 0 {
				return nil, fmt.Errorf("config: agent.profiles.%s.verify[%d].timeout_ms must be a positive number of milliseconds (got %v)", profile, i, t)
			}
			timeout = n
		}
		out = append(out, VerifyCheck{Name: name, Run: run, TimeoutMs: timeout})
	}
	return out, nil
}
