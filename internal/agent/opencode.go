package agent

import (
	"context"
)

// OpenCodeRunner runs an OpenCode turn: `opencode run --format json`, the
// prompt on stdin, its JSON event stream on stdout (opencode_events.go).
//
// Fresh turn (sessionID == nil):
//
//	<command> run --format json [--auto] < prompt
//
// Continuation:
//
//	<command> run --format json --session <id> [--auto] < prompt
//
// "run" goes right after the program, so flags in the configured command
// (`opencode --model anthropic/claude-sonnet-4-5 --agent build`) are flags
// of `opencode run`. A model is chosen with --model provider/model there.
type OpenCodeRunner struct {
	costs sessionCostLedger
}

// NewOpenCodeRunner constructs an OpenCodeRunner.
func NewOpenCodeRunner() *OpenCodeRunner {
	return &OpenCodeRunner{}
}

// ValidateOpenCodeCLICommand checks that the program the command names (or
// `opencode` for an empty command) runs `--version`. The command's own
// flags are `run` flags, so only its program is checked.
func ValidateOpenCodeCLICommand(command string) error {
	return validateCLI(programOf(command, "opencode"), "ensure 'opencode' is installed and on PATH")
}

// openCodePermissionFlags maps a permission mode onto `opencode run`.
//
// A non-interactive run cannot show a permission prompt: without --auto it
// rejects every request the configured permission rules leave at "ask"
// (the model sees the rejection and continues), with --auto it approves
// every request no rule denies. OpenCode has no filesystem sandbox, so
// sandbox mode confines the run only as far as the project's OpenCode
// permission config does (site/src/content/docs/guides/backends.mdx).
func openCodePermissionFlags(mode PermissionMode) []string {
	if mode == PermissionSandbox {
		return nil
	}
	return []string{"--auto"}
}

// openCodeArgs are the turn's arguments after `run`.
func openCodeArgs(sessionID *string, mode PermissionMode) []string {
	args := []string{"--format", "json"}
	if sessionID != nil && *sessionID != "" {
		args = append(args, "--session", *sessionID)
	}
	return append(args, openCodePermissionFlags(mode)...)
}

// openCodeTurn is the cliTurn for one OpenCode turn.
func openCodeTurn(command string, sessionID *string, mode PermissionMode, stream *openCodeStream) cliTurn {
	return cliTurn{
		name: "opencode", fallback: "opencode", command: command,
		subcommand: "run", args: openCodeArgs(sessionID, mode), parse: stream.parse,
	}
}

// RunTurn runs a single OpenCode turn (see the type comment). Session cost
// is reported cumulatively per session (CORE-091), summed from the steps'
// cost across this daemon's turns of that session.
func (r *OpenCodeRunner) RunTurn(
	ctx context.Context,
	log Logger,
	onProgress func(TurnResult),
	sessionID *string,
	prompt, workspacePath, command, workerHost, _ string,
	readTimeoutMs, turnTimeoutMs int,
	permissionMode PermissionMode,
) (TurnResult, error) {
	stream := &openCodeStream{}
	result, _, err := runCLITurn(ctx, log, onProgress, openCodeTurn(command, sessionID, permissionMode, stream),
		prompt, workspacePath, workerHost, readTimeoutMs, turnTimeoutMs)
	if result.SessionID == "" {
		result.SessionID = stream.sessionID
	}
	if result.SessionID == "" && sessionID != nil {
		result.SessionID = *sessionID
	}
	if stream.costKnown {
		c := r.costs.add(result.SessionID, stream.cost)
		result.CostUSD = &c
	}
	return result, err
}
