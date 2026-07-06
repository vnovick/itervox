package app

import (
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

func ptr[T any](v T) *T { return &v }

// baseIssue returns a minimal domain.Issue suitable for most tests.
func baseIssue() domain.Issue {
	return domain.Issue{
		ID:         "uuid-1",
		Identifier: "ENG-42",
		Title:      "Fix login bug",
		State:      "In Progress",
	}
}

// emptyState returns a State with all maps initialised but empty.
func emptyState() orchestrator.State {
	return orchestrator.State{
		Running:               make(map[string]*orchestrator.RunEntry),
		Claimed:               make(map[string]struct{}),
		RetryAttempts:         make(map[string]*orchestrator.RetryEntry),
		PausedIdentifiers:     make(map[string]string),
		PausedSessions:        make(map[string]*orchestrator.PausedSessionInfo),
		IssueProfiles:         make(map[string]string),
		IssueBackends:         make(map[string]string),
		ForceReanalyze:        make(map[string]struct{}),
		PrevActiveIdentifiers: make(map[string]struct{}),
		DiscardingIdentifiers: make(map[string]struct{}),
		InputRequiredIssues:   make(map[string]*orchestrator.InputRequiredEntry),
		PendingInputResumes:   make(map[string]*orchestrator.PendingInputResumeEntry),
		ActiveStates:          []string{"In Progress"},
		TerminalStates:        []string{"Done"},
		MaxConcurrentAgents:   5,
	}
}

// baseCfg returns a minimal config for enrichment tests.
func baseCfg() *config.Config {
	return &config.Config{
		Tracker: config.TrackerConfig{
			ActiveStates:   []string{"In Progress"},
			TerminalStates: []string{"Done"},
		},
		Agent: config.AgentConfig{
			MaxConcurrentAgents: 5,
		},
	}
}

func TestEnrichIssue(t *testing.T) {
	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		issue domain.Issue
		snap  orchestrator.State
		cfg   *config.Config
		check func(t *testing.T, ti server.TrackerIssue)
	}{
		{
			name:  "running issue gets state and metrics",
			issue: baseIssue(),
			snap: func() orchestrator.State {
				s := emptyState()
				s.Running["uuid-1"] = &orchestrator.RunEntry{
					Issue:       baseIssue(),
					TurnCount:   7,
					TotalTokens: 1500,
					StartedAt:   now.Add(-30 * time.Second),
					LastMessage: "reading file",
				}
				return s
			}(),
			cfg: baseCfg(),
			check: func(t *testing.T, ti server.TrackerIssue) {
				if ti.OrchestratorState != "running" {
					t.Fatalf("expected running, got %s", ti.OrchestratorState)
				}
				if ti.TurnCount != 7 {
					t.Fatalf("expected TurnCount 7, got %d", ti.TurnCount)
				}
				if ti.Tokens != 1500 {
					t.Fatalf("expected Tokens 1500, got %d", ti.Tokens)
				}
				if ti.ElapsedMs != 30000 {
					t.Fatalf("expected ElapsedMs 30000, got %d", ti.ElapsedMs)
				}
				if ti.LastMessage != "reading file" {
					t.Fatalf("expected LastMessage 'reading file', got %q", ti.LastMessage)
				}
			},
		},
		{
			name:  "retrying issue with error",
			issue: baseIssue(),
			snap: func() orchestrator.State {
				s := emptyState()
				errMsg := "rate limited"
				s.RetryAttempts["uuid-1"] = &orchestrator.RetryEntry{
					IssueID:    "uuid-1",
					Identifier: "ENG-42",
					Attempt:    2,
					Error:      &errMsg,
				}
				return s
			}(),
			cfg: baseCfg(),
			check: func(t *testing.T, ti server.TrackerIssue) {
				if ti.OrchestratorState != "retrying" {
					t.Fatalf("expected retrying, got %s", ti.OrchestratorState)
				}
				if ti.Error != "rate limited" {
					t.Fatalf("expected error 'rate limited', got %q", ti.Error)
				}
			},
		},
		{
			name:  "retrying issue without error",
			issue: baseIssue(),
			snap: func() orchestrator.State {
				s := emptyState()
				s.RetryAttempts["uuid-1"] = &orchestrator.RetryEntry{
					IssueID:    "uuid-1",
					Identifier: "ENG-42",
					Attempt:    1,
				}
				return s
			}(),
			cfg: baseCfg(),
			check: func(t *testing.T, ti server.TrackerIssue) {
				if ti.OrchestratorState != "retrying" {
					t.Fatalf("expected retrying, got %s", ti.OrchestratorState)
				}
				if ti.Error != "" {
					t.Fatalf("expected empty error, got %q", ti.Error)
				}
			},
		},
		{
			name:  "paused issue",
			issue: baseIssue(),
			snap: func() orchestrator.State {
				s := emptyState()
				s.PausedIdentifiers["ENG-42"] = "uuid-1"
				return s
			}(),
			cfg: baseCfg(),
			check: func(t *testing.T, ti server.TrackerIssue) {
				if ti.OrchestratorState != "paused" {
					t.Fatalf("expected paused, got %s", ti.OrchestratorState)
				}
			},
		},
		{
			name:  "pending input resume shows dedicated non-idle state",
			issue: baseIssue(),
			snap: func() orchestrator.State {
				s := emptyState()
				s.PendingInputResumes["ENG-42"] = &orchestrator.PendingInputResumeEntry{
					IssueID:     "uuid-1",
					Identifier:  "ENG-42",
					Context:     "Need approval",
					UserMessage: "Approved.",
				}
				return s
			}(),
			cfg: baseCfg(),
			check: func(t *testing.T, ti server.TrackerIssue) {
				if ti.OrchestratorState != "pending_input_resume" {
					t.Fatalf("expected pending_input_resume, got %s", ti.OrchestratorState)
				}
				if ti.Error == "" {
					t.Fatalf("expected pending resume message, got empty error")
				}
			},
		},
		{
			name:  "idle issue",
			issue: baseIssue(),
			snap:  emptyState(),
			cfg:   baseCfg(),
			check: func(t *testing.T, ti server.TrackerIssue) {
				if ti.OrchestratorState != "idle" {
					t.Fatalf("expected idle, got %s", ti.OrchestratorState)
				}
			},
		},
		{
			name:  "idle issue with ineligible reason (no slots)",
			issue: baseIssue(),
			snap: func() orchestrator.State {
				s := emptyState()
				s.MaxConcurrentAgents = 0
				return s
			}(),
			cfg: baseCfg(),
			check: func(t *testing.T, ti server.TrackerIssue) {
				if ti.OrchestratorState != "idle" {
					t.Fatalf("expected idle, got %s", ti.OrchestratorState)
				}
				if ti.IneligibleReason != "no_slots" {
					t.Fatalf("expected ineligible reason 'no_slots', got %q", ti.IneligibleReason)
				}
			},
		},
		{
			name:  "profile override applied",
			issue: baseIssue(),
			snap: func() orchestrator.State {
				s := emptyState()
				s.IssueProfiles["ENG-42"] = "reviewer"
				return s
			}(),
			cfg: baseCfg(),
			check: func(t *testing.T, ti server.TrackerIssue) {
				if ti.AgentProfile != "reviewer" {
					t.Fatalf("expected AgentProfile 'reviewer', got %q", ti.AgentProfile)
				}
			},
		},
		{
			name:  "empty profile override is ignored",
			issue: baseIssue(),
			snap: func() orchestrator.State {
				s := emptyState()
				s.IssueProfiles["ENG-42"] = ""
				return s
			}(),
			cfg: baseCfg(),
			check: func(t *testing.T, ti server.TrackerIssue) {
				if ti.AgentProfile != "" {
					t.Fatalf("expected empty AgentProfile, got %q", ti.AgentProfile)
				}
			},
		},
		{
			name: "description and URL mapped",
			issue: func() domain.Issue {
				i := baseIssue()
				i.Description = ptr("fix the auth flow")
				i.URL = ptr("https://linear.app/eng/ENG-42")
				return i
			}(),
			snap: emptyState(),
			cfg:  baseCfg(),
			check: func(t *testing.T, ti server.TrackerIssue) {
				if ti.Description != "fix the auth flow" {
					t.Fatalf("expected description, got %q", ti.Description)
				}
				if ti.URL != "https://linear.app/eng/ENG-42" {
					t.Fatalf("expected URL, got %q", ti.URL)
				}
			},
		},
		{
			name: "comments mapped with timestamps",
			issue: func() domain.Issue {
				i := baseIssue()
				ts := time.Date(2025, 6, 14, 10, 0, 0, 0, time.UTC)
				i.Comments = []domain.Comment{
					{AuthorName: "alice", Body: "looks good", CreatedAt: &ts},
					{AuthorName: "bob", Body: "needs work"},
				}
				return i
			}(),
			snap: emptyState(),
			cfg:  baseCfg(),
			check: func(t *testing.T, ti server.TrackerIssue) {
				if len(ti.Comments) != 2 {
					t.Fatalf("expected 2 comments, got %d", len(ti.Comments))
				}
				if ti.Comments[0].Author != "alice" {
					t.Fatalf("expected author alice, got %q", ti.Comments[0].Author)
				}
				if ti.Comments[0].CreatedAt == "" {
					t.Fatal("expected non-empty CreatedAt for first comment")
				}
				if ti.Comments[1].CreatedAt != "" {
					t.Fatalf("expected empty CreatedAt for second comment, got %q", ti.Comments[1].CreatedAt)
				}
			},
		},
		{
			name: "blockedBy mapped from BlockerRef",
			issue: func() domain.Issue {
				i := baseIssue()
				i.BlockedBy = []domain.BlockerRef{
					{Identifier: ptr("ENG-10"), State: ptr("In Progress"), URL: ptr("https://linear.app/issue/ENG-10")},
					{Identifier: ptr("")}, // empty identifier is skipped
					{Identifier: nil},     // nil identifier is skipped
					{Identifier: ptr("ENG-11"), State: ptr("Done")},
				}
				return i
			}(),
			snap: emptyState(),
			cfg:  baseCfg(),
			check: func(t *testing.T, ti server.TrackerIssue) {
				if len(ti.BlockedBy) != 2 {
					t.Fatalf("expected 2 blockers, got %d: %v", len(ti.BlockedBy), ti.BlockedBy)
				}
				if ti.BlockedBy[0] != "ENG-10" || ti.BlockedBy[1] != "ENG-11" {
					t.Fatalf("unexpected blockedBy: %v", ti.BlockedBy)
				}
				if len(ti.BlockedByDetails) != 2 {
					t.Fatalf("expected 2 blocker details, got %d: %v", len(ti.BlockedByDetails), ti.BlockedByDetails)
				}
				if ti.BlockedByDetails[0].Identifier != "ENG-10" {
					t.Fatalf("expected first blocker identifier ENG-10, got %q", ti.BlockedByDetails[0].Identifier)
				}
				if ti.BlockedByDetails[0].State != "In Progress" {
					t.Fatalf("expected first blocker state In Progress, got %q", ti.BlockedByDetails[0].State)
				}
				if ti.BlockedByDetails[0].URL != "https://linear.app/issue/ENG-10" {
					t.Fatalf("expected first blocker URL, got %q", ti.BlockedByDetails[0].URL)
				}
				if ti.BlockedByDetails[1].Identifier != "ENG-11" {
					t.Fatalf("expected second blocker identifier ENG-11, got %q", ti.BlockedByDetails[1].Identifier)
				}
				if ti.BlockedByDetails[1].State != "Done" {
					t.Fatalf("expected second blocker state Done, got %q", ti.BlockedByDetails[1].State)
				}
				if ti.BlockedByDetails[1].URL != "" {
					t.Fatalf("expected second blocker URL to be empty, got %q", ti.BlockedByDetails[1].URL)
				}
			},
		},
		{
			name: "labels and priority passed through",
			issue: func() domain.Issue {
				i := baseIssue()
				i.Labels = []string{"bug", "critical"}
				i.Priority = ptr(1)
				return i
			}(),
			snap: emptyState(),
			cfg:  baseCfg(),
			check: func(t *testing.T, ti server.TrackerIssue) {
				if len(ti.Labels) != 2 || ti.Labels[0] != "bug" {
					t.Fatalf("expected labels [bug critical], got %v", ti.Labels)
				}
				if ti.Priority == nil || *ti.Priority != 1 {
					t.Fatalf("expected priority 1, got %v", ti.Priority)
				}
			},
		},
		{
			name:  "running takes precedence over paused",
			issue: baseIssue(),
			snap: func() orchestrator.State {
				s := emptyState()
				s.Running["uuid-1"] = &orchestrator.RunEntry{
					Issue:     baseIssue(),
					StartedAt: now,
				}
				s.PausedIdentifiers["ENG-42"] = "uuid-1"
				return s
			}(),
			cfg: baseCfg(),
			check: func(t *testing.T, ti server.TrackerIssue) {
				if ti.OrchestratorState != "running" {
					t.Fatalf("expected running (precedence), got %s", ti.OrchestratorState)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ti := EnrichIssue(tt.issue, tt.snap, now, tt.cfg)
			// Common assertions
			if ti.Identifier != tt.issue.Identifier {
				t.Fatalf("Identifier mismatch: got %q", ti.Identifier)
			}
			if ti.Title != tt.issue.Title {
				t.Fatalf("Title mismatch: got %q", ti.Title)
			}
			if ti.State != tt.issue.State {
				t.Fatalf("State mismatch: got %q", ti.State)
			}
			tt.check(t, ti)
		})
	}
}
