package orchestrator

import (
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/config"
)

// TestPruneTerminalRuntimeLedgers_EmitsIssueTerminalReason — codex-B2.
// When pruning removes runtime entries, a status-history row with
// source=janitor, reason=issue_terminal must be appended for each affected
// identifier.
func TestPruneTerminalRuntimeLedgers_EmitsIssueTerminalReason(t *testing.T) {
	cfg := &config.Config{}
	state := NewState(cfg)
	state.PrevIssueStates = map[string]string{"ENG-1": "Done"}
	state.InputRequiredIssues = map[string]*InputRequiredEntry{
		"ENG-1": {IssueID: "id", Context: "q", QueuedAt: time.Now()},
	}
	terminal := map[string]struct{}{"ENG-1": {}}

	counts := pruneTerminalRuntimeLedgers(&state, terminal)
	if counts.InputRequired != 1 {
		t.Fatalf("expected 1 input-required entry pruned; got %+v", counts)
	}
	history := state.IssueStatusHistory["ENG-1"]
	found := false
	for _, c := range history {
		if c.Source == StatusSourceJanitor && c.Reason == JanitorReasonIssueTerminal {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a janitor/issue_terminal status row; got %+v", history)
	}
}

func TestJanitorReasonStringsAreCanonical(t *testing.T) {
	if JanitorReasonIssueTerminal != "issue_terminal" {
		t.Errorf("issue_terminal constant drifted: %q", JanitorReasonIssueTerminal)
	}
	if JanitorReasonAbsentFromTracker != "absent_from_tracker" {
		t.Errorf("absent_from_tracker constant drifted: %q", JanitorReasonAbsentFromTracker)
	}
}
