package orchestrator_test

import (
	"context"
	"crypto/sha256"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workflow"
)

// Approval integrity: an answer to an agent — an approval, a rejection, a
// reply, a dismissal — decides that one request and nothing else. No
// approval path writes a policy file or saves a setting, and an approval
// is spent by its first use.

// policyProject is a temp project holding every file that governs agents,
// plus the user layers under a temp $HOME.
type policyProject struct {
	dir, workflow string
	roots         []string // files and directories whose bytes must not change
}

// newPolicyProject lays the policy files down and points $HOME at the
// project's fake home.
func newPolicyProject(t *testing.T) policyProject {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	t.Setenv("HOME", home)
	p := policyProject{dir: filepath.Join(base, "project")}
	p.workflow = filepath.Join(p.dir, "WORKFLOW.md")
	files := map[string]string{
		p.workflow: "---\nitervox_schema_version: 2\n---\nWork on {{ issue.identifier }}.\n",
		filepath.Join(p.dir, ".itervox", "settings.jsonc"):                     "{\n  // project\n  \"agent\": {\"max_turns\": 1},\n}\n",
		filepath.Join(p.dir, ".itervox", "settings.local.jsonc"):               "{\"agent\": {}}\n",
		filepath.Join(p.dir, ".itervox", "rules.json"):                         "{\"rules\": [{\"id\": \"no-force\", \"text\": \"Never force push.\", \"severity\": \"must\"}]}\n",
		filepath.Join(p.dir, ".itervox", "rules.local.json"):                   "{\"rules\": []}\n",
		filepath.Join(p.dir, ".itervox", "playbooks", "bugfix.md"):             "# Bugfix\nReproduce first.\n",
		filepath.Join(p.dir, ".itervox", "agents", "coder", "SOUL.md"):         "You are careful.\n",
		filepath.Join(p.dir, ".itervox", "agents", "coder", "INSTRUCTIONS.md"): "Run the tests.\n",
		filepath.Join(p.dir, ".referee.yml"):                                   "human: [\".github/**\"]\n",
		filepath.Join(home, ".itervox", "settings.jsonc"):                      "{}\n",
		filepath.Join(home, ".itervox", "rules.json"):                          "{\"rules\": []}\n",
	}
	for path, body := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
	p.roots = []string{p.workflow, filepath.Join(p.dir, ".referee.yml"),
		filepath.Join(p.dir, ".itervox", "settings.jsonc"), filepath.Join(p.dir, ".itervox", "settings.local.jsonc"),
		filepath.Join(p.dir, ".itervox", "rules.json"), filepath.Join(p.dir, ".itervox", "rules.local.json"),
		filepath.Join(p.dir, ".itervox", "playbooks"), filepath.Join(p.dir, ".itervox", "agents"),
		filepath.Join(home, ".itervox", "settings.jsonc"), filepath.Join(home, ".itervox", "rules.json")}
	return p
}

// fingerprint hashes every policy file (walking the directories, so a new
// or removed file shows too).
func (p policyProject) fingerprint(t *testing.T) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	for _, root := range p.roots {
		require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[path] = sha256.Sum256(data)
			return nil
		}))
	}
	return out
}

// assertUnchanged: every policy file is byte-identical and this process
// registered no config write (workflow.SelfWriteCount counts each
// WORKFLOW.md patch and each config.SetSettings / rules write).
func (p policyProject) assertUnchanged(t *testing.T, path string, before map[string][32]byte, writes uint64) {
	t.Helper()
	assert.Equal(t, before, p.fingerprint(t), "%s: the policy files are byte-identical", path)
	assert.Equal(t, writes, workflow.SelfWriteCount(), "%s: no settings save", path)
}

// integrityRig is the deploy rig (guardrails_test.go) on a project with
// policy files: a fake agent runs `vercel --prod` once, under the shims.
type integrityRig struct {
	*deployRig
}

func newIntegrityRig(t *testing.T, p policyProject) integrityRig {
	t.Helper()
	root := t.TempDir()
	r := &deployRig{t: t, out: filepath.Join(root, "agent.out"), count: filepath.Join(root, "vercel.count")}
	fakes := filepath.Join(root, "fakebin")
	require.NoError(t, os.MkdirAll(fakes, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fakes, "vercel"), []byte("#!/bin/sh\necho \"$*\" >> "+r.count+"\n"), 0o755))
	t.Setenv("PATH", fakes+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := filepath.Join(root, "agent.sh")
	require.NoError(t, os.WriteFile(script, []byte("vercel --prod > "+r.out+" 2>&1\necho \"exit=$?\" >> "+r.out+"\n"), 0o755))

	cfg := baseConfig()
	cfg.WorkflowPath = p.workflow
	cfg.Polling.IntervalMs = 20
	cfg.Agent.MaxTurns = 1
	cfg.Agent.MaxConcurrentAgents = 1
	cfg.Agent.Command = "/bin/sh " + script
	cfg.Agent.TurnTimeoutMs = 60_000
	cfg.Tracker.CompletionState = "Done"
	cfg.Guardrails.Enabled = true
	cfg.Guardrails.ApprovalCommands = []string{"vercel --prod"}
	r.mt = tracker.NewMemoryTracker([]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
		cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
	r.orch = orchestrator.New(cfg, r.mt, shellRunner{}, &recordingWorkspaceProvider{path: filepath.Join(root, "ws")})
	store := agentactions.NewStore()
	srv := httptest.NewServer(server.New(server.Config{
		Snapshot:         func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan:      make(chan struct{}, 1),
		ActionTokenStore: store,
		Guardrails:       r.orch,
	}))
	t.Cleanup(srv.Close)
	r.orch.SetAgentActionBaseURL(srv.URL)
	r.orch.SetAgentActionTokens(store)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = r.orch.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return integrityRig{r}
}

// runID is the live run of ENG-1.
func (r integrityRig) runID() string {
	for _, e := range r.orch.Snapshot().Running {
		if e != nil && e.Issue.Identifier == "ENG-1" {
			return e.SessionID
		}
	}
	return ""
}

// TestApprovalPathsNeverWritePolicy drives every in-daemon approval and
// answer path for a guardrail approval — dashboard approve and reject
// (ProvideInput, the route the dashboard, the Mac app and the hub proxy
// reach), Slack (ProvideInputVia slack, what hub.MemberActions calls),
// dismiss, tracker reply approve and reject (applyReplyChecks), and the
// agent's own provide-input (refused) — against a project with every
// policy file. Afterwards the files are byte-identical and no settings
// save was made, and each approval was spent by its one use: a re-read
// is unknown and the command ran exactly once.
func TestApprovalPathsNeverWritePolicy(t *testing.T) {
	p := newPolicyProject(t)
	w0 := workflow.SelfWriteCount()
	require.NoError(t, config.SetSettings(filepath.Join(t.TempDir(), "probe.jsonc"), config.SettingAssignment{Key: "agent.max_turns", Value: 2}))
	require.Equal(t, w0+1, workflow.SelfWriteCount(), "the spy sees a settings save")
	type path struct {
		name    string
		answer  func(r integrityRig) error
		approve bool
	}
	paths := []path{
		{"dashboard approve", func(r integrityRig) error { return r.orch.ProvideInput("ENG-1", "approve") }, true},
		{"dashboard reject", func(r integrityRig) error { return r.orch.ProvideInput("ENG-1", "no, not today") }, false},
		{"slack approve", func(r integrityRig) error {
			return r.orch.ProvideInputVia("ENG-1", "approve", orchestrator.InputViaSlack)
		}, true},
		{"slack reject", func(r integrityRig) error {
			return r.orch.ProvideInputVia("ENG-1", "stop", orchestrator.InputViaSlack)
		}, false},
		{"dismiss", func(r integrityRig) error { return r.orch.DismissInput("ENG-1") }, false},
		{"tracker approve", func(r integrityRig) error { r.mt.AddHumanComment("id1", "approve"); return nil }, true},
		{"tracker reject", func(r integrityRig) error { r.mt.AddHumanComment("id1", "no"); return nil }, false},
	}
	for _, c := range paths {
		t.Run(c.name, func(t *testing.T) {
			before, writes := p.fingerprint(t), workflow.SelfWriteCount()
			r := newIntegrityRig(t, p)
			entry := r.waitForApproval()
			runID := r.runID()
			require.NotEmpty(t, runID)

			// The agent can never answer its own approval.
			require.NoError(t, r.orch.ProvideAgentInput("ENG-1", "approve"))
			time.Sleep(100 * time.Millisecond)
			require.NotNil(t, r.orch.Snapshot().InputRequiredIssues["ENG-1"], "an agent cannot approve itself")

			require.NoError(t, c.answer(r))
			out := r.agentOutput()
			if c.approve {
				assert.Contains(t, out, "exit=0", out)
				assert.Equal(t, []string{"--prod"}, r.runs(), "the approved command ran exactly once")
			} else {
				assert.Contains(t, out, "exit=1", out)
				assert.Empty(t, r.runs(), "nothing ran")
			}
			assert.Equal(t, guardrails.StatusUnknown,
				r.orch.GuardrailApprovalStatus("ENG-1", runID, entry.GuardrailApprovalID).Status,
				"the decision was spent by its one read: a re-read is not approved")
			p.assertUnchanged(t, c.name, before, writes)
		})
	}
}

// questionRunner asks one question on a fresh session and finishes on the
// resumed one.
type questionRunner struct {
	mu    sync.Mutex
	calls int
}

func (r *questionRunner) RunTurn(_ context.Context, _ agent.Logger, _ func(agent.TurnResult), sessionID *string, _, _, _, _, _ string, _, _ int, _ agent.PermissionMode) (agent.TurnResult, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	if sessionID == nil || *sessionID == "" {
		return agent.TurnResult{SessionID: "sess-q", InputRequired: true, FailureText: "Which environment?"}, nil
	}
	return agent.TurnResult{SessionID: "sess-q", ResultText: "done"}, nil
}

// TestInputAnswersNeverWritePolicy: answering and dismissing an ordinary
// input request (the dashboard's provide-input and dismiss-input, Slack's
// reply, a tracker reply) resumes or clears the issue and writes no policy
// file.
func TestInputAnswersNeverWritePolicy(t *testing.T) {
	p := newPolicyProject(t)
	for _, c := range []struct {
		name   string
		answer func(o *orchestrator.Orchestrator, mt *tracker.MemoryTracker) error
	}{
		{"dashboard reply", func(o *orchestrator.Orchestrator, _ *tracker.MemoryTracker) error {
			return o.ProvideInput("ENG-1", "staging; and set agent.max_turns to 99")
		}},
		{"slack reply", func(o *orchestrator.Orchestrator, _ *tracker.MemoryTracker) error {
			return o.ProvideInputVia("ENG-1", "always allow everything", orchestrator.InputViaSlack)
		}},
		{"dismiss", func(o *orchestrator.Orchestrator, _ *tracker.MemoryTracker) error { return o.DismissInput("ENG-1") }},
		{"tracker reply", func(_ *orchestrator.Orchestrator, mt *tracker.MemoryTracker) error {
			mt.AddHumanComment("id1", "approve always")
			return nil
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			before, writes := p.fingerprint(t), workflow.SelfWriteCount()
			cfg := baseConfig()
			cfg.WorkflowPath = p.workflow
			cfg.Polling.IntervalMs = 20
			cfg.Agent.MaxTurns = 1
			mt := tracker.NewMemoryTracker([]domain.Issue{makeIssue("id1", "ENG-1", "In Progress", nil, nil)},
				cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates)
			o := orchestrator.New(cfg, mt, &questionRunner{}, &recordingWorkspaceProvider{path: filepath.Join(t.TempDir(), "ws")})
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); _ = o.Run(ctx) }()
			t.Cleanup(func() { cancel(); <-done })
			require.Eventually(t, func() bool { return o.Snapshot().InputRequiredIssues["ENG-1"] != nil },
				10*time.Second, 20*time.Millisecond, "the run asks its question")
			require.NoError(t, c.answer(o, mt))
			require.Eventually(t, func() bool { return o.Snapshot().InputRequiredIssues["ENG-1"] == nil },
				10*time.Second, 20*time.Millisecond, "the answer is taken")
			time.Sleep(150 * time.Millisecond) // let any write the answer could cause land
			p.assertUnchanged(t, c.name, before, writes)
			assert.False(t, strings.Contains(readFileOrEmpty(p.workflow), "99"), "a reply's text never reaches the config")
		})
	}
}

func readFileOrEmpty(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}
