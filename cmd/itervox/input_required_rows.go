package main

import (
	"sort"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// File extracted from main.go to keep that file under the size-budget cap
// (T-57). Concerns here are the input-required panel surface — building
// snapshot rows, picking the dashboard-wide stale threshold, and stamping
// per-row Stale + AgeMinutes for gap A.

func sortedInputRequiredRows(entries map[string]*orchestrator.InputRequiredEntry, pending map[string]*orchestrator.PendingInputResumeEntry, staleAfter time.Duration, now time.Time) []server.InputRequiredRow {
	rows := make([]server.InputRequiredRow, 0, len(entries)+len(pending))
	for _, entry := range entries {
		rows = append(rows, makeInputRequiredRow("input_required", entry.Identifier, entry.SessionID, entry.Context, entry.Backend, entry.ProfileName, entry.QueuedAt, staleAfter, now))
	}
	for _, entry := range pending {
		context := "Reply received, waiting to resume."
		if entry.Context != "" {
			context = context + "\n\nOriginal request:\n" + entry.Context
		}
		rows = append(rows, makeInputRequiredRow("pending_input_resume", entry.Identifier, entry.SessionID, context, entry.Backend, entry.ProfileName, entry.QueuedAt, staleAfter, now))
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Identifier < rows[j].Identifier
	})
	return rows
}

// longestInputRequiredMaxAge returns the maximum MaxAgeMinutes (as a
// Duration) across all enabled input_required automations. Used by the
// snapshot path to pick a single dashboard-visible stale threshold (gap A).
// Returns the supplied fallback when no rule configures a positive value.
func longestInputRequiredMaxAge(rules []config.AutomationConfig, fallback time.Duration) time.Duration {
	var max int
	for i := range rules {
		if !rules[i].Enabled {
			continue
		}
		if rules[i].Trigger.Type != config.AutomationTriggerInputRequired {
			continue
		}
		if rules[i].Filter.MaxAgeMinutes > max {
			max = rules[i].Filter.MaxAgeMinutes
		}
	}
	if max == 0 {
		return fallback
	}
	return time.Duration(max) * time.Minute
}

// makeInputRequiredRow builds one snapshot row, computing the stale flag
// when an age threshold is configured. `staleAfter == 0` means "no stale
// concept configured" — every entry returns `Stale: false` and a non-zero
// AgeMinutes still surfaces so the dashboard can render age regardless.
func makeInputRequiredRow(state, identifier, sessionID, context, backend, profile string, queuedAt time.Time, staleAfter time.Duration, now time.Time) server.InputRequiredRow {
	row := server.InputRequiredRow{
		Identifier: identifier,
		SessionID:  sessionID,
		State:      state,
		Context:    context,
		Backend:    backend,
		Profile:    profile,
		QueuedAt:   queuedAt.Format(time.RFC3339),
	}
	if !queuedAt.IsZero() {
		age := now.Sub(queuedAt)
		if age > 0 {
			row.AgeMinutes = int(age.Minutes())
		}
		if staleAfter > 0 && age > staleAfter {
			row.Stale = true
		}
	}
	return row
}
