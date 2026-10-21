package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/memory"
)

// Lessons (#203). Detection runs on the event loop, from the events that
// carry a human correction or a run's outcome:
//
//   - EventProvideInput and a tracker reply to an input-required question
//     (input_reply),
//   - EventSessionMessageDelivered (steering),
//   - a worker's successful exit after a failed or stalled run on the same
//     issue (failed_then_succeeded).
//
// The loop only records the correction (State.LessonCorrections), applies
// the per-issue rate limit (State.LessonProposals) and builds a
// memory.Candidate. Drafting and writing the proposal run on a goroutine
// (goSafe, joined by Run through lessonWg) that reports back with
// EventLessonDrafted; nothing off the loop touches State.

// EventLessonDrafted reports a lesson-drafting goroutine's result (#203).
const EventLessonDrafted EventType = "LessonDrafted"

// LessonCorrection is a human correction or change recorded for an issue
// (#203): an input-required reply or a delivered steering message,
// redacted and bounded. Kept until the issue's next successful run, so a
// failed-then-succeeded lesson can say what changed in between.
type LessonCorrection struct {
	Trigger memory.LessonTrigger
	Text    string
	RunID   string
	At      time.Time
}

// LessonReport is the payload of EventLessonDrafted.
type LessonReport struct {
	Identifier string
	Outcome    memory.LessonOutcome
	EntryID    string
	DraftedBy  string
	// ReservedAt is the rate-limit stamp the proposal took; a proposal that
	// wrote nothing new gives it back.
	ReservedAt time.Time
	Err        error
}

// Bounds of the lesson ledgers in State.
const (
	// maxLessonCorrectionsPerIssue keeps the newest corrections per issue.
	maxLessonCorrectionsPerIssue = 8
	// maxLessonCorrectionIssues bounds how many issues keep corrections;
	// the issue with the oldest newest correction goes first.
	maxLessonCorrectionIssues = 200
	// lessonCorrectionBytes bounds one recorded correction.
	lessonCorrectionBytes = 800
	// lessonSituationBytes bounds the situation taken from a question.
	lessonSituationBytes = 240
)

// lessonsOn reports whether lessons are proposed: memory and
// memory.lessons both enabled (load-time config).
func (o *Orchestrator) lessonsOn() bool {
	return o != nil && o.cfg != nil && o.cfg.Memory.Enabled && o.cfg.Memory.Lessons.Enabled
}

// observeCorrection records a human correction for identifier and, when the
// text reads as one (memory.LooksLikeCorrection), proposes a lesson from it.
// Event loop only.
func (o *Orchestrator) observeCorrection(state *State, identifier string, c LessonCorrection, title, profile, situation string, links []string) {
	if !o.lessonsOn() || strings.TrimSpace(c.Text) == "" || state == nil || identifier == "" {
		return
	}
	text := boundUTF8(strings.TrimSpace(logging.RedactString(c.Text)), lessonCorrectionBytes)
	recordLessonCorrection(state, identifier, LessonCorrection{Trigger: c.Trigger, Text: text, RunID: c.RunID, At: c.At})
	if !memory.LooksLikeCorrection(text) {
		return
	}
	cand := memory.Candidate{
		Trigger: c.Trigger, Issue: identifier, Title: title, Profile: profile,
		Situation: situation, Corrections: []string{text}, Links: links,
	}
	if c.RunID != "" {
		cand.RunIDs = []string{c.RunID}
	}
	o.proposeLesson(state, cand, c.At)
}

// recordLessonCorrection appends a correction to the issue's bounded list.
func recordLessonCorrection(state *State, identifier string, c LessonCorrection) {
	if state.LessonCorrections == nil {
		state.LessonCorrections = map[string][]LessonCorrection{}
	}
	list := append(state.LessonCorrections[identifier], c)
	if len(list) > maxLessonCorrectionsPerIssue {
		list = slices.Clone(list[len(list)-maxLessonCorrectionsPerIssue:])
	}
	state.LessonCorrections[identifier] = list
	if len(state.LessonCorrections) <= maxLessonCorrectionIssues {
		return
	}
	oldest, oldestAt := "", time.Time{}
	for id, l := range state.LessonCorrections {
		if len(l) == 0 {
			oldest = id
			break
		}
		if last := l[len(l)-1].At; oldest == "" || last.Before(oldestAt) {
			oldest, oldestAt = id, last
		}
	}
	delete(state.LessonCorrections, oldest)
}

// observeInputReply handles a human's reply to an input-required question
// (dashboard or tracker). Event loop only.
func (o *Orchestrator) observeInputReply(state *State, entry *InputRequiredEntry, reply string) {
	if entry == nil || !o.lessonsOn() {
		return
	}
	title, runID, links := "", "", []string(nil)
	if last, ok := o.lastRunOf(entry.Identifier); ok {
		title, runID = last.Title, last.RunID
		if last.PRURL != "" {
			links = []string{last.PRURL}
		}
	}
	situation := ""
	if q := firstSentence(entry.Context); q != "" {
		situation = "an agent asks “" + boundUTF8(q, lessonSituationBytes) + "”"
	}
	o.observeCorrection(state, entry.Identifier,
		LessonCorrection{Trigger: memory.TriggerInputReply, Text: reply, RunID: runID, At: time.Now()},
		title, entry.ProfileName, situation, links)
}

// observeSteers handles steering messages delivered to a run. Event loop
// only.
func (o *Orchestrator) observeSteers(state *State, runID string, d *SessionDelivery) {
	if d == nil || !o.lessonsOn() {
		return
	}
	title, profile := "", ""
	var links []string
	if r := runEntryByRunID(*state, d.Identifier, runID); r != nil {
		title, profile = r.Issue.Title, r.ProfileName
		if r.Issue.URL != nil {
			links = []string{*r.Issue.URL}
		}
	}
	now := time.Now()
	for _, text := range d.Texts {
		o.observeCorrection(state, d.Identifier,
			LessonCorrection{Trigger: memory.TriggerSteering, Text: text, RunID: runID, At: now},
			title, profile, "", links)
	}
}

// observeRunSucceeded looks for a failed-then-succeeded pair when a worker
// run on issue succeeds, and forgets the issue's recorded corrections (they
// belong to the work that just finished). Event loop only; call after the
// run is in history.
func (o *Orchestrator) observeRunSucceeded(state *State, entry *RunEntry, issue domain.Issue) {
	if state == nil || (entry != nil && entry.Kind != "" && entry.Kind != "worker") {
		return // a reviewer's or an automation's success is not the issue's work succeeding
	}
	corrections := state.LessonCorrections[issue.Identifier]
	delete(state.LessonCorrections, issue.Identifier)
	if !o.lessonsOn() || entry == nil {
		return
	}
	failed, success, ok := failedBeforeSuccess(o.RunHistory(), issue.Identifier, entry.RunID)
	if !ok {
		return
	}
	since := failed[0].StartedAt
	var changes []string
	for _, c := range corrections {
		if !c.At.Before(since) {
			changes = append(changes, c.Text)
		}
	}
	for _, v := range state.ReviewVerdicts[issue.Identifier] {
		if v.Verdict == ReviewVerdictBlock && !v.RecordedAt.Before(since) && len(v.Reasons) > 0 {
			changes = append(changes, "reviewer feedback: "+boundUTF8(strings.Join(v.Reasons, "; "), lessonCorrectionBytes))
		}
	}
	if len(changes) == 0 {
		// A retry that simply worked the second time teaches nothing.
		slog.Debug("orchestrator: failed-then-succeeded run with no recorded change; no lesson",
			"issue_identifier", issue.Identifier)
		return
	}
	statuses := make([]string, 0, len(failed))
	runIDs := make([]string, 0, len(failed)+1)
	for _, r := range failed {
		statuses = append(statuses, r.Status)
		if r.RunID != "" {
			runIDs = append(runIDs, r.RunID)
		}
	}
	if success.RunID != "" {
		runIDs = append(runIDs, success.RunID)
	}
	var links []string
	if success.PRURL != "" {
		links = append(links, success.PRURL)
	}
	o.proposeLesson(state, memory.Candidate{
		Trigger: memory.TriggerFailedThenSucceeded, Issue: issue.Identifier, Title: issue.Title,
		Profile:     entry.ProfileName,
		WentWrong:   fmt.Sprintf("the earlier run %s", strings.Join(slices.Compact(statuses), " / ")),
		Corrections: changes, RunIDs: runIDs, Links: links,
	}, time.Now())
}

// failedBeforeSuccess finds, in history (oldest first), the successful run
// runID of identifier and the worker runs that failed or stalled since the
// issue's previous successful worker run. ok is false when there were none.
func failedBeforeSuccess(hist []CompletedRun, identifier, runID string) (failed []CompletedRun, success CompletedRun, ok bool) {
	at := -1
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].Identifier == identifier && hist[i].Status == "succeeded" && (runID == "" || hist[i].RunID == runID) {
			at = i
			break
		}
	}
	if at < 0 {
		return nil, CompletedRun{}, false
	}
	for i := at - 1; i >= 0; i-- {
		r := hist[i]
		if r.Identifier != identifier || (r.Kind != "" && r.Kind != "worker") {
			continue
		}
		if r.Status == "succeeded" {
			break
		}
		if r.Status == "failed" || r.Status == "stalled" {
			failed = append(failed, r)
		}
	}
	slices.Reverse(failed)
	return failed, hist[at], len(failed) > 0
}

// lastRunOf is the newest history entry of identifier.
func (o *Orchestrator) lastRunOf(identifier string) (CompletedRun, bool) {
	hist := o.RunHistory()
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].Identifier == identifier {
			return hist[i], true
		}
	}
	return CompletedRun{}, false
}

// proposeLesson applies the per-issue rate limit and hands the candidate to
// a drafting goroutine. Event loop only.
func (o *Orchestrator) proposeLesson(state *State, cand memory.Candidate, now time.Time) {
	lc := o.cfg.Memory.Lessons
	if !takeLessonSlot(state, cand.Issue, now, lc.MaxPerIssue, time.Duration(lc.RateWindowHours)*time.Hour) {
		slog.Info("orchestrator: lesson not proposed: per-issue rate limit reached",
			"issue_identifier", cand.Issue, "trigger", cand.Trigger, "max_per_issue", lc.MaxPerIssue)
		return
	}
	plan := o.lessonDraftPlan()
	store := memory.Store{WorkflowPath: o.cfg.WorkflowPath}
	opts := memory.ProposeOptions{Scope: memory.Scope(lc.Scope), AutoApprove: lc.AutoApprove(lc.Scope)}
	ctx := context.Background()
	if p := o.runCtx.Load(); p != nil {
		ctx = *p
	}
	identifier := cand.Issue
	goSafe(&o.lessonWg, "lesson-draft", identifier, func() {
		rep := o.draftAndPropose(ctx, store, cand, plan, opts)
		rep.Identifier, rep.ReservedAt = identifier, now
		select {
		case o.events <- OrchestratorEvent{Type: EventLessonDrafted, Identifier: identifier, Lesson: &rep}:
		case <-ctx.Done():
		}
	}, func() {
		o.sendEventBounded(OrchestratorEvent{Type: EventLessonDrafted, Identifier: identifier, Lesson: &LessonReport{
			Identifier: identifier, ReservedAt: now, Err: fmt.Errorf("lesson drafting panicked"),
		}}, identifier)
	})
}

// takeLessonSlot reserves one of the issue's proposals in the rate-limit
// window, dropping stamps older than the window. False when the issue has
// used them all.
func takeLessonSlot(state *State, identifier string, now time.Time, maxPerIssue int, window time.Duration) bool {
	if state.LessonProposals == nil {
		state.LessonProposals = map[string][]time.Time{}
	}
	kept := slices.DeleteFunc(slices.Clone(state.LessonProposals[identifier]), func(t time.Time) bool {
		return now.Sub(t) >= window
	})
	if maxPerIssue > 0 && len(kept) >= maxPerIssue {
		state.LessonProposals[identifier] = kept
		return false
	}
	state.LessonProposals[identifier] = append(kept, now)
	return true
}

// applyLessonDrafted handles EventLessonDrafted: logs the outcome, gives
// the rate-limit slot back when nothing new was written, records a failure
// and pushes a snapshot so the dashboard's lesson count updates. Event loop
// only.
func (o *Orchestrator) applyLessonDrafted(state *State, ev OrchestratorEvent) {
	rep := ev.Lesson
	if rep == nil {
		return
	}
	if rep.Err != nil || (rep.Outcome != memory.LessonProposed && rep.Outcome != memory.LessonApproved) {
		releaseLessonSlot(state, rep.Identifier, rep.ReservedAt)
	}
	if rep.Err != nil {
		slog.Warn("orchestrator: lesson not proposed", "issue_identifier", rep.Identifier, "error", rep.Err)
		appendRecentFailure(state, FailureRecord{
			Kind: FailureKindPersist, Source: "lessons", Identifier: rep.Identifier,
			Message: fmt.Sprintf("lesson not proposed: %v", rep.Err), OccurredAt: time.Now(),
		}, time.Now())
		return
	}
	slog.Info("orchestrator: lesson drafted", "issue_identifier", rep.Identifier,
		"outcome", rep.Outcome, "memory_id", rep.EntryID, "drafted_by", rep.DraftedBy)
	if o.OnStateChange != nil {
		o.OnStateChange()
	}
}

// releaseLessonSlot removes one reservation stamp.
func releaseLessonSlot(state *State, identifier string, at time.Time) {
	list := state.LessonProposals[identifier]
	if i := slices.IndexFunc(list, func(t time.Time) bool { return t.Equal(at) }); i >= 0 {
		state.LessonProposals[identifier] = slices.Delete(slices.Clone(list), i, i+1)
	}
}

// firstSentence is the first sentence (or line) of s, whitespace collapsed.
func firstSentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.IndexAny(s, ".?!"); i >= 0 && i+1 < len(s) {
		return s[:i+1]
	}
	return s
}
