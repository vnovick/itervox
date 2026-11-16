package guardrails

import (
	"slices"
	"strings"
)

// Read-only git calls with a core.hooksPath override.
//
// Claude Code runs its own read-only git calls with hooks disabled
// (`git -c core.hooksPath=/dev/null … ls-files --error-unmatch -- …`).
// Refusing every override as a hook_bypass refused all of them. A git
// builtin that runs no hook cannot be used to skip one, so for the builtins
// in hookFreeGitBuiltins, and the read-only forms of `remote` and `config`,
// the override is let through. `status`, `diff` and `describe`
// (hookRunningReadCommands) may run post-index-change; they are let through
// too because the override is dropped before git runs, so their hooks run as
// configured. Everything else is still refused. Rules:
//
//   - The subcommand is found by parsing git's global options exactly as
//     git.c's handle_options does (git 2.43; checked against real git): a
//     global option is matched exactly, never abbreviated (`--no-pag` and
//     `--git-d=` are "unknown option"); `-c` and `-C` take their value as
//     the next argument only (`-ccore.x=y` and `-C.` are unknown options).
//     Any option this parser does not know, and the ones that change what
//     runs (`--exec-path`, `--super-prefix`, `--list-cmds`, `-h`/`--help`,
//     which runs `git help <word>`), refuse the override: fail closed.
//   - The subcommand must be exactly one of hookFreeGitBuiltins. Builtins
//     cannot be shadowed: git runs a builtin before it looks at aliases
//     ("aliases that hide existing Git commands are ignored", git-config(1)
//     alias.*; `git -c alias.ls-files='!echo x' ls-files` lists files), and
//     help.autocorrect only applies to unknown commands. The match is case
//     sensitive (git's is: `git LS-FILES` is "not a git command"), so a
//     case-insensitive file system cannot turn another spelling into an
//     external git-ls-files. Deprecated builtins (`whatchanged`) are left
//     out: newer git documents that an alias may replace a deprecated
//     command.
//   - With an override, `-c alias.*`, `-c include.path` and
//     `-c includeIf.*.path` (and their --config-env forms) are refused
//     anywhere in the global options, belt and braces.
//   - The override is dropped from the forwarded command (forwardArgs): the
//     builtin runs the same, and a child process it starts (a pager, an
//     external diff or textconv program) inherits the guardrail hooks
//     rather than the override, even if it calls git by absolute path.
//
// Every entry below was run against real git 2.43 with core.hooksPath
// pointing at a directory holding every hook git knows, each logging its
// call, in a repository whose files had fresh mtimes (so a refresh would
// write the index): none of them ran a hook. `status`, `diff` and
// `describe --dirty` did (post-index-change, when they refresh and write
// the index; `diff` even with --no-optional-locks), so they are not here
// but in hookRunningReadCommands, where dropping the override keeps their
// hooks running.

// hookFreeGitBuiltins are the git builtins that read the repository and run
// no hook (no index write, no ref update, no commit, no checkout, no
// fetch or push). Keep sorted.
var hookFreeGitBuiltins = []string{
	"blame",
	"cat-file",
	"check-attr",
	"check-ignore",
	"check-ref-format",
	"cherry",
	"count-objects",
	"diff-files",
	"diff-index",
	"diff-tree",
	"for-each-ref",
	"grep",
	"log",
	"ls-files",
	"ls-tree",
	"merge-base",
	"name-rev",
	"rev-list",
	"rev-parse",
	"shortlog",
	"show",
	"show-branch",
	"show-ref",
	"var",
	"version",
}

// gitFlagGlobals are git's global options that take no value and do not
// change which command runs.
var gitFlagGlobals = []string{
	"-p", "--paginate", "-P", "--no-pager", "--bare", "--no-replace-objects",
	"--no-lazy-fetch", "--no-optional-locks", "--no-advice",
	"--literal-pathspecs", "--no-literal-pathspecs", "--glob-pathspecs",
	"--noglob-pathspecs", "--icase-pathspecs",
}

// gitValueGlobals are git's global options that take a value, as the next
// argument or after `=`, and do not change which command runs.
var gitValueGlobals = []string{"--git-dir", "--work-tree", "--namespace", "--attr-source"}

// readOnlyHooksOverride reports whether args (git's arguments, after
// `git`) override core.hooksPath only for a builtin that runs no hook, and
// which arguments are the override (to drop). ok is false whenever anything
// is unknown or ambiguous. A command line that ends inside the global
// options runs no command at all (git prints its usage or an error), so it
// is ok.
func readOnlyHooksOverride(args []string) (drop []int, ok bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-c" || a == "--config-env":
			if i+1 >= len(args) {
				return drop, true
			}
			if isCoreHooksPath(args[i+1]) {
				drop = append(drop, i, i+1)
			} else if !overrideCompanionConfigOK(args[i+1]) {
				return nil, false
			}
			i++
		case strings.HasPrefix(a, "--config-env="):
			kv := strings.TrimPrefix(a, "--config-env=")
			if isCoreHooksPath(kv) {
				drop = append(drop, i)
			} else if !overrideCompanionConfigOK(kv) {
				return nil, false
			}
		case a == "-C" || slices.Contains(gitValueGlobals, a):
			if i+1 >= len(args) {
				return drop, true
			}
			i++
		case slices.Contains(gitFlagGlobals, a) || hasValueGlobalPrefix(a):
		case a == "-v" || a == "--version":
			return drop, true // git runs `git version`
		case strings.HasPrefix(a, "-"):
			return nil, false // unknown, or changes what runs
		default:
			return drop, overrideAllowedFor(a, args[i+1:])
		}
	}
	return drop, true
}

// overrideAllowedFor reports whether a hooksPath override may be dropped
// and the command run: a hook-free builtin, a read-only form of `remote` or
// `config`, or one of hookRunningReadCommands.
func overrideAllowedFor(sub string, rest []string) bool {
	if _, found := slices.BinarySearch(hookFreeGitBuiltins, sub); found {
		return true
	}
	switch sub {
	case "remote":
		return readOnlyRemote(rest)
	case "config":
		return readOnlyConfig(rest)
	}
	return slices.Contains(hookRunningReadCommands, sub)
}

// hookRunningReadCommands read the repository but may run post-index-change
// when they refresh the index. The override is dropped from what the shim
// forwards (forwardArgs), so git runs them with the guardrail hooks path and
// every hook runs as configured: the agent's request to skip hooks is not
// honoured, and nothing is skipped. Claude Code runs `status --short` this
// way on every turn.
var hookRunningReadCommands = []string{"describe", "diff", "status"}

// readOnlyRemote: `git remote`, `git remote -v` and `git remote get-url …`
// read the configured remotes and run no hook; any other form (add, remove,
// set-url, prune, update, show, which may contact the remote) is refused.
func readOnlyRemote(rest []string) bool {
	switch {
	case len(rest) == 0:
		return true
	case len(rest) == 1 && (rest[0] == "-v" || rest[0] == "--verbose"):
		return true
	default:
		return rest[0] == "get-url"
	}
}

// configReadActions select a read; configWriteActions change the config.
var (
	configReadActions = []string{"--get", "--get-all", "--get-regexp", "--get-urlmatch",
		"--get-color", "--get-colorbool", "--list", "-l"}
	configWriteActions = []string{"--add", "--replace-all", "--unset", "--unset-all",
		"--rename-section", "--remove-section", "--edit", "-e"}
	// configValueFlags take their value as the next argument (or after `=`).
	configValueFlags = []string{"-f", "--file", "--blob", "--type", "--default"}
	// configPlainFlags change how a read is scoped or printed.
	configPlainFlags = []string{"--local", "--global", "--system", "--worktree",
		"--includes", "--no-includes", "-z", "--null", "--name-only",
		"--show-origin", "--show-scope", "--fixed-value",
		"--bool", "--int", "--bool-or-int", "--path", "--expiry-date", "--no-type"}
)

// readOnlyConfig reports whether a `git config` command line only reads:
// an explicit read action (`--get`, `--list`, …), the `get`/`list`
// subcommands of newer git, or one key and no value (`git config
// user.name`). A write action, a `set`/`unset`/… subcommand, a key with a
// value, or any option not listed here is refused (fail closed): a config
// write could, for one, set core.hooksPath for good.
func readOnlyConfig(rest []string) bool {
	var positional []string
	read := false
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		name, _, hasValue := strings.Cut(a, "=")
		switch {
		case slices.Contains(configWriteActions, name):
			return false
		case slices.Contains(configReadActions, name):
			read = true
		case slices.Contains(configValueFlags, name):
			if !hasValue {
				i++ // its value
			}
		case slices.Contains(configPlainFlags, name):
		case strings.HasPrefix(a, "-"):
			return false
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) > 0 && !read {
		switch positional[0] {
		case "get", "list":
			return true
		case "set", "unset", "rename-section", "remove-section", "edit":
			return false
		}
	}
	return read || len(positional) == 1
}

func hasValueGlobalPrefix(a string) bool {
	name, _, ok := strings.Cut(a, "=")
	return ok && slices.Contains(gitValueGlobals, name)
}

// isCoreHooksPath reports whether a `-c`/`--config-env` assignment sets
// core.hooksPath itself (case-insensitive, as git's section and key names
// are).
func isCoreHooksPath(kv string) bool {
	key, _, _ := strings.Cut(kv, "=")
	return strings.EqualFold(strings.TrimSpace(key), "core.hooksPath")
}

// overrideCompanionConfigOK reports whether another `-c` assignment may
// sit next to a hooksPath override: not an alias and not a config include.
func overrideCompanionConfigOK(kv string) bool {
	if isHooksPathAssignment(kv) {
		return false // include.path, includeIf.*.path
	}
	key, _, _ := strings.Cut(kv, "=")
	section, _, _ := strings.Cut(strings.TrimSpace(key), ".")
	return !strings.EqualFold(section, "alias")
}

// forwardArgs is what the shim hands real git: args, without the
// core.hooksPath override when d let one through.
func forwardArgs(d decision, args []string) []string {
	if !d.dropHooksPath {
		return args
	}
	drop, _ := readOnlyHooksOverride(args) // ok: decide said so for these args
	skip := make([]bool, len(args))
	for _, i := range drop {
		skip[i] = true
	}
	out := make([]string, 0, len(args)-len(drop))
	for i, a := range args {
		if !skip[i] {
			out = append(out, a)
		}
	}
	return out
}
