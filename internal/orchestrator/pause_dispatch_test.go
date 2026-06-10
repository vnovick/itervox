package orchestrator

import (
	"testing"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

func TestNormalizePauseStates_DedupsAndLowercases(t *testing.T) {
	got := normalizePauseStates([]string{"In Review", " in review ", "in review", "InReview"})
	if len(got) != 2 {
		t.Fatalf("got %v; want 2 entries", got)
	}
	if got[0] != "in review" || got[1] != "inreview" {
		t.Errorf("got %v; want [\"in review\", \"inreview\"]", got)
	}
}

func TestPausedByAnyInState_EmptyConfigShortCircuit(t *testing.T) {
	state := State{}
	if _, paused := pausedByAnyInState(state); paused {
		t.Error("empty config must not fire guard")
	}
}

func TestPausedByAnyInState_RunningInPauseState(t *testing.T) {
	state := State{
		PauseDispatchWhenAnyInState: []string{"in review"},
		Running: map[string]*RunEntry{
			"id1": {Issue: domain.Issue{State: "In Review"}},
		},
	}
	matched, paused := pausedByAnyInState(state)
	if !paused {
		t.Fatal("expected guard to fire")
	}
	if matched != "In Review" {
		t.Errorf("got %q; want In Review", matched)
	}
}

func TestPausedByAnyInState_PrevStateMatch(t *testing.T) {
	state := State{
		PauseDispatchWhenAnyInState: []string{"in review"},
		PrevIssueStates: map[string]string{
			"ENG-1": "In Review",
		},
	}
	matched, paused := pausedByAnyInState(state)
	if !paused {
		t.Fatal("expected guard to fire")
	}
	if matched != "In Review" {
		t.Errorf("got %q; want In Review", matched)
	}
}

func TestPausedByAnyInState_NoMatch(t *testing.T) {
	state := State{
		PauseDispatchWhenAnyInState: []string{"in review"},
		Running: map[string]*RunEntry{
			"id1": {Issue: domain.Issue{State: "Todo"}},
		},
		PrevIssueStates: map[string]string{
			"ENG-1": "In Progress",
		},
	}
	if _, paused := pausedByAnyInState(state); paused {
		t.Error("guard should not fire when no issue is in a pause state")
	}
}

func TestIneligibleReasonShared_ReturnsPausedByStateBeforeBlockerCheck(t *testing.T) {
	cfg := &config.Config{}
	cfg.Agent.MaxConcurrentAgents = 10
	state := State{
		MaxConcurrentAgents:         10,
		PauseDispatchWhenAnyInState: []string{"in review"},
		Running: map[string]*RunEntry{
			"id-other": {Issue: domain.Issue{State: "In Review"}},
		},
	}
	issue := domain.Issue{ID: "ID-X", Identifier: "ENG-2", State: "Todo"}
	got := ineligibleReasonShared(issue, state, cfg, false)
	want := "paused_by_state:In Review"
	if got != want {
		t.Errorf("got %q; want %q", got, want)
	}
}
