package memory

import (
	"cmp"
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/vnovick/itervox/internal/logging"
)

// DefaultProfile is what an entry's profiles list names a run without a
// named profile by (the same name the rules use).
const DefaultProfile = "default"

// Target is the run a memory selection is made for.
type Target struct {
	// Profile is the run's agent profile; "" is matched as "default".
	Profile string
	// Title, Description and Labels are the issue's.
	Title       string
	Description string
	Labels      []string
}

// maxTargetBytes bounds how much of an issue description is scored.
const maxTargetBytes = 32 << 10

// Selection is the memory one run gets: pinned entries first, then the
// ranked rest that fit in the budget.
type Selection struct {
	Entries []Entry
	// Dropped counts the active, in-scope entries the budget left out.
	Dropped int
	// Tokens is the estimated size of the rendered block.
	Tokens int
	// OverBudget is set when the pinned entries alone exceed the budget
	// (they are included anyway).
	OverBudget bool
}

// ApproxTokens estimates the tokens of s the way the context-budget report
// does: a token per four bytes, rounded up.
func ApproxTokens(s string) int { return (len(s) + 3) / 4 }

// blockHeader opens the rendered `## Memory` block.
const blockHeader = "## Memory\n\nWhat earlier runs and the people you work with want remembered. " +
	"Treat each item as context, not as a rule. If one is wrong or out of date for this task, " +
	"say so in your handoff."

// Applies reports whether the entry is used for a run of profile: it is
// active and its profiles list (if any) names the profile.
func (e Entry) Applies(profile string) bool {
	if e.Status != StatusActive {
		return false
	}
	if len(e.Profiles) == 0 {
		return true
	}
	profile = strings.ToLower(cmp.Or(profile, DefaultProfile))
	for _, p := range e.Profiles {
		p = strings.ToLower(p)
		if p == profile {
			return true
		}
		if ok, err := path.Match(p, profile); err == nil && ok {
			return true
		}
	}
	return false
}

// Select picks the entries a run of t gets from entries (both files'), within
// budget tokens (the rendered block's estimated size). Pinned entries are
// always included; the rest are ranked by lexical overlap with the issue and
// profile (ties: more used, then more recently updated, then id) and added
// in rank order while they fit. The first entry that does not fit ends the
// selection, so overflow always drops the lowest-ranked entries first.
// Entries sharing an id (case-insensitive) count once, the project file's
// winning.
func Select(entries []Entry, t Target, budget int) Selection {
	var pinned, rest []scored
	terms := targetTerms(t)
	seen := map[string]bool{}
	ordered := slices.Clone(entries)
	// Project entries first so they win an id clash.
	slices.SortStableFunc(ordered, func(a, b Entry) int {
		return cmp.Compare(scopeRank(a.Scope), scopeRank(b.Scope))
	})
	for _, e := range ordered {
		key := strings.ToLower(e.ID)
		if seen[key] || !e.Applies(t.Profile) {
			continue
		}
		seen[key] = true
		s := scored{Entry: e, score: score(e, t, terms)}
		if e.Pinned {
			pinned = append(pinned, s)
		} else {
			rest = append(rest, s)
		}
	}
	slices.SortStableFunc(pinned, byRank)
	slices.SortStableFunc(rest, byRank)

	var sel Selection
	if len(pinned)+len(rest) == 0 {
		return sel
	}
	used := ApproxTokens(blockHeader)
	for _, s := range pinned {
		sel.Entries = append(sel.Entries, s.Entry)
		used += ApproxTokens(line(s.Entry))
	}
	if used > budget {
		sel.OverBudget = true
	}
	for i, s := range rest {
		cost := ApproxTokens(line(s.Entry))
		if used+cost > budget {
			sel.Dropped = len(rest) - i
			break
		}
		sel.Entries = append(sel.Entries, s.Entry)
		used += cost
	}
	if len(sel.Entries) > 0 {
		sel.Tokens = used
	}
	return sel
}

type scored struct {
	Entry
	score int
}

func byRank(a, b scored) int {
	return cmp.Or(
		cmp.Compare(b.score, a.score),
		cmp.Compare(b.UseCount, a.UseCount),
		b.Updated.Compare(a.Updated),
		cmp.Compare(a.ID, b.ID),
	)
}

func scopeRank(s Scope) int {
	if s == ScopeProject {
		return 0
	}
	return 1
}

// score is the entry's lexical relevance to the run: one point per distinct
// word of its text and tags that the issue (title, description, labels) or
// the profile name also uses, two more per tag that names a label or an
// issue word, and one more when the entry names the run's profile
// explicitly.
func score(e Entry, t Target, terms map[string]bool) int {
	n := 0
	for w := range words(e.Text + " " + strings.Join(e.Tags, " ")) {
		if terms[w] {
			n++
		}
	}
	for _, tag := range e.Tags {
		tag = strings.ToLower(tag)
		if terms[tag] || slices.ContainsFunc(t.Labels, func(l string) bool { return strings.EqualFold(l, tag) }) {
			n += 2
		}
	}
	if t.Profile != "" && slices.ContainsFunc(e.Profiles, func(p string) bool { return strings.EqualFold(p, t.Profile) }) {
		n++
	}
	return n
}

func targetTerms(t Target) map[string]bool {
	desc := t.Description
	if len(desc) > maxTargetBytes {
		desc = desc[:maxTargetBytes]
	}
	return words(strings.Join([]string{t.Title, desc, strings.Join(t.Labels, " "), t.Profile}, " "))
}

// stopWords are common words that say nothing about relevance.
var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "are": true, "but": true, "not": true, "you": true,
	"all": true, "any": true, "can": true, "has": true, "have": true, "this": true, "that": true,
	"with": true, "from": true, "they": true, "will": true, "when": true, "what": true, "which": true,
	"into": true, "than": true, "then": true, "them": true, "use": true, "uses": true, "used": true,
	"its": true, "was": true, "were": true, "been": true, "our": true, "out": true, "one": true,
	"should": true, "would": true, "could": true, "does": true, "only": true, "also": true, "just": true,
	"before": true, "after": true, "there": true, "their": true, "about": true, "every": true,
}

// words returns the distinct lowercase words of s (letters and digits, at
// least three long), stop words left out.
func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(w) >= 3 && !stopWords[w] {
			out[w] = true
		}
	}
	return out
}

// line renders one entry as a list item. The text is redacted again here:
// a secret typed into a file by hand never reaches a prompt either.
func line(e Entry) string {
	text := strings.ReplaceAll(strings.TrimSpace(logging.RedactString(e.Text)), "\n", "\n  ")
	return "- [" + string(e.Type) + "] " + text + " (" + e.ID + ")\n"
}

// Block renders the selection as the `## Memory` prompt block, "" when it
// is empty.
func (s Selection) Block() string {
	if len(s.Entries) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(blockHeader)
	b.WriteString("\n\n")
	for _, e := range s.Entries {
		b.WriteString(line(e))
	}
	return strings.TrimRight(b.String(), "\n")
}

// IDs are the ids of the selected entries in block order.
func (s Selection) IDs() []string {
	if len(s.Entries) == 0 {
		return nil
	}
	ids := make([]string, len(s.Entries))
	for i, e := range s.Entries {
		ids[i] = e.ID
	}
	return ids
}

// FileError is a memory file that contributed nothing because it could not
// be read or parsed.
type FileError struct {
	Scope   Scope  `json:"scope"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Resolve reads both files for a workflow and selects the memory for a run
// of t within budget. It reads the files on every call, so an edit applies
// to the next run without a restart.
func Resolve(workflowPath string, t Target, budget int) (Selection, []FileError) {
	var all []Entry
	var errs []FileError
	for _, f := range ReadAll(workflowPath) {
		if f.Err != nil {
			errs = append(errs, FileError{Scope: f.Scope, Path: f.Path, Message: f.Err.Error()})
			continue
		}
		all = append(all, f.Entries...)
	}
	return Select(all, t, budget), errs
}
