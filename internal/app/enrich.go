// Package app contains extracted business logic from cmd/itervox that is
// testable in isolation. Functions here depend only on domain, orchestrator,
// config, and server — no side effects.
package app

import (
	"fmt"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

// EnrichIssue maps a domain.Issue to a server.TrackerIssue, overlaying live
// orchestrator state (running, retrying, paused, idle) and ineligibility reasons.
// now is the current wall-clock time used to compute ElapsedMs for running issues.
func EnrichIssue(issue domain.Issue, snap orchestrator.State, now time.Time, cfg *config.Config) server.TrackerIssue {
	ti := server.TrackerIssue{
		Identifier: issue.Identifier,
		Title:      issue.Title,
		State:      issue.State,
		Labels:     issue.Labels,
		Priority:   issue.Priority,
		BranchName: issue.BranchName,
	}
	if issue.Description != nil {
		ti.Description = *issue.Description
	}
	if issue.URL != nil {
		ti.URL = *issue.URL
	}
	// BlockedBy: collect non-nil identifiers from []domain.BlockerRef
	for _, b := range issue.BlockedBy {
		if b.Identifier != nil && *b.Identifier != "" {
			ti.BlockedBy = append(ti.BlockedBy, *b.Identifier)
			detail := server.BlockerDetail{Identifier: *b.Identifier}
			if b.State != nil {
				detail.State = *b.State
			}
			if b.URL != nil {
				detail.URL = *b.URL
			}
			ti.BlockedByDetails = append(ti.BlockedByDetails, detail)
		}
	}
	// Comments: map domain.Comment to server.CommentRow
	for _, c := range issue.Comments {
		row := server.CommentRow{Author: c.AuthorName, Body: c.Body}
		if c.CreatedAt != nil {
			row.CreatedAt = c.CreatedAt.Format(time.RFC3339)
		}
		ti.Comments = append(ti.Comments, row)
	}
	// Per-issue agent profile override.
	if profileName, ok := snap.IssueProfiles[issue.Identifier]; ok && profileName != "" {
		ti.AgentProfile = profileName
	}
	// Per-issue agent backend override.
	if backendName, ok := snap.IssueBackends[issue.Identifier]; ok && backendName != "" {
		ti.AgentBackend = backendName
	}
	// Orchestrator state
	if re, ok := snap.Running[issue.ID]; ok {
		ti.OrchestratorState = "running"
		ti.TurnCount = re.TurnCount
		ti.Tokens = re.TotalTokens
		ti.ElapsedMs = now.Sub(re.StartedAt).Milliseconds()
		if re.LastMessage != "" {
			ti.LastMessage = re.LastMessage
		}
	} else if re, ok := snap.RetryAttempts[issue.ID]; ok {
		ti.OrchestratorState = "retrying"
		if re.Error != nil {
			ti.Error = *re.Error
		}
	} else if entry, inputReq := snap.InputRequiredIssues[issue.Identifier]; inputReq {
		// input_required wins over paused: an issue waiting on a human is the
		// more actionable signal, even though the worker has technically paused.
		ti.OrchestratorState = "input_required"
		ti.Error = entry.Context
	} else if entry, pendingResume := snap.PendingInputResumes[issue.Identifier]; pendingResume {
		ti.OrchestratorState = "pending_input_resume"
		ti.Error = pendingInputResumeMessage(entry)
	} else if _, paused := snap.PausedIdentifiers[issue.Identifier]; paused {
		ti.OrchestratorState = "paused"
	} else {
		ti.OrchestratorState = "idle"
		// IneligibleReason: only for active-state idle issues.
		// Calling IneligibleReason on terminal/inactive issues returns
		// "not_active_state" which is noise — filter those out.
		reason := orchestrator.IneligibleReason(issue, snap, cfg)
		if reason != "" && reason != "not_active_state" && reason != "terminal_state" {
			ti.IneligibleReason = reason
		}
	}
	return ti
}

func pendingInputResumeMessage(entry *orchestrator.PendingInputResumeEntry) string {
	if entry == nil {
		return "Reply received, waiting to resume."
	}
	if entry.Context == "" {
		return "Reply received, waiting to resume."
	}
	return fmt.Sprintf("Reply received, waiting to resume.\n\nOriginal request:\n%s", entry.Context)
}
