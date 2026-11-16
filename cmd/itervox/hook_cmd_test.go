package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const hookWorkflow = `---
itervox_schema_version: 2
tracker:
  kind: memory
guardrails:
  approval_commands:
    - "vercel --prod"
    - match: "terraform apply"
      when_env: {TF_WORKSPACE: "prod*"}
---
Prompt.
`

func writeHookWorkflow(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "WORKFLOW.md")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// hookCall runs `itervox hook <agent>-pretooluse args…` with input and
// returns its decision and reason ("" for no output).
func hookCall(t *testing.T, agent string, input map[string]any, env []string, args ...string) (decision, reason string) {
	t.Helper()
	data, err := json.Marshal(input)
	require.NoError(t, err)
	var stdout, stderr bytes.Buffer
	code := runHook(append([]string{agent + "-pretooluse"}, args...), bytes.NewReader(data), &stdout, &stderr, env)
	require.Zero(t, code, stderr.String())
	if stdout.Len() == 0 {
		return "", ""
	}
	var out struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &out), stdout.String())
	assert.Equal(t, "PreToolUse", out.HookSpecificOutput.HookEventName)
	return out.HookSpecificOutput.PermissionDecision, out.HookSpecificOutput.PermissionDecisionReason
}

func bashInput(cwd, command string) map[string]any {
	return map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": cwd,
		"tool_input": map[string]any{"command": command, "description": "x"}}
}

// The CLI end to end: a guardrail refusal denies, an approval command
// asks, an env-scoped one asks only in the matching environment, and an
// ordinary command produces no output (allow through the normal flow).
func TestHookDeniesGuardrailCommand(t *testing.T) {
	wf := writeHookWorkflow(t, hookWorkflow)
	dir := filepath.Dir(wf)
	d, reason := hookCall(t, "claude", bashInput(dir, "gh pr merge 12 --squash"), nil, "--workflow", wf)
	assert.Equal(t, "deny", d)
	assert.Contains(t, reason, "agents do not merge their own pull requests")
	d, _ = hookCall(t, "claude", bashInput(dir, "vercel deploy --prod"), nil, "--workflow", wf)
	assert.Equal(t, "ask", d)
	d, _ = hookCall(t, "claude", bashInput(dir, "terraform apply"), []string{"TF_WORKSPACE=prod"}, "--workflow", wf)
	assert.Equal(t, "ask", d)
	d, _ = hookCall(t, "claude", bashInput(dir, "terraform apply"), []string{"TF_WORKSPACE=dev"}, "--workflow", wf)
	assert.Equal(t, "", d)
	d, _ = hookCall(t, "claude", bashInput(dir, "go test ./..."), nil, "--workflow", wf)
	assert.Equal(t, "", d)
	// Codex: an ask is a deny (Codex fails open on ask).
	d, _ = hookCall(t, "codex", bashInput(dir, "vercel --prod"), nil, "--workflow", wf)
	assert.Equal(t, "deny", d)
	// Without --workflow the nearest WORKFLOW.md above the session's cwd is used.
	sub := filepath.Join(dir, "a", "b")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	d, _ = hookCall(t, "claude", bashInput(sub, "glab mr merge 3"), nil)
	assert.Equal(t, "deny", d)
}

// The default human lane applies to edits: WORKFLOW.md itself and CI
// workflows ask with the pipeline-file reason.
func TestHookAsksForHumanLaneEdit(t *testing.T) {
	wf := writeHookWorkflow(t, hookWorkflow)
	dir := filepath.Dir(wf)
	for _, p := range []string{wf, filepath.Join(dir, ".github", "workflows", "ci.yml")} {
		d, reason := hookCall(t, "claude", map[string]any{"tool_name": "Edit", "cwd": dir,
			"tool_input": map[string]any{"file_path": p, "old_string": "a", "new_string": "b"}}, nil, "--workflow", wf)
		assert.Equal(t, "ask", d, p)
		assert.Contains(t, reason, "pipeline file: an agent may change what CI checks, not how", p)
	}
	d, _ := hookCall(t, "claude", map[string]any{"tool_name": "Write", "cwd": dir,
		"tool_input": map[string]any{"file_path": filepath.Join(dir, "main.go"), "content": "package main"}}, nil, "--workflow", wf)
	assert.Equal(t, "", d)
}

// Fail-safe: a workflow that cannot be loaded (missing, unparseable, an
// invalid guardrails entry) and unreadable hook input ask, never allow
// silently; with --require-workflow so does finding none.
func TestHookAsksWhenWorkflowUnreadable(t *testing.T) {
	dir := t.TempDir()
	for _, wf := range []string{
		filepath.Join(dir, "missing", "WORKFLOW.md"),
		writeHookWorkflow(t, "---\ntracker: [unclosed\n---\n"),
		writeHookWorkflow(t, "---\nitervox_schema_version: 2\ntracker:\n  kind: memory\nguardrails:\n  approval_commands:\n    - when_env: {X: a}\n---\n"),
	} {
		d, reason := hookCall(t, "claude", bashInput(dir, "ls"), nil, "--workflow", wf)
		assert.Equal(t, "ask", d, wf)
		assert.Contains(t, reason, "could not be applied", wf)
		d, _ = hookCall(t, "codex", bashInput(dir, "ls"), nil, "--workflow", wf)
		assert.Equal(t, "deny", d, "codex cannot ask: fail closed")
	}
	var stdout bytes.Buffer
	require.Zero(t, runHook([]string{"claude-pretooluse", "--workflow", filepath.Join(dir, "x")}, strings.NewReader("not json"), &stdout, &bytes.Buffer{}, nil))
	assert.Contains(t, stdout.String(), `"permissionDecision":"ask"`)

	empty := t.TempDir()
	d, _ := hookCall(t, "claude", bashInput(empty, "gh pr merge 1"), nil)
	assert.Equal(t, "", d, "no WORKFLOW.md found: not an Itervox project, no policy")
	d, _ = hookCall(t, "claude", bashInput(empty, "ls"), nil, "--require-workflow")
	assert.Equal(t, "ask", d)
}

// install merges the hook entry into .claude/settings.json: other keys
// and their order are kept, a second install changes nothing, an old
// itervox entry is replaced rather than duplicated, and --print writes
// nothing.
func TestHookInstallMergesSettings(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, ".claude", "settings.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(settings), 0o755))
	require.NoError(t, os.WriteFile(settings, []byte(`{"permissions":{"allow":["Bash(ls)"]},"hooks":{"PostToolUse":[{"matcher":"Edit","hooks":[{"type":"command","command":"fmt.sh"}]}],"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"other.sh"}]},{"matcher":"Bash","hooks":[{"type":"command","command":"itervox hook claude-pretooluse --old"}]}]},"model":"m"}`), 0o644))

	var out bytes.Buffer
	require.Zero(t, runHook([]string{"install", "claude", "--project", "--dir", dir, "--print"}, nil, &out, &bytes.Buffer{}, nil))
	assert.Contains(t, out.String(), `itervox hook claude-pretooluse --workflow \"$CLAUDE_PROJECT_DIR\"/WORKFLOW.md`)
	before, _ := os.ReadFile(settings)
	assert.NotContains(t, string(before), "CLAUDE_PROJECT_DIR", "--print writes nothing")

	out.Reset()
	require.Zero(t, runHook([]string{"install", "claude", "--project", "--dir", dir}, nil, &out, &bytes.Buffer{}, nil))
	assert.Contains(t, out.String(), "Wrote the itervox PreToolUse hook")
	data, err := os.ReadFile(settings)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(data, &got))
	assert.Equal(t, "m", got["model"])
	assert.Equal(t, map[string]any{"allow": []any{"Bash(ls)"}}, got["permissions"])
	hooks := got["hooks"].(map[string]any)
	assert.Len(t, hooks["PostToolUse"], 1)
	pre := hooks["PreToolUse"].([]any)
	require.Len(t, pre, 2, "the old itervox entry is replaced, the other hook kept")
	assert.Equal(t, "other.sh", pre[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"])
	mine := pre[1].(map[string]any)
	assert.Equal(t, "Bash|Edit|Write|MultiEdit|NotebookEdit", mine["matcher"])
	assert.Equal(t, `itervox hook claude-pretooluse --workflow "$CLAUDE_PROJECT_DIR"/WORKFLOW.md`, mine["hooks"].([]any)[0].(map[string]any)["command"])
	s := string(data)
	assert.Less(t, strings.Index(s, `"permissions"`), strings.Index(s, `"hooks"`), "key order kept")
	assert.Less(t, strings.Index(s, `"hooks"`), strings.Index(s, `"model"`), "key order kept")

	out.Reset()
	require.Zero(t, runHook([]string{"install", "claude", "--dir", dir}, nil, &out, &bytes.Buffer{}, nil))
	assert.Contains(t, out.String(), "already has the itervox hook")
	again, _ := os.ReadFile(settings)
	assert.Equal(t, string(data), string(again), "idempotent")

	// A fresh project, and Codex's hooks.json.
	fresh := t.TempDir()
	require.Zero(t, runHook([]string{"install", "codex", "--dir", fresh}, nil, &bytes.Buffer{}, &bytes.Buffer{}, nil))
	data, err = os.ReadFile(filepath.Join(fresh, ".codex", "hooks.json"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"hooks":{"PreToolUse":[{"matcher":"^(Bash|apply_patch)$","hooks":[{"type":"command","command":"itervox hook codex-pretooluse --require-workflow"}]}]}}`, string(data))
}

// TestHookInstallWarnsAboutPATHAndWritesAbsolutePath: the installed hook runs
// whatever `itervox` is on PATH. Install warns when none is there or it is
// another binary (an older one has no `itervox hook`, the hook fails and the
// agent treats that as non-blocking); --absolute writes this binary's path,
// and switching between the two forms replaces the entry rather than adding
// one.
func TestHookInstallWarnsAboutPATHAndWritesAbsolutePath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	settings := filepath.Join(dir, ".claude", "settings.json")
	self, err := os.Executable()
	require.NoError(t, err)

	// No itervox on PATH.
	t.Setenv("PATH", t.TempDir())
	var out, errOut bytes.Buffer
	require.Zero(t, runHook([]string{"install", "claude", "--dir", dir}, nil, &out, &errOut, nil))
	assert.Contains(t, errOut.String(), "no `itervox` is on PATH")

	// Another itervox on PATH.
	other := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(other, "itervox"), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("PATH", other)
	errOut.Reset()
	require.Zero(t, runHook([]string{"install", "claude", "--dir", dir}, nil, &bytes.Buffer{}, &errOut, nil))
	assert.Contains(t, errOut.String(), "not this binary")

	// This binary on PATH (as a symlink named itervox): no warning, and
	// --absolute writes the PATH path, which survives package upgrades.
	mine := t.TempDir()
	link := filepath.Join(mine, "itervox")
	require.NoError(t, os.Symlink(self, link))
	t.Setenv("PATH", mine)
	errOut.Reset()
	require.Zero(t, runHook([]string{"install", "claude", "--dir", dir, "--absolute"}, nil, &bytes.Buffer{}, &errOut, nil))
	assert.Empty(t, errOut.String())
	commands := func() []string {
		data, err := os.ReadFile(settings)
		require.NoError(t, err)
		var got struct {
			Hooks struct {
				PreToolUse []hookGroup `json:"PreToolUse"`
			} `json:"hooks"`
		}
		require.NoError(t, json.Unmarshal(data, &got))
		var cmds []string
		for _, g := range got.Hooks.PreToolUse {
			for _, h := range g.Hooks {
				cmds = append(cmds, h.Command)
			}
		}
		return cmds
	}
	assert.Equal(t, []string{link + ` hook claude-pretooluse --workflow "$CLAUDE_PROJECT_DIR"/WORKFLOW.md`}, commands(), "the absolute form replaced the bare entry")
	assert.Empty(t, installedBareHookFiles(dir), "an absolute hook does not depend on PATH")

	require.Zero(t, runHook([]string{"install", "claude", "--dir", dir}, nil, &bytes.Buffer{}, &bytes.Buffer{}, nil))
	assert.Equal(t, []string{`itervox hook claude-pretooluse --workflow "$CLAUDE_PROJECT_DIR"/WORKFLOW.md`}, commands(), "and back: still one entry")
	assert.Equal(t, []string{settings}, installedBareHookFiles(dir))
}

func TestIsItervoxHookCommand(t *testing.T) {
	marker := hookMarker("claude")
	for cmd, want := range map[string]bool{
		"itervox hook claude-pretooluse":                                true,
		"  itervox hook claude-pretooluse --workflow x":                 true,
		"/opt/homebrew/bin/itervox hook claude-pretooluse --workflow x": true,
		"'/Users/me/My Apps/itervox' hook claude-pretooluse":            true,
		"/usr/local/bin/other hook claude-pretooluse":                   false,
		"itervox hook codex-pretooluse":                                 false,
		"echo itervox hook claude-pretooluse":                           false,
		"other.sh":                                                      false,
	} {
		assert.Equal(t, want, isItervoxHookCommand(cmd, marker), cmd)
	}
}

// TestDoctorWarnsWhenTheHookRunsAnotherItervox: doctor names each settings
// file whose bare `itervox` hook would not run this binary.
func TestDoctorWarnsWhenTheHookRunsAnotherItervox(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir()) // no itervox at all
	dir := t.TempDir()
	settings := filepath.Join(dir, ".claude", "settings.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(settings), 0o755))
	require.NoError(t, os.WriteFile(settings, []byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"itervox hook claude-pretooluse"}]}]}}`), 0o644))
	require.Equal(t, []string{settings}, installedBareHookFiles(dir))
	w := hookPathWarning()
	require.NotEmpty(t, w)
	out := renderDoctorReport(DoctorReport{Workflow: filepath.Join(dir, "WORKFLOW.md"), HookPathWarnings: []string{"the itervox PreToolUse hook in " + settings + ": " + w}})
	assert.Contains(t, out, "WARNING: the itervox PreToolUse hook in "+settings+": no `itervox` is on PATH")
}
