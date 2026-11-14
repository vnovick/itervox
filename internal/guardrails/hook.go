package guardrails

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/gitexec"
)

// PreToolUse hooks (itervox hook): the guardrail policy applied to an
// interactive agent session's tool calls before they run. A convenience
// layer: the hook reads the command the agent wrote, before the shell
// expands it, so a command assembled at run time, a script file, or a
// program reached another way gets past it. The exec-time shims and the
// pre-push hook (runs Itervox starts) and remote branch protection remain
// the enforcement.

// Hook decisions. HookAllow is no decision: the tool call goes through the
// agent's normal permission flow.
const (
	HookAllow = ""
	HookAsk   = "ask"
	HookDeny  = "deny"
)

// Hook agents: whose hook protocol the input and output follow.
const (
	HookAgentClaude = "claude"
	HookAgentCodex  = "codex"
)

// HumanLaneEditReason is the reason a human-lane file edit asks.
const HumanLaneEditReason = "pipeline file: an agent may change what CI checks, not how"

// HookVerdict is the hook's answer to one tool call.
type HookVerdict struct {
	Decision string
	Reason   string
}

// stronger reports whether v outranks w (deny > ask > allow).
func (v HookVerdict) stronger(w HookVerdict) bool {
	rank := func(d string) int { return slices.Index([]string{HookAllow, HookAsk, HookDeny}, d) }
	return rank(v.Decision) > rank(w.Decision)
}

// HookInput is the part of a PreToolUse hook's stdin the guardrails read.
// Claude Code and Codex both send tool_name, tool_input and cwd.
type HookInput struct {
	HookEventName string    `json:"hook_event_name"`
	ToolName      string    `json:"tool_name"`
	ToolInput     toolInput `json:"tool_input"`
	Cwd           string    `json:"cwd"`
}

// toolInput holds the tool_input fields the rules look at: command (Bash;
// Codex apply_patch carries its patch here), file_path (Edit, Write,
// MultiEdit) and notebook_path (NotebookEdit).
type toolInput struct {
	Command      string `json:"command"`
	FilePath     string `json:"file_path"`
	NotebookPath string `json:"notebook_path"`
}

// ParseHookInput decodes a PreToolUse hook's stdin.
func ParseHookInput(data []byte) (HookInput, error) {
	var in HookInput
	if err := json.Unmarshal(data, &in); err != nil {
		return in, fmt.Errorf("guardrails: hook input is not PreToolUse JSON: %w", err)
	}
	if in.ToolName == "" {
		return in, fmt.Errorf("guardrails: hook input has no tool_name")
	}
	return in, nil
}

// EvaluateHook applies the policy to one tool call. env is the hook's own
// environment (the session's, which the command inherits); root is the
// directory edited paths are taken relative to when they are not in a git
// repository (the workflow's directory).
func (p Policy) EvaluateHook(in HookInput, env []string, root string) HookVerdict {
	cwd := in.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	switch in.ToolName {
	case "Bash":
		return p.hookCommandLine(in.ToolInput.Command, cwd, LookupIn(env), currentBranch, 0)
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		path := in.ToolInput.FilePath
		if path == "" {
			path = in.ToolInput.NotebookPath
		}
		return p.hookEdit([]string{path}, cwd, root)
	case "apply_patch": // Codex
		return p.hookEdit(patchPaths(in.ToolInput.Command), cwd, root)
	}
	return HookVerdict{}
}

// maxHookDepth bounds nested command strings (`bash -c "$(…)"`).
const maxHookDepth = 6

// hookEnv is the environment one command line's commands see: the hook's
// environment under the assignments the line made.
type hookEnv struct {
	base EnvLookup
	set  map[string]*string // nil: assigned a value only known at run time
}

func (e hookEnv) with(name string, value *string) hookEnv {
	out := hookEnv{base: e.base, set: map[string]*string{}}
	for k, v := range e.set {
		out.set[k] = v
	}
	out.set[envKey(name)] = value
	return out
}

func (e hookEnv) lookup(name string) (string, bool) {
	if v, ok := e.set[envKey(name)]; ok {
		if v == nil {
			return "", false
		}
		return *v, true
	}
	return e.base(name)
}

// hookCommandLine checks every simple command of line, in order, carrying
// `cd DIR` and assignments forward; the strongest verdict wins.
func (p Policy) hookCommandLine(line, cwd string, base EnvLookup, branchOf func(string) string, depth int) HookVerdict {
	if depth > maxHookDepth {
		return HookVerdict{Decision: HookAsk, Reason: "itervox hook: the command nests too deeply to check; confirm it yourself"}
	}
	parsed := parseShellLine(line)
	var verdict HookVerdict
	keep := func(v HookVerdict) {
		if v.stronger(verdict) {
			verdict = v
		}
	}
	env := hookEnv{base: base}
	for _, cmd := range parsed.Commands {
		words, cmdEnv := splitAssignments(cmd.Words, env)
		if len(words) == 0 {
			env = cmdEnv // NAME=value alone: the shell keeps it
			continue
		}
		var nested []string
		words, cmdEnv, nested = unwrap(words, cmdEnv)
		for _, n := range nested {
			keep(p.hookCommandLine(n, cwd, cmdEnv.lookup, branchOf, depth+1))
		}
		if len(words) == 0 {
			continue
		}
		prog := words[0]
		name := filepath.Base(prog.Text)
		switch {
		case name == "export" || name == "declare" || name == "typeset" || name == "readonly":
			_, env = splitAssignments(skipFlags(words[1:], nil), env)
			continue
		case name == "cd":
			if len(words) > 1 && !words[1].Dynamic {
				if d := words[1].Text; filepath.IsAbs(d) {
					cwd = d
				} else {
					cwd = filepath.Join(cwd, d)
				}
			}
			continue
		case prog.Dynamic:
			keep(HookVerdict{Decision: HookAsk, Reason: fmt.Sprintf(
				"itervox hook: the program `%s` is only known when the command runs, so the guardrails cannot check it; confirm it yourself", prog.Text)})
			continue
		}
		args := make([]string, 0, len(words)-1)
		for _, w := range words[1:] {
			args = append(args, w.Text)
		}
		keep(p.hookCommand(name, args, cwd, cmdEnv.lookup, branchOf))
	}
	for _, n := range parsed.Nested {
		keep(p.hookCommandLine(n, cwd, env.lookup, branchOf, depth+1))
	}
	return verdict
}

// hookCommand applies the shim's rules, and the push rules a pre-push hook
// would apply, to one command.
func (p Policy) hookCommand(tool string, args []string, cwd string, lookup EnvLookup, branchOf func(string) string) HookVerdict {
	d := p.decide(tool, args, cwd, lookup, branchOf)
	if d.act == actForward && tool == "git" {
		d = p.hookGitPush(args, cwd, branchOf)
	}
	display := displayCommand(tool, args)
	switch d.act {
	case actRefuse:
		return HookVerdict{Decision: HookDeny, Reason: d.message + " (itervox hook; if you mean it, run it yourself.)"}
	case actApprove:
		return HookVerdict{Decision: HookAsk, Reason: fmt.Sprintf("guardrails: `%s` needs a human approval (guardrails.approval_commands: %s).", display, d.pattern)}
	}
	return HookVerdict{}
}

// gitPushFlagsWithValue are git push options that take the next argument.
var gitPushFlagsWithValue = []string{"-o", "--push-option", "--repo", "--receive-pack", "--exec"}

// hookGitPush applies the pre-push rules to the refspecs a `git push`
// names (the hook cannot see the ref updates the pre-push hook gets): no
// push to a protected branch, no tag push, and no force push or branch
// deletion unless allow_force_push (and never of a protected branch). An
// interactive session has no issue branch, so any other branch may be
// pushed.
func (p Policy) hookGitPush(args []string, cwd string, branchOf func(string) string) decision {
	dir, i, _ := gitGlobals(args, cwd)
	if i >= len(args) || args[i] != "push" {
		return decision{}
	}
	var force, del, tags, all bool
	var pos []string
	rest := args[i+1:]
	for j := 0; j < len(rest); j++ {
		a := rest[j]
		switch {
		case a == "--":
			pos = append(pos, rest[j+1:]...)
			j = len(rest)
		case slices.Contains(gitPushFlagsWithValue, a):
			j++
		case a == "--force" || a == "--force-if-includes" || strings.HasPrefix(a, "--force-with-lease"):
			force = true
		case a == "--delete":
			del = true
		case a == "--tags" || a == "--follow-tags":
			tags = true
		case a == "--all" || a == "--branches" || a == "--mirror":
			all = true
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-") && len(a) > 1:
			force = force || strings.ContainsRune(a[1:], 'f')
			del = del || strings.ContainsRune(a[1:], 'd')
		default:
			pos = append(pos, a)
		}
	}
	refuse := func(kind, msg string) decision {
		return decision{act: actRefuse, kind: kind, message: "guardrails: " + msg}
	}
	if tags {
		return refuse(KindTag, "pushing tags is refused: agents do not push tags. Leave releases to a human.")
	}
	if all {
		return refuse(KindBranch, "pushing every branch is refused: agents push only their own branch.")
	}
	var dsts []string
	if len(pos) <= 1 { // no refspec: the current branch
		dsts = append(dsts, branchOf(dir))
	}
	for _, spec := range pos[min(len(pos), 1):] {
		if strings.HasPrefix(spec, "+") {
			force = true
			spec = spec[1:]
		}
		src, dst, colon := strings.Cut(spec, ":")
		if !colon {
			dst = src
		}
		if colon && src == "" {
			del = true
		}
		if strings.HasPrefix(src, "refs/tags/") || strings.HasPrefix(dst, "refs/tags/") {
			return refuse(KindTag, "pushing tags is refused: agents do not push tags. Leave releases to a human.")
		}
		if dst == "HEAD" {
			dst = branchOf(dir)
		}
		dsts = append(dsts, strings.TrimPrefix(dst, "refs/heads/"))
	}
	for _, dst := range dsts {
		if dst != "" && p.protected(dst) {
			return refuse(KindBranch, fmt.Sprintf("push to %s is refused: agents do not push to a protected branch. Push a branch of your own and open a pull request.", dst))
		}
	}
	switch {
	case del && !p.AllowForcePush:
		return refuse(KindDelete, "deleting a remote branch is refused (guardrails.allow_force_push is off).")
	case force && !p.AllowForcePush:
		return refuse(KindForcePush, "force pushes are refused (guardrails.allow_force_push is off). Push new commits instead of rewriting history.")
	}
	return decision{}
}

// splitAssignments separates a command's leading NAME=value words and
// returns env with them applied: what the command sees (and, for a line
// of assignments only or export, what the shell keeps).
func splitAssignments(words []shellWord, env hookEnv) (rest []shellWord, cmdEnv hookEnv) {
	cmdEnv = env
	i := 0
	for ; i < len(words); i++ {
		name, value, ok := splitAssignment(words[i].Text)
		if !ok {
			break
		}
		var v *string
		if !words[i].Dynamic {
			v = &value
		}
		cmdEnv = cmdEnv.with(name, v)
	}
	return words[i:], cmdEnv
}

// unwrap strips the programs that run another program (sudo, env,
// command, exec, nohup, time, nice, timeout, xargs, shell keywords) and
// returns the command strings run through a shell (`sh -c '…'`, `eval`).
func unwrap(words []shellWord, env hookEnv) ([]shellWord, hookEnv, []string) {
	for len(words) > 0 {
		name := filepath.Base(words[0].Text)
		switch name {
		case "{", "}", "!", "if", "then", "else", "elif", "do", "while", "until", "builtin", "nohup":
			words = words[1:]
		case "command", "exec", "time":
			words = skipFlags(words[1:], nil)
		case "sudo", "doas":
			words = skipFlags(words[1:], []string{"-u", "-g", "-C", "-D", "-h", "-p", "-r", "-t", "-U", "-T"})
		case "nice":
			words = skipFlags(words[1:], []string{"-n"})
		case "timeout":
			words = skipFlags(words[1:], []string{"-s", "-k"})
			if len(words) > 0 {
				words = words[1:] // the duration
			}
		case "xargs":
			words = skipFlags(words[1:], []string{"-I", "-n", "-P", "-L", "-d", "-s", "-E", "-a"})
		case "env":
			words = skipFlags(words[1:], []string{"-u", "-C", "-S"})
			words, env = splitAssignments(words, env)
		case "eval":
			var parts []string
			for _, w := range words[1:] {
				parts = append(parts, w.Text)
			}
			return nil, env, []string{strings.Join(parts, " ")}
		case "sh", "bash", "zsh", "dash", "ksh":
			for j := 1; j < len(words); j++ {
				a := words[j].Text
				if !strings.HasPrefix(a, "-") || strings.HasPrefix(a, "--") {
					break
				}
				if strings.ContainsRune(a[1:], 'c') && j+1 < len(words) {
					return nil, env, []string{words[j+1].Text}
				}
			}
			return words, env, nil
		default:
			return words, env, nil
		}
	}
	return words, env, nil
}

// skipFlags drops leading flags (and the value of those in withValue).
func skipFlags(words []shellWord, withValue []string) []shellWord {
	for len(words) > 0 {
		a := words[0].Text
		if a == "--" {
			return words[1:]
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			return words
		}
		words = words[1:]
		if slices.Contains(withValue, a) && len(words) > 0 {
			words = words[1:]
		}
	}
	return words
}

// patchPaths are the files a Codex apply_patch patch touches.
func patchPaths(patch string) []string {
	var out []string
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "} {
			if p, ok := strings.CutPrefix(line, prefix); ok && strings.TrimSpace(p) != "" {
				out = append(out, strings.TrimSpace(p))
			}
		}
	}
	return out
}

// hookEdit asks when an edited file is in the human lane
// (guardrails.lanes.human): an agent may change what CI checks, not how.
func (p Policy) hookEdit(paths []string, cwd, root string) HookVerdict {
	var human []string
	for _, path := range paths {
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(cwd, path)
		}
		if rel, ok := repoRelative(path, root); ok && p.Lanes.Lane(rel) == LaneHuman {
			human = append(human, rel)
		}
	}
	if len(human) == 0 {
		return HookVerdict{}
	}
	return HookVerdict{Decision: HookAsk, Reason: HumanLaneEditReason + " (" + strings.Join(human, ", ") +
		" is in guardrails.lanes.human). Approve only if you meant the agent to change it."}
}

// repoRelative is path relative to the root of the git repository holding
// it (or to fallback, outside a repository). ok is false for a path
// outside both.
func repoRelative(path, fallback string) (string, bool) {
	path = resolveExisting(filepath.Clean(path))
	root := gitTopLevel(filepath.Dir(path))
	if root == "" {
		root = fallback
	}
	if root == "" {
		return "", false
	}
	rel, err := filepath.Rel(resolveExisting(root), path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// resolveExisting resolves symlinks in the longest existing prefix of path
// (a file about to be written does not exist yet).
func resolveExisting(path string) string {
	rest := ""
	for p := path; ; p = filepath.Dir(p) {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		if filepath.Dir(p) == p {
			return path
		}
		rest = filepath.Join(filepath.Base(p), rest)
	}
}

// gitTopLevel is the root of the work tree holding dir (or its nearest
// existing ancestor); "" outside a repository.
func gitTopLevel(dir string) string {
	for ; ; dir = filepath.Dir(dir) {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			break
		}
		if filepath.Dir(dir) == dir {
			return ""
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := gitexec.Command(ctx, dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// HookOutput is what the hook prints for v: nothing for allow (exit 0
// with no output leaves the call to the agent's permission flow), else
// the PreToolUse decision JSON. Codex does not support "ask" (a PreToolUse
// hook answering ask fails open there), so for Codex an ask is a deny
// that says a human has to run or approve the command.
func HookOutput(agent string, v HookVerdict) []byte {
	if v.Decision == HookAllow {
		return nil
	}
	decision, reason := v.Decision, v.Reason
	if agent == HookAgentCodex && decision == HookAsk {
		decision = HookDeny
		reason += " Codex hooks cannot ask for a confirmation, so it is refused: ask the user to run it, or to approve it."
	}
	out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName":            "PreToolUse",
		"permissionDecision":       decision,
		"permissionDecisionReason": reason,
	}})
	return append(out, '\n')
}
