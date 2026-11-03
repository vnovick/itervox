package memory

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// Lesson proposals, approval and rejection (#203), on the same files and
// under the same process-wide lock as every other memory write.
//
// Rejections are remembered in a sidecar, .itervox/lessons-rejected.json
// next to the workflow (beside the private memory file when the daemon has
// no workflow path). It keeps only each rejected lesson's normalized word
// sets and fingerprint, never its text, and is gitignored with the other
// runtime JSON under .itervox/ (the reviewer's "no" is local to the
// checkout, like the Lessons tab that recorded it).

// RejectedFileName is the rejected-lessons sidecar's file name.
const RejectedFileName = "lessons-rejected.json"

// MaxRejected caps the remembered rejections; the oldest go first.
const MaxRejected = 500

// RejectedPath is the rejected-lessons sidecar of a workflow; empty when
// neither a workflow path nor a private memory file is known.
func RejectedPath(workflowPath string) string {
	if workflowPath != "" {
		return filepath.Join(filepath.Dir(workflowPath), ".itervox", RejectedFileName)
	}
	if p := UserPath(); p != "" {
		return filepath.Join(filepath.Dir(p), RejectedFileName)
	}
	return ""
}

// Rejection is one remembered rejection.
type Rejection struct {
	Fingerprint string `json:"fingerprint"`
	// Text / Correction are the normalized word sets of the lesson text and
	// of the correction it came from (NormalizeLessonText).
	Text       string    `json:"text,omitempty"`
	Correction string    `json:"correction,omitempty"`
	Issue      string    `json:"issue,omitempty"`
	At         time.Time `json:"at"`
}

type rejectedDoc struct {
	Rejected []Rejection `json:"rejected"`
}

// readRejected reads the sidecar; a missing or unreadable file is empty
// (an unreadable one is reported and left alone by writes).
func readRejected(path string) ([]Rejection, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("memory: read %s: %w", path, err)
	}
	var doc rejectedDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("memory: %s: %w", path, err)
	}
	return doc.Rejected, nil
}

func writeRejected(path string, list []Rejection) error {
	if len(list) > MaxRejected {
		list = list[len(list)-MaxRejected:]
	}
	b, _ := json.MarshalIndent(rejectedDoc{Rejected: list}, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("memory: %w", err)
	}
	if err := atomicfs.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("memory: write %s: %w", path, err)
	}
	return nil
}

// rejectedMatch reports whether a lesson (text, correction) was rejected
// before: the same fingerprint, or near-duplicate text or correction.
func rejectedMatch(list []Rejection, text, correction string) bool {
	fps := []string{LessonFingerprint(text), LessonFingerprint(correction)}
	for _, r := range list {
		if r.Fingerprint != "" && slices.Contains(fps, r.Fingerprint) {
			return true
		}
		if sameLesson(text, correction, r.Text, r.Correction) {
			return true
		}
	}
	return false
}

// LessonOutcome is what proposing a lesson did.
type LessonOutcome string

// Lesson outcomes.
const (
	// LessonNew: no existing or rejected lesson matches (PrecheckLesson).
	LessonNew LessonOutcome = "new"
	// LessonProposed: a new proposed entry was written.
	LessonProposed LessonOutcome = "proposed"
	// LessonApproved: a new entry was written already active
	// (memory.lessons.auto_approve).
	LessonApproved LessonOutcome = "approved"
	// LessonBumped: a near-duplicate lesson exists; its evidence was
	// bumped instead of proposing a new one.
	LessonBumped LessonOutcome = "bumped"
	// LessonSuppressed: the same lesson was rejected before.
	LessonSuppressed LessonOutcome = "suppressed"
)

// ProposeOptions are where and how a proposal is written.
type ProposeOptions struct {
	// Scope is the file a new proposal goes to; private falls back to the
	// project file when private memory is off.
	Scope Scope
	// AutoApprove writes a new lesson as active.
	AutoApprove bool
}

// ProposeResult is the outcome of ProposeLesson.
type ProposeResult struct {
	Outcome LessonOutcome
	// Entry is the written or bumped entry (zero when suppressed).
	Entry Entry
}

// lessonMatch finds a lesson entry (proposed or active, either file) that is
// the same lesson as (text, correction).
func (f files) lessonMatch(text, correction string) (Scope, int, bool) {
	for _, sc := range Scopes {
		file := f.byScope[sc]
		if file == nil || file.Err != nil {
			continue
		}
		for i, e := range file.Entries {
			if e.Type != TypeLesson || e.Status == StatusArchived {
				continue
			}
			corr := ""
			if e.Lesson != nil {
				corr = e.Lesson.Correction
			}
			if sameLesson(text, correction, e.Text, corr) {
				return sc, i, true
			}
		}
	}
	return "", 0, false
}

// PrecheckLesson tells, before any drafting, whether a correction would be
// suppressed (rejected before), bump an existing lesson, or be new. It
// compares the correction text only (the draft does not exist yet) and
// writes nothing.
func (s Store) PrecheckLesson(correction string) LessonOutcome {
	writeMu.Lock()
	defer writeMu.Unlock()
	rejected, _ := readRejected(RejectedPath(s.WorkflowPath))
	if rejectedMatch(rejected, "", correction) {
		return LessonSuppressed
	}
	if _, _, ok := s.load().lessonMatch("", correction); ok {
		return LessonBumped
	}
	return LessonNew
}

// ProposeLesson writes a lesson Itervox drafted. A lesson rejected before is
// suppressed; a near-duplicate of an existing proposed or active lesson
// bumps that lesson's evidence (run ids, links, count, confidence) instead
// of adding another; otherwise e is added as proposed (active with
// AutoApprove). Secrets are redacted before anything is compared or saved.
func (s Store) ProposeLesson(e Entry, opt ProposeOptions) (ProposeResult, error) {
	if e.Lesson == nil {
		return ProposeResult{}, fmt.Errorf("%w: a lesson needs its lesson record", ErrInvalid)
	}
	e.Text = boundText(e.Text, MaxTextBytes)
	writeMu.Lock()
	defer writeMu.Unlock()
	now := s.now()
	e = scrub(e)
	rejected, _ := readRejected(RejectedPath(s.WorkflowPath))
	if rejectedMatch(rejected, e.Text, e.Lesson.Correction) {
		return ProposeResult{Outcome: LessonSuppressed}, nil
	}
	fs := s.load()
	if sc, i, ok := fs.lessonMatch(e.Text, e.Lesson.Correction); ok {
		file, err := fs.writable(sc)
		if err != nil {
			return ProposeResult{}, err
		}
		cur := bumpLesson(file.Entries[i], e, now)
		if err := validate(cur); err != nil {
			return ProposeResult{}, err
		}
		file.Entries[i] = cur
		fs.dirty[sc] = true
		return ProposeResult{Outcome: LessonBumped, Entry: cur}, fs.save()
	}
	sc := opt.Scope
	if sc == "" {
		sc = ScopePrivate
	}
	if sc == ScopePrivate && fs.byScope[ScopePrivate].Path == "" {
		sc = ScopeProject
	}
	file, err := fs.writable(sc)
	if err != nil {
		return ProposeResult{}, err
	}
	e.ID, e.Scope, e.Type = NewID(), sc, TypeLesson
	e.Created, e.Updated = now, now
	e.Status = StatusProposed
	out := LessonProposed
	if opt.AutoApprove {
		e.Status = StatusActive
		t := now
		e.Lesson.ApprovedAt = &t
		out = LessonApproved
	}
	if err := validate(e); err != nil {
		return ProposeResult{}, err
	}
	file.Entries = append(file.Entries, e)
	fs.dirty[sc] = true
	return ProposeResult{Outcome: out, Entry: e}, fs.save()
}

// bumpLesson folds a repeat sighting into an existing lesson: its run ids
// and links are added (newest kept), the count goes up and confidence rises.
// The existing wording stays: it may already have been reviewed or edited.
func bumpLesson(cur, seen Entry, now time.Time) Entry {
	cur.Source.RunIDs = boundList(appendNew(cur.Source.RunIDs, seen.Source.RunIDs))
	cur.Source.Links = boundList(appendNew(cur.Source.Links, seen.Source.Links))
	if cur.Lesson == nil {
		cur.Lesson = &Lesson{Trigger: seen.Lesson.Trigger, Correction: seen.Lesson.Correction, Evidence: 1}
	} else {
		l := *cur.Lesson
		cur.Lesson = &l
	}
	cur.Lesson.Evidence = max(cur.Lesson.Evidence, 1) + 1
	cur.Lesson.Confidence = min(maxConfidence, max(cur.Lesson.Confidence, seen.Lesson.Confidence)+confidenceStep)
	cur.Updated = now
	return cur
}

// appendNew appends the items of add not already in list (case-insensitive).
func appendNew(list, add []string) []string {
	out := slices.Clone(list)
	for _, s := range add {
		if !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, s) }) {
			out = append(out, s)
		}
	}
	return out
}

// ApproveOptions are a reviewer's choices on approval.
type ApproveOptions struct {
	// Scope is where the approved lesson lives: private or project. Empty
	// keeps the file it was proposed in.
	Scope Scope `json:"scope,omitempty"`
	// Text, when set, replaces the lesson text (edit, then approve).
	Text *string `json:"text,omitempty"`
}

// ApproveLesson makes a proposed entry active, in the chosen scope (moving
// it between the files when needed, keeping its id, provenance and record),
// with the reviewer's edited text when given. ErrConflict when the entry is
// not proposed.
func (s Store) ApproveLesson(id string, opt ApproveOptions) (Entry, error) {
	if opt.Text != nil {
		if err := checkRawSize(*opt.Text); err != nil {
			return Entry{}, err
		}
	}
	if opt.Scope != "" && !slices.Contains(Scopes, opt.Scope) {
		return Entry{}, fieldError{"scope", fmt.Sprintf(`must be "private" or "project", not %q`, opt.Scope), false}
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	fs := s.load()
	from, i, ok := fs.find(id)
	if !ok {
		return Entry{}, ErrNotFound
	}
	src, err := fs.writable(from)
	if err != nil {
		return Entry{}, err
	}
	e := src.Entries[i]
	if e.Status != StatusProposed {
		return Entry{}, fmt.Errorf("%w: %q is %s, not proposed", ErrConflict, id, e.Status)
	}
	to := cmp.Or(opt.Scope, from)
	dst, err := fs.writable(to)
	if err != nil {
		return Entry{}, err
	}
	now := s.now()
	if opt.Text != nil {
		e.Text = *opt.Text
	}
	e.Status = StatusActive
	if e.Lesson == nil {
		e.Lesson = &Lesson{Trigger: TriggerInputReply}
	} else {
		l := *e.Lesson
		e.Lesson = &l
	}
	t := now
	e.Lesson.ApprovedAt = &t
	e.Scope = to
	e.Updated = now
	e = scrub(e)
	if err := validate(e); err != nil {
		return Entry{}, err
	}
	if to == from {
		src.Entries[i] = e
	} else {
		src.Entries = slices.Delete(slices.Clone(src.Entries), i, i+1)
		dst.Entries = append(dst.Entries, e)
	}
	fs.dirty[from], fs.dirty[to] = true, true
	return e, fs.save()
}

// RejectLesson removes a proposed entry and remembers it, so the same
// lesson (or a near-duplicate of its text or of the correction it came
// from) is not proposed again. ErrConflict when the entry is not proposed.
// The rejection is recorded before the entry is removed: a failure between
// the two writes leaves a remembered rejection and the proposal, never a
// forgotten one.
func (s Store) RejectLesson(id string) (Entry, error) {
	writeMu.Lock()
	defer writeMu.Unlock()
	fs := s.load()
	sc, i, ok := fs.find(id)
	if !ok {
		return Entry{}, ErrNotFound
	}
	file, err := fs.writable(sc)
	if err != nil {
		return Entry{}, err
	}
	e := file.Entries[i]
	if e.Status != StatusProposed {
		return Entry{}, fmt.Errorf("%w: %q is %s, not proposed", ErrConflict, id, e.Status)
	}
	if path := RejectedPath(s.WorkflowPath); path != "" {
		list, err := readRejected(path)
		if err != nil {
			return Entry{}, fmt.Errorf("%w: fix the rejected-lessons file first: %w", ErrInvalid, err)
		}
		corr := ""
		if e.Lesson != nil {
			corr = e.Lesson.Correction
		}
		list = append(list, Rejection{
			Fingerprint: cmp.Or(LessonFingerprint(corr), LessonFingerprint(e.Text)), Text: NormalizeLessonText(e.Text),
			Correction: NormalizeLessonText(corr), Issue: e.Source.Issue, At: s.now(),
		})
		if err := writeRejected(path, list); err != nil {
			return Entry{}, err
		}
	}
	file.Entries = slices.Delete(slices.Clone(file.Entries), i, i+1)
	fs.dirty[sc] = true
	return e, fs.save()
}

// Rejected reads the remembered rejections (for the Memory page).
func (s Store) Rejected() ([]Rejection, error) {
	return readRejected(RejectedPath(s.WorkflowPath))
}
