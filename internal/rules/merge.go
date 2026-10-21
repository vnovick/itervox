package rules

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Effective is a rule in effect, with the layer it came from.
type Effective struct {
	Rule
	Layer string `json:"layer"`
	// Overrides names the lower layer whose rule with the same id this one
	// replaced; empty when it replaced none.
	Overrides string `json:"overrides,omitempty"`
}

// Conflict is a later layer's rule that was not applied because it tried to
// replace a user `must` rule, which no later layer can override.
type Conflict struct {
	ID string `json:"id"`
	// Layer is the layer whose rule was dropped.
	Layer string `json:"layer"`
	// KeptLayer is the layer whose rule stands.
	KeptLayer string `json:"keptLayer"`
	Message   string `json:"message"`
}

// LayerError is a layer that contributed no rules because its file could not
// be read or parsed.
type LayerError struct {
	Layer   string `json:"layer"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Set is the rule set one run uses.
type Set struct {
	Rules     []Effective
	Conflicts []Conflict
	Errors    []LayerError
}

// Merge merges the layers, lowest first. A rule replaces a lower layer's rule
// with the same id (case-insensitive) in place; a user `must` rule is locked,
// so a later rule with its id is dropped and reported as a Conflict (unless it
// is identical). Layers with an error are skipped and reported.
func Merge(layers []Layer) Set {
	var s Set
	index := map[string]int{}
	for _, l := range layers {
		if l.Err != nil {
			s.Errors = append(s.Errors, LayerError{Layer: l.Name, Path: l.Path, Message: l.Err.Error()})
			continue
		}
		for _, r := range l.Rules {
			key := strings.ToLower(r.ID)
			i, seen := index[key]
			if !seen {
				index[key] = len(s.Rules)
				s.Rules = append(s.Rules, Effective{Rule: r, Layer: l.Name})
				continue
			}
			prev := s.Rules[i]
			if prev.Layer == LayerUser && prev.Severity == SeverityMust && l.Name != LayerUser {
				if !sameRule(prev.Rule, r) {
					s.Conflicts = append(s.Conflicts, Conflict{
						ID: prev.ID, Layer: l.Name, KeptLayer: prev.Layer,
						Message: fmt.Sprintf("the %s rule %q was ignored: the user layer sets it as a must rule, which a later layer cannot override", l.Name, r.ID),
					})
				}
				continue
			}
			s.Rules[i] = Effective{Rule: r, Layer: l.Name, Overrides: prev.Layer}
		}
	}
	return s
}

func sameRule(a, b Rule) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// Target is the run a rule set is filtered for.
type Target struct {
	// Projects are the names the run's project goes by (see ProjectNames).
	Projects []string
	// Profile is the run's agent profile; "" is matched as "default".
	Profile string
	// Backend is the run's agent backend ("claude", "codex").
	Backend string
}

// DefaultProfile is what a scope's profiles list names a run without a named
// profile by.
const DefaultProfile = "default"

// ProjectNames are the names a rule's scope.projects can give the daemon's
// project: the directory holding WORKFLOW.md, and each tracker.project_slug
// entry (comma-separated).
func ProjectNames(workflowPath, projectSlug string) []string {
	var out []string
	if workflowPath != "" {
		if abs, err := filepath.Abs(workflowPath); err == nil {
			workflowPath = abs
		}
		if dir := filepath.Base(filepath.Dir(workflowPath)); dir != "" && dir != "." && dir != string(filepath.Separator) {
			out = append(out, dir)
		}
	}
	for _, s := range strings.Split(projectSlug, ",") {
		if s = strings.TrimSpace(s); s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// Applies reports whether the rule's scope matches the target.
func (r Rule) Applies(t Target) bool {
	profile := t.Profile
	if profile == "" {
		profile = DefaultProfile
	}
	return matchAny(r.Scope.Projects, t.Projects...) &&
		matchAny(r.Scope.Profiles, profile) &&
		matchAny(r.Scope.Backends, t.Backend)
}

// matchAny reports whether any pattern matches any name; an empty pattern
// list matches everything.
func matchAny(patterns []string, names ...string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		p = strings.ToLower(p)
		for _, n := range names {
			n = strings.ToLower(n)
			if n == "" {
				continue
			}
			if p == n {
				return true
			}
			if ok, err := path.Match(p, n); err == nil && ok {
				return true
			}
		}
	}
	return false
}

// For returns the set with only the rules in scope for the target. Conflicts
// and errors are kept: they describe the files, not the run.
func (s Set) For(t Target) Set {
	out := Set{Conflicts: s.Conflicts, Errors: s.Errors}
	for _, r := range s.Rules {
		if r.Applies(t) {
			out.Rules = append(out.Rules, r)
		}
	}
	return out
}

// Resolve reads the layers for a workflow and returns the rule set for the
// target. It reads the files every time it is called, so an edit applies to
// the next run without a restart.
func Resolve(workflowPath string, t Target) Set {
	return Merge(ReadLayers(workflowPath)).For(t)
}

// Ordered returns the rules in render order: must rules first, then should
// rules, each in merged order.
func (s Set) Ordered() []Effective {
	out := make([]Effective, 0, len(s.Rules))
	for _, sev := range []Severity{SeverityMust, SeverityShould} {
		for _, r := range s.Rules {
			if r.Severity == sev {
				out = append(out, r)
			}
		}
	}
	return out
}

// IDs are the ids of the set's rules in render order.
func (s Set) IDs() []string {
	ordered := s.Ordered()
	ids := make([]string, len(ordered))
	for i, r := range ordered {
		ids[i] = r.ID
	}
	return ids
}

// Hash identifies the set's content (each rule's id, severity and text, in
// render order): two runs with the same hash were given the same rules. ""
// for an empty set.
func (s Set) Hash() string {
	if len(s.Rules) == 0 {
		return ""
	}
	type item struct {
		ID       string   `json:"id"`
		Severity Severity `json:"severity"`
		Text     string   `json:"text"`
	}
	ordered := s.Ordered()
	items := make([]item, len(ordered))
	for i, r := range ordered {
		items[i] = item{ID: r.ID, Severity: r.Severity, Text: r.Text}
	}
	b, _ := json.Marshal(items)
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:8])
}

// Record is what a run's record keeps of the rule set it used.
type Record struct {
	Hash string
	IDs  []string
}

// Record returns the set's run record.
func (s Set) Record() Record {
	if len(s.Rules) == 0 {
		return Record{}
	}
	return Record{Hash: s.Hash(), IDs: s.IDs()}
}
