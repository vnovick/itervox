// Package rules owns Itervox's layered rules (#157): short instructions an
// operator writes once, in JSONC, and that every agent run follows.
//
//	user          ~/.itervox/rules.json        (never committed)
//	project       .itervox/rules.json          (committed)
//	project local .itervox/rules.local.json    (gitignored)
//
// At dispatch the daemon reads the layers again, merges them (a later layer
// replaces an earlier rule with the same id, except a user `must` rule, which
// is locked), keeps the rules in scope for the run's project, profile and
// backend, and renders them as a `## Rules` block ahead of the profile's
// SOUL.md and INSTRUCTIONS.md. JSONC is read and written with the settings
// layers' reader and comment-preserving writer (internal/config).
package rules

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/config"
)

// Severity is how binding a rule is.
type Severity string

// Rule severities: a must rule is a requirement, a should rule a strong
// default.
const (
	SeverityMust   Severity = "must"
	SeverityShould Severity = "should"
)

// Scope narrows the runs a rule applies to. An empty list matches every run;
// a non-empty one matches when any entry matches (case-insensitive, with
// path.Match globs such as "web-*"). All three lists must match.
type Scope struct {
	// Projects names the projects the rule applies to: the directory that
	// holds WORKFLOW.md, or a tracker.project_slug entry.
	Projects []string `json:"projects,omitempty"`
	// Profiles names agent profiles; a run without a named profile is
	// matched as "default".
	Profiles []string `json:"profiles,omitempty"`
	// Backends names agent backends ("claude", "codex").
	Backends []string `json:"backends,omitempty"`
}

// IsZero reports whether the scope matches every run.
func (s Scope) IsZero() bool {
	return len(s.Projects) == 0 && len(s.Profiles) == 0 && len(s.Backends) == 0
}

// Rule is one rule as a rules file holds it. The JSON field order is the
// order the editor writes a new rule in.
type Rule struct {
	ID          string   `json:"id"`
	Severity    Severity `json:"severity"`
	Text        string   `json:"text"`
	Description string   `json:"description,omitempty"`
	Scope       Scope    `json:"scope,omitzero"`
}

// SchemaURL is where the rules JSON Schema is published
// (site/public/schema/rules.schema.json).
const SchemaURL = "https://itervox.dev/schema/rules.schema.json"

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

var (
	topKeys   = []string{"$schema", "rules"}
	ruleKeys  = []string{"id", "severity", "text", "description", "scope"}
	scopeKeys = []string{"projects", "profiles", "backends"}
)

// Parse reads a rules file: a JSONC object with a "rules" array (and an
// optional "$schema"). Empty input is a file with no rules. Every problem is
// an error naming where it is ("rules[2].severity: ...").
func Parse(raw []byte) ([]Rule, error) {
	if strings.TrimSpace(string(raw)) == "" {
		return nil, nil
	}
	v, err := config.ParseJSONC(raw)
	if err != nil {
		return nil, err
	}
	top, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New(`a rules file must be a JSON object with a "rules" array`)
	}
	if err := onlyKeys(top, topKeys, ""); err != nil {
		return nil, err
	}
	if s, ok := top["$schema"]; ok {
		if _, isString := s.(string); !isString {
			return nil, errors.New(`"$schema" must be a string`)
		}
	}
	rawRules, ok := top["rules"]
	if !ok || rawRules == nil {
		return nil, nil
	}
	list, ok := rawRules.([]any)
	if !ok {
		return nil, errors.New(`"rules" must be an array`)
	}
	out := make([]Rule, 0, len(list))
	seen := map[string]int{}
	for i, item := range list {
		at := fmt.Sprintf("rules[%d]", i)
		r, err := parseRule(item, at)
		if err != nil {
			return nil, err
		}
		if j, dup := seen[strings.ToLower(r.ID)]; dup {
			return nil, fmt.Errorf("%s.id: %q is already used by rules[%d]", at, r.ID, j)
		}
		seen[strings.ToLower(r.ID)] = i
		out = append(out, r)
	}
	return out, nil
}

func parseRule(item any, at string) (Rule, error) {
	m, ok := item.(map[string]any)
	if !ok {
		return Rule{}, fmt.Errorf("%s: must be an object", at)
	}
	if err := onlyKeys(m, ruleKeys, at+"."); err != nil {
		return Rule{}, err
	}
	var r Rule
	var err error
	if r.ID, err = stringField(m, "id", at, true); err != nil {
		return Rule{}, err
	}
	if !idRe.MatchString(r.ID) {
		return Rule{}, fmt.Errorf("%s.id: %q must start with a letter or digit and use only letters, digits, '.', '_' and '-' (64 at most)", at, r.ID)
	}
	if r.Text, err = stringField(m, "text", at, true); err != nil {
		return Rule{}, err
	}
	sev, err := stringField(m, "severity", at, true)
	if err != nil {
		return Rule{}, err
	}
	r.Severity = Severity(sev)
	if r.Severity != SeverityMust && r.Severity != SeverityShould {
		return Rule{}, fmt.Errorf(`%s.severity: must be "must" or "should", not %q`, at, sev)
	}
	if r.Description, err = stringField(m, "description", at, false); err != nil {
		return Rule{}, err
	}
	if sc, ok := m["scope"]; ok && sc != nil {
		sm, ok := sc.(map[string]any)
		if !ok {
			return Rule{}, fmt.Errorf("%s.scope: must be an object", at)
		}
		if err := onlyKeys(sm, scopeKeys, at+".scope."); err != nil {
			return Rule{}, err
		}
		for _, k := range scopeKeys {
			list, err := stringList(sm[k], fmt.Sprintf("%s.scope.%s", at, k))
			if err != nil {
				return Rule{}, err
			}
			switch k {
			case "projects":
				r.Scope.Projects = list
			case "profiles":
				r.Scope.Profiles = list
			case "backends":
				r.Scope.Backends = list
			}
		}
	}
	return r, nil
}

func onlyKeys(m map[string]any, allowed []string, prefix string) error {
	for k := range m {
		if !slices.Contains(allowed, k) {
			return fmt.Errorf("%s%s: unknown field (allowed: %s)", prefix, k, strings.Join(allowed, ", "))
		}
	}
	return nil
}

func stringField(m map[string]any, key, at string, required bool) (string, error) {
	v, ok := m[key]
	if !ok || v == nil {
		if required {
			return "", fmt.Errorf("%s.%s: required", at, key)
		}
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s.%s: must be a string", at, key)
	}
	s = strings.TrimSpace(s)
	if required && s == "" {
		return "", fmt.Errorf("%s.%s: must not be empty", at, key)
	}
	return s, nil
}

func stringList(v any, at string) ([]string, error) {
	if v == nil {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s: must be an array of strings", at)
	}
	out := make([]string, 0, len(list))
	for i, e := range list {
		s, ok := e.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return nil, fmt.Errorf("%s[%d]: must be a non-empty string", at, i)
		}
		out = append(out, strings.TrimSpace(s))
	}
	return out, nil
}
