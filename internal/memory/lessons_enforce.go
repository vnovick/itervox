package memory

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Lessons round 2 (#217): a lesson that keeps coming back should be
// enforced, not just remembered. Once a lesson's evidence reaches
// memory.lessons.enforce_threshold, Itervox drafts the strongest
// enforcement it can (a lint rule, a type, a test, a CLAUDE.md / AGENTS.md
// rule, or a rules-layer entry) and records it on the lesson as a
// suggestion. Accepting it creates an "Enforce: <lesson>" issue in the
// tracker carrying the draft and the evidence, so an agent implements it
// like any other issue: nothing here ever commits code. A reviewer can also
// promote an approved lesson to a rule in a rules layer; the lesson is then
// archived with a pointer to the rule.

// EnforcementKind is what kind of enforcement a suggestion drafts.
type EnforcementKind string

// Enforcement kinds, strongest first: fix a repeated mistake at the highest
// level that works.
const (
	EnforceLint EnforcementKind = "lint"
	EnforceType EnforcementKind = "type"
	EnforceTest EnforcementKind = "test"
	// EnforceDocs is a CLAUDE.md or AGENTS.md rule.
	EnforceDocs EnforcementKind = "docs"
	// EnforceRule is an Itervox rules-layer entry.
	EnforceRule EnforcementKind = "rule"
)

// EnforcementKinds lists the kinds, strongest first.
var EnforcementKinds = []EnforcementKind{EnforceLint, EnforceType, EnforceTest, EnforceDocs, EnforceRule}

// EnforcementStatus is where a suggestion is in its review.
type EnforcementStatus string

// Enforcement statuses.
const (
	EnforcementSuggested EnforcementStatus = "suggested"
	// EnforcementAccepted: a reviewer accepted it and an "Enforce:" issue
	// was created.
	EnforcementAccepted EnforcementStatus = "accepted"
	// EnforcementDismissed: a reviewer turned it down; it is not suggested
	// again for this lesson.
	EnforcementDismissed EnforcementStatus = "dismissed"
)

// EnforcementStatuses lists the statuses.
var EnforcementStatuses = []EnforcementStatus{EnforcementSuggested, EnforcementAccepted, EnforcementDismissed}

// Enforcement is a lesson's enforcement suggestion and its review.
type Enforcement struct {
	Kind   EnforcementKind   `json:"kind"`
	Status EnforcementStatus `json:"status"`
	// Title is a short name for the enforcement ("Lint rule against …").
	Title string `json:"title,omitempty"`
	// Draft is the suggested enforcement, in words: what to add and where.
	Draft string `json:"draft"`
	// DraftedBy is "template" or "profile:<name>".
	DraftedBy   string     `json:"draftedBy,omitempty"`
	SuggestedAt time.Time  `json:"suggestedAt"`
	DecidedAt   *time.Time `json:"decidedAt,omitempty"`
	// Issue / IssueURL name the "Enforce:" issue an accepted suggestion
	// created.
	Issue    string `json:"issue,omitempty"`
	IssueURL string `json:"issueUrl,omitempty"`
}

var enforcementKeys = []string{"kind", "status", "title", "draft", "draftedBy", "suggestedAt", "decidedAt", "issue", "issueUrl"}

// RuleLink points from a lesson to the rule it was promoted to.
type RuleLink struct {
	// Layer is the rules layer ("user" or "project").
	Layer string `json:"layer"`
	// ID is the rule's id in that layer.
	ID string    `json:"id"`
	At time.Time `json:"at"`
}

var ruleLinkKeys = []string{"layer", "id", "at"}

func validateEnforcement(e *Enforcement) error {
	if e == nil {
		return nil
	}
	if !slices.Contains(EnforcementKinds, e.Kind) {
		return fieldError{"lesson.enforcement.kind", fmt.Sprintf("must be one of %s, not %q", quotedList(EnforcementKinds), e.Kind), false}
	}
	if !slices.Contains(EnforcementStatuses, e.Status) {
		return fieldError{"lesson.enforcement.status", fmt.Sprintf("must be one of %s, not %q", quotedList(EnforcementStatuses), e.Status), false}
	}
	if strings.TrimSpace(e.Draft) == "" {
		return fieldError{"lesson.enforcement.draft", "must not be empty", false}
	}
	if len(e.Draft) > MaxTextBytes {
		return fieldError{"lesson.enforcement.draft", fmt.Sprintf("is %d bytes, at most %d", len(e.Draft), MaxTextBytes), true}
	}
	for name, s := range map[string]string{
		"lesson.enforcement.title": e.Title, "lesson.enforcement.draftedBy": e.DraftedBy,
		"lesson.enforcement.issue": e.Issue, "lesson.enforcement.issueUrl": e.IssueURL,
	} {
		if len(s) > MaxItemBytes {
			return fieldError{name, fmt.Sprintf("is %d bytes, at most %d", len(s), MaxItemBytes), true}
		}
	}
	return nil
}

func validateRuleLink(r *RuleLink) error {
	if r == nil {
		return nil
	}
	if strings.TrimSpace(r.Layer) == "" || len(r.Layer) > 32 {
		return fieldError{"lesson.rule.layer", "must name a rules layer", false}
	}
	if !idRe.MatchString(r.ID) {
		return fieldError{"lesson.rule.id", fmt.Sprintf("%q is not a rule id", r.ID), false}
	}
	return nil
}

// NeedsEnforcement reports whether a lesson has been seen threshold times
// and has no enforcement suggestion yet (a dismissed one is not suggested
// again). Archived lessons (promoted to a rule) need none.
func NeedsEnforcement(e Entry, threshold int) bool {
	return threshold > 0 && e.Type == TypeLesson && e.Status != StatusArchived &&
		e.Lesson != nil && e.Lesson.Enforcement == nil && e.Lesson.Rule == nil && e.Lesson.Evidence >= threshold
}

// TemplateEnforcement is the suggestion when no drafting profile is
// configured or the drafting call fails: a rules-layer entry carrying the
// lesson, with the ladder of stronger enforcements for whoever implements
// it. It never calls anything.
func TemplateEnforcement(e Entry) Enforcement {
	var b strings.Builder
	b.WriteString("Enforce this lesson at the highest level that works, in this order: a lint rule, a type, a test, a CLAUDE.md or AGENTS.md rule, and only then an Itervox rule.\n\n")
	b.WriteString("Lesson: ")
	b.WriteString(oneLine(e.Text))
	if e.Lesson != nil && e.Lesson.Situation != "" {
		b.WriteString("\nApplies when: ")
		b.WriteString(oneLine(e.Lesson.Situation))
	}
	b.WriteString("\n\nIf no check can catch it, add it as a project rule (severity should) so every run is told.")
	return Enforcement{
		Kind: EnforceRule, Status: EnforcementSuggested,
		Title: boundText("Rule: "+oneLine(e.Text), 200), Draft: boundText(b.String(), MaxTextBytes),
		DraftedBy: "template",
	}
}

// EnforcementIssueTitle is the title of an accepted suggestion's issue:
// "Enforce: <lesson>".
func EnforcementIssueTitle(e Entry) string {
	return boundText("Enforce: "+oneLine(e.Text), 200)
}

// EnforcementIssueBody is the body of an accepted suggestion's issue: the
// draft, the lesson and its evidence.
func EnforcementIssueBody(e Entry) string {
	var b strings.Builder
	enf := e.Lesson.Enforcement
	b.WriteString("Itervox suggested enforcing a lesson that keeps coming back. Implement the enforcement below; do not just repeat the lesson.\n\n")
	fmt.Fprintf(&b, "## Suggested enforcement (%s)\n\n%s\n\n", enf.Kind, strings.TrimSpace(enf.Draft))
	fmt.Fprintf(&b, "## Lesson\n\n%s\n\n", strings.TrimSpace(e.Text))
	b.WriteString("## Evidence\n\n")
	fmt.Fprintf(&b, "- Memory entry: `%s` (%s)\n", e.ID, e.Scope)
	fmt.Fprintf(&b, "- Trigger: %s, seen %d times\n", e.Lesson.Trigger, e.Lesson.Evidence)
	if e.Source.Issue != "" {
		fmt.Fprintf(&b, "- First seen on: %s\n", e.Source.Issue)
	}
	if e.Lesson.Correction != "" {
		fmt.Fprintf(&b, "- In their words: %s\n", oneLine(e.Lesson.Correction))
	}
	if len(e.Source.RunIDs) > 0 {
		fmt.Fprintf(&b, "- Runs: %s\n", strings.Join(e.Source.RunIDs, ", "))
	}
	for _, l := range e.Source.Links {
		fmt.Fprintf(&b, "- %s\n", l)
	}
	return b.String()
}

// Get reads the entry with id.
func (s Store) Get(id string) (Entry, error) {
	writeMu.Lock()
	defer writeMu.Unlock()
	fs := s.load()
	sc, i, ok := fs.find(id)
	if !ok {
		return Entry{}, ErrNotFound
	}
	return fs.byScope[sc].Entries[i], nil
}

// modifyLesson applies change to the lesson entry with id under the write
// lock. change returns an error to refuse (nothing is written) or false to
// leave the entry as it is.
func (s Store) modifyLesson(id string, change func(e *Entry, now time.Time) (bool, error)) (Entry, bool, error) {
	writeMu.Lock()
	defer writeMu.Unlock()
	fs := s.load()
	sc, i, ok := fs.find(id)
	if !ok {
		return Entry{}, false, ErrNotFound
	}
	file, err := fs.writable(sc)
	if err != nil {
		return Entry{}, false, err
	}
	e := file.Entries[i]
	if e.Type != TypeLesson || e.Lesson == nil {
		return Entry{}, false, fmt.Errorf("%w: %q is not a lesson", ErrConflict, id)
	}
	l := *e.Lesson
	e.Lesson = &l
	now := s.now()
	changed, err := change(&e, now)
	if err != nil || !changed {
		return file.Entries[i], false, err
	}
	e.Updated = now
	e = scrub(e)
	if err := validate(e); err != nil {
		return Entry{}, false, err
	}
	file.Entries[i] = e
	fs.dirty[sc] = true
	return e, true, fs.save()
}

// SuggestEnforcement records an enforcement suggestion on the lesson with
// id, unless it already has one (or was promoted to a rule): ok is false
// then and nothing is written.
func (s Store) SuggestEnforcement(id string, enf Enforcement) (Entry, bool, error) {
	return s.modifyLesson(id, func(e *Entry, now time.Time) (bool, error) {
		if e.Lesson.Enforcement != nil || e.Lesson.Rule != nil || e.Status == StatusArchived {
			return false, nil
		}
		enf.Status, enf.SuggestedAt, enf.DecidedAt = EnforcementSuggested, now, nil
		enf.Issue, enf.IssueURL = "", ""
		e.Lesson.Enforcement = &enf
		return true, nil
	})
}

// DecideEnforcement records the reviewer's decision on a suggestion:
// accepted (with the issue it created) or dismissed. ErrConflict when the
// lesson has no pending suggestion.
func (s Store) DecideEnforcement(id string, accepted bool, issue, issueURL string) (Entry, error) {
	e, _, err := s.modifyLesson(id, func(e *Entry, now time.Time) (bool, error) {
		cur := e.Lesson.Enforcement
		if cur == nil || cur.Status != EnforcementSuggested {
			return false, fmt.Errorf("%w: %q has no pending enforcement suggestion", ErrConflict, e.ID)
		}
		enf := *cur
		t := now
		enf.DecidedAt = &t
		enf.Status = EnforcementDismissed
		if accepted {
			enf.Status, enf.Issue, enf.IssueURL = EnforcementAccepted, boundText(issue, MaxItemBytes), boundText(issueURL, MaxItemBytes)
		}
		e.Lesson.Enforcement = &enf
		return true, nil
	})
	return e, err
}

// ArchiveLessonAsRule archives an approved (active) lesson that was promoted
// to a rule, pointing it at the rule. ErrConflict when the entry is not an
// active lesson.
func (s Store) ArchiveLessonAsRule(id string, link RuleLink) (Entry, error) {
	e, _, err := s.modifyLesson(id, func(e *Entry, now time.Time) (bool, error) {
		if e.Status != StatusActive {
			return false, fmt.Errorf("%w: %q is %s; only an approved lesson can become a rule", ErrConflict, e.ID, e.Status)
		}
		link.At = now
		e.Lesson.Rule = &link
		e.Status = StatusArchived
		e.Pinned = false
		return true, nil
	})
	return e, err
}

// LessonAnalytics is how a lesson has been used (#217): how many runs had
// it in their prompt (UseCount, maintained by the daemon), and the outcome
// of the runs in history that used it.
type LessonAnalytics struct {
	Applied   int `json:"applied"`
	Runs      int `json:"runs"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
}

// AnalyticsOf counts the runs in history that used the entry.
func AnalyticsOf(e Entry, runs []RunUse) LessonAnalytics {
	a := LessonAnalytics{Applied: e.UseCount}
	for _, r := range runs {
		if !slices.ContainsFunc(r.MemoryIDs, func(id string) bool { return strings.EqualFold(id, e.ID) }) {
			continue
		}
		a.Runs++
		switch {
		case r.Failed:
			a.Failed++
		case r.Succeeded:
			a.Succeeded++
		}
	}
	return a
}
