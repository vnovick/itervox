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
	// they are removed again after the profile ran (`env -u`), so a profile
	// that exports a token does not hand it back. Names must be shell
	// identifiers; others are ignored.
	Unset []string
}

type runEnvKey struct{}

// WithRunEnv returns ctx carrying env for the runner.
func WithRunEnv(ctx context.Context, env RunEnv) context.Context {
	return context.WithValue(ctx, runEnvKey{}, env)
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

// unsetPrefix is `env -u NAME … ` for the valid names in e.Unset ("" when
// none). env, not the shell's unset builtin: it works the same under every
// login shell, and fails closed (the command does not start) where a
// command line is not a single command.
func (e RunEnv) unsetPrefix() string {
	var b strings.Builder
	for _, name := range e.Unset {
		if !isEnvName(name) {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("env")
		}
		b.WriteString(" -u " + name)
	}
	if b.Len() == 0 {
		return ""
	}
	return b.String() + " "
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
	out := slices.Clone(e.Vars)
	if e.PathPrepend != "" {
		out = append(out, "PATH="+e.PathPrepend+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return out
}

// shellPrefix returns the assignment to put in front of a shell command
// line: PATH with PathPrepend first, expanded by that shell after its
// profile ran, then `env -u` for Unset. Empty without either.
func (e RunEnv) shellPrefix() string {
	if e.PathPrepend == "" {
		return e.unsetPrefix()
	}
	return "PATH=" + ShellQuote(procgroup.ShellPathList(e.PathPrepend)) + `:"$PATH" ` + e.unsetPrefix()
}
