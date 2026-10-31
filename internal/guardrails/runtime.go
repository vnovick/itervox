package guardrails

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/gitexec"
)

// Entrypoints of the itervox binary that shims and hooks call (Main).
const (
	shimEntrypoint = "guardrail-shim"
	hookEntrypoint = "guardrail-hook"
)

// shimMarker is in every shim script, so resolving the real program never
// picks a shim (ours or a nested run's) and recursion is impossible.
const shimMarker = "itervox-guardrail-shim"

// gitHookNames are the hooks git runs from core.hooksPath. Every one gets a
// forwarder to the repository's own hooks directory (pre-push runs the
// guardrail checks first), so pointing core.hooksPath at the runtime
// directory never stops the repository's own hooks.
var gitHookNames = []string{
	"applypatch-msg", "pre-applypatch", "post-applypatch", "pre-commit",
	"pre-merge-commit", "prepare-commit-msg", "commit-msg", "post-commit",
	"pre-rebase", "post-checkout", "post-merge", "pre-push", "pre-receive",
	"update", "proc-receive", "post-receive", "post-update",
	"reference-transaction", "push-to-checkout", "pre-auto-gc",
	"post-rewrite", "sendemail-validate", "post-index-change",
	"p4-changelist", "p4-prepare-changelist", "p4-post-changelist", "p4-pre-submit",
}

// Options configures Prepare.
type Options struct {
	// ParentDir holds the runtime directory (os.TempDir() when empty).
	ParentDir string
	// Executable is the itervox binary shims and hooks run.
	Executable string
	// WorkDir is the agent's working directory: its repository's hooks
	// directory is chained after the guardrail checks.
	WorkDir string
	// Identifier, IssueBranch, ProtectedBranches, AllowForcePush,
	// ApprovalCommands, ApprovalsEnabled, DaemonURL and Token fill the
	// Policy.
	Identifier        string
	IssueBranch       string
	ProtectedBranches []string
	AllowForcePush    bool
	ApprovalCommands  []string
	ApprovalsEnabled  bool
	DaemonURL         string
	Token             string
	// PreApproved are approval commands the operator approved before the
	// run (Policy.PreApproved).
	PreApproved []string
}

// Runtime is one run's prepared guardrails directory.
type Runtime struct {
	policy Policy
}

// Prepare writes a run's runtime directory: the policy, a shim per
// ShimNames program, and a hook per gitHookNames name.
func Prepare(ctx context.Context, opts Options) (*Runtime, error) {
	if opts.Executable == "" {
		return nil, errors.New("guardrails: no itervox executable")
	}
	dir, err := os.MkdirTemp(opts.ParentDir, "itervox-guardrails-*")
	if err != nil {
		return nil, fmt.Errorf("guardrails: create runtime dir: %w", err)
	}
	// A symlinked temp root (macOS /var → /private/var) must not let a
	// PATH entry and a resolved shim path disagree.
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	p := Policy{
		Identifier:        opts.Identifier,
		IssueBranch:       opts.IssueBranch,
		ProtectedBranches: opts.ProtectedBranches,
		AllowForcePush:    opts.AllowForcePush,
		ApprovalCommands:  opts.ApprovalCommands,
		ApprovalsEnabled:  opts.ApprovalsEnabled && opts.DaemonURL != "" && opts.Token != "",
		OrigHooksDir:      repoHooksDir(ctx, opts.WorkDir),
		Dir:               dir,
		BinDir:            filepath.Join(dir, "bin"),
		DaemonURL:         opts.DaemonURL,
		Token:             opts.Token,
		PreApproved:       opts.PreApproved,
	}
	r := &Runtime{policy: p}
	if err := r.write(opts.Executable); err != nil {
		r.Remove()
		return nil, err
	}
	return r, nil
}

func (r *Runtime) write(exe string) error {
	p := r.policy
	for _, d := range []string{p.BinDir, r.HooksDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("guardrails: create %s: %w", d, err)
		}
	}
	if err := writePolicy(p); err != nil {
		return err
	}
	policyPath := r.PolicyPath()
	for _, name := range ShimNames(p.ApprovalCommands) {
		script := "#!/bin/sh\n# " + shimMarker + "\nexec " + agent.ShellQuote(exe) + " " + shimEntrypoint + " " +
			agent.ShellQuote(policyPath) + " " + agent.ShellQuote(name) + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(p.BinDir, name), []byte(script), 0o700); err != nil {
			return fmt.Errorf("guardrails: write shim %s: %w", name, err)
		}
		if err := writeNativeShim(runtime.GOOS, p.BinDir, name, exe, policyPath); err != nil {
			return err
		}
	}
	for _, name := range gitHookNames {
		var script string
		if name == "pre-push" {
			script = "#!/bin/sh\nexec " + agent.ShellQuote(exe) + " " + hookEntrypoint + " " +
				agent.ShellQuote(policyPath) + " " + name + " \"$@\"\n"
		} else {
			script = forwarderScript(p.OrigHooksDir, name)
		}
		if err := os.WriteFile(filepath.Join(r.HooksDir(), name), []byte(script), 0o700); err != nil {
			return fmt.Errorf("guardrails: write hook %s: %w", name, err)
		}
	}
	return nil
}

// writeNativeShim writes, on Windows, a name.cmd wrapper next to the sh
// shim: bash (Git Bash, the agent's Bash tool) runs the sh shim, while
// cmd.exe, PowerShell and PATHEXT lookups only consider files with an
// executable extension. Elsewhere it writes nothing.
func writeNativeShim(goos, binDir, name, exe, policyPath string) error {
	if goos != "windows" {
		return nil
	}
	path := filepath.Join(binDir, name+".cmd")
	if err := os.WriteFile(path, []byte(cmdShimScript(exe, policyPath, name)), 0o700); err != nil {
		return fmt.Errorf("guardrails: write shim %s.cmd: %w", name, err)
	}
	return nil
}

// cmdShimScript is the batch-file form of the sh shim: it carries
// shimMarker in its first lines (so resolveReal skips it) and forwards
// every argument and the exit status. Arguments are double-quoted, with
// '%' doubled so cmd.exe does not expand it.
func cmdShimScript(exe, policyPath, name string) string {
	q := func(s string) string { return `"` + strings.ReplaceAll(s, "%", "%%") + `"` }
	return "@echo off\r\nrem " + shimMarker + "\r\n" +
		q(exe) + " " + shimEntrypoint + " " + q(policyPath) + " " + q(name) + " %*\r\n" +
		"exit /b %ERRORLEVEL%\r\n"
}

// forwarderScript runs the repository's own hook name, when there is one.
func forwarderScript(origDir, name string) string {
	if origDir == "" {
		return "#!/bin/sh\nexit 0\n"
	}
	h := agent.ShellQuote(filepath.Join(origDir, name))
	return "#!/bin/sh\nif [ -x " + h + " ]; then exec " + h + " \"$@\"; fi\nexit 0\n"
}

// repoHooksDir is the hooks directory git would use in dir without the
// guardrails (core.hooksPath honoured, worktrees resolved to the common
// directory), as an absolute path; "" when dir is not a repository.
func repoHooksDir(ctx context.Context, dir string) string {
	if dir == "" {
		return ""
	}
	out, err := gitexec.Command(ctx, dir, "rev-parse", "--git-path", "hooks").Output()
	if err != nil {
		return ""
	}
	hooks := strings.TrimSpace(string(out))
	if hooks != "" && !filepath.IsAbs(hooks) {
		hooks = filepath.Join(dir, hooks)
	}
	return hooks
}

// Policy returns the runtime's policy.
func (r *Runtime) Policy() Policy { return r.policy }

// Dir is the runtime directory.
func (r *Runtime) Dir() string { return r.policy.Dir }

// BinDir is the shim directory put first on the agent's PATH.
func (r *Runtime) BinDir() string { return r.policy.BinDir }

// HooksDir is the directory core.hooksPath points at.
func (r *Runtime) HooksDir() string { return filepath.Join(r.policy.Dir, "hooks") }

// PolicyPath is the policy file shims and hooks read.
func (r *Runtime) PolicyPath() string { return filepath.Join(r.policy.Dir, policyFileName) }

// SetIssueBranch records the issue branch after Itervox adopted the branch
// the agent switched to (the worker's per-turn branch tracking).
func (r *Runtime) SetIssueBranch(branch string) error {
	if r == nil || branch == "" || branch == r.policy.IssueBranch {
		return nil
	}
	r.policy.IssueBranch = branch
	return writePolicy(r.policy)
}

// Remove deletes the runtime directory. Safe on nil.
func (r *Runtime) Remove() {
	if r == nil || r.policy.Dir == "" {
		return
	}
	_ = os.RemoveAll(r.policy.Dir)
}

// gitConfigParameters is the GIT_CONFIG_PARAMETERS value that points git at
// the runtime's hooks: the format `git -c` itself exports to subprocesses.
func (r *Runtime) gitConfigParameters() string {
	return gitConfigParam("core.hooksPath", r.HooksDir())
}

func gitConfigParam(key, value string) string {
	return sqQuote(key) + "=" + sqQuote(value)
}

// sqQuote is git's sq_quote: '…' with ' written as '\”.
func sqQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// AgentEnv is the environment of a guarded dispatched run, applied by the
// agent runner (agent.WithRunEnv): the shims first on PATH, core.hooksPath
// through GIT_CONFIG_PARAMETERS, and the dashboard API token blanked so the
// agent cannot answer its own approval through the operator's API.
func (r *Runtime) AgentEnv() agent.RunEnv {
	return agent.RunEnv{
		Vars:        []string{"GIT_CONFIG_PARAMETERS=" + r.gitConfigParameters(), "ITERVOX_API_TOKEN="},
		PathPrepend: r.BinDir(),
	}
}

// CommandPrefix is AgentEnv as assignments in front of a POSIX shell
// command line (tmux sessions): PATH is the shell's own PATH with the shims
// in front.
func (r *Runtime) CommandPrefix() string {
	return "GIT_CONFIG_PARAMETERS=" + agent.ShellQuote(r.gitConfigParameters()) +
		" ITERVOX_API_TOKEN= PATH=" + agent.ShellQuote(r.BinDir()) + `:"$PATH" `
}
