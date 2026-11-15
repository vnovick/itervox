package agent

import (
	"context"
	"os"
	"slices"
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
// profile ran. Empty without a PathPrepend.
func (e RunEnv) shellPrefix() string {
	if e.PathPrepend == "" {
		return ""
	}
	return "PATH=" + ShellQuote(e.PathPrepend) + `:"$PATH" `
}
