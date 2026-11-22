package server

import (
	"cmp"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Run and PR outcomes (cost and outcome visibility). GET /api/v1/outcomes
// aggregates the run history of the same snapshot GET /api/v1/state serves,
// per profile and per playbook, over a window. Nothing here is stored: every
// figure is computed from history rows on request.

// PR outcome values on HistoryRow.PROutcome.
const (
	PROutcomeOpen     = "open"
	PROutcomeMerged   = "merged"
	PROutcomeClosed   = "closed"
	PROutcomeReverted = "reverted"
)

const (
	defaultOutcomesWindow = 30 * 24 * time.Hour
	maxOutcomesWindow     = 90 * 24 * time.Hour
)

// OutcomeGroupRow is one profile's or playbook's figures over the window.
type OutcomeGroupRow struct {
	// Name is the profile or playbook; "" is the default profile, or runs
	// given no playbook.
	Name string `json:"name"`
	Runs int    `json:"runs"`
	// Finished counts the runs that ended (every status but input_required,
	// a run paused for a human answer); SuccessRate is Succeeded/Finished.
	Finished    int     `json:"finished"`
	Succeeded   int     `json:"succeeded"`
	SuccessRate float64 `json:"successRate"`
	// Retries counts the runs that were retries (attempt > 0); RetryRate is
	// Retries/Runs.
	Retries   int     `json:"retries"`
	RetryRate float64 `json:"retryRate"`
	// PRs counts the distinct PRs the runs created or continued, by outcome.
	PRs         int `json:"prs"`
	PRsMerged   int `json:"prsMerged"`
	PRsClosed   int `json:"prsClosed"`
	PRsReverted int `json:"prsReverted"`
	PRsOpen     int `json:"prsOpen"`
	// MedianElapsedMs / MedianTokens are per-run medians (tokens = input +
	// output, as HistoryRow.TotalTokens). TotalTokens sums the runs' tokens.
	MedianElapsedMs int64 `json:"medianElapsedMs"`
	MedianTokens    int   `json:"medianTokens"`
	TotalTokens     int   `json:"totalTokens"`
	CacheReadTokens int   `json:"cacheReadTokens"`
}

// OutcomesResponse is GET /api/v1/outcomes.
type OutcomesResponse struct {
	// Window is the window as given ("30d" by default); WindowStart the
	// oldest finish time counted.
	Window      string    `json:"window"`
	WindowStart time.Time `json:"windowStart"`
	GeneratedAt time.Time `json:"generatedAt"`
	// HistoryRuns counts every history row; EarliestRunAt is the oldest
	// row's finish time. History is bounded, so a window that starts
	// before EarliestRunAt covers less than it names.
	HistoryRuns   int               `json:"historyRuns"`
	EarliestRunAt *time.Time        `json:"earliestRunAt,omitempty"`
	Total         OutcomeGroupRow   `json:"total"`
	Profiles      []OutcomeGroupRow `json:"profiles"`
	Playbooks     []OutcomeGroupRow `json:"playbooks"`
}

// IssueOutcomeRow sums one issue's recorded runs for the issue detail.
type IssueOutcomeRow struct {
	Runs             int    `json:"runs"`
	Succeeded        int    `json:"succeeded"`
	Retries          int    `json:"retries"`
	Turns            int    `json:"turns"`
	ElapsedMs        int64  `json:"elapsedMs"`
	InputTokens      int    `json:"inputTokens"`
	OutputTokens     int    `json:"outputTokens"`
	CacheReadTokens  int    `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int    `json:"cacheWriteTokens,omitempty"`
	LastStatus       string `json:"lastStatus"`
	// PRURL / PROutcome are the newest run's PR (among the runs that
	// carried one) and what became of it.
	PRURL     string `json:"prUrl,omitempty"`
	PROutcome string `json:"prOutcome,omitempty"`
}

// ParseOutcomesWindow reads a window: "<n>d" (days) or a Go duration
// ("36h"), between one hour and 90 days; "" is 30 days.
func ParseOutcomesWindow(s string) (time.Duration, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return defaultOutcomesWindow, true
	}
	var d time.Duration
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil {
			return 0, false
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, false
		}
	}
	if d < time.Hour || d > maxOutcomesWindow {
		return 0, false
	}
	return d, true
}

// outcomeAcc accumulates one group.
type outcomeAcc struct {
	row     OutcomeGroupRow
	elapsed []int64
	tokens  []int
	prs     map[string]string // PR URL → its outcome
}

func (a *outcomeAcc) add(r HistoryRow) {
	a.row.Runs++
	if r.Status != "input_required" {
		a.row.Finished++
	}
	if r.Status == "succeeded" {
		a.row.Succeeded++
	}
	if r.Attempt > 0 {
		a.row.Retries++
	}
	a.row.TotalTokens += r.TotalTokens
	a.row.CacheReadTokens += r.CacheReadTokens
	a.elapsed = append(a.elapsed, r.ElapsedMs)
	a.tokens = append(a.tokens, r.TotalTokens)
	if r.PRURL != "" {
		if a.prs == nil {
			a.prs = map[string]string{}
		}
		// The newest row's outcome wins (rows are oldest first); every row
		// of one PR carries the same ledger outcome anyway.
		a.prs[r.PRURL] = r.PROutcome
	}
}

func (a *outcomeAcc) finish() OutcomeGroupRow {
	row := a.row
	if row.Finished > 0 {
		row.SuccessRate = float64(row.Succeeded) / float64(row.Finished)
	}
	if row.Runs > 0 {
		row.RetryRate = float64(row.Retries) / float64(row.Runs)
	}
	row.MedianElapsedMs = median(a.elapsed)
	row.MedianTokens = median(a.tokens)
	row.PRs = len(a.prs)
	for _, o := range a.prs {
		switch o {
		case PROutcomeMerged:
			row.PRsMerged++
		case PROutcomeClosed:
			row.PRsClosed++
		case PROutcomeReverted:
			row.PRsReverted++
		default: // open, or no longer in the ledger
			row.PRsOpen++
		}
	}
	return row
}

// median is the middle value (the mean of the two middle values for an even
// count, rounded down); 0 for none.
func median[T int | int64](vals []T) T {
	if len(vals) == 0 {
		return 0
	}
	s := slices.Clone(vals)
	slices.Sort(s)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}

// ComputeOutcomes aggregates the history rows that finished within window
// before now, per profile and per playbook (each sorted by runs, then name).
// A merged PR counts as merged for every group whose runs worked on it.
func ComputeOutcomes(rows []HistoryRow, now time.Time, window time.Duration, label string) OutcomesResponse {
	start := now.Add(-window)
	resp := OutcomesResponse{Window: label, WindowStart: start, GeneratedAt: now, HistoryRuns: len(rows),
		Profiles: []OutcomeGroupRow{}, Playbooks: []OutcomeGroupRow{}}
	var total outcomeAcc
	profiles := map[string]*outcomeAcc{}
	playbooks := map[string]*outcomeAcc{}
	group := func(m map[string]*outcomeAcc, key string) *outcomeAcc {
		a := m[key]
		if a == nil {
			a = &outcomeAcc{row: OutcomeGroupRow{Name: key}}
			m[key] = a
		}
		return a
	}
	for _, r := range rows {
		if resp.EarliestRunAt == nil || r.FinishedAt.Before(*resp.EarliestRunAt) {
			at := r.FinishedAt
			resp.EarliestRunAt = &at
		}
		if r.FinishedAt.Before(start) || r.FinishedAt.After(now) {
			continue
		}
		total.add(r)
		group(profiles, r.ProfileName).add(r)
		group(playbooks, r.Playbook).add(r)
	}
	resp.Total = total.finish()
	byRuns := func(a, b OutcomeGroupRow) int {
		return cmp.Or(cmp.Compare(b.Runs, a.Runs), strings.Compare(a.Name, b.Name))
	}
	for _, a := range profiles {
		resp.Profiles = append(resp.Profiles, a.finish())
	}
	for _, a := range playbooks {
		resp.Playbooks = append(resp.Playbooks, a.finish())
	}
	slices.SortFunc(resp.Profiles, byRuns)
	slices.SortFunc(resp.Playbooks, byRuns)
	return resp
}

// IssueOutcome sums identifier's history rows; nil when it has none.
func IssueOutcome(rows []HistoryRow, identifier string) *IssueOutcomeRow {
	var out *IssueOutcomeRow
	for _, r := range rows {
		if r.Identifier != identifier {
			continue
		}
		if out == nil {
			out = &IssueOutcomeRow{}
		}
		out.Runs++
		if r.Status == "succeeded" {
			out.Succeeded++
		}
		if r.Attempt > 0 {
			out.Retries++
		}
		out.Turns += r.TurnCount
		out.ElapsedMs += r.ElapsedMs
		out.InputTokens += r.InputTokens
		out.OutputTokens += r.OutputTokens
		out.CacheReadTokens += r.CacheReadTokens
		out.CacheWriteTokens += r.CacheWriteTokens
		out.LastStatus = r.Status
		if r.PRURL != "" {
			out.PRURL, out.PROutcome = r.PRURL, r.PROutcome
		}
	}
	return out
}

// handleOutcomes is GET /api/v1/outcomes[?window=30d] (viewer).
func (s *Server) handleOutcomes(w http.ResponseWriter, r *http.Request) {
	label := strings.TrimSpace(r.URL.Query().Get("window"))
	window, ok := ParseOutcomesWindow(label)
	if !ok {
		writeErrorWithField(w, http.StatusBadRequest, "invalid_window",
			`window must be "<days>d" or a duration such as "36h", between 1h and 90d`, "window")
		return
	}
	if label == "" {
		label = "30d"
	}
	writeJSON(w, http.StatusOK, ComputeOutcomes(s.snapshot().History, time.Now(), window, label))
}
