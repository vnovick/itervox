package config

import (
	"log/slog"
	"maps"
	"strings"
)

// DefaultPlaybooksDir is where playbook files live, relative to WORKFLOW.md
// (#218). `itervox init` scaffolds one file per task type there.
const DefaultPlaybooksDir = ".itervox/playbooks"

// DefaultPlaybookLabels maps issue labels (lower-case) to the playbooks
// `itervox init` scaffolds (#218). Used when agent.playbooks.labels is not
// set; a configured map replaces it entirely.
var DefaultPlaybookLabels = map[string]string{
	"bug":           "bug-fix",
	"bugfix":        "bug-fix",
	"bug-fix":       "bug-fix",
	"regression":    "bug-fix",
	"feature":       "feature",
	"enhancement":   "feature",
	"refactor":      "refactor",
	"refactoring":   "refactor",
	"tech-debt":     "refactor",
	"perf":          "perf",
	"performance":   "perf",
	"investigation": "investigation",
	"spike":         "investigation",
	"research":      "investigation",
	"docs":          "docs",
	"documentation": "docs",
}

// PlaybooksConfig is agent.playbooks (#218): per-task-type playbooks
// inserted into a worker run's prompt after the profile's INSTRUCTIONS.md.
// Load-time config (no runtime setter, so not in the cfgMu allowlist); the
// playbook files themselves are read again for every run.
type PlaybooksConfig struct {
	// Enabled turns playbook selection on. Default true; with no playbook
	// files on disk nothing is attached either way.
	Enabled bool
	// Dir is the playbooks directory, relative to WORKFLOW.md when not
	// absolute. Default .itervox/playbooks.
	Dir string
	// Labels maps an issue label (matched case-insensitively) to a playbook
	// name. The first of an issue's labels that maps to an existing
	// playbook chooses it. Default DefaultPlaybookLabels.
	Labels map[string]string
	// Classify turns the keyword classifier on: when no label matches, the
	// title and description are scored against each playbook's keywords
	// and a confident winner is attached. Default true.
	Classify bool
}

// parsePlaybooksConfig reads agent.playbooks. Label keys are lower-cased
// and trimmed; entries with an empty key or value are dropped.
func parsePlaybooksConfig(m map[string]any) PlaybooksConfig {
	c := PlaybooksConfig{
		Enabled:  boolField(m, "enabled", true),
		Dir:      strings.TrimSpace(strField(m, "dir", DefaultPlaybooksDir)),
		Classify: boolField(m, "classify", true),
	}
	raw := stringMapField(m, "labels", nil)
	if raw == nil {
		c.Labels = maps.Clone(DefaultPlaybookLabels)
		return c
	}
	c.Labels = make(map[string]string, len(raw))
	for k, v := range raw {
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		if k == "" || v == "" {
			slog.Warn("config: agent.playbooks.labels entry ignored (empty label or playbook)", "label", k, "playbook", v)
			continue
		}
		c.Labels[k] = v
	}
	return c
}

// ValidEvidenceCheckName reports whether s is a well-formed require_evidence
// entry (#80): a short lower-case name. Playbook frontmatter (#218) is read
// at run time, so its entries are filtered with this instead of failing the
// config load.
func ValidEvidenceCheckName(s string) bool { return evidenceCheckNameRe.MatchString(s) }
