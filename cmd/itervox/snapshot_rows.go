package main

import (
	"sort"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/depsanalysis"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// nilIfZero returns nil for a zero time.Time, otherwise a pointer to the
// value. Used to populate *time.Time DTO fields where the JSON omitempty tag
// must actually drop the field on absence (Go's encoding/json does not call
// IsZero on time.Time-typed fields). v0.2.0 audit P1-5.
func nilIfZero(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func convertModelsForSnapshot(models map[string][]config.ModelOption) map[string][]server.ModelOption {
	if len(models) == 0 {
		return nil
	}
	result := make(map[string][]server.ModelOption, len(models))
	for backend, opts := range models {
		converted := make([]server.ModelOption, len(opts))
		for i, m := range opts {
			converted[i] = server.ModelOption{ID: m.ID, Label: m.Label}
		}
		result[backend] = converted
	}
	return result
}

func automationQueueBackpressureRow(bp orchestrator.AutomationQueueBackpressure) *server.AutomationQueueBackpressureRow {
	if bp.MaxLength == 0 && bp.Length == 0 && bp.RejectedSinceBoot == 0 && !bp.Saturated && !bp.PausedProducers {
		return nil
	}
	return &server.AutomationQueueBackpressureRow{
		Length:             bp.Length,
		MaxLength:          bp.MaxLength,
		Saturated:          bp.Saturated,
		PausedProducers:    bp.PausedProducers,
		RejectedSinceBoot:  bp.RejectedSinceBoot,
		LastRejectedAt:     nilIfZero(bp.LastRejectedAt),
		LastRejectedReason: bp.LastRejectedReason,
	}
}

func automationQueueRows(s orchestrator.State) []server.AutomationQueueRow {
	if len(s.AutomationQueue) == 0 {
		return nil
	}
	keys := append([]string(nil), s.AutomationQueueOrder...)
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		seen[key] = struct{}{}
	}
	var extra []string
	for key := range s.AutomationQueue {
		if _, ok := seen[key]; !ok {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	keys = append(keys, extra...)

	rows := make([]server.AutomationQueueRow, 0, len(keys))
	for _, key := range keys {
		entry := s.AutomationQueue[key]
		if entry == nil {
			continue
		}
		rows = append(rows, server.AutomationQueueRow{
			ID:                entry.ID,
			AutomationID:      entry.AutomationID,
			TriggerType:       entry.TriggerType,
			Identifier:        entry.Issue.Identifier,
			Title:             entry.Issue.Title,
			IssueState:        entry.Issue.State,
			Profile:           entry.ProfileName,
			Backend:           entry.Trigger.SwitchedToBackend,
			Status:            string(entry.Status),
			Reason:            string(entry.Reason),
			ReasonDetail:      entry.ReasonDetail,
			QueuedAt:          entry.QueuedAt,
			FiredAt:           entry.FiredAt,
			LastFiredAt:       nilIfZero(entry.LastFiredAt),
			LastAttemptAt:     nilIfZero(entry.LastAttemptAt),
			AttemptCount:      entry.AttemptCount,
			Cron:              entry.Trigger.Cron,
			Timezone:          entry.Trigger.Timezone,
			PRURL:             entry.Trigger.PRURL,
			InputContext:      entry.Trigger.InputContext,
			ErrorMessage:      entry.LastError,
			SwitchedToProfile: entry.Trigger.SwitchedToProfile,
			SwitchedToBackend: entry.Trigger.SwitchedToBackend,
			MoveToState:       entry.MoveToState,
		})
	}
	return rows
}

func dependencyAuditRows(audit map[string]*orchestrator.DependencyAuditEntry) []server.DependencyAuditRow {
	if len(audit) == 0 {
		return nil
	}
	keys := make([]string, 0, len(audit))
	for key := range audit {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	rows := make([]server.DependencyAuditRow, 0, len(keys))
	for _, key := range keys {
		entry := audit[key]
		if entry == nil {
			continue
		}
		sources := make([]string, 0, len(entry.Sources))
		for _, source := range entry.Sources {
			sources = append(sources, string(source))
		}
		rows = append(rows, server.DependencyAuditRow{
			Identifier:            entry.Identifier,
			IssueState:            entry.IssueState,
			Status:                string(entry.Status),
			Sources:               sources,
			BlockedBy:             blockerRefRows(entry.BlockedBy),
			UnresolvedBlockers:    blockerRefRows(entry.UnresolvedBlockers),
			ResolvedBlockers:      blockerRefRows(entry.ResolvedBlockers),
			WasBlocked:            entry.WasBlocked,
			FirstBlockedAt:        nilIfZero(entry.FirstBlockedAt),
			UnblockedAt:           nilIfZero(entry.UnblockedAt),
			LastAuditedAt:         nilIfZero(entry.LastAuditedAt),
			LastTransitionVersion: entry.LastTransitionVersion,
			LastTransitionReason:  entry.LastTransitionReason,
		})
	}
	return rows
}

func blockerRefRows(blockers []domain.BlockerRef) []server.BlockerRefRow {
	if len(blockers) == 0 {
		return nil
	}
	rows := make([]server.BlockerRefRow, 0, len(blockers))
	for _, blocker := range blockers {
		rows = append(rows, server.BlockerRefRow{
			ID:         stringValue(blocker.ID),
			Identifier: stringValue(blocker.Identifier),
			State:      stringValue(blocker.State),
			URL:        stringValue(blocker.URL),
		})
	}
	return rows
}

func statusChangeRows(changes []orchestrator.IssueStatusChange) []server.IssueStatusChangeRow {
	if len(changes) == 0 {
		return nil
	}
	rows := make([]server.IssueStatusChangeRow, 0, len(changes))
	for _, change := range changes {
		rows = append(rows, server.IssueStatusChangeRow{
			FromState:    change.FromState,
			ToState:      change.ToState,
			Source:       string(change.Source),
			AutomationID: change.AutomationID,
			TriggerType:  change.TriggerType,
			ProfileName:  change.ProfileName,
			Backend:      change.Backend,
			WorkerHost:   change.WorkerHost,
			At:           change.At,
		})
	}
	return rows
}

func dependencyGraphRows(s orchestrator.State, sidecar *depsanalysis.Sidecar) ([]server.DependencyGraphNodeRow, []server.DependencyGraphEdgeRow) {
	if len(s.DependencyAudit) == 0 && (sidecar == nil || len(sidecar.Edges) == 0) {
		return nil, nil
	}
	running := make(map[string]bool, len(s.Running))
	nodeTitles := make(map[string]string)
	nodeURLs := make(map[string]string)
	for _, run := range s.Running {
		if run == nil || run.Issue.Identifier == "" {
			continue
		}
		running[run.Issue.Identifier] = true
		nodeTitles[run.Issue.Identifier] = run.Issue.Title
		nodeURLs[run.Issue.Identifier] = stringValue(run.Issue.URL)
	}
	queued := make(map[string]bool, len(s.AutomationQueue))
	for _, entry := range s.AutomationQueue {
		if entry == nil || entry.Issue.Identifier == "" {
			continue
		}
		queued[entry.Issue.Identifier] = true
		if entry.Issue.Title != "" {
			nodeTitles[entry.Issue.Identifier] = entry.Issue.Title
		}
		nodeURLs[entry.Issue.Identifier] = stringValue(entry.Issue.URL)
	}

	nodes := make(map[string]server.DependencyGraphNodeRow)
	var edges []server.DependencyGraphEdgeRow
	trackerEdgeKeys := make(map[string]struct{})
	for _, entry := range s.DependencyAudit {
		if entry == nil || entry.Identifier == "" {
			continue
		}
		targetID := entry.Identifier
		nodes[targetID] = mergeDependencyGraphNode(nodes[targetID], server.DependencyGraphNodeRow{
			ID:         targetID,
			Identifier: targetID,
			Title:      nodeTitles[targetID],
			State:      entry.IssueState,
			Status:     string(entry.Status),
			Running:    running[targetID],
			Queued:     queued[targetID],
			Terminal:   isSnapshotTerminal(entry.IssueState, s.TerminalStates),
			URL:        nodeURLs[targetID],
		})
		for _, blocker := range entry.BlockedBy {
			sourceID := blockerGraphIdentifier(blocker)
			if sourceID == "" {
				sourceID = "unknown:" + targetID
			}
			sourceState := stringValue(blocker.State)
			sourceKnown := stringValue(blocker.Identifier) != ""
			resolved := isSnapshotTerminal(sourceState, s.TerminalStates)
			nodes[sourceID] = mergeDependencyGraphNode(nodes[sourceID], server.DependencyGraphNodeRow{
				ID:         sourceID,
				Identifier: sourceID,
				Title:      nodeTitles[sourceID],
				State:      sourceState,
				Status:     dependencyGraphSourceStatus(sourceState, resolved, sourceKnown),
				Running:    running[sourceID],
				Queued:     queued[sourceID],
				Terminal:   resolved,
				URL:        stringValue(blocker.URL),
			})
			edges = append(edges, server.DependencyGraphEdgeRow{
				ID:               sourceID + "->" + targetID,
				SourceIdentifier: sourceID,
				TargetIdentifier: targetID,
				SourceState:      sourceState,
				TargetState:      entry.IssueState,
				Resolved:         resolved,
				SourceKnown:      sourceKnown,
				Origin:           string(depsanalysis.OriginTracker),
			})
			trackerEdgeKeys[sourceID+"->"+targetID] = struct{}{}
		}
	}
	if sidecar != nil {
		for _, ie := range sidecar.Edges {
			if ie.Source == "" || ie.Target == "" {
				continue
			}
			edgeID := ie.Source + "->" + ie.Target
			if _, dup := trackerEdgeKeys[edgeID]; dup {
				continue
			}
			// Ensure both endpoints have at least a minimal node entry.
			nodes[ie.Source] = mergeDependencyGraphNode(nodes[ie.Source], server.DependencyGraphNodeRow{
				ID:         ie.Source,
				Identifier: ie.Source,
				Title:      nodeTitles[ie.Source],
				Running:    running[ie.Source],
				Queued:     queued[ie.Source],
				URL:        nodeURLs[ie.Source],
			})
			nodes[ie.Target] = mergeDependencyGraphNode(nodes[ie.Target], server.DependencyGraphNodeRow{
				ID:         ie.Target,
				Identifier: ie.Target,
				Title:      nodeTitles[ie.Target],
				Running:    running[ie.Target],
				Queued:     queued[ie.Target],
				URL:        nodeURLs[ie.Target],
			})
			edges = append(edges, server.DependencyGraphEdgeRow{
				ID:               edgeID,
				SourceIdentifier: ie.Source,
				TargetIdentifier: ie.Target,
				SourceState:      nodes[ie.Source].State,
				TargetState:      nodes[ie.Target].State,
				Resolved:         nodes[ie.Source].Terminal,
				SourceKnown:      true,
				Origin:           string(depsanalysis.OriginInferred),
				Evidence:         ie.Evidence,
			})
		}
	}
	nodeRows := make([]server.DependencyGraphNodeRow, 0, len(nodes))
	for _, node := range nodes {
		nodeRows = append(nodeRows, node)
	}
	sort.Slice(nodeRows, func(i, j int) bool { return nodeRows[i].Identifier < nodeRows[j].Identifier })
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	return nodeRows, edges
}

// mergeDependencyGraphNode combines two partial DependencyGraphNodeRow values
// into one row by preferring `prev`'s non-empty scalar fields and ORing the
// boolean flags.
//
// v0.2.0 audit P3-1 — the earlier `if prev.ID == "" { return next }` short
// circuit produced an order-dependent merge: when prev was the empty seed and
// next carried boolean flags, the seed's later edge-merge could have set
// `Running = true` and that flag would be lost on the next call. Always
// running the per-field merge keeps the operation commutative under the
// flags' OR semantics. If `prev.ID` is empty, `next.ID` provides it.
func mergeDependencyGraphNode(prev, next server.DependencyGraphNodeRow) server.DependencyGraphNodeRow {
	if prev.ID == "" {
		prev.ID = next.ID
	}
	if prev.Identifier == "" {
		prev.Identifier = next.Identifier
	}
	if prev.Title == "" {
		prev.Title = next.Title
	}
	if prev.State == "" {
		prev.State = next.State
	}
	if prev.Status == "" {
		prev.Status = next.Status
	}
	if prev.URL == "" {
		prev.URL = next.URL
	}
	if prev.UpdatedAt == "" {
		prev.UpdatedAt = next.UpdatedAt
	}
	prev.Running = prev.Running || next.Running
	prev.Queued = prev.Queued || next.Queued
	prev.Terminal = prev.Terminal || next.Terminal
	return prev
}

func dependencyGraphSourceStatus(state string, resolved bool, known bool) string {
	if !known || state == "" {
		return string(orchestrator.DependencyAuditUnknown)
	}
	if resolved {
		return string(orchestrator.DependencyAuditUnblocked)
	}
	return string(orchestrator.DependencyAuditBlocked)
}

func blockerGraphIdentifier(blocker domain.BlockerRef) string {
	if v := stringValue(blocker.Identifier); v != "" {
		return v
	}
	if v := stringValue(blocker.ID); v != "" {
		return v
	}
	return stringValue(blocker.URL)
}

func isSnapshotTerminal(state string, terminalStates []string) bool {
	for _, terminal := range terminalStates {
		if strings.EqualFold(strings.TrimSpace(state), strings.TrimSpace(terminal)) && strings.TrimSpace(state) != "" {
			return true
		}
	}
	return false
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
