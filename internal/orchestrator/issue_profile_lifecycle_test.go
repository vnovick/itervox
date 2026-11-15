package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agent/agenttest"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// TestIssueProfileOverridePersistsAcrossRestart: the operator's per-issue
// profile override is written to issue_profiles.json and loaded by the next
// orchestrator (a restart, or the next generation after a config reload);
// clearing it removes it from the file too.
func TestIssueProfileOverridePersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".itervox", "issue_profiles.json")
	cfg := automationBaseCfg()
	first := New(cfg, nil, nil, nil)
	first.SetIssueProfilesFile(path)
	first.SetIssueProfile("ENG-1", "researcher")
	first.SetIssueProfile("ENG-2", "implementer")

	second := New(cfg, nil, nil, nil)
	second.SetIssueProfilesFile(path)
	assert.Equal(t, "researcher", second.Snapshot().IssueProfiles["ENG-1"])
	assert.Equal(t, "researcher", second.issueProfileForDispatch(NewState(cfg), "ENG-1"))
	assert.False(t, second.issueProfileSetAt["ENG-1"].IsZero(), "the set time persists with the override")

	second.SetIssueProfile("ENG-1", "")
	third := New(cfg, nil, nil, nil)
	third.SetIssueProfilesFile(path)
	assert.NotContains(t, third.Snapshot().IssueProfiles, "ENG-1", "a cleared override stays cleared")
	assert.Equal(t, "implementer", third.Snapshot().IssueProfiles["ENG-2"])
}

// TestRateLimitedSwitchReassignsIssueProfile: a rate_limited rule with
// switch_to_profile "reassigns the issue to this profile" (docs). With an
// operator override on the primary profile, the switch used to be masked:
// the fallback run used implementer-fallback, but the issue's agentProfile
// (Snapshot) and its next dispatch stayed on the primary. The switch now
// supersedes the older override until it is reverted
// (switch_revert_hours), after which the operator's choice is back; an
// override set after the switch wins. The operator's map is never written
// by the switch.
func TestRateLimitedSwitchReassignsIssueProfile(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.MaxRetries = 1
	cfg.Agent.MaxSwitchesPerIssuePerWindow = 5
	cfg.Agent.SwitchWindowHours = 6
	cfg.Agent.SwitchRevertHours = 1
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"implementer":          {Command: "claude"},
		"implementer-fallback": {Command: "codex"},
		"other":                {Command: "claude"},
	}
	issue := automationIssue("In Progress")
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := &Orchestrator{
		cfg:           cfg,
		tracker:       mt,
		runner:        &agenttest.FakeRunner{Stall: true},
		events:        make(chan OrchestratorEvent, 2),
		workerCancels: map[string]context.CancelFunc{},
	}
	o.SetRateLimitedAutomations([]RateLimitedAutomation{{
		ID:              "rl-fallback",
		ProfileName:     "implementer",
		SwitchToProfile: "implementer-fallback",
		SwitchToBackend: "codex",
		AutoResume:      true,
	}})
	o.SetIssueProfile(issue.Identifier, "implementer") // the operator's pin
	time.Sleep(2 * time.Millisecond)

	attempt := 1
	run := &RunEntry{Issue: issue, Backend: "claude", ProfileName: "implementer", RetryAttempt: &attempt}
	state := NewState(cfg)
	state.Running[issue.ID] = run
	state.Claimed[issue.ID] = struct{}{}
	out := o.handleEvent(t.Context(), state, OrchestratorEvent{
		Type: EventWorkerExited, IssueID: issue.ID, RunEntry: run, Error: errors.New("rate_limit_exceeded"),
	})
	require.Contains(t, out.Running, issue.ID)
	assert.Equal(t, "implementer-fallback", out.Running[issue.ID].ProfileName, "the fallback run uses the switch target")

	o.storeSnap(out)
	assert.Equal(t, "implementer-fallback", o.Snapshot().IssueProfiles[issue.Identifier],
		"the issue's agentProfile must show the switch target")
	assert.Equal(t, "codex", o.Snapshot().IssueBackends[issue.Identifier])
	assert.Equal(t, "implementer-fallback", o.issueProfileForDispatch(out, issue.Identifier),
		"the issue's next dispatch must use the switch target")
	o.issueProfilesMu.RLock()
	assert.Equal(t, "implementer", o.issueProfiles[issue.Identifier], "the switch never writes the operator's override")
	o.issueProfilesMu.RUnlock()

	// switch_revert_hours: once the switch expires, the operator's pin is back.
	reverted := out.Clone()
	assert.Equal(t, 1, RevertExpiredAutoSwitches(&reverted, time.Hour, time.Now().Add(2*time.Hour)))
	assert.Equal(t, "implementer", o.issueProfileForDispatch(reverted, issue.Identifier))
	o.storeSnap(reverted)
	assert.Equal(t, "implementer", o.Snapshot().IssueProfiles[issue.Identifier])

	// An override the operator sets after the switch is the newer choice.
	o.storeSnap(out)
	time.Sleep(2 * time.Millisecond)
	o.SetIssueProfile(issue.Identifier, "other")
	assert.Equal(t, "other", o.issueProfileForDispatch(out, issue.Identifier))
	assert.Equal(t, "other", o.Snapshot().IssueProfiles[issue.Identifier])
}

// questionComment is an unanswered Itervox input-required question.
func questionComment(id, question string) domain.Comment {
	return domain.Comment{
		ID: id, AuthorID: "itervox", AuthorName: "Itervox",
		Body: itervoxCommentPrefix + "\n\n" + question + "\n\n---\n_Reply in the tracker or via the Itervox dashboard to continue._",
	}
}

// TestDismissedInputQuestionIsNotRehydrated: dismiss-input answers
// "dismissed and paused", but once the issue was active again the claim
// read rehydrated an input-required entry from the same unanswered question
// comment, blocking the issue (input_required) until someone replied. The
// dismissed question is now recorded by its comment identity: the same
// comment is not rehydrated again (also after a restart), and a NEW
// question still blocks.
func TestDismissedInputQuestionIsNotRehydrated(t *testing.T) {
	cfg := automationBaseCfg()
	issue := automationIssue("In Progress")
	issue.Comments = []domain.Comment{questionComment("q1", "Which database should I use?")}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	o := New(cfg, mt, &agenttest.FakeRunner{Stall: true}, nil)
	o.DryRun = true // a dispatch claims the issue without starting a worker
	o.SetInputRequiredFile(filepath.Join(t.TempDir(), "input_required.json"))
	ctx := t.Context()
	admit := func(state *State) {
		t.Helper()
		detailed, err := mt.FetchIssueDetail(ctx, issue.ID)
		require.NoError(t, err)
		o.admitClaimRead(ctx, state, issue, detailed, nil, time.Now())
	}

	state := NewState(cfg)
	admit(&state)
	require.Contains(t, state.InputRequiredIssues, issue.Identifier, "precondition: the question is rehydrated")

	state = o.handleEvent(ctx, state, OrchestratorEvent{Type: EventDismissInput, Identifier: issue.Identifier})
	require.Contains(t, state.PausedIdentifiers, issue.Identifier, "dismissed and paused")
	state = o.handleEvent(ctx, state, OrchestratorEvent{Type: EventResumeIssue, Identifier: issue.Identifier})
	require.NotContains(t, state.PausedIdentifiers, issue.Identifier)

	admit(&state)
	assert.NotContains(t, state.InputRequiredIssues, issue.Identifier,
		"a dismissed question must not be rehydrated from the same tracker comment")
	assert.Contains(t, state.Claimed, issue.ID, "the issue dispatches fresh instead")

	// The dismissal survives a restart (input_required.json).
	o.storeSnap(state)
	restarted := o.loadInputRequiredFromDisk(NewState(cfg))
	assert.Equal(t, "q1", restarted.DismissedInputQuestions[issue.Identifier].CommentID)

	// A new question on the same issue still blocks.
	delete(state.Claimed, issue.ID)
	newQ, err := mt.CreateComment(ctx, issue.ID, questionComment("", "And which cache?").Body)
	require.NoError(t, err)
	admit(&state)
	require.Contains(t, state.InputRequiredIssues, issue.Identifier, "a NEW question still blocks")
	assert.Equal(t, newQ.ID, state.InputRequiredIssues[issue.Identifier].QuestionCommentID)
}

// cmdRecordingRunner records the command each turn ran with and stalls
// until its context ends. Synchronized: RunTurn runs on worker goroutines.
type cmdRecordingRunner struct {
	mu   sync.Mutex
	cmds []string
}

func (r *cmdRecordingRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, command, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mu.Lock()
	r.cmds = append(r.cmds, command)
	r.mu.Unlock()
	<-ctx.Done()
	return agent.TurnResult{}, ctx.Err()
}

func (r *cmdRecordingRunner) commands() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.cmds...)
}

// TestRehydratedInputResumeUsesIssueProfile: a rehydrated input-required
// entry carries no command or profile, so provide-input logged "resume
// entry had empty command, resolved from config … profile=\"\"" and ran the
// default agent command, not the issue's agentProfile. The rehydrated entry
// now records the issue's effective profile and the resume resolves it.
func TestRehydratedInputResumeUsesIssueProfile(t *testing.T) {
	cfg := automationBaseCfg()
	cfg.Agent.Command = "claude"
	cfg.Agent.MaxTurns = 1
	cfg.Agent.Profiles = map[string]config.AgentProfile{
		"researcher": {Command: "claude --model researcher-model"},
	}
	issue := automationIssue("In Progress")
	issue.Comments = []domain.Comment{questionComment("q1", "Which database should I use?")}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &cmdRecordingRunner{}
	o := New(cfg, mt, runner, nil)
	o.SetIssueProfile(issue.Identifier, "researcher")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	state := NewState(cfg)
	detailed, err := mt.FetchIssueDetail(ctx, issue.ID)
	require.NoError(t, err)
	o.admitClaimRead(ctx, &state, issue, detailed, nil, time.Now())
	require.Contains(t, state.InputRequiredIssues, issue.Identifier, "precondition: the question is rehydrated")
	assert.Equal(t, "researcher", state.InputRequiredIssues[issue.Identifier].ProfileName,
		"a rehydrated entry records the issue's profile")

	state = o.handleEvent(ctx, state, OrchestratorEvent{Type: EventProvideInput, Identifier: issue.Identifier, Message: "Postgres"})
	require.Contains(t, state.Running, issue.ID, "the reply resumes the issue")
	assert.Equal(t, "researcher", state.Running[issue.ID].ProfileName, "the resume runs the issue's profile")
	require.Eventually(t, func() bool { return len(runner.commands()) > 0 }, 5*time.Second, 10*time.Millisecond)
	assert.Contains(t, runner.commands()[0], "researcher-model", "the resume runs the profile's command, not agent.command")
	cancel()
	o.workersWg.Wait(5 * time.Second)
}
