package orchestrator

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/memory"
	"github.com/vnovick/itervox/internal/prdetector"
)

// Lessons round 2 (#217): three more triggers, all detected on the event
// loop and proposed through proposeLesson (rate limit, drafting goroutine,
// EventLessonDrafted, the lesson_proposed feed event) like #203's.
//
//   - reviewer_ask: a reviewer's blocking verdict asks for a change it (or
//     another reviewer) already asked for on another run or issue.
//     State.ReviewerAsks keeps the recent asks to compare against.
//   - reverted_pr: a merged agent PR was reverted on the base branch. The
//     git scan (#201's revert detection, supplied by cmd/itervox as
//     Orchestrator.RevertScan) runs off the loop every revertScanEvery and
//     reports back with EventRevertsScanned.
//   - recipe: a worker run succeeded after a non-obvious fix (at least
//     memory.lessons.recipe_min_failed_runs failed attempts, a steer, or
//     memory.lessons.recipe_min_turns turns). The recipe is what worked and
//     the verification that proved it, read from the run's handoff.

// EventRevertsScanned reports a reverted-PR scan's result (#217).
const EventRevertsScanned EventType = "RevertsScanned"

// RevertedPR is a PR the base branch reverted: its number and the revert's
// commit subject.
type RevertedPR struct {
	Number  int
	Subject string
}

// RevertScanReport is the payload of EventRevertsScanned.
type RevertScanReport struct {
	Reverted []RevertedPR
	Err      error
}

// ReviewerAsk is one change a reviewer's blocking verdict asked for (#217),
// redacted and bounded.
type ReviewerAsk struct {
	Identifier string
	Profile    string
	RunID      string
	Text       string
	At         time.Time
}

const (
	// maxReviewerAsks bounds State.ReviewerAsks; the oldest go first.
	maxReviewerAsks = 200
	// maxAsksPerVerdict bounds the asks taken from one verdict.
	maxAsksPerVerdict = 8
	// reviewerAskRepeats is how many times the same kind of change must be
	// asked for before it is a lesson.
	reviewerAskRepeats = 2
	// maxRevertsSeen bounds State.LessonRevertsSeen.
	maxRevertsSeen = 500
	// revertScanEvery is how often the base branch is scanned for reverts.
	revertScanEvery = 30 * time.Minute
	// recipeHandoffBytes bounds the handoff read for a recipe.
	recipeHandoffBytes = 16 << 10
	// recipeSectionBytes bounds what worked / the verification of a recipe.
	recipeSectionBytes = 600
)

// syntheticVerdictReasons are reasons the daemon itself writes into a
// verdict; they are not a reviewer's request.
var syntheticVerdictReasons = []string{"no parseable verdict recorded", "reviewer changed the branch"}

// reviewAsks is what a blocking verdict asked for: its reasons and its
// line comments, without the daemon's own reasons, redacted and bounded.
func reviewAsks(v ReviewVerdict) []string {
	var out []string
	add := func(s string) {
		s = boundUTF8(strings.Join(strings.Fields(logging.RedactString(s)), " "), lessonCorrectionBytes)
		if len(strings.Fields(s)) < 3 || len(out) >= maxAsksPerVerdict {
			return
		}
		if slices.ContainsFunc(syntheticVerdictReasons, func(p string) bool { return strings.HasPrefix(s, p) }) {
			return
		}
		out = append(out, s)
	}
	for _, r := range v.Reasons {
		add(r)
	}
	for _, c := range v.Comments {
		add(c.Body)
	}
	return out
}

// observeReviewerAsk records what a finished reviewer's blocking verdict
// asked for and proposes a reviewer_ask lesson for each ask that repeats
// one from another run or issue. Event loop only; call after the verdict is
// recorded (advanceReviewChainForIssue).
func (o *Orchestrator) observeReviewerAsk(state *State, live *RunEntry, issue domain.Issue, profile string) {
	if state == nil || !o.lessonsOn() {
		return
	}
	var verdict *ReviewVerdict
	for i, v := range state.ReviewVerdicts[issue.Identifier] {
		if v.Profile == profile {
			verdict = &state.ReviewVerdicts[issue.Identifier][i]
		}
	}
	if verdict == nil || verdict.Verdict != ReviewVerdictBlock {
		return
	}
	runID := ""
	if live != nil {
		runID = live.RunID
	}
	now := time.Now()
	for _, ask := range reviewAsks(*verdict) {
		var matches []ReviewerAsk
		for _, prev := range state.ReviewerAsks {
			if (prev.Identifier != issue.Identifier || prev.RunID != runID) &&
				memory.Similarity(ask, prev.Text) >= memory.DuplicateSimilarity {
				matches = append(matches, prev)
			}
		}
		recordReviewerAsk(state, ReviewerAsk{Identifier: issue.Identifier, Profile: profile, RunID: runID, Text: ask, At: now})
		if len(matches)+1 < reviewerAskRepeats {
			continue
		}
		var runIDs []string
		issues := []string{issue.Identifier}
		for _, m := range matches {
			if m.RunID != "" {
				runIDs = append(runIDs, m.RunID)
			}
			if !slices.Contains(issues, m.Identifier) {
				issues = append(issues, m.Identifier)
			}
		}
		if runID != "" {
			runIDs = append(runIDs, runID)
		}
		var links []string
		if issue.URL != nil {
			links = []string{*issue.URL}
		}
		o.proposeLesson(state, memory.Candidate{
			Trigger: memory.TriggerReviewerAsk, Issue: issue.Identifier, Title: issue.Title,
			WentWrong: fmt.Sprintf("a reviewer asked for this change %d times (%s)", len(matches)+1,
				strings.Join(issues, ", ")),
			Corrections: []string{ask}, RunIDs: slices.Compact(runIDs), Links: links,
		}, now)
	}
}

// recordReviewerAsk appends an ask to the bounded ledger.
func recordReviewerAsk(state *State, a ReviewerAsk) {
	state.ReviewerAsks = append(state.ReviewerAsks, a)
	if n := len(state.ReviewerAsks); n > maxReviewerAsks {
		state.ReviewerAsks = slices.Clone(state.ReviewerAsks[n-maxReviewerAsks:])
	}
}

// maybeScanReverts starts a reverted-PR scan off the loop when lessons are
// on, cmd/itervox supplied RevertScan, none is in flight and the last one
// is older than revertScanEvery. Event loop only.
func (o *Orchestrator) maybeScanReverts(state *State, now time.Time) {
	if o.RevertScan == nil || !o.lessonsOn() || state.RevertScanInFlight ||
		(!state.RevertScanAt.IsZero() && now.Sub(state.RevertScanAt) < revertScanEvery) {
		return
	}
	state.RevertScanInFlight, state.RevertScanAt = true, now
	scan := o.RevertScan
	// Run's context, not the tick's iteration context: the scan outlives
	// the iteration that started it.
	ctx := context.Background()
	if p := o.runCtx.Load(); p != nil {
		ctx = *p
	}
	goSafe(&o.lessonWg, "revert-scan", "", func() {
		reverted, err := scan(ctx)
		select {
		case o.events <- OrchestratorEvent{Type: EventRevertsScanned, Reverts: &RevertScanReport{Reverted: reverted, Err: err}}:
		case <-ctx.Done():
		}
	}, func() {
		o.sendEventBounded(OrchestratorEvent{Type: EventRevertsScanned, Reverts: &RevertScanReport{
			Err: fmt.Errorf("revert scan panicked"),
		}}, "")
	})
}

// applyRevertsScanned handles EventRevertsScanned: each reverted PR that an
// agent's worker run opened (and that has not been seen before) proposes a
// reverted_pr lesson on that run's issue. Event loop only.
func (o *Orchestrator) applyRevertsScanned(state *State, rep *RevertScanReport) {
	state.RevertScanInFlight = false
	if rep == nil {
		return
	}
	if rep.Err != nil {
		slog.Warn("orchestrator: reverted-PR scan failed", "error", rep.Err)
		return
	}
	if len(rep.Reverted) == 0 || !o.lessonsOn() {
		return
	}
	hist := o.RunHistory()
	now := time.Now()
	for _, rv := range rep.Reverted {
		run, ok := newestRunOfPR(hist, rv.Number)
		if !ok {
			continue
		}
		if _, seen := state.LessonRevertsSeen[run.PRURL]; seen {
			continue
		}
		markRevertSeen(state, run.PRURL, now)
		subject := boundUTF8(strings.TrimSpace(logging.RedactString(rv.Subject)), 300)
		change := "the change in " + run.PRURL + " was reverted on the base branch"
		if subject != "" {
			change += " (" + subject + ")"
		}
		runIDs := []string{}
		if run.RunID != "" {
			runIDs = append(runIDs, run.RunID)
		}
		o.proposeLesson(state, memory.Candidate{
			Trigger: memory.TriggerRevertedPR, Issue: run.Identifier, Title: run.Title, Profile: run.ProfileName,
			WentWrong:   "the agent's merged PR was reverted",
			Corrections: []string{change + "; confirm the approach with the issue's owner and the full test suite before merging similar work"},
			RunIDs:      runIDs, Links: []string{run.PRURL},
		}, now)
	}
}

// newestRunOfPR is the newest non-reviewer run in history whose PR has the
// number.
func newestRunOfPR(hist []CompletedRun, number int) (CompletedRun, bool) {
	if number <= 0 {
		return CompletedRun{}, false
	}
	for i := len(hist) - 1; i >= 0; i-- {
		r := hist[i]
		if r.PRURL != "" && r.Kind != "reviewer" && prdetector.PRNumber(r.PRURL) == number {
			return r, true
		}
	}
	return CompletedRun{}, false
}

// markRevertSeen records a reverted PR's lesson, bounded (oldest first out).
func markRevertSeen(state *State, url string, at time.Time) {
	if state.LessonRevertsSeen == nil {
		state.LessonRevertsSeen = map[string]time.Time{}
	}
	state.LessonRevertsSeen[url] = at
	for len(state.LessonRevertsSeen) > maxRevertsSeen {
		oldest, oldestAt := "", time.Time{}
		for u, t := range state.LessonRevertsSeen {
			if oldest == "" || t.Before(oldestAt) {
				oldest, oldestAt = u, t
			}
		}
		delete(state.LessonRevertsSeen, oldest)
	}
}

// hardWon says why a successful run was hard-won ("" when it was not): at
// least minFailed failed runs before it, a steer delivered to it, or at
// least minTurns turns.
func hardWon(failedBefore int, success CompletedRun, minFailed, minTurns int) string {
	var why []string
	if minFailed > 0 && failedBefore >= minFailed {
		why = append(why, fmt.Sprintf("%d failed attempts", failedBefore))
	}
	if n := len(success.Steers) + success.SteersDropped; n > 0 {
		why = append(why, fmt.Sprintf("%d steering message(s)", n))
	}
	if minTurns > 0 && success.TurnCount >= minTurns {
		why = append(why, fmt.Sprintf("%d turns", success.TurnCount))
	}
	return strings.Join(why, ", ")
}

// observeRecipe proposes a recipe lesson when a worker run's success was
// hard-won: what worked (the handoff's account of it, else the steers that
// got it there) and the verification that proved it. Event loop only; the
// handoff is a small local file read with a bound.
func (o *Orchestrator) observeRecipe(state *State, entry *RunEntry, issue domain.Issue, success CompletedRun, failed []CompletedRun) {
	lc := o.cfg.Memory.Lessons
	why := hardWon(len(failed), success, lc.RecipeMinFailedRuns, lc.RecipeMinTurns)
	if why == "" {
		return
	}
	worked, verification := "", ""
	if o.workspace != nil {
		worked, verification = recipeFromHandoff(o.workspace.ResolvePath(issue.Identifier), success.StartedAt)
	}
	if worked == "" {
		var steers []string
		for _, s := range success.Steers {
			steers = append(steers, s.Text)
		}
		worked = boundUTF8(strings.Join(steers, "; "), recipeSectionBytes)
	}
	if strings.TrimSpace(worked) == "" {
		slog.Debug("orchestrator: hard-won success with no recorded fix; no recipe",
			"issue_identifier", issue.Identifier, "why", why)
		return
	}
	runIDs := make([]string, 0, len(failed)+1)
	for _, r := range failed {
		if r.RunID != "" {
			runIDs = append(runIDs, r.RunID)
		}
	}
	if success.RunID != "" {
		runIDs = append(runIDs, success.RunID)
	}
	var links []string
	if success.PRURL != "" {
		links = []string{success.PRURL}
	}
	o.proposeLesson(state, memory.Candidate{
		Trigger: memory.TriggerRecipe, Issue: issue.Identifier, Title: issue.Title, Profile: entry.ProfileName,
		WentWrong: "it took " + why, Corrections: []string{worked}, Verification: verification,
		RunIDs: runIDs, Links: links,
	}, time.Now())
}

var (
	// verifyHeading names a handoff section that says how the work was
	// checked.
	verifyHeading = regexp.MustCompile(`(?i)verif|test|check|proof|validat`)
	// workedHeading names a handoff section that says what was done.
	workedHeading = regexp.MustCompile(`(?i)what (worked|changed)|fix|approach|solution|summary|changes|done`)
)

// recipeFromHandoff reads the newest completed handoff written since
// notBefore in the workspace and returns what worked and how it was
// verified, each bounded and redacted. Empty when there is none.
func recipeFromHandoff(wsPath string, notBefore time.Time) (worked, verification string) {
	if wsPath == "" {
		return "", ""
	}
	dir := filepath.Join(wsPath, HandoffDirRelPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", ""
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".md") && !strings.HasSuffix(e.Name(), ".partial.md") {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	if len(names) == 0 {
		return "", ""
	}
	path := filepath.Join(dir, names[len(names)-1])
	if info, err := os.Stat(path); err != nil || info.ModTime().Before(notBefore.Add(-time.Second)) {
		return "", "" // an earlier run's handoff
	}
	f, err := os.Open(path) //nolint:gosec // daemon-controlled workspace path
	if err != nil {
		return "", ""
	}
	defer func() { _ = f.Close() }()
	sections := handoffSections(io.LimitReader(f, recipeHandoffBytes))
	clean := func(s string) string {
		return boundUTF8(strings.Join(strings.Fields(logging.RedactString(s)), " "), recipeSectionBytes)
	}
	for _, s := range sections {
		switch {
		case verification == "" && verifyHeading.MatchString(s.heading):
			verification = clean(s.body)
		case worked == "" && s.heading != "" && workedHeading.MatchString(s.heading):
			worked = clean(s.body)
		}
	}
	if worked == "" && len(sections) > 0 {
		worked = clean(sections[0].body)
	}
	return worked, verification
}

type handoffSection struct{ heading, body string }

// handoffSections splits Markdown into its heading sections; text before
// the first heading is a section with no heading. Empty sections are
// dropped.
func handoffSections(r io.Reader) []handoffSection {
	var out []handoffSection
	cur := handoffSection{}
	var body strings.Builder
	flush := func() {
		cur.body = strings.TrimSpace(body.String())
		if cur.body != "" {
			out = append(out, cur)
		}
		body.Reset()
	}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "#") {
			flush()
			cur = handoffSection{heading: strings.TrimSpace(strings.TrimLeft(t, "#"))}
			continue
		}
		body.WriteString(line)
		body.WriteByte('\n')
	}
	flush()
	return out
}
