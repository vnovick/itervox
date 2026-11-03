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

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestPlaybookReachesWorkerPromptAndEvidenceGate (#218): a dispatched
// worker's real prompt carries the label-chosen playbook right after its
// INSTRUCTIONS.md, the Run Context names run.decisions_path, and the
// playbook's require_evidence holds the issue in the one #80 evidence gate
// (the profile itself requires nothing) instead of moving it to Done.
func TestPlaybookReachesWorkerPromptAndEvidenceGate(t *testing.T) {
	project := t.TempDir()
	pbDir := filepath.Join(project, ".itervox", "playbooks")
	require.NoError(t, os.MkdirAll(pbDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pbDir, "bug-fix.md"),
		[]byte("---\nrequire_evidence: [test]\n---\nPLAYBOOK-MARK reproduce first."), 0o644))

	cfg := baseConfig()
	cfg.WorkflowPath = filepath.Join(project, "WORKFLOW.md")
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Tracker.CompletionState = "Done"
	cfg.PromptTemplate = "Implement {{ issue.identifier }}."
	cfg.Agent.Playbooks = config.PlaybooksConfig{Enabled: true, Classify: true, Dir: config.DefaultPlaybooksDir, Labels: config.DefaultPlaybookLabels}
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"implementer": {Command: "claude", Soul: "SOUL-MARK", Instructions: "INSTRUCTIONS-MARK"},
	}
	issue := makeIssue("id1", "ENG-1", "In Progress", nil, nil)
	issue.Labels = []string{"bug"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &evidenceRunner{}
	orch := orchestrator.New(cfg, mt, runner, &recordingWorkspaceProvider{path: t.TempDir()})
	orch.SetIssueProfile("ENG-1", "implementer")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	go orch.Run(ctx) //nolint:errcheck

	require.Eventually(t, func() bool {
		_, held := orch.Snapshot().InputRequiredIssues["ENG-1"]
		return held || issueState(t, mt) == "Done"
	}, 6*time.Second, 20*time.Millisecond)
	_, held := orch.Snapshot().InputRequiredIssues["ENG-1"]
	assert.True(t, held, "the playbook's require_evidence must hold the issue")
	assert.Equal(t, "In Progress", issueState(t, mt))

	runner.mu.Lock()
	prompts := append([]string(nil), runner.prompts...)
	runner.mu.Unlock()
	require.NotEmpty(t, prompts)
	p := prompts[0]
	instr, pb := strings.Index(p, "INSTRUCTIONS-MARK"), strings.Index(p, "## Playbook: bug-fix")
	require.True(t, instr > 0 && pb > instr, "the playbook follows INSTRUCTIONS.md (instructions %d, playbook %d)", instr, pb)
	assert.Contains(t, p[pb:], "PLAYBOOK-MARK")
	assert.Contains(t, p, "run.decisions_path: `.itervox/handoff/")
	assert.Contains(t, p, "required checks: `test`")
}
