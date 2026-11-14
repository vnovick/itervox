package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tailscale/hujson"

	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/guardrails"
)

// itervox hook: the guardrail policy as a PreToolUse hook for an agent
// session the developer runs themselves (Claude Code, Codex), with no
// daemon. A convenience layer: it reads the command string before the
// shell expands it. The exec-time shims and the pre-push hook of runs
// Itervox starts remain the enforcement.

const hookUsage = `Usage:
  itervox hook claude-pretooluse [--workflow WORKFLOW.md] [--require-workflow]
  itervox hook codex-pretooluse  [--workflow WORKFLOW.md] [--require-workflow]
  itervox hook install claude|codex [--project|--user] [--dir DIR] [--print]

The pretooluse commands read a PreToolUse hook's JSON on stdin and answer
with the hook's decision: no output allows (the agent's own permission
rules still apply), a deny for a guardrail refusal, an ask for an
approval_commands command or an edit of a guardrails.lanes.human file
(Codex cannot ask, so there it is a deny). Without --workflow, the nearest
WORKFLOW.md at or above the session's directory is used, and none found
means no policy (allow) unless --require-workflow. A workflow that is named
or found but cannot be loaded always asks.
`

// runHookEntrypoint runs `itervox hook …` and exits when args name it. It
// runs before the daemon's start-up work: the env-scoped approval rules
// must see the session's environment, not one with .itervox/.env loaded.
func runHookEntrypoint(args []string) {
	if len(args) > 1 && args[1] == "hook" {
		exitWithoutCleanup(runHook(args[2:], os.Stdin, os.Stdout, os.Stderr, os.Environ()))
	}
}

func runHook(args []string, stdin io.Reader, stdout, stderr io.Writer, env []string) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, hookUsage)
		return 2
	}
	switch args[0] {
	case "claude-pretooluse":
		return hookPreToolUse(guardrails.HookAgentClaude, args[1:], stdin, stdout, stderr, env)
	case "codex-pretooluse":
		return hookPreToolUse(guardrails.HookAgentCodex, args[1:], stdin, stdout, stderr, env)
	case "install":
		return hookInstall(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(stdout, hookUsage)
		return 0
	}
	_, _ = fmt.Fprintf(stderr, "itervox hook: unknown subcommand %q\n%s", args[0], hookUsage)
	return 2
}

// hookPreToolUse answers one PreToolUse call. It always exits 0: the
// decision is in the JSON, and a failure to decide is an ask, never a
// silent allow.
func hookPreToolUse(agent string, args []string, stdin io.Reader, stdout, stderr io.Writer, env []string) int {
	fs := flag.NewFlagSet("itervox hook "+agent+"-pretooluse", flag.ContinueOnError)
	fs.SetOutput(stderr)
	workflowPath := fs.String("workflow", "", "path to WORKFLOW.md (default: the nearest at or above the session's directory)")
	require := fs.Bool("require-workflow", false, "ask when no WORKFLOW.md is found (default: no workflow, no policy)")
	answer := func(v guardrails.HookVerdict) int {
		if out := guardrails.HookOutput(agent, v); out != nil {
			_, _ = stdout.Write(out)
		}
		return 0
	}
	ask := func(reason string) int {
		return answer(guardrails.HookVerdict{Decision: guardrails.HookAsk, Reason: "itervox hook: " + reason + "; the guardrail policy could not be applied, so confirm this tool call yourself."})
	}
	if err := fs.Parse(args); err != nil {
		return ask("bad hook arguments: " + err.Error())
	}
	data, err := io.ReadAll(io.LimitReader(stdin, 16<<20))
	if err != nil {
		return ask("cannot read the hook input: " + err.Error())
	}
	in, err := guardrails.ParseHookInput(data)
	if err != nil {
		return ask(err.Error())
	}
	path := *workflowPath
	if path == "" {
		start := in.Cwd
		if start == "" {
			start, _ = os.Getwd()
		}
		path = findWorkflow(start)
		if path == "" {
			if *require {
				return ask("no WORKFLOW.md at or above " + start)
			}
			return 0 // not an Itervox project: no policy
		}
	}
	policy, root, err := hookPolicy(path)
	if err != nil {
		return ask(err.Error())
	}
	if policy == nil {
		return 0 // guardrails.enabled: false
	}
	return answer(policy.EvaluateHook(in, env, root))
}

// findWorkflow is the nearest WORKFLOW.md at or above dir ("" when none).
func findWorkflow(dir string) string {
	dir, _ = filepath.Abs(dir)
	for {
		p := filepath.Join(dir, "WORKFLOW.md")
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// hookPolicy builds the guardrail policy of the workflow at path, and the
// directory edited paths fall back to outside a git repository. A nil
// policy means guardrails are off.
func hookPolicy(path string) (*guardrails.Policy, string, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, "", fmt.Errorf("cannot load %s: %w", path, err)
	}
	g := cfg.Guardrails
	if err := errors.Join(config.ValidateGuardrailLanes(g.Lanes), config.ValidateGuardrailApprovals(g)); err != nil {
		return nil, "", fmt.Errorf("invalid guardrails in %s: %w", path, err)
	}
	if !g.Enabled {
		return nil, "", nil
	}
	root, _ := filepath.Abs(filepath.Dir(path))
	return &guardrails.Policy{
		ProtectedBranches:   guardrails.ProtectedBranches(cfg.Agent.BaseBranch, cfg.Workspace.BaseBranch),
		AllowForcePush:      g.AllowForcePush,
		ApprovalCommands:    g.ApprovalCommands,
		EnvApprovalCommands: guardrails.EnvApprovalsFromConfig(g.EnvApprovalCommands),
		Lanes:               guardrails.Lanes{Human: g.Lanes.Human, SecondOpinion: g.Lanes.SecondOpinion},
	}, root, nil
}

// hookTarget is where `itervox hook install` writes.
type hookTarget struct {
	file    string // settings file
	command string // the hook command
	matcher string
}

// hookMarker identifies the hook entry this command manages.
func hookMarker(agent string) string { return "itervox hook " + agent + "-pretooluse" }

func hookInstall(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		_, _ = fmt.Fprint(stderr, hookUsage)
		return 2
	}
	agent := args[0]
	fs := flag.NewFlagSet("itervox hook install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.Bool("project", false, "write the project's settings (default)")
	user := fs.Bool("user", false, "write your user settings (every project)")
	dir := fs.String("dir", ".", "project directory")
	workflow := fs.String("workflow", "WORKFLOW.md", "the project's workflow, relative to --dir (with --project)")
	printOnly := fs.Bool("print", false, "print the settings snippet; write nothing")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *project && *user {
		_, _ = fmt.Fprintln(stderr, "itervox hook install: --project and --user are exclusive")
		return 2
	}
	t, err := installTarget(agent, *user, *dir, *workflow)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "itervox hook install: %v\n", err)
		return 2
	}
	entry := hookEntry(t)
	if *printOnly {
		snippet, _ := json.MarshalIndent(map[string]any{"hooks": map[string]any{"PreToolUse": []any{entry}}}, "", "  ")
		_, _ = fmt.Fprintf(stdout, "# %s\n%s\n", t.file, snippet)
		return 0
	}
	changed, err := mergeHookSettings(t.file, hookMarker(agent), entry)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "itervox hook install: %v\n", err)
		return 1
	}
	if !changed {
		_, _ = fmt.Fprintf(stdout, "%s already has the itervox hook.\n", t.file)
		return 0
	}
	_, _ = fmt.Fprintf(stdout, "Wrote the itervox PreToolUse hook to %s.\n", t.file)
	if agent == guardrails.HookAgentCodex {
		_, _ = fmt.Fprintln(stdout, "Codex asks you to review new hooks: open /hooks in Codex and trust it.")
	}
	return 0
}

func installTarget(agent string, user bool, dir, workflow string) (hookTarget, error) {
	home, _ := os.UserHomeDir()
	switch agent {
	case guardrails.HookAgentClaude:
		t := hookTarget{matcher: "Bash|Edit|Write|MultiEdit|NotebookEdit"}
		if user {
			t.file = filepath.Join(home, ".claude", "settings.json")
			t.command = "itervox hook claude-pretooluse"
			return t, nil
		}
		t.file = filepath.Join(dir, ".claude", "settings.json")
		t.command = "itervox hook claude-pretooluse --workflow \"$CLAUDE_PROJECT_DIR\"/" + shellWord(filepath.ToSlash(filepath.Clean(workflow)))
		return t, nil
	case guardrails.HookAgentCodex:
		t := hookTarget{matcher: "^(Bash|apply_patch)$"}
		if user {
			t.file = filepath.Join(home, ".codex", "hooks.json")
			t.command = "itervox hook codex-pretooluse"
			return t, nil
		}
		t.file = filepath.Join(dir, ".codex", "hooks.json")
		t.command = "itervox hook codex-pretooluse --require-workflow"
		return t, nil
	}
	return hookTarget{}, fmt.Errorf("unknown agent %q (want claude or codex)", agent)
}

// hookGroup is one hooks.PreToolUse matcher group (the shape Claude Code
// and Codex share), in the key order the docs show.
type hookGroup struct {
	Matcher string        `json:"matcher"`
	Hooks   []hookHandler `json:"hooks"`
}

type hookHandler struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

func hookEntry(t hookTarget) hookGroup {
	return hookGroup{Matcher: t.matcher, Hooks: []hookHandler{{Type: "command", Command: t.command}}}
}

// mergeHookSettings adds entry to file's hooks.PreToolUse, or replaces the
// entry whose command starts with marker, keeping every other key, its
// order and comments. changed is false when the file already has it.
func mergeHookSettings(file, marker string, entry hookGroup) (changed bool, err error) {
	orig, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read %s: %w", file, err)
	}
	if len(bytes.TrimSpace(orig)) == 0 {
		orig = []byte("{}")
	}
	v, err := hujson.Parse(orig)
	if err != nil {
		return false, fmt.Errorf("parse %s: %w", file, err)
	}
	std := v.Clone()
	std.Standardize()
	var cur struct {
		Hooks *struct {
			PreToolUse []struct {
				Hooks []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(std.Pack(), &cur); err != nil {
		return false, fmt.Errorf("%s is not a JSON object with a hooks map: %w", file, err)
	}
	var op map[string]any
	switch {
	case cur.Hooks == nil:
		op = map[string]any{"op": "add", "path": "/hooks", "value": map[string]any{"PreToolUse": []any{entry}}}
	case cur.Hooks.PreToolUse == nil:
		op = map[string]any{"op": "add", "path": "/hooks/PreToolUse", "value": []any{entry}}
	default:
		op = map[string]any{"op": "add", "path": "/hooks/PreToolUse/-", "value": entry}
		for i, g := range cur.Hooks.PreToolUse {
			for _, h := range g.Hooks {
				if strings.HasPrefix(strings.TrimSpace(h.Command), marker) {
					op = map[string]any{"op": "replace", "path": fmt.Sprintf("/hooks/PreToolUse/%d", i), "value": entry}
				}
			}
		}
	}
	patch, _ := json.Marshal([]any{op})
	before := v.Clone()
	before.Standardize()
	if err := v.Patch(patch); err != nil {
		return false, fmt.Errorf("update %s: %w", file, err)
	}
	after := v.Clone()
	after.Standardize()
	if jsonEqual(before.Pack(), after.Pack()) {
		return false, nil
	}
	var out []byte
	var indented bytes.Buffer
	if json.Valid(orig) && json.Indent(&indented, after.Pack(), "", "  ") == nil {
		// Plain JSON (what both agents write): re-indented, key order kept.
		out = append(indented.Bytes(), '\n')
	} else {
		v.Format() // JSONC: keep the comments
		out = v.Pack()
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return false, fmt.Errorf("create %s: %w", filepath.Dir(file), err)
	}
	if err := atomicfs.WriteFile(file, out, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", file, err)
	}
	return true, nil
}

// jsonEqual compares two JSON documents by value.
func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	ax, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	return bytes.Equal(ax, by)
}
