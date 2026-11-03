// Package memory owns Itervox's managed memory (#202): short facts,
// preferences, decisions and lessons that carry from one run to the next, so
// an operator who corrects an agent once does not have to repeat it on the
// next issue.
//
//	private        ~/.itervox/memory.json   (never committed)
//	project shared .itervox/memory.json     (committed)
//
// Both files hold the same JSON shape (a "memories" array, schema published
// at SchemaURL). At dispatch the daemon reads them again, keeps the active
// entries in scope for the run's profile, always includes the pinned ones,
// ranks the rest against the issue with simple lexical scoring and adds them
// within a token budget, and renders the result as a `## Memory` block after
// the `## Rules` block and ahead of the profile's SOUL.md and INSTRUCTIONS.md.
//
// Every save redacts secrets (internal/logging's RedactString) and rejects
// entries over the size caps. The daemon rewrites the files (it records
// lastUsed/useCount), so comments in them are not preserved.
package memory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/logging"
)

// Type is what kind of thing an entry remembers.
type Type string

// Entry types.
const (
	TypePreference Type = "preference"
	TypeFact       Type = "fact"
	TypeDecision   Type = "decision"
	TypeLesson     Type = "lesson"
)

// Types lists the entry types.
var Types = []Type{TypePreference, TypeFact, TypeDecision, TypeLesson}

// Status is where an entry is in its life. Only active entries reach a
// prompt: a proposed entry (a lesson Itervox drafted, #203) waits for a
// human to approve it, and an archived one is kept but no longer used.
type Status string

// Entry statuses.
const (
	StatusActive   Status = "active"
	StatusProposed Status = "proposed"
	StatusArchived Status = "archived"
)

// Statuses lists the entry statuses.
var Statuses = []Status{StatusActive, StatusProposed, StatusArchived}

// Scope is which file holds an entry, and so who shares it.
type Scope string

// Entry scopes.
const (
	// ScopePrivate is ~/.itervox/memory.json: only the user.
	ScopePrivate Scope = "private"
	// ScopeProject is .itervox/memory.json next to WORKFLOW.md: everyone on
	// the repository (the file is committed).
	ScopeProject Scope = "project"
)

// Scopes lists the scopes, private first.
var Scopes = []Scope{ScopePrivate, ScopeProject}

// Source is an entry's provenance: where it came from and the evidence for
// it.
type Source struct {
	// Kind says how the entry was made: "manual" (written by a person),
	// "lesson" (proposed by Itervox, #203), "import".
	Kind string `json:"kind,omitempty"`
	// Issue is the tracker identifier the entry came from.
	Issue string `json:"issue,omitempty"`
	// RunIDs are the runs the entry came from.
	RunIDs []string `json:"runIds,omitempty"`
	// Links are PR, comment or other URLs backing the entry.
	Links []string `json:"links,omitempty"`
}

// IsZero reports whether the source records nothing.
func (s Source) IsZero() bool {
	return s.Kind == "" && s.Issue == "" && len(s.RunIDs) == 0 && len(s.Links) == 0
}

// Entry is one memory as a memory file holds it. The JSON field order is
// the order the daemon writes an entry in.
type Entry struct {
	ID    string `json:"id"`
	Type  Type   `json:"type"`
	Text  string `json:"text"`
	Scope Scope  `json:"scope"`
	// Profiles narrows the entry to runs of these agent profiles (globs
	// allowed, a run without a named profile is "default"); empty: every
	// run.
	Profiles []string `json:"profiles,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	// Pinned entries are always included in a run's memory block.
	Pinned  bool      `json:"pinned,omitempty"`
	Status  Status    `json:"status"`
	Source  Source    `json:"source,omitzero"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	// LastUsed / UseCount are maintained by the daemon: each run that had
	// the entry in its prompt bumps them (through the orchestrator's event
	// loop).
	LastUsed *time.Time `json:"lastUsed,omitempty"`
	UseCount int        `json:"useCount,omitempty"`
	// Lesson is a lesson's review record (#203): what it applies to, what
	// went wrong and what worked, how sure Itervox is and how often it was
	// seen. Set on lessons Itervox proposes; nil on other entries.
	Lesson *Lesson `json:"lesson,omitempty"`
}

// SchemaURL is where the memory JSON Schema is published
// (site/public/schema/memory.schema.json).
const SchemaURL = "https://itervox.dev/schema/memory.schema.json"

// Size caps. An entry over a cap is rejected, never truncated: a cut-off
// memory would tell an agent something nobody wrote.
const (
	// MaxTextBytes caps an entry's text.
	MaxTextBytes = 2000
	// MaxListItems caps each list (profiles, tags, source run IDs, links).
	MaxListItems = 32
	// MaxItemBytes caps one list item.
	MaxItemBytes = 512
	// MaxEntries caps the entries one file holds.
	MaxEntries = 1000
)

// Errors returned by the store; the API maps them to status codes.
var (
	// ErrNotFound: no entry has the id.
	ErrNotFound = errors.New("memory: no entry with that id")
	// ErrInvalid wraps an entry or file that does not validate.
	ErrInvalid = errors.New("memory: invalid entry")
	// ErrTooLarge wraps an entry over a size cap.
	ErrTooLarge = errors.New("memory: entry too large")
	// ErrConflict: the id is already used, or the entry is already where a
	// move would put it.
	ErrConflict = errors.New("memory: conflict")
	// ErrScopeOff: the scope has no file (the private file is off, or the
	// daemon has no workflow path).
	ErrScopeOff = errors.New("memory: that scope has no file")
)

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

var (
	topKeys   = []string{"$schema", "memories"}
	entryKeys = []string{"id", "type", "text", "scope", "profiles", "tags", "pinned", "status", "source", "created", "updated", "lastUsed", "useCount", "lesson"}
)

// fileDoc is a memory file as written.
type fileDoc struct {
	Schema   string  `json:"$schema,omitempty"`
	Memories []Entry `json:"memories"`
}

// Parse reads a memory file of the given scope: a JSON object (comments
// allowed) with a "memories" array. Empty input is a file with no entries.
// An entry's scope is the file's, whatever the entry says. Every problem is
// an error naming where it is ("memories[2].type: ...").
func Parse(raw []byte, scope Scope) ([]Entry, error) {
	if strings.TrimSpace(string(raw)) == "" {
		return nil, nil
	}
	v, err := config.ParseJSONC(raw)
	if err != nil {
		return nil, err
	}
	top, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New(`a memory file must be a JSON object with a "memories" array`)
	}
	if err := onlyKeys(top, topKeys, ""); err != nil {
		return nil, err
	}
	raws, ok := top["memories"]
	if !ok || raws == nil {
		return nil, nil
	}
	list, ok := raws.([]any)
	if !ok {
		return nil, errors.New(`"memories" must be an array`)
	}
	if len(list) > MaxEntries {
		return nil, fmt.Errorf(`"memories": %d entries, at most %d`, len(list), MaxEntries)
	}
	out := make([]Entry, 0, len(list))
	seen := map[string]int{}
	for i, item := range list {
		at := fmt.Sprintf("memories[%d]", i)
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: must be an object", at)
		}
		if err := onlyKeys(m, entryKeys, at+"."); err != nil {
			return nil, err
		}
		if l, ok := m["lesson"].(map[string]any); ok {
			if err := onlyKeys(l, lessonKeys, at+".lesson."); err != nil {
				return nil, err
			}
		}
		b, _ := json.Marshal(m)
		var e Entry
		dec := json.NewDecoder(bytes.NewReader(b))
		if err := dec.Decode(&e); err != nil {
			return nil, fmt.Errorf("%s: %w", at, err)
		}
		e.Scope = scope
		if err := validate(e); err != nil {
			return nil, fmt.Errorf("%s.%w", at, err)
		}
		key := strings.ToLower(e.ID)
		if j, dup := seen[key]; dup {
			return nil, fmt.Errorf("%s.id: %q is already used by memories[%d]", at, e.ID, j)
		}
		seen[key] = i
		out = append(out, e)
	}
	return out, nil
}

// encode renders entries as a memory file.
func encode(entries []Entry) []byte {
	if entries == nil {
		entries = []Entry{}
	}
	b, _ := json.MarshalIndent(fileDoc{Schema: SchemaURL, Memories: entries}, "", "  ")
	return append(b, '\n')
}

// fieldError is a validation problem on one field; Parse prefixes its
// position, the API returns it as is.
type fieldError struct {
	field, msg string
	tooLarge   bool
}

func (e fieldError) Error() string { return e.field + ": " + e.msg }

func (e fieldError) Unwrap() error {
	if e.tooLarge {
		return ErrTooLarge
	}
	return ErrInvalid
}

// validate checks an entry against the schema and the size caps.
func validate(e Entry) error {
	if !idRe.MatchString(e.ID) {
		return fieldError{"id", fmt.Sprintf("%q must start with a letter or digit and use only letters, digits, '.', '_' and '-' (64 at most)", e.ID), false}
	}
	if !slices.Contains(Types, e.Type) {
		return fieldError{"type", fmt.Sprintf(`must be "preference", "fact", "decision" or "lesson", not %q`, e.Type), false}
	}
	if strings.TrimSpace(e.Text) == "" {
		return fieldError{"text", "must not be empty", false}
	}
	if len(e.Text) > MaxTextBytes {
		return fieldError{"text", fmt.Sprintf("is %d bytes, at most %d", len(e.Text), MaxTextBytes), true}
	}
	if !slices.Contains(Scopes, e.Scope) {
		return fieldError{"scope", fmt.Sprintf(`must be "private" or "project", not %q`, e.Scope), false}
	}
	if !slices.Contains(Statuses, e.Status) {
		return fieldError{"status", fmt.Sprintf(`must be "active", "proposed" or "archived", not %q`, e.Status), false}
	}
	if e.UseCount < 0 {
		return fieldError{"useCount", "must not be negative", false}
	}
	for name, list := range map[string][]string{
		"profiles": e.Profiles, "tags": e.Tags, "source.runIds": e.Source.RunIDs, "source.links": e.Source.Links,
	} {
		if len(list) > MaxListItems {
			return fieldError{name, fmt.Sprintf("has %d items, at most %d", len(list), MaxListItems), true}
		}
		for i, s := range list {
			if strings.TrimSpace(s) == "" {
				return fieldError{fmt.Sprintf("%s[%d]", name, i), "must be a non-empty string", false}
			}
			if len(s) > MaxItemBytes {
				return fieldError{fmt.Sprintf("%s[%d]", name, i), fmt.Sprintf("is %d bytes, at most %d", len(s), MaxItemBytes), true}
			}
		}
	}
	for name, s := range map[string]string{"source.kind": e.Source.Kind, "source.issue": e.Source.Issue} {
		if len(s) > MaxItemBytes {
			return fieldError{name, fmt.Sprintf("is %d bytes, at most %d", len(s), MaxItemBytes), true}
		}
	}
	return validateLesson(e.Lesson)
}

// scrub redacts secrets from every free-text field of an entry and trims
// it. Called on every save, before validation, so a secret never reaches
// the file.
func scrub(e Entry) Entry {
	e.Text = strings.TrimSpace(logging.RedactString(e.Text))
	e.Tags = scrubList(e.Tags)
	e.Profiles = scrubList(e.Profiles)
	e.Source.Kind = strings.TrimSpace(logging.RedactString(e.Source.Kind))
	e.Source.Issue = strings.TrimSpace(logging.RedactString(e.Source.Issue))
	e.Source.RunIDs = scrubList(e.Source.RunIDs)
	e.Source.Links = scrubList(e.Source.Links)
	e.Lesson = scrubLesson(e.Lesson)
	return e
}

// scrubList redacts and trims each item, drops empty ones and duplicates
// (case-insensitive), and returns nil for an empty result.
func scrubList(in []string) []string {
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(logging.RedactString(s))
		if s == "" || slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, s) }) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// checkRawSize rejects text over the cap before redaction, so what is
// rejected is what the person sent.
func checkRawSize(text string) error {
	if n := len(strings.TrimSpace(text)); n > MaxTextBytes {
		return fieldError{"text", fmt.Sprintf("is %d bytes, at most %d", n, MaxTextBytes), true}
	}
	return nil
}

func onlyKeys(m map[string]any, allowed []string, prefix string) error {
	for k := range m {
		if !slices.Contains(allowed, k) {
			return fmt.Errorf("%s%s: unknown field (allowed: %s)", prefix, k, strings.Join(allowed, ", "))
		}
	}
	return nil
}
