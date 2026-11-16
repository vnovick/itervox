package guardrails

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claudeCodeLsFiles is the read-only call Claude Code makes with its own
// hooks disabled (84 refusals in one minute before the fix).
const claudeCodeLsFiles = "-c core.hooksPath=/dev/null ls-files --error-unmatch -- :(icase).claude/settings.local.json"

// TestDecideHooksPathOverrideOnHookFreeBuiltins: a core.hooksPath
// override is let through for the fixed list of git builtins that run no
// hook, with the override dropped from what is forwarded; everything else
// that overrides the hooks path stays a hook_bypass refusal.
func TestDecideHooksPathOverrideOnHookFreeBuiltins(t *testing.T) {
	branchOf := func(string) string { return "itervox/eng-1" }
	allowed := []struct{ cmd, forwarded string }{
		{claudeCodeLsFiles, "ls-files --error-unmatch -- :(icase).claude/settings.local.json"},
		{"-c core.hooksPath=/dev/null rev-parse --show-toplevel", "rev-parse --show-toplevel"},
		{"-c CORE.HOOKSPATH=/dev/null log -1 --format=%H", "log -1 --format=%H"},
		{"-c core.hooksPath=/dev/null -c core.quotepath=false show HEAD:f.txt", "-c core.quotepath=false show HEAD:f.txt"},
		{"--config-env=core.hooksPath=NULL_HOOKS cat-file -t HEAD", "cat-file -t HEAD"},
		{"--config-env core.hooksPath=NULL_HOOKS diff-index --cached HEAD", "diff-index --cached HEAD"},
		{"-C sub --no-pager -c core.hooksPath=/dev/null --git-dir=.git --work-tree . --literal-pathspecs ls-tree HEAD", "-C sub --no-pager --git-dir=.git --work-tree . --literal-pathspecs ls-tree HEAD"},
		{"--no-optional-locks -P -c core.hooksPath=/dev/null --namespace ns --attr-source HEAD grep -n x", "--no-optional-locks -P --namespace ns --attr-source HEAD grep -n x"},
		{"-c core.hooksPath=/dev/null -c core.hooksPath=/tmp for-each-ref refs/heads", "for-each-ref refs/heads"},
		{"-c core.hooksPath=/dev/null --version", "--version"},
		// Read-only forms of remote and config run no hook.
		{"-c core.hooksPath=/dev/null remote", "remote"},
		{"-c core.hooksPath=/dev/null remote -v", "remote -v"},
		{"-c core.hooksPath=/dev/null remote get-url --push origin", "remote get-url --push origin"},
		{"-c core.hooksPath=/dev/null config --get remote.origin.url", "config --get remote.origin.url"},
		{"-c core.hooksPath=/dev/null config --local --includes -z --list", "config --local --includes -z --list"},
		{"-c core.hooksPath=/dev/null config --file=.gitmodules --get-regexp path", "config --file=.gitmodules --get-regexp path"},
		{"-c core.hooksPath=/dev/null config -f .gitmodules -l", "config -f .gitmodules -l"},
		{"-c core.hooksPath=/dev/null config user.name", "config user.name"},
		{"-c core.hooksPath=/dev/null config get user.name", "config get user.name"},
		{"-c core.hooksPath=/dev/null config list --show-origin", "config list --show-origin"},
		// They may run post-index-change, but the override is dropped, so
		// their hooks run as configured.
		{"-c core.hooksPath=/dev/null --no-optional-locks status --short", "--no-optional-locks status --short"},
		{"-c core.hooksPath=/dev/null diff", "diff"},
		{"-c core.hooksPath=/dev/null describe --dirty", "describe --dirty"},
		// No subcommand: git prints its usage or an error and runs nothing.
		{"-c core.hooksPath=/dev/null", ""},
		{"-c core.hooksPath=/dev/null -c", "-c"},
	}
	for _, tc := range allowed {
		args := strings.Fields(tc.cmd)
		d := fuzzPolicy.decide("git", args, "/repo", noEnv, branchOf)
		assert.Equal(t, actForward, d.act, "git %s: %+v", tc.cmd, d)
		assert.True(t, d.dropHooksPath, "git %s: the override is dropped", tc.cmd)
		assert.Equal(t, strings.Fields(tc.forwarded), forwardArgs(d, args), "git %s", tc.cmd)
		// What is forwarded overrides nothing.
		fwd := fuzzPolicy.decide("git", forwardArgs(d, args), "/repo", noEnv, branchOf)
		assert.False(t, fwd.dropHooksPath, "git %s", tc.forwarded)
	}

	refused := []string{
		// The hook-running commands, and every one that is not on the list.
		"-c core.hooksPath=/dev/null commit -m x",
		"-c core.hooksPath=/dev/null push origin HEAD",
		"-c core.hooksPath=/dev/null push",
		"--config-env=core.hooksPath=X push",
		"-c core.hooksPath=/dev/null merge feature",
		"-c core.hooksPath=/dev/null rebase main",
		"-c core.hooksPath=/dev/null am x.patch",
		"-c core.hooksPath=/dev/null checkout main",
		"-c core.hooksPath=/dev/null switch main",
		"-c core.hooksPath=/dev/null pull",
		"-c core.hooksPath=/dev/null cherry-pick HEAD",
		"-c core.hooksPath=/dev/null fetch",
		"-c core.hooksPath=/dev/null gc --auto",
		"-c core.hooksPath=/dev/null update-ref refs/heads/x HEAD",
		"-c core.hooksPath=/dev/null worktree add ../x",
		"-c core.hooksPath=/dev/null send-pack origin",
		// remote and config only in their read-only forms.
		"-c core.hooksPath=/dev/null remote add x ../x.git",
		"-c core.hooksPath=/dev/null remote set-url origin ../x.git",
		"-c core.hooksPath=/dev/null remote show origin",
		"-c core.hooksPath=/dev/null remote -v update",
		"-c core.hooksPath=/dev/null config core.hooksPath /tmp/x",
		"-c core.hooksPath=/dev/null config set core.hooksPath /tmp/x",
		"-c core.hooksPath=/dev/null config --add a.b c",
		"-c core.hooksPath=/dev/null config --unset a.b",
		"-c core.hooksPath=/dev/null config --get a.b --unset",
		"-c core.hooksPath=/dev/null config --edit",
		"-c core.hooksPath=/dev/null config -e",
		"-c core.hooksPath=/dev/null config --local a.b c",
		"-c core.hooksPath=/dev/null config --bogus a.b",
		"-c core.hooksPath=/dev/null config",
		// Names are exact: no alias, no other case, no abbreviation.
		"-c core.hooksPath=/dev/null LS-FILES",
		"-c core.hooksPath=/dev/null ls",
		"-c core.hooksPath=/dev/null lsfiles",
		"-c core.hooksPath=/dev/null ''",
		// Alias tricks and includes, ahead of or behind the override.
		"-c alias.ls-files=!git-push -c core.hooksPath=/dev/null ls-files",
		"-c core.hooksPath=/dev/null -c alias.x=push x",
		"-c core.hooksPath=/dev/null -c ALIAS.log=!sh log",
		"--config-env=alias.st=ENV -c core.hooksPath=/dev/null status",
		"-c core.hooksPath=/dev/null -c include.path=/tmp/x ls-files",
		"-c include.path=/tmp/x ls-files",
		"-c includeIf.onbranch:main.path=/tmp/x log",
		// Forms git itself does not accept, or options that change what
		// runs: refused rather than guessed.
		"-ccore.hooksPath=/dev/null ls-files",
		"-c core.hooksPath=/dev/null --exec-path=/tmp/evil ls-files",
		"-c core.hooksPath=/dev/null --exec-path ls-files",
		"-c core.hooksPath=/dev/null --super-prefix=x/ ls-files",
		"-c core.hooksPath=/dev/null --list-cmds=builtins",
		"-c core.hooksPath=/dev/null --no-pag ls-files",
		"-c core.hooksPath=/dev/null -- ls-files",
		"-c core.hooksPath=/dev/null -Csub ls-files",
		"-c core.hooksPath=/dev/null --git-d=.git ls-files",
		"-c core.hooksPath=/dev/null --bogus ls-files",
		// --help runs `git help <command>`, so the next word is not the
		// subcommand git runs; refused like any other doubt.
		"-c core.hooksPath=/dev/null --help commit",
	}
	for _, cmd := range refused {
		args := strings.Fields(cmd)
		d := fuzzPolicy.decide("git", args, "/repo", noEnv, branchOf)
		assert.Equal(t, KindHookBypass, d.kind, "git %s must be refused: %+v", cmd, d)
		assert.False(t, d.dropHooksPath, "git %s", cmd)
	}

	// The list is the documented one, every entry a builtin name.
	assert.True(t, slices.IsSorted(hookFreeGitBuiltins), "keep hookFreeGitBuiltins sorted")
	for _, name := range hookFreeGitBuiltins {
		assert.NotContains(t, []string{"status", "diff", "describe", "commit", "push", "merge", "checkout", "switch", "rebase", "am", "pull", "fetch", "gc", "worktree"}, name)
	}
}

// claudeCodeCompanions are the -c settings Claude Code (2.1.x) puts ahead of
// core.hooksPath on its own git calls, captured with a logging git wrapper.
// `hook.<name>.enabled=false` disables a config-defined hook of that name,
// not a hooks-directory script: with git 2.50.1 a pre-push script still ran
// under `-c hook.pre-push.enabled=false`.
var claudeCodeCompanions = []string{
	"-c", "core.askPass=", "-c", "protocol.ext.allow=never",
	"-c", `core.alternateRefsCommand=f() { git --git-dir="$1" for-each-ref --format='%(objectname)'; }; f`,
	"-c", "submodule.recurse=false", "-c", "log.showSignature=false", "-c", "format.pretty=medium",
	"-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "diff.autoRefreshIndex=false",
	"-c", "hook.pre-commit.enabled=false", "-c", "hook.pre-push.enabled=false",
	"-c", "hook.post-index-change.enabled=false",
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.fsmonitor=", "-c", "safe.bareRepository=explicit", "-c", "protocol.file.allow=never",
}

// TestDecideAllowsClaudeCodesGitCalls: every git call Claude Code made in a
// session (captured with a logging wrapper) is let through, with only the
// core.hooksPath override dropped; before, all but ls-files/rev-parse/log
// were hook_bypass refusals (4 per run in Recent failures).
func TestDecideAllowsClaudeCodesGitCalls(t *testing.T) {
	branchOf := func(string) string { return "itervox/eng-1" }
	for _, sub := range [][]string{
		{"ls-files", "--error-unmatch", "--", ":(icase).claude/settings.local.json"},
		{"remote", "get-url", "origin"},
		{"config", "--get", "remote.origin.url"},
		{"remote", "get-url", "--push", "origin"},
		{"config", "--local", "--includes", "-z", "--list"},
		{"rev-parse", "--is-inside-work-tree", "--show-cdup"},
		{"config", "user.name"},
		{"log", "--oneline", "-n", "5"},
		{"config", "--worktree", "--includes", "-z", "--list"},
		{"--no-optional-locks", "status", "--short", "--ignore-submodules=dirty"},
	} {
		args := append(slices.Clone(claudeCodeCompanions), sub...)
		d := fuzzPolicy.decide("git", args, "/repo", noEnv, branchOf)
		require.Equal(t, actForward, d.act, "git … %v: %+v", sub, d)
		fwd := forwardArgs(d, args)
		assert.NotContains(t, strings.Join(fwd, " "), "core.hooksPath", "git … %v", sub)
		assert.Contains(t, fwd, "hook.pre-push.enabled=false", "companions are kept")
		assert.Equal(t, sub, fwd[len(fwd)-len(sub):], "git … %v", sub)
	}
}

// TestDecideKeepsApprovalForReadOnlyOverride: an approval rule still
// applies to an allowed read-only call, and the forward still drops the
// override.
func TestDecideKeepsApprovalForReadOnlyOverride(t *testing.T) {
	p := Policy{ApprovalCommands: []string{"git log"}}
	args := strings.Fields("-c core.hooksPath=/dev/null log -1")
	d := p.decide("git", args, "/repo", noEnv, func(string) string { return "" })
	assert.Equal(t, actApprove, d.act)
	assert.True(t, d.dropHooksPath)
	assert.Equal(t, []string{"log", "-1"}, forwardArgs(d, args))
}

// TestShimRunsClaudeCodeReadOnlyGitCalls: through a real shim and real git,
// Claude Code's read-only calls run (and nothing is reported), the override
// is dropped so git still sees the guardrail hooks, and a hook-running
// command with the override is still refused and reported.
func TestShimRunsClaudeCodeReadOnlyGitCalls(t *testing.T) {
	d := newFakeDaemon(t)
	r := newRig(t, func(o *Options) { o.DaemonURL, o.Token = d.srv.URL, "tok" })

	out, code := r.agent("git -c core.hooksPath=/dev/null ls-files --error-unmatch -- ':(icase)F.TXT'")
	require.Zero(t, code, out)
	assert.Equal(t, "f.txt\n", out)

	out, code = r.agent("git -c core.hooksPath=/dev/null ls-files --error-unmatch -- ':(icase).claude/settings.local.json'")
	assert.Equal(t, 1, code, "git's own answer: the file is not tracked: %s", out)
	assert.Contains(t, out, "did not match any file")
	assert.NotContains(t, out, "guardrails:")

	out, code = r.agent("git -c core.hooksPath=/dev/null rev-parse --git-path hooks")
	require.Zero(t, code, out)
	assert.Equal(t, filepath.Clean(r.rt.HooksDir()), filepath.Clean(strings.TrimSpace(out)), "the override never reached git")

	// Claude Code's other calls: status, remote and config reads.
	for _, script := range []string{
		"git -c core.hooksPath=/dev/null -c core.fsmonitor= --no-optional-locks status --short",
		"git -c core.hooksPath=/dev/null remote -v",
		"git -c core.hooksPath=/dev/null config --local --includes -z --list",
	} {
		out, code := r.agent(script)
		require.Zero(t, code, "%s: %s", script, out)
		assert.NotContains(t, out, "guardrails:", script)
	}
	out, code = r.agent("git -c core.hooksPath=/dev/null config --get core.hooksPath")
	require.Zero(t, code, out)
	assert.Equal(t, filepath.Clean(r.rt.HooksDir()), filepath.Clean(strings.TrimSpace(out)), "config reads see the guardrail hooks path")

	assert.Empty(t, d.kinds(), "an allowed call is not a refusal")

	for _, script := range []string{
		"git -c core.hooksPath=/dev/null commit --allow-empty -m x",
		"git -c core.hooksPath=/dev/null push origin HEAD",
	} {
		out, code := r.agent(script)
		assert.Equal(t, 1, code, "%s: %s", script, out)
		assert.Contains(t, out, "replacing or skipping Itervox's git hooks is refused", script)
	}
	assert.Equal(t, []string{KindHookBypass, KindHookBypass}, d.kinds())
}
