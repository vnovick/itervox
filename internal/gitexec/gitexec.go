// Package gitexec is the single place itervox builds a `git` subprocess.
//
// Why it exists: git reads a family of environment variables that say WHICH
// repository to operate on (GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE, …), and
// those variables beat both `cmd.Dir` and `git -C <dir>`. Git exports several
// of them into the environment of every hook it runs. So when itervox — or
// its test suite — runs under a git hook (lefthook pre-push running
// `make test`, `git rebase -x`, a post-checkout hook starting the daemon), an
// unscrubbed `git worktree add` / `git commit` / `git branch -m` aimed at a
// temporary or per-issue repository silently operates on the ENCLOSING
// repository instead. That is not hypothetical: it once set core.bare=true,
// renamed a branch and wrote dozens of commits into a developer's checkout.
//
// Every git invocation must therefore go through [Command], which strips the
// repository-selecting variables and requires an explicit working directory.
// An AST audit test (TestNoRawGitExecOutsideGitexec) fails the build if a
// file anywhere under cmd/ or internal/ calls exec.Command*("git", …)
// directly.
package gitexec

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// repoLocationVars are the variables that make git operate on a repository
// other than the one discovered from the working directory.
//
// The first block is exactly `git rev-parse --local-env-vars` (git 2.50) —
// the set git itself clears when it crosses into a different repository (for
// example when recursing into a submodule). The second block adds the
// discovery and namespace variables from the ENVIRONMENT section of
// `git help git` that also change which repository or which refs a command
// sees, plus GIT_QUARANTINE_PATH which git sets for pre-receive hooks.
//
// Deliberately NOT scrubbed: authentication and transport (GIT_SSH,
// GIT_SSH_COMMAND, GIT_ASKPASS, GIT_TERMINAL_PROMPT), identity
// (GIT_AUTHOR_*, GIT_COMMITTER_*), user-level config location
// (GIT_CONFIG_GLOBAL, GIT_CONFIG_SYSTEM, GIT_CONFIG_NOSYSTEM), editors,
// pagers and tracing. None of those select a repository.
var repoLocationVars = []string{
	// git rev-parse --local-env-vars
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_CONFIG",
	"GIT_CONFIG_PARAMETERS",
	"GIT_CONFIG_COUNT",
	"GIT_OBJECT_DIRECTORY",
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_IMPLICIT_WORK_TREE",
	"GIT_GRAFT_FILE",
	"GIT_INDEX_FILE",
	"GIT_NO_REPLACE_OBJECTS",
	"GIT_REPLACE_REF_BASE",
	"GIT_PREFIX",
	"GIT_SHALLOW_FILE",
	"GIT_COMMON_DIR",
	// git help git, ENVIRONMENT: repository discovery / namespacing.
	"GIT_CEILING_DIRECTORIES",
	"GIT_DISCOVERY_ACROSS_FILESYSTEM",
	"GIT_NAMESPACE",
	"GIT_INDEX_VERSION",
	"GIT_ATTR_SOURCE",
	"GIT_QUARANTINE_PATH",
}

// repoLocationPrefixes cover the numbered companions of GIT_CONFIG_COUNT
// (GIT_CONFIG_KEY_<n>, GIT_CONFIG_VALUE_<n>). Dropping the count without the
// pairs is harmless, but a stray pair is repository-scoped config injected by
// the parent `git -c`, so both go.
var repoLocationPrefixes = []string{"GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_"}

// errNoDir is returned by Start/Run/Output when [Command] is given no
// directory. Discovering the repository from the daemon's own working
// directory is exactly the ambiguity this package exists to remove.
var errNoDir = errors.New("gitexec: git command requires an explicit working directory")

// IsRepoLocationVar reports whether key (a variable NAME, not "NAME=value")
// is one of the variables that redirect git to another repository.
func IsRepoLocationVar(key string) bool {
	for _, v := range repoLocationVars {
		if key == v {
			return true
		}
	}
	for _, p := range repoLocationPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// Scrub returns a copy of env ("NAME=value" entries) with every
// repository-location variable removed. Everything else, including git auth
// variables, is kept in order.
func Scrub(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if IsRepoLocationVar(name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Environ returns the current process environment, scrubbed, with extra
// appended. Use it for any subprocess that may itself run git — hooks, agent
// CLIs, `gh` — so an inherited GIT_DIR cannot reach them either.
func Environ(extra ...string) []string {
	return append(Scrub(os.Environ()), extra...)
}

// Command returns an *exec.Cmd for `git args...` run in dir with a scrubbed
// environment. dir must be non-empty; an empty dir yields a command whose
// Start/Run/Output fail with an error rather than silently using the
// daemon's working directory.
//
// Callers that need extra variables append to cmd.Env (never replace it with
// os.Environ(), which would reintroduce the inherited variables).
func Command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = Environ()
	cmd.Dir = dir
	if dir == "" {
		cmd.Err = errNoDir
	}
	return cmd
}

// UnsetInProcess removes every repository-location variable from the
// current process environment. It is the belt to [Command]'s braces: the
// daemon calls it at startup and git-invoking test packages call it from
// TestMain, so a subprocess built without this package (or a future one)
// still cannot inherit a redirect.
func UnsetInProcess() {
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if IsRepoLocationVar(name) {
			_ = os.Unsetenv(name)
		}
	}
}
