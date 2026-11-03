package demoagent

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
)

type recordingLogger struct {
	mu   sync.Mutex
	msgs []string
}

func (l *recordingLogger) Info(msg string, _ ...any) {
	l.mu.Lock()
	l.msgs = append(l.msgs, msg)
	l.mu.Unlock()
}
func (l *recordingLogger) Warn(msg string, _ ...any) { l.Info(msg) }
func (l *recordingLogger) Debug(string, ...any)      {}

// TestDemoRunnerScripts pins the three demo scripts (#76): DEMO-2 asks for
// input then finishes, DEMO-4 fails then succeeds on the retry, others
// succeed at once; every turn streams progress and a successful one logs a
// pr_opened line.
func TestDemoRunnerScripts(t *testing.T) {
	r := NewDemoRunner(time.Millisecond)
	run := func(identifier string, session *string) (agent.TurnResult, *recordingLogger, int) {
		log := &recordingLogger{}
		progress := 0
		res, err := r.RunTurn(context.Background(), log, func(agent.TurnResult) { progress++ }, session,
			"prompt", filepath.Join("/tmp/ws", identifier), "claude", "", "", 0, 0, "")
		require.NoError(t, err)
		return res, log, progress
	}

	ok, log, progress := run("DEMO-1", nil)
	assert.False(t, ok.Failed)
	assert.False(t, ok.InputRequired)
	assert.Positive(t, progress)
	assert.Contains(t, log.msgs, "worker: pr_opened")
	assert.Contains(t, ok.ResultText, "https://example.com/itervox-demo/pull/101")

	ask, _, _ := run("DEMO-2", nil)
	assert.True(t, ask.InputRequired)
	assert.Contains(t, ask.ResultText, "Should the invoice total round")
	resumed, _, _ := run("DEMO-2", &ask.SessionID)
	assert.False(t, resumed.InputRequired)
	assert.False(t, resumed.Failed)
	assert.Equal(t, ask.SessionID, resumed.SessionID, "a resume continues the session")

	fail, _, _ := run("DEMO-4", nil)
	assert.True(t, fail.Failed)
	assert.NotEmpty(t, fail.FailureText)
	assert.Positive(t, fail.InputTokens, "a failure with tokens is a real failure, not a clean session end")
	retry, _, _ := run("DEMO-4", nil)
	assert.False(t, retry.Failed)
	assert.Equal(t, 2, r.Turns("DEMO-4"))
}

func TestDemoRunnerStopsOnCancel(t *testing.T) {
	r := NewDemoRunner(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.RunTurn(ctx, nil, nil, nil, "", "/tmp/ws/DEMO-1", "", "", "", 0, 0, "")
	assert.ErrorIs(t, err, context.Canceled)
}
