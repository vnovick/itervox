package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/metrics"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
)

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gitexec.Command(context.Background(), dir, args...)
	cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// #204: a tmux session started with "New session" runs its agent behind
// the same guardrails as a dispatched run: the shims come first on its
// PATH (gh pr merge refused), git uses the guardrail hooks (a push to main
// refused, the session's own branch accepted), deploy commands are refused
// (no approval channel for a session), refusals reach the daemon, and
// killing the session removes its guardrails.
func TestGuardrailsAppliedToItervoxTmuxSessions(t *testing.T) {
	r := newTmuxRig(t, "", true)
	root := filepath.Dir(r.bin)
	out := filepath.Join(root, "out")
	remote := filepath.Join(root, "remote.git")
	gitOut(t, root, "init", "-q", "--bare", "-b", "main", remote)
	gitOut(t, r.project, "init", "-q", "-b", "main")
	gitOut(t, r.project, "remote", "add", "origin", remote)
	require.NoError(t, os.WriteFile(filepath.Join(r.project, "f.txt"), []byte("x\n"), 0o644))
	gitOut(t, r.project, "add", "f.txt")
	gitOut(t, r.project, "commit", "-q", "-m", "base")
	gitOut(t, r.project, "push", "-q", "origin", "main")
	gitOut(t, r.project, "checkout", "-q", "-b", "feature-x")
	gitOut(t, r.project, "commit", "-q", "--allow-empty", "-m", "work")
	mainBefore := gitOut(t, remote, "rev-parse", "main")

	ran := filepath.Join(out, "real.ran")
	for _, name := range []string{"gh", "vercel"} {
		require.NoError(t, os.WriteFile(filepath.Join(r.bin, name), []byte("#!/bin/sh\necho "+name+" >> "+ran+"\n"), 0o755))
	}
	step := func(name, cmd string) string {
		f := shellQuote(filepath.Join(out, name+".txt"))
		return cmd + " > " + f + " 2>&1; echo \"exit=$?\" >> " + f + "\n"
	}
	agentScript := "#!/bin/sh\n" + step("merge", "gh pr merge 3") + step("pushmain", "git push origin HEAD:main") +
		step("pushown", "git push origin HEAD") + step("deploy", "vercel --prod") +
		"echo \"$GIT_CONFIG_PARAMETERS\" > " + shellQuote(filepath.Join(out, "params.txt")) + "\n" +
		"echo ready\nwhile IFS= read -r line; do echo \"got: $line\"; done\n"
	require.NoError(t, os.WriteFile(filepath.Join(r.bin, "claude"), []byte(agentScript), 0o755))

	cfg, err := config.Load(filepath.Join(r.project, "WORKFLOW.md"))
	require.NoError(t, err)
	require.True(t, cfg.Guardrails.Enabled, "guardrails default on")
	store := agentactions.NewStore()
	daemon := httptest.NewServer(server.New(server.Config{
		Snapshot:         func() server.StateSnapshot { return server.StateSnapshot{} },
		RefreshChan:      make(chan struct{}, 1),
		ActionTokenStore: store,
		Guardrails:       guardrailsAdapter{orch: r.orch},
	}))
	t.Cleanup(daemon.Close)
	exe, err := os.Executable()
	require.NoError(t, err)
	r.ts.guard = newTmuxGuardrails(cfg, exe, store, daemon.URL)
	mergesBefore := metrics.GuardrailBlockCount(guardrails.KindSelfMerge)

	w := r.do(http.MethodPost, "/api/v1/sessions", `{"backend":"claude"}`)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	var created struct {
		Session server.SessionRow `json:"session"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	id := created.Session.ID
	r.screenShows(id, "ready")

	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(out, name+".txt"))
		require.NoError(t, err)
		return string(data)
	}
	merge := read("merge")
	assert.Contains(t, merge, "gh pr merge is refused")
	assert.Contains(t, merge, "exit=1")
	pushMain := read("pushmain")
	assert.Contains(t, pushMain, `agents may push only to the issue branch "feature-x"`)
	assert.Equal(t, mainBefore, gitOut(t, remote, "rev-parse", "main"), "main did not move")
	assert.Contains(t, read("pushown"), "exit=0")
	assert.Equal(t, gitOut(t, r.project, "rev-parse", "HEAD"), gitOut(t, remote, "rev-parse", "feature-x"))
	deploy := read("deploy")
	assert.Contains(t, deploy, "needs a human approval")
	assert.Contains(t, deploy, "exit=1")
	_, err = os.Stat(ran)
	assert.True(t, os.IsNotExist(err), "neither the real gh nor the real vercel ran")

	r.ts.guard.mu.Lock()
	rt := r.ts.guard.runtimes[id].rt
	r.ts.guard.mu.Unlock()
	require.NotNil(t, rt)
	assert.Contains(t, read("params"), rt.HooksDir())
	assert.Equal(t, mergesBefore+1, metrics.GuardrailBlockCount(guardrails.KindSelfMerge), "the refusal reached the daemon")
	require.Eventually(t, func() bool {
		for _, f := range r.orch.Snapshot().RecentFailures {
			if f.Kind == orchestrator.FailureKindGuardrail && f.Identifier == id && f.Source == guardrails.KindBranch {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)

	w = r.do(http.MethodDelete, "/api/v1/sessions/"+id, "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	_, err = os.Stat(rt.Dir())
	assert.True(t, os.IsNotExist(err), "killing the session removes its guardrails")
}

// #204: `itervox init --update` adds the guardrails: block with its
// defaults to a workflow that has none — as a text append when nothing else
// needs migrating (every other byte kept), as a node in the rewrite
// otherwise — and a new workflow has it too.
func TestInitUpdateAddsGuardrailsBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	original := "---\nitervox_schema_version: 2\n# tracker notes\ntracker:\n  kind: linear     # aligned comment\n  api_key: k\n  project_slug: p\nagent:\n  deps_analyzer_profile: deps-analyzer\n  profiles:\n    deps-analyzer:\n      command: claude\n      soul_file: S.md\n      instructions_file: I.md\n---\n\nPrompt.\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0o644))
	for _, f := range []string{"S.md", "I.md"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte("# x\n"), 0o644))
	}
	result, err := migrateWorkflowToSchema2(path, false, time.Now())
	require.NoError(t, err)
	assert.False(t, result.Changed, "no schema rewrite")
	assert.True(t, result.AddedGuardrails)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	front, _, _ := splitWorkflowFrontMatter(original)
	assert.True(t, strings.HasPrefix(string(data), "---\n"+front), "the existing front matter is kept byte for byte:\n%s", data)
	assert.True(t, strings.HasSuffix(string(data), "\n---\n\nPrompt.\n"))
	assert.Contains(t, string(data), "# Guardrails (#204)")
	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, config.GuardrailsConfig{Enabled: true, ApprovalCommands: config.DefaultGuardrailApprovalCommands}, cfg.Guardrails)

	again, err := migrateWorkflowToSchema2(path, false, time.Now())
	require.NoError(t, err)
	assert.False(t, again.AddedGuardrails, "idempotent")
	unchanged, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(data), string(unchanged))

	// A schema 1 workflow is rewritten anyway; the block is part of it.
	old := filepath.Join(t.TempDir(), "WORKFLOW.md")
	require.NoError(t, os.WriteFile(old, []byte("---\ntracker:\n  kind: linear\n  api_key: k\n  project_slug: p\n---\n\nPrompt.\n"), 0o644))
	rewritten, err := migrateWorkflowToSchema2(old, false, time.Now())
	require.NoError(t, err)
	assert.True(t, rewritten.Changed)
	assert.True(t, rewritten.AddedGuardrails)
	data, err = os.ReadFile(old)
	require.NoError(t, err)
	oldFront, _, ok := splitWorkflowFrontMatter(string(data))
	require.True(t, ok)
	var doc map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(oldFront), &doc))
	assert.Equal(t, true, doc["guardrails"].(map[string]any)["enabled"])
	assert.Contains(t, oldFront, "# Guardrails (#204)")

	var b strings.Builder
	writeGuardrailsBlock(&b)
	var fresh map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(b.String()), &fresh))
	assert.Len(t, fresh["guardrails"].(map[string]any)["approval_commands"], len(config.DefaultGuardrailApprovalCommands))
}
