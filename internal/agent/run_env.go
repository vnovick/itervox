package agent

import (
	"context"
	"os"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/procgroup"
)

// RunEnv is extra environment for a run's local agent turns (#204: the
// guardrail shims and hooks). It travels in the turn's context so the runner
// can apply it the right way for how it starts the CLI, without changing
// how it starts it: a bare absolute command still runs without a shell.
type RunEnv struct {
	// Vars are NAME=value entries added to the agent's environment (later
	// entries win over the inherited environment).
	Vars []string
	// PathPrepend is put first on the agent's PATH. Through a login shell it
	// is prepended after the profile ran, so the profile's own PATH setup
	// (which may replace PATH outright) keeps it first.
	PathPrepend string
	// Unset are variable names removed from the agent's environment (push:
	// daemon strips forge and tracker credentials). Through a login shell
	// they are removed again after the profile ran (`unset`), so a profile
	// that exports a token does not hand it back. Names must be shell
	// identifiers; others are ignored.
	Unset []string
	// Set are NAME=value entries that must reach the CLI as given, even
	// through a login shell whose profile exports the same name: directly
	// they join the environment like Vars, and through a shell they are
	// exported again after the profile ran (`export NAME='value'`). A profile's
	// model_server is set this way. Names must be shell identifiers; others
	// are ignored.
	Set []string
}

type runEnvKey struct{}

// WithRunEnv returns ctx carrying env for the runner.
func WithRunEnv(ctx context.Context, env RunEnv) context.Context {
	return context.WithValue(ctx, runEnvKey{}, env)
}

// WithSetEnv returns ctx whose RunEnv also has assignment (NAME=value) in
// its Set list; ctx unchanged for an empty assignment.
func WithSetEnv(ctx context.Context, assignment string) context.Context {
	if assignment == "" {
		return ctx
	}
	env := RunEnvFrom(ctx)
	env.Set = append(slices.Clone(env.Set), assignment)
	return WithRunEnv(ctx, env)
}

// RunEnvFrom returns the RunEnv ctx carries (zero when none).
func RunEnvFrom(ctx context.Context) RunEnv {
	env, _ := ctx.Value(runEnvKey{}).(RunEnv)
	return env
}

// strip removes e.Unset from env (an agent turn's cmd.Env).
func (e RunEnv) strip(env []string) []string {
	if len(e.Unset) == 0 {
		return env
	}
	return slices.DeleteFunc(env, func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(e.Unset, name)
	})
}

// unsetPrefix is `unset NAME … || exit 1; export NAME='value' || exit 1; `
// for the valid names in e.Unset and e.Set ("" when none). Statements of
// their own, not `env -u NAME NAME=value` in front of the command: a
// prefix applies to the command line's first command only, so a profile
// command such as `source ~/.nvm/nvm.sh && claude`, `cd x && claude`,
// `exec claude` or a shell function would either fail (`env: 'source': No
// such file or directory`) or start the CLI without the assignment. Each
// statement ends the turn when it fails (a read-only variable; a login
// shell without `unset`, such as fish), so a credential that cannot be
// removed or a model server that cannot be set never reaches a CLI that
// then runs without it (fail closed).
func (e RunEnv) unsetPrefix() string {
	var b strings.Builder
	var names []string
	for _, name := range e.Unset {
		if isEnvName(name) {
			names = append(names, name)
		}
	}
	if len(names) > 0 {
		b.WriteString("unset " + strings.Join(names, " ") + " || exit 1; ")
	}
	for _, kv := range e.validSet() {
		b.WriteString(ExportStatement(kv))
	}
	return b.String()
}

// ExportStatement is `export NAME='value' || exit 1; ` for a NAME=value
// assignment: a statement of its own in front of a shell command line, so
// the variable reaches every command of a compound line (see unsetPrefix),
// and the line ends when it cannot be set. The value is ShellQuote'd.
func ExportStatement(assignment string) string {
	name, value, _ := strings.Cut(assignment, "=")
	return "export " + name + "=" + ShellQuote(value) + " || exit 1; "
}

// validSet returns the entries of e.Set whose name is a shell identifier.
func (e RunEnv) validSet() []string {
	var out []string
	for _, kv := range e.Set {
		if name, _, ok := strings.Cut(kv, "="); ok && isEnvName(name) {
			out = append(out, kv)
		}
	}
	return out
}

// isEnvName reports whether name is a portable environment variable name.
func isEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// direct returns the entries for a CLI started without a shell.
func (e RunEnv) direct() []string {
	out := append(slices.Clone(e.Vars), e.validSet()...)
	if e.PathPrepend != "" {
		out = append(out, "PATH="+e.PathPrepend+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return out
}

// shellPrefix returns the statements to put in front of a shell command
// line: PATH exported with PathPrepend first, expanded by that shell after
// its profile ran, then unsetPrefix's Unset and Set statements. Statements,
// not assignments in front of the command, so they hold for every command
// of a compound line. Empty without any.
func (e RunEnv) shellPrefix() string {
	if e.PathPrepend == "" {
		return e.unsetPrefix()
	}
	return "export PATH=" + ShellQuote(procgroup.ShellPathList(e.PathPrepend)) + `:"$PATH"; ` + e.unsetPrefix()
}
