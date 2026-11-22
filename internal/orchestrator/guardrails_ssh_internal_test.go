package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/guardrails/sshtest"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/tracker"
)

// runEnvRecordingRunner records the run environment of each turn.
type runEnvRecordingRunner struct {
	mu   sync.Mutex
	envs []agent.RunEnv
}

func (r *runEnvRecordingRunner) RunTurn(ctx context.Context, _ agent.Logger, _ func(agent.TurnResult), _ *string, _, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mu.Lock()
	r.envs = append(r.envs, agent.RunEnvFrom(ctx))
	r.mu.Unlock()
	return agent.TurnResult{ResultText: "done", InputTokens: 1, OutputTokens: 1, TotalTokens: 2}, nil
}

func (r *runEnvRecordingRunner) turns() []agent.RunEnv {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agent.RunEnv(nil), r.envs...)
}

// runSSHGuardedWorker runs the real runWorker once on the SSH worker
// "worker-1" (a fake ssh: sshtest) with guardrails on.
func runSSHGuardedWorker(t *testing.T, allowUnguarded bool) (*runEnvRecordingRunner, OrchestratorEvent, *Orchestrator) {
	t.Helper()
	cfg := automationBaseCfg()
	cfg.Agent.MaxTurns = 1
	cfg.Agent.SSHHosts = []string{"worker-1"}
	cfg.Guardrails.Enabled = true
	cfg.Guardrails.AllowUnguardedSSH = allowUnguarded
	issue := domain.Issue{ID: "id1", Identifier: "ENG-1", Title: "T", State: "In Progress"}
	mt := tracker.NewMemoryTracker([]domain.Issue{issue}, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	runner := &runEnvRecordingRunner{}
	o := New(cfg, mt, runner, &stubWorkspaceProvider{path: t.TempDir()})
	o.logBuf = logbuffer.New()
	o.events = make(chan OrchestratorEvent, 64)
	o.workersWg.Add(issue.Identifier)
	o.runWorker(t.Context(), issue, 0, "worker-1", "claude", "claude", "", true, nil, nil, nil)
	var events []OrchestratorEvent
	for len(o.events) > 0 {
		events = append(events, <-o.events)
	}
	for _, ev := range events {
		if ev.Type == EventWorkerExited {
			return runner, ev, o
		}
	}
	t.Fatal("the worker sent no exit")
	return nil, OrchestratorEvent{}, nil
}

// fakeWorkerPlatform makes the fake worker's uname report another platform.
func fakeWorkerPlatform(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "uname"),
		[]byte("#!/bin/sh\ncase \"$1\" in -s) echo Plan9;; -m) echo mips;; esac\n"), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// 0.3.4: with guardrails on, a run on an SSH worker whose guardrails cannot
// be installed there (here: the worker is another platform than this
// binary) does not start: the agent never runs, the exit is failed with the
// reason, and the issue's log says so.
func TestSSHWorkerRunFailsClosedWithoutGuardrails(t *testing.T) {
	sshtest.Install(t)
	fakeWorkerPlatform(t)
	runner, exit, o := runSSHGuardedWorker(t, false)
	assert.Empty(t, runner.turns(), "the agent must not start unguarded")
	assert.Equal(t, TerminalFailed, exit.RunEntry.TerminalReason)
	require.ErrorIs(t, exit.Error, guardrails.ErrRemotePlatform)
	assert.Contains(t, exit.Error.Error(), "run not started (set guardrails.allow_unguarded_ssh to run it unguarded)")
	logged := false
	for _, l := range o.logBuf.Get("ENG-1") {
		logged = logged || strings.Contains(l, "worker: run not started") && strings.Contains(l, "allow_unguarded_ssh")
	}
	assert.True(t, logged, "the issue's log records the reason: %v", o.logBuf.Get("ENG-1"))
}

// guardrails.allow_unguarded_ssh is the documented opt-out: the same run
// starts unguarded (no remote run environment), with a warning.
func TestSSHWorkerRunUnguardedWithOptOut(t *testing.T) {
	sshtest.Install(t)
	fakeWorkerPlatform(t)
	runner, exit, _ := runSSHGuardedWorker(t, true)
	require.Len(t, runner.turns(), 1, "exit: %v", exit.Error)
	assert.Nil(t, runner.turns()[0].Remote, "unguarded")
}

// A worker of this binary's platform gets its guardrails: the turn's run
// environment carries the remote shim directory and GIT_CONFIG_PARAMETERS.
func TestSSHWorkerRunGetsRemoteGuardrails(t *testing.T) {
	home, _ := sshtest.Install(t)
	runner, exit, _ := runSSHGuardedWorker(t, false)
	require.Len(t, runner.turns(), 1, "exit: %v", exit.Error)
	remote := runner.turns()[0].Remote
	require.NotNil(t, remote)
	assert.Contains(t, filepath.Base(filepath.Dir(remote.PathPrepend)), "itervox-guardrails-")
	assert.Contains(t, remote.Exports[0], "GIT_CONFIG_PARAMETERS='core.hooksPath'=")
	assert.NoDirExists(t, filepath.Dir(remote.PathPrepend), "removed at run end")
	assert.DirExists(t, filepath.Join(home, ".cache", "itervox", "guardrails"))
}
