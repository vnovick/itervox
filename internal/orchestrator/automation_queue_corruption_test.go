package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/config"
)

// TestLoadAutomationQueue_EmptyFile_ReturnsEmptyQueue — todolist4 P2-12 named.
// Loading a path with an empty file (zero-byte) must not panic and must
// produce an empty queue state.
func TestLoadAutomationQueue_EmptyFile_ReturnsEmptyQueue(t *testing.T) {
	cfg := &config.Config{}
	path := filepath.Join(t.TempDir(), "automation_queue.json")
	require.NoError(t, os.WriteFile(path, []byte(""), 0o644))

	r := New(cfg, nil, nil, nil)
	r.SetAutomationQueueFile(path)
	state := r.loadAutomationQueueFromDisk(NewState(cfg))

	require.Empty(t, state.AutomationQueue)
	require.Empty(t, state.AutomationQueueOrder)
}

// TestLoadAutomationQueue_MalformedJSON_ReturnsEmptyQueueWithWarn —
// todolist4 P2-12. Malformed JSON returns the original state without panic;
// the warning is logged at runtime.
func TestLoadAutomationQueue_MalformedJSON_ReturnsEmptyQueueWithWarn(t *testing.T) {
	cfg := &config.Config{}
	path := filepath.Join(t.TempDir(), "automation_queue.json")
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o644))

	r := New(cfg, nil, nil, nil)
	r.SetAutomationQueueFile(path)
	state := r.loadAutomationQueueFromDisk(NewState(cfg))

	require.Empty(t, state.AutomationQueue)
	require.Empty(t, state.AutomationQueueOrder)
}

// TestLoadAutomationQueue_PartiallyValidEntries_DropsBadOnesPreservesGood —
// todolist4 P2-12. An entries map with one valid + one nil + one missing
// required fields must surface only the valid one.
func TestLoadAutomationQueue_PartiallyValidEntries_DropsBadOnesPreservesGood(t *testing.T) {
	cfg := &config.Config{}
	path := filepath.Join(t.TempDir(), "automation_queue.json")
	payload := `{
		"Entries": {
			"good": {"ID":"good","AutomationID":"a","TriggerType":"cron","ProfileName":"p","Issue":{"ID":"x"},"Reason":"no_slots"},
			"missing_fields": {"ID":"missing_fields","TriggerType":"","ProfileName":""},
			"nil_entry": null
		},
		"Order": ["good", "missing_fields", "nil_entry"]
	}`
	require.NoError(t, os.WriteFile(path, []byte(payload), 0o644))

	r := New(cfg, nil, nil, nil)
	r.SetAutomationQueueFile(path)
	state := r.loadAutomationQueueFromDisk(NewState(cfg))

	require.Len(t, state.AutomationQueue, 1, "only the well-formed entry should survive")
	if _, ok := state.AutomationQueue["good"]; !ok {
		t.Errorf("expected 'good' entry to survive; got %v", keys(state.AutomationQueue))
	}
	require.Equal(t, []string{"good"}, state.AutomationQueueOrder)
}

func keys(m map[string]*AutomationQueueEntry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
