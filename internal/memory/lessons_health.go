package memory

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Stale lessons (#203): an approved lesson that later runs keep leaving out,
// or that the runs given it keep failing, is listed in the Memory page's
// "not helping" filter so a human can edit or archive it.

// LessonHealth says whether an approved lesson is helping.
type LessonHealth string

// Lesson health values.
const (
	// LessonHealthy: nothing suggests the lesson is not helping (or it is
	// not an approved lesson, or there is too little history to tell).
	LessonHealthy LessonHealth = ""
	// LessonUnused: none of the last StaleAfterRuns runs since its approval
	// had it in their prompt.
	LessonUnused LessonHealth = "unused"
	// LessonFailing: most of the recent runs that had it failed.
	LessonFailing LessonHealth = "failing"
	// LessonEnforced: its enforcement suggestion was accepted (#217): an
	// "Enforce:" issue carries it, so once that lands the lesson can be
	// archived.
	LessonEnforced LessonHealth = "enforced"
)

// RunUse is one finished run as lesson health reads it.
type RunUse struct {
	MemoryIDs  []string
	FinishedAt time.Time
	// Failed is true for a failed or stalled run.
	Failed bool
	// Succeeded is true for a successful run (#217 analytics).
	Succeeded bool
}

// failingWindow is how many of the newest runs that used a lesson are
// looked at; failingMinRuns is how many there must be to call it failing.
const (
	failingWindow  = 5
	failingMinRuns = 2
)

// HealthOf judges an entry against run history (oldest first). Only active
// lessons are judged; the clock starts at their approval (else creation).
func HealthOf(e Entry, runs []RunUse, staleAfterRuns int) (LessonHealth, string) {
	if e.Type != TypeLesson || e.Status != StatusActive {
		return LessonHealthy, ""
	}
	if enf := enforcementOf(e); enf != nil && enf.Status == EnforcementAccepted {
		reason := "an enforcement issue was created"
		if enf.Issue != "" {
			reason = "enforced by " + enf.Issue
		}
		return LessonEnforced, reason
	}
	if staleAfterRuns <= 0 {
		return LessonHealthy, ""
	}
	since := e.Created
	if e.Lesson != nil && e.Lesson.ApprovedAt != nil {
		since = *e.Lesson.ApprovedAt
	}
	var after, used []RunUse
	for _, r := range runs {
		if r.FinishedAt.Before(since) {
			continue
		}
		after = append(after, r)
		if slices.ContainsFunc(r.MemoryIDs, func(id string) bool { return strings.EqualFold(id, e.ID) }) {
			used = append(used, r)
		}
	}
	if len(used) > failingWindow {
		used = used[len(used)-failingWindow:]
	}
	failed := 0
	for _, r := range used {
		if r.Failed {
			failed++
		}
	}
	if len(used) >= failingMinRuns && failed*2 > len(used) {
		return LessonFailing, fmt.Sprintf("%d of the last %d runs that used it failed", failed, len(used))
	}
	if len(after) >= staleAfterRuns {
		recent := after[len(after)-staleAfterRuns:]
		if !slices.ContainsFunc(recent, func(r RunUse) bool {
			return slices.ContainsFunc(r.MemoryIDs, func(id string) bool { return strings.EqualFold(id, e.ID) })
		}) {
			return LessonUnused, fmt.Sprintf("not used by the last %d runs", staleAfterRuns)
		}
	}
	return LessonHealthy, ""
}

func enforcementOf(e Entry) *Enforcement {
	if e.Lesson == nil {
		return nil
	}
	return e.Lesson.Enforcement
}
