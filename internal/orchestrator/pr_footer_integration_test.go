package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workspace"
)

// runFooterWorker dispatches ENG-3 once with agent.pr_footer set to footer,
// against a fake gh that reports prURL as the branch's open PR and keeps its
// body in a file. Returns the final body and the gh calls.
func runFooterWorker(t *testing.T, footer bool) (body string, calls []string) {
	t.Helper()
	const prURL = "https://github.com/o/r/pull/31"
	dir := t.TempDir()
	bodyFile := filepath.Join(dir, "body")
	logFile := filepath.Join(dir, "calls")
	require.NoError(t, os.WriteFile(bodyFile, []byte("Agent-written description.\n"), 0o644))
	script := `#!/bin/sh
echo "$*" >> "` + logFile + `"
case "$*" in
  "pr view --json url,state "*) echo "` + prURL + `" ;;
  "pr view ` + prURL + ` --json body"*) cat "` + bodyFile + `" ;;
  "pr edit ` + prURL + ` --body-file -") cat > "` + bodyFile + `" ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := baseConfig()
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Agent.PRFooter = footer
	cfg.Tracker.CompletionState = "Done"
	mt := tracker.NewMemoryTracker(
		[]domain.Issue{makeIssue("id3", "ENG-3", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates,
	)
	runner := &promptCaptureRunner{done: make(chan struct{}, 1)}
	orch := orchestrator.New(cfg, mt, runner, &stackedWorkspaceProvider{path: t.TempDir()})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go orch.Run(ctx) //nolint:errcheck
	require.Eventually(t, func() bool {
		issues, err := mt.FetchIssueStatesByIDs(ctx, []string{"id3"})
		return err == nil && len(issues) == 1 && issues[0].State == "Done"
	}, 6*time.Second, 20*time.Millisecond)
	cancel()

	raw, _ := os.ReadFile(bodyFile)
	logged, _ := os.ReadFile(logFile)
	return string(raw), strings.Split(strings.TrimSpace(string(logged)), "\n")
}

// TestPRFooterAddedOncePerPR (#81): with agent.pr_footer on, the PR a run
// produced gets the footer appended to its body; with it off (the default)
// the body is never read or edited.
func TestPRFooterAddedOncePerPR(t *testing.T) {
	body, _ := runFooterWorker(t, true)
	assert.Equal(t, "Agent-written description.\n\n---\n"+workspace.PRFooterMarker+"\n"+workspace.PRFooterText+"\n", body)

	body, calls := runFooterWorker(t, false)
	assert.Equal(t, "Agent-written description.\n", body)
	for _, c := range calls {
		assert.NotContains(t, c, "--json body", "off by default: the body is not touched")
	}
}
