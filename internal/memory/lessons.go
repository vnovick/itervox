package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vnovick/itervox/internal/logging"
)

// Lessons (#203). Itervox proposes a lesson (a `lesson` entry with status
// `proposed`, never used until a human approves it) when a human corrects
// an agent (an input-required reply or a steering message) and when a run
// that failed is followed by a successful one on the same issue. This file
// holds the pure parts: the review record, correction detection, the
// template draft and near-duplicate matching. lessons_store.go writes
// proposals, approvals and rejections.

// LessonTrigger is what made Itervox propose a lesson.
type LessonTrigger string

// Lesson triggers.
const (
	// TriggerInputReply: a human's reply to an input-required question.
	TriggerInputReply LessonTrigger = "input_reply"
	// TriggerSteering: an operator's steering message to a running agent.
	TriggerSteering LessonTrigger = "steering"
	// TriggerFailedThenSucceeded: a run failed or stalled and a later run on
	// the same issue succeeded after a change.
	TriggerFailedThenSucceeded LessonTrigger = "failed_then_succeeded"
)

// Lesson is the review record of a lesson entry.
type Lesson struct {
	Trigger LessonTrigger `json:"trigger"`
	// Situation is when the lesson applies.
	Situation string `json:"situation,omitempty"`
	// WentWrong is what the agent did, or what failed.
	WentWrong string `json:"wentWrong,omitempty"`
	// Worked is what fixed it: the correction, or what changed before the
	// successful run.
	Worked string `json:"worked,omitempty"`
	// Correction is the human's own words the lesson came from; proposals
	// are deduplicated (and rejections remembered) on it as well as on the
	// lesson text.
	Correction string `json:"correction,omitempty"`
	// Confidence is 0..1: higher for stronger triggers, raised each time the
	// same lesson is seen again.
	Confidence float64 `json:"confidence,omitempty"`
	// Evidence is how many times the lesson was seen (1 when proposed).
	Evidence int `json:"evidence,omitempty"`
	// DraftedBy is "template" or "profile:<name>".
	DraftedBy string `json:"draftedBy,omitempty"`
	// ApprovedAt is when a human approved it (or it was auto-approved).
	ApprovedAt *time.Time `json:"approvedAt,omitempty"`
}

var lessonKeys = []string{"trigger", "situation", "wentWrong", "worked", "correction", "confidence", "evidence", "draftedBy", "approvedAt"}

// LessonTriggers lists the triggers.
var LessonTriggers = []LessonTrigger{TriggerInputReply, TriggerSteering, TriggerFailedThenSucceeded}

func validateLesson(l *Lesson) error {
	if l == nil {
		return nil
	}
	if !slices.Contains(LessonTriggers, l.Trigger) {
		return fieldError{"lesson.trigger", fmt.Sprintf(`must be "input_reply", "steering" or "failed_then_succeeded", not %q`, l.Trigger), false}
	}
	if l.Confidence < 0 || l.Confidence > 1 {
		return fieldError{"lesson.confidence", "must be between 0 and 1", false}
	}
	if l.Evidence < 0 {
		return fieldError{"lesson.evidence", "must not be negative", false}
	}
	for name, s := range map[string]string{
		"lesson.situation": l.Situation, "lesson.wentWrong": l.WentWrong,
		"lesson.worked": l.Worked, "lesson.correction": l.Correction,
	} {
		if len(s) > MaxTextBytes {
			return fieldError{name, fmt.Sprintf("is %d bytes, at most %d", len(s), MaxTextBytes), true}
		}
	}
	if len(l.DraftedBy) > MaxItemBytes {
		return fieldError{"lesson.draftedBy", fmt.Sprintf("is %d bytes, at most %d", len(l.DraftedBy), MaxItemBytes), true}
	}
	return nil
}

func scrubLesson(l *Lesson) *Lesson {
	if l == nil {
		return nil
	}
	c := *l
	for _, s := range []*string{&c.Situation, &c.WentWrong, &c.Worked, &c.Correction, &c.DraftedBy} {
		*s = strings.TrimSpace(logging.RedactString(*s))
	}
	return &c
}

// Candidate is what a detected trigger hands the drafting step: the
// evidence, never a drafted text.
type Candidate struct {
	Trigger LessonTrigger
	// Issue / Title name the issue the trigger happened on.
	Issue string
	Title string
	// Profile is the agent profile of the corrected run ("" = default).
	Profile string
	// Situation is what the agent was doing or asking.
	Situation string
	// WentWrong is what the agent did or what failed.
	WentWrong string
	// Corrections are the human's words, or the changes between a failed and
	// a successful run, oldest first.
	Corrections []string
	// RunIDs / Links are the evidence.
	RunIDs []string
	Links  []string
}

// CorrectionText is the candidate's corrections as one bounded text: the
// key proposals are deduplicated and rejections remembered on.
func (c Candidate) CorrectionText() string {
	return boundText(strings.Join(c.Corrections, "\n"), 1200)
}

// Draft is a lesson's drafted wording.
type Draft struct {
	Text      string
	Situation string
	WentWrong string
	Worked    string
	DraftedBy string
}

// Base confidence per trigger: a failed run fixed by a change is the
// strongest signal, a mid-run steer the weakest.
var triggerConfidence = map[LessonTrigger]float64{
	TriggerInputReply:          0.6,
	TriggerSteering:            0.5,
	TriggerFailedThenSucceeded: 0.7,
}

// confidenceStep is how much each repeat sighting raises confidence, up to
// maxConfidence.
const (
	confidenceStep = 0.1
	maxConfidence  = 0.95
)

// TemplateDraft fills the lesson template ("When …, do … instead of …")
// from the candidate. It is the fallback when no lesson profile is
// configured or the drafting call fails, and never calls anything.
func TemplateDraft(c Candidate) Draft {
	situation := boundText(oneLine(c.Situation), 300)
	if situation == "" {
		if c.Title != "" {
			situation = "working on issues like “" + boundText(oneLine(c.Title), 200) + "”"
		} else {
			situation = "working on this repository"
		}
	}
	worked := boundText(oneLine(strings.Join(c.Corrections, "; ")), 1000)
	wentWrong := boundText(oneLine(c.WentWrong), 300)
	var b strings.Builder
	b.WriteString("When ")
	b.WriteString(strings.TrimRight(situation, ".!?:; "))
	b.WriteString(", ")
	b.WriteString(sentence(lowerFirst(worked)))
	if wentWrong != "" {
		b.WriteString(" Instead of: ")
		b.WriteString(sentence(wentWrong))
	}
	return Draft{
		Text: boundText(b.String(), MaxTextBytes), Situation: situation,
		WentWrong: wentWrong, Worked: worked, DraftedBy: "template",
	}
}

// NewLessonEntry is the proposed entry for a candidate and its draft.
func NewLessonEntry(c Candidate, d Draft) Entry {
	e := Entry{
		Type:   TypeLesson,
		Text:   d.Text,
		Status: StatusProposed,
		Tags:   []string{"lesson"},
		Source: Source{Kind: "lesson", Issue: c.Issue, RunIDs: boundList(c.RunIDs), Links: boundList(c.Links)},
		Lesson: &Lesson{
			Trigger: c.Trigger, Situation: d.Situation, WentWrong: d.WentWrong, Worked: d.Worked,
			Correction: c.CorrectionText(), Confidence: triggerConfidence[c.Trigger], Evidence: 1,
			DraftedBy: d.DraftedBy,
		},
	}
	if c.Profile != "" {
		e.Profiles = []string{c.Profile}
	}
	return e
}

// approvalWords are replies that agree rather than correct.
var approvalRe = regexp.MustCompile(`^(yes|yep|yeah|ok|okay|sure|go ahead|approved?|lgtm|sounds good|proceed|continue|looks good|thanks|thank you|done)\b`)

// correctionRe matches the words a correction usually carries: it tells the
// agent to do something differently.
var correctionRe = regexp.MustCompile(`\b(instead|don'?t|do not|never|always|stop|wrong|rather|should(n'?t)?|must|avoid|prefer|not|no longer|make sure|remember to|switch to|use .+ (over|not))\b`)

// LooksLikeCorrection reports whether a human's reply or steering message
// tells the agent to do something differently (and so is worth a lesson),
// as opposed to answering a question or agreeing. Lexical: a correction cue
// in a message of at least three words that is not a bare approval.
func LooksLikeCorrection(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if len(strings.Fields(t)) < 3 {
		return false
	}
	if approvalRe.MatchString(t) && len(strings.Fields(t)) < 8 {
		return false
	}
	return correctionRe.MatchString(t)
}

// NormalizeLessonText is text's normalized word set (select.go's words:
// lower-cased, three letters or more, stop words left out), sorted and
// joined: what similarity compares and the rejected-lessons file keeps.
func NormalizeLessonText(text string) string {
	toks := words(text)
	words := make([]string, 0, len(toks))
	for w := range toks {
		words = append(words, w)
	}
	slices.Sort(words)
	return strings.Join(words, " ")
}

// LessonFingerprint is a short stable hash of text's normalized words.
func LessonFingerprint(text string) string {
	n := NormalizeLessonText(text)
	if n == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(n))
	return hex.EncodeToString(sum[:8])
}

// Similarity is the Jaccard similarity of two texts' normalized word sets,
// 0 (nothing shared, or either empty) to 1 (same words).
func Similarity(a, b string) float64 {
	return jaccard(words(a), words(b))
}

func jaccard(x, y map[string]bool) float64 {
	if len(x) == 0 || len(y) == 0 {
		return 0
	}
	inter := 0
	for w := range x {
		if y[w] {
			inter++
		}
	}
	return float64(inter) / float64(len(x)+len(y)-inter)
}

// DuplicateSimilarity is how similar two lessons (by text, or by the
// correction they came from) must be to count as the same lesson.
const DuplicateSimilarity = 0.6

// sameLesson reports whether two (text, correction) pairs are the same
// lesson: either pair of non-empty texts is at least DuplicateSimilarity
// alike.
func sameLesson(textA, corrA, textB, corrB string) bool {
	if textA != "" && textB != "" && Similarity(textA, textB) >= DuplicateSimilarity {
		return true
	}
	return corrA != "" && corrB != "" && Similarity(corrA, corrB) >= DuplicateSimilarity
}

// oneLine collapses whitespace runs (newlines included) to single spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// boundText cuts s to at most n bytes on a rune boundary, ending in "…"
// when it was cut.
func boundText(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	const ellipsis = "…"
	cut := n - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + ellipsis
}

// boundList keeps at most MaxListItems items, the newest (last) ones, each
// cut to MaxItemBytes.
func boundList(in []string) []string {
	if len(in) > MaxListItems {
		in = in[len(in)-MaxListItems:]
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = boundText(s, MaxItemBytes); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func lowerFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	// Keep acronyms and identifiers ("PR", "API") as written.
	if r2, _ := utf8.DecodeRuneInString(s[n:]); unicode.IsUpper(r2) {
		return s
	}
	return string(unicode.ToLower(r)) + s[n:]
}

// sentence ends s with a period unless it already ends a sentence.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasSuffix(s, ".") || strings.HasSuffix(s, "!") || strings.HasSuffix(s, "?") || strings.HasSuffix(s, "…") {
		return s
	}
	return s + "."
}
