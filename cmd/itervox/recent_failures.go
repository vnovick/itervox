package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// recentFailureRows converts State.RecentFailures into its wire rows
// (CORE-046). Never nil, so the snapshot always carries an array. Messages
// were redacted when the event loop recorded them; they are redacted again
// here (idempotent) so the wire field cannot carry a secret even if a future
// producer bypasses appendRecentFailure.
func recentFailureRows(s orchestrator.State) []server.FailureRow {
	rows := make([]server.FailureRow, 0, len(s.RecentFailures))
	for _, f := range s.RecentFailures {
		rows = append(rows, server.FailureRow{
			Kind:       string(f.Kind),
			Identifier: f.Identifier,
			Source:     f.Source,
			Message:    logging.RedactString(f.Message),
			OccurredAt: f.OccurredAt,
			RecordedAt: f.RecordedAt,
			Count:      f.Count,
		})
	}
	return rows
}

// recentFailuresCarry holds the RecentFailures ring between run()
// generations. Every WORKFLOW.md reload builds a fresh Orchestrator; without
// this hand-over the ring (and the evidence of what just went wrong) would
// vanish on exactly the save an operator makes to fix it. Process-scoped
// only: nothing is persisted across a restart (CORE-046).
//
// The operator's failure acks (CORE-175) travel with the ring: an ack hides
// ring rows, so it must live exactly as long as they do (M6-close V3 — every
// dashboard settings save reloads and used to resurface acknowledged
// failures).
type recentFailuresCarry struct {
	mu      sync.Mutex
	entries []orchestrator.FailureRecord
	acks    map[string]time.Time
}

// store records the ring and acks a finished generation left behind.
func (c *recentFailuresCarry) store(entries []orchestrator.FailureRecord, acks map[string]time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append([]orchestrator.FailureRecord(nil), entries...)
	c.acks = maps.Clone(acks)
}

// take hands the stored ring and acks to the next generation, once.
func (c *recentFailuresCarry) take() ([]orchestrator.FailureRecord, map[string]time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out, acks := c.entries, c.acks
	c.entries, c.acks = nil, nil
	return out, acks
}

// failureCarry is the daemon's single carry (one reload loop per process).
var failureCarry recentFailuresCarry

// failureRecorder is the slice of *orchestrator.Orchestrator the client-error
// reporter needs.
type failureRecorder interface {
	RecordFailure(orchestrator.FailureRecord) bool
}

// clientErrorReporter adapts a redacted web client error report (CORE-048)
// into a RecentFailures entry. Non-blocking: false (event channel full)
// makes the handler answer 503.
func clientErrorReporter(rec failureRecorder) func(server.ClientErrorReport) bool {
	return func(r server.ClientErrorReport) bool {
		msg := r.Message
		if r.Route != "" {
			msg = fmt.Sprintf("%s (route %s)", r.Message, r.Route)
		}
		return rec.RecordFailure(orchestrator.FailureRecord{
			Kind:    orchestrator.FailureKindClient,
			Source:  r.Kind,
			Message: msg,
		})
	}
}

// failureAckRows renders State.FailureAcks sorted by identifier (CORE-175).
func failureAckRows(s orchestrator.State) []server.FailureAckRow {
	if len(s.FailureAcks) == 0 {
		return nil
	}
	rows := make([]server.FailureAckRow, 0, len(s.FailureAcks))
	for id, upTo := range s.FailureAcks {
		rows = append(rows, server.FailureAckRow{Identifier: id, UpTo: upTo.UTC()})
	}
	slices.SortFunc(rows, func(a, b server.FailureAckRow) int { return strings.Compare(a.Identifier, b.Identifier) })
	return rows
}

// totalsRow renders State.Totals (CORE-091).
func totalsRow(s orchestrator.State) *server.TotalsRow {
	t := s.Totals.Snapshot()
	return &server.TotalsRow{
		InputTokens:      t.InputTokens,
		OutputTokens:     t.OutputTokens,
		CostUSDEstimated: t.CostUSDEstimated,
		CostCoverage:     server.TotalsCoverageRow{ClaudeRuns: t.ClaudeRuns, CodexRuns: t.CodexRuns},
	}
}

// pauseReasonsMap is State.PauseReasons restricted to currently paused
// issues (M6-close BH-M6-3); nil when none.
func pauseReasonsMap(s orchestrator.State) map[string]string {
	var out map[string]string
	for id := range s.PausedIdentifiers {
		reason := s.PauseReasons[id]
		if reason == "" {
			continue
		}
		if out == nil {
			out = make(map[string]string)
		}
		out[id] = reason
	}
	return out
}
