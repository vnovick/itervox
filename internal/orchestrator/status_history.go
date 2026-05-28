package orchestrator

import (
	"log/slog"
	"time"

	"github.com/vnovick/itervox/internal/domain"
)

type IssueStatusSource string

const (
	StatusSourceTrackerObserved IssueStatusSource = "tracker_observed"
	StatusSourceDashboard       IssueStatusSource = "dashboard"
	StatusSourceWorkerLifecycle IssueStatusSource = "worker_lifecycle"
	StatusSourceAutomation      IssueStatusSource = "automation"
	StatusSourceSystem          IssueStatusSource = "system"

	maxIssueStatusHistory = 50
)

type IssueStatusChange struct {
	Identifier   string
	FromState    string
	ToState      string
	Source       IssueStatusSource
	AutomationID string
	TriggerType  string
	ProfileName  string
	Backend      string
	WorkerHost   string
	At           time.Time
}

func recordObservedIssueState(state *State, issue domain.Issue, now time.Time) {
	if issue.Identifier == "" || issue.State == "" {
		return
	}
	if state.PrevIssueStates == nil {
		state.PrevIssueStates = make(map[string]string)
	}
	prev := state.PrevIssueStates[issue.Identifier]
	if prev == "" {
		state.PrevIssueStates[issue.Identifier] = issue.State
		return
	}
	appendIssueStatusChange(state, IssueStatusChange{
		Identifier: issue.Identifier,
		FromState:  prev,
		ToState:    issue.State,
		Source:     StatusSourceTrackerObserved,
		At:         now,
	})
}

func appendIssueStatusChange(state *State, change IssueStatusChange) {
	if change.Identifier == "" || change.ToState == "" {
		return
	}
	if state.PrevIssueStates == nil {
		state.PrevIssueStates = make(map[string]string)
	}
	if state.IssueStatusHistory == nil {
		state.IssueStatusHistory = make(map[string][]IssueStatusChange)
	}
	if change.Source == "" {
		change.Source = StatusSourceSystem
	}
	if change.At.IsZero() {
		change.At = time.Now()
	}
	if change.FromState == "" {
		change.FromState = state.PrevIssueStates[change.Identifier]
	}
	if change.FromState == change.ToState {
		state.PrevIssueStates[change.Identifier] = change.ToState
		return
	}

	history := state.IssueStatusHistory[change.Identifier]
	if len(history) > 0 {
		last := history[len(history)-1]
		if last.FromState == change.FromState &&
			last.ToState == change.ToState &&
			last.Source == change.Source &&
			last.AutomationID == change.AutomationID {
			state.PrevIssueStates[change.Identifier] = change.ToState
			return
		}
	}

	history = append(history, change)
	if len(history) > maxIssueStatusHistory {
		history = history[len(history)-maxIssueStatusHistory:]
	}
	state.IssueStatusHistory[change.Identifier] = history
	state.PrevIssueStates[change.Identifier] = change.ToState
}

func (o *Orchestrator) RecordIssueStatusChange(change IssueStatusChange) bool {
	if change.Identifier == "" || change.ToState == "" {
		return false
	}
	select {
	case o.events <- OrchestratorEvent{Type: EventIssueStatusChanged, StatusChange: &change}:
		return true
	default:
		slog.Warn("orchestrator: status change event dropped", "identifier", change.Identifier)
		return false
	}
}
