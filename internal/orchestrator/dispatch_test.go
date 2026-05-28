package orchestrator_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
)

func baseConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Tracker.ActiveStates = []string{"Todo", "In Progress"}
	cfg.Tracker.TerminalStates = []string{"Done", "Cancelled"}
	cfg.Agent.MaxConcurrentAgents = 3
	cfg.Agent.MaxConcurrentAgentsByState = map[string]int{}
	cfg.Agent.MaxTurns = 5 // non-zero so the turn loop actually executes
	return cfg
}

func makeIssue(id, identifier, state string, priority *int, createdAt *time.Time) domain.Issue {
	return domain.Issue{
		ID: id, Identifier: identifier, Title: "T", State: state,
		Priority: priority, CreatedAt: createdAt,
	}
}

func prio(n int) *int { return &n }

func TestIsEligibleHappyPath(t *testing.T) {
	cfg := baseConfig()
	state := orchestrator.NewState(cfg)
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	assert.True(t, orchestrator.IsEligible(issue, state, cfg))
}

func TestIsEligibleAlreadyRunning(t *testing.T) {
	cfg := baseConfig()
	state := orchestrator.NewState(cfg)
	state.Running["id1"] = &orchestrator.RunEntry{}
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	assert.False(t, orchestrator.IsEligible(issue, state, cfg))
}

func TestIsEligibleAlreadyClaimed(t *testing.T) {
	cfg := baseConfig()
	state := orchestrator.NewState(cfg)
	state.Claimed["id1"] = struct{}{}
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	assert.False(t, orchestrator.IsEligible(issue, state, cfg))
}

func TestIsEligibleNonActiveState(t *testing.T) {
	cfg := baseConfig()
	state := orchestrator.NewState(cfg)
	issue := makeIssue("id1", "ENG-1", "Done", nil, nil)
	assert.False(t, orchestrator.IsEligible(issue, state, cfg))
}

func TestIsEligibleNoSlotsAvailable(t *testing.T) {
	cfg := baseConfig()
	cfg.Agent.MaxConcurrentAgents = 1
	state := orchestrator.NewState(cfg)
	state.Running["other"] = &orchestrator.RunEntry{}
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	assert.False(t, orchestrator.IsEligible(issue, state, cfg))
}

func TestIsEligibleTodoWithNonTerminalBlocker(t *testing.T) {
	cfg := baseConfig()
	state := orchestrator.NewState(cfg)
	blockerState := "In Progress"
	issue := makeIssue("id1", "ENG-1", "Todo", nil, nil)
	issue.BlockedBy = []domain.BlockerRef{{State: &blockerState}}
	assert.False(t, orchestrator.IsEligible(issue, state, cfg))
}

func TestIsEligibleTodoWithUnknownBlockerIsBlocked(t *testing.T) {
	cfg := baseConfig()
	state := orchestrator.NewState(cfg)
	blockerID := "ENG-0"
	issue := makeIssue("id1", "ENG-1", "Todo", nil, nil)
	issue.BlockedBy = []domain.BlockerRef{{Identifier: &blockerID}}

	assert.Equal(t, "blocked_by:ENG-0", orchestrator.IneligibleReason(issue, state, cfg))
	assert.False(t, orchestrator.IsEligible(issue, state, cfg))
}

func TestIsEligibleTodoWithTerminalBlockerIsEligible(t *testing.T) {
	cfg := baseConfig()
	state := orchestrator.NewState(cfg)
	blockerState := "Done"
	issue := makeIssue("id1", "ENG-1", "Todo", nil, nil)
	issue.BlockedBy = []domain.BlockerRef{{State: &blockerState}}
	assert.True(t, orchestrator.IsEligible(issue, state, cfg))
}

func TestSortForDispatch(t *testing.T) {
	t1 := time.Unix(1000, 0)
	t2 := time.Unix(2000, 0)
	issues := []domain.Issue{
		makeIssue("c", "ENG-3", "Todo", nil, &t1),     // null priority, older
		makeIssue("a", "ENG-1", "Todo", prio(2), &t2), // priority 2
		makeIssue("b", "ENG-2", "Todo", prio(1), &t1), // priority 1, oldest
	}
	sorted := orchestrator.SortForDispatch(issues)
	assert.Equal(t, "ENG-2", sorted[0].Identifier) // prio 1 first
	assert.Equal(t, "ENG-1", sorted[1].Identifier) // prio 2 second
	assert.Equal(t, "ENG-3", sorted[2].Identifier) // null prio last
}

func TestDependencyAwareDispatchOrder(t *testing.T) {
	cfg := baseConfig()
	state := orchestrator.NewState(cfg)
	created := time.Unix(1000, 0)
	blockerState := "In Progress"
	blockerID := "ENG-0"
	dependent := makeIssue("dependent", "ENG-1", "Todo", prio(0), &created)
	dependent.BlockedBy = []domain.BlockerRef{{Identifier: &blockerID, State: &blockerState}}
	blocker := makeIssue("blocker", "ENG-0", "Todo", prio(1), &created)

	sorted := orchestrator.SortForDispatch([]domain.Issue{dependent, blocker})
	var eligible []domain.Issue
	for _, issue := range sorted {
		if orchestrator.IsEligible(issue, state, cfg) {
			eligible = append(eligible, issue)
		}
	}
	assert.Equal(t, []string{"ENG-0"}, []string{eligible[0].Identifier})

	done := "Done"
	dependent.BlockedBy[0].State = &done
	sorted = orchestrator.SortForDispatch([]domain.Issue{dependent, blocker})
	eligible = eligible[:0]
	for _, issue := range sorted {
		if orchestrator.IsEligible(issue, state, cfg) {
			eligible = append(eligible, issue)
		}
	}
	assert.Equal(t, []string{"ENG-1", "ENG-0"}, []string{eligible[0].Identifier, eligible[1].Identifier})
}

func TestAvailableSlots(t *testing.T) {
	cfg := baseConfig()
	cfg.Agent.MaxConcurrentAgents = 3
	state := orchestrator.NewState(cfg)
	state.Running["a"] = &orchestrator.RunEntry{}
	assert.Equal(t, 2, orchestrator.AvailableSlots(state))
}
