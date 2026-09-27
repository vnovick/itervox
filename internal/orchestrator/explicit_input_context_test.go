package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// CORE-164: the explicit input-required question is built from agent-written
// text, never from FailureText's stderr segment.
func TestExplicitInputRequiredContextNeverUsesStderr(t *testing.T) {
	const secret = "SENTINEL_STDERR_SECRET_b41d"
	failure := "Human turn required | stderr: export GITHUB_TOKEN=" + secret
	for _, tc := range []struct {
		name   string
		result agent.TurnResult
		want   string
	}{
		{"result text wins", agent.TurnResult{ResultText: "Deploy to prod?", LastText: "earlier", Failed: true, FailureText: failure}, "Deploy to prod?"},
		{"last assistant text", agent.TurnResult{LastText: "Which branch should I rebase onto?", Failed: true, FailureText: failure}, "Which branch should I rebase onto?"},
		{"vendor message, stderr split off", agent.TurnResult{Failed: true, FailureText: failure}, "Human turn required"},
		{"stderr only falls back to the default", agent.TurnResult{Failed: true, FailureText: "stderr: export GITHUB_TOKEN=" + secret}, explicitInputRequiredDefault},
		{"nothing at all", agent.TurnResult{}, explicitInputRequiredDefault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := explicitInputRequiredContext(tc.result)
			assert.Equal(t, tc.want, got)
			assert.NotContains(t, got, secret)
		})
	}
}

// CORE-164: every input-required exit funnels through queueInputRequiredEntry,
// which redacts the context before it can become a tracker comment.
func TestQueueInputRequiredEntryRedactsContext(t *testing.T) {
	cfg := testConfig()
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "t", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, &blockedRunner{}, nil)
	token := "sk-ant-" + strings.Repeat("a1B2", 10)

	o.queueInputRequiredEntry(context.Background(), issue, 0, "run-1", nil, "claude", "claude", "", "", "",
		"Should I use key "+token+" for the deploy?", "", &RunEntry{Issue: issue, StartedAt: time.Now()})

	select {
	case ev := <-o.events:
		require.NotNil(t, ev.InputRequiredEntry)
		assert.NotContains(t, ev.InputRequiredEntry.Context, token)
		assert.Contains(t, ev.InputRequiredEntry.Context, "Should I use key")
		assert.NotContains(t, buildInputRequiredComment(ev.InputRequiredEntry, false), token)
	case <-time.After(2 * time.Second):
		t.Fatal("no exit event sent")
	}
}
