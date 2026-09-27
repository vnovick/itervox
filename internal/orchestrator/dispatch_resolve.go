package orchestrator

import (
	"fmt"
	"path/filepath"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
)

// dispatchTargetInput is everything the dispatch resolver needs, already
// snapshotted by the caller (cfg read under cfgMu, per-issue overrides under
// their own locks). The resolver itself reads nothing shared.
type dispatchTargetInput struct {
	DefaultCommand string // cfg.Agent.Command
	DefaultBackend string // cfg.Agent.Backend
	Profile        *config.AgentProfile
	// IssueBackend is the per-issue backend: the operator pin when set,
	// otherwise the rate_limited auto-switch (issueBackendForDispatch
	// decides that precedence once).
	IssueBackend string
	// RecoveryBackend is a rate_limited rule's switch_to_backend for the
	// recovery run itself; it is the final override.
	RecoveryBackend string
}

// dispatchTarget is the effective (command, runnerCommand, backend) triple
// plus, when a requested backend was refused, the reason the caller logs.
type dispatchTarget struct {
	Command       string // the command as configured (no backend hint)
	RunnerCommand string // the command MultiRunner receives (may carry a hint)
	Backend       string // the backend MultiRunner will select
	Reason        string // non-empty when a requested backend was refused
}

// commandBinaryBackend returns "claude" or "codex" when the command's first
// real token (after any backend hint, env assignments and an `env` prefix)
// is that binary, and "" for anything else — an opaque wrapper script, npx,
// a shell. Unlike agent.BackendFromCommand it ignores the hint: it answers
// "what will actually execute".
func commandBinaryBackend(command string) string {
	if hinted, rest := config.ParseBackendHint(command); hinted != "" {
		command = rest
	}
	first := config.FirstCommandToken(command)
	if first == "" {
		return ""
	}
	if base := filepath.Base(first); config.IsSupportedBackend(base) {
		return base
	}
	return ""
}

// stripBackendHint returns command without a leading backend hint.
func stripBackendHint(command string) string {
	if hinted, rest := config.ParseBackendHint(command); hinted != "" {
		return rest
	}
	return command
}

// resolveDispatchTarget is the single decision point for which backend runs
// a dispatch and with which command (CORE-115). Worker dispatch, reviewer
// dispatch and automation runs (including the rate_limited recovery run)
// all call it.
//
// Resolution priority (lowest → highest):
//  1. Default command, backend derived from it.
//  2. agent.backend.
//  3. Profile.Command (replaces the command and its backend).
//  4. Profile.Backend.
//  5. The per-issue backend (operator pin, else auto-switch).
//  6. The recovery run's switch_to_backend.
//
// Steps 2 and 4–6 request a backend for the current command. A request is
// honoured when the command's binary is not recognisable (a wrapper — the
// hint is how MultiRunner learns the backend) or already matches. When the
// binary is claude and codex is requested (or the reverse) the request is
// refused: the command keeps its own backend and Reason says why. Emitting
// the pair would route e.g. "claude ..." to the Codex runner, which runs
// `claude ... exec --json`. Refusing is preferred over inventing the other
// backend's command, which would drop the configured flags and model
// (mapping to a configured per-backend command is CORE-054).
//
// Pure: no goroutines, no locks, no global reads.
func resolveDispatchTarget(in dispatchTargetInput) dispatchTarget {
	t := dispatchTarget{
		Command:       in.DefaultCommand,
		RunnerCommand: in.DefaultCommand,
		Backend:       agent.BackendFromCommand(in.DefaultCommand),
	}
	request := func(backend, source string) {
		if backend == "" {
			return
		}
		bin := commandBinaryBackend(t.Command)
		if bin != "" && bin != backend {
			t.Reason = fmt.Sprintf("%s requested backend %q but the command runs %q; kept %q",
				source, backend, bin, bin)
			t.Backend = bin
			t.RunnerCommand = stripBackendHint(t.Command)
			return
		}
		t.Backend = backend
		t.RunnerCommand = agent.CommandWithBackendHint(t.Command, backend)
	}

	request(in.DefaultBackend, "agent.backend")
	if in.Profile != nil {
		if in.Profile.Command != "" {
			t.Command = in.Profile.Command
			t.RunnerCommand = t.Command
			t.Backend = agent.BackendFromCommand(t.Command)
			t.Reason = "" // an earlier refusal concerned the replaced command
		}
		request(in.Profile.Backend, "profile backend")
	}
	request(in.IssueBackend, "per-issue backend")
	request(in.RecoveryBackend, "rate_limited switch_to_backend")
	return t
}

// resolveResumeTarget resolves the command for resuming a stored
// input-required session (CORE-115). Here the stored backend is
// authoritative, because the session id being resumed belongs to it:
//
//   - An empty stored command resolves to the profile's command, else — for
//     a non-claude backend — the backend name as its binary, else
//     agent.command (the pre-existing fallback, preserved).
//   - When the resolved command's binary is recognisable and disagrees with
//     the stored backend (a stored "claude ..." for a codex session, or an
//     empty command whose profile says claude), the backend name is used as
//     the binary instead, and Reason says why. A wrapper keeps its command
//     and gets the hint.
func resolveResumeTarget(storedCommand, storedBackend, profileCommand, defaultCommand string) dispatchTarget {
	cmd := storedCommand
	if cmd == "" {
		cmd = profileCommand
		if cmd == "" && storedBackend != "" && storedBackend != "claude" {
			cmd = storedBackend // the backend name IS the binary name
		}
		if cmd == "" {
			cmd = defaultCommand
		}
	}
	if storedBackend == "" {
		return dispatchTarget{Command: cmd, RunnerCommand: cmd, Backend: agent.BackendFromCommand(cmd)}
	}
	t := dispatchTarget{Command: stripBackendHint(cmd), Backend: storedBackend}
	if bin := commandBinaryBackend(cmd); bin != "" && bin != storedBackend && config.IsSupportedBackend(storedBackend) {
		t.Reason = fmt.Sprintf("stored %s session cannot resume with a %q command; using the %q binary",
			storedBackend, bin, storedBackend)
		t.Command = storedBackend
	}
	t.RunnerCommand = agent.CommandWithBackendHint(t.Command, storedBackend)
	return t
}
