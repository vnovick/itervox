package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/procgroup"
)

// cliTurn describes one headless turn of an agent CLI that runs to
// completion per invocation and prints a JSON-lines event stream: the
// OpenCode, Cursor and custom-protocol runners. runCLITurn launches it on
// exactly the paths the Claude and Codex runners use — a clean absolute
// path directly, anything else through the login shell, an SSH worker
// through the remote bash wrapper — and through agentCommand, so process
// isolation (#258) covers every local launch path.
type cliTurn struct {
	// name is the backend name: the log prefix and error prefix.
	name string
	// fallback is the executable for an empty command.
	fallback string
	// command is the configured command (after any runner-specific
	// rewrite); the turn's own arguments follow it.
	command string
	// subcommand is put right after the program, before any argument the
	// configured command carries (OpenCode's "run": the command's own
	// flags are flags of that subcommand).
	subcommand string
	// args are the turn's arguments after the command. In the shell form
	// each is quoted only when it needs to be (shellWord).
	args []string
	// promptArg passes the prompt as the last argument, after "--",
	// instead of on stdin (the Cursor CLI reads it from argv only).
	promptArg bool
	// env are NAME=value entries for the agent process. Locally they are
	// added to its environment; over SSH they are exported in the script.
	env []string
	// parse maps one stdout line to a StreamEvent.
	parse func([]byte) (StreamEvent, error)
}

// shellSafeWord matches an argument bash reads literally without quotes.
var shellSafeWord = regexp.MustCompile(`^[A-Za-z0-9_./=:@%+,-]+$`)

// shellWord quotes s for bash unless it is a plain word.
func shellWord(s string) string {
	if shellSafeWord.MatchString(s) {
		return s
	}
	return ShellQuote(s)
}

// shellCmd is the turn's command line for a shell: the command, its
// arguments and promptTail — the stdin redirect (localPromptRedirect,
// remotePromptRedirect) or, for promptArg, " -- <prompt expansion>".
func (t cliTurn) shellCmd(promptTail string) string {
	command := strings.TrimSpace(t.command)
	if command == "" {
		command = t.fallback
	}
	if t.subcommand != "" {
		command = insertAfterProgram(command, t.subcommand)
	}
	var b strings.Builder
	b.WriteString(command)
	for _, a := range t.args {
		b.WriteString(" ")
		b.WriteString(shellWord(a))
	}
	b.WriteString(promptTail)
	return b.String()
}

// programOf is the program a command runs (config.FirstCommandToken), or
// fallback when it names none.
func programOf(command, fallback string) string {
	if p := config.FirstCommandToken(command); p != "" {
		return p
	}
	return fallback
}

// commandWord matches one whitespace-separated word of a command line.
var commandWord = regexp.MustCompile(`\S+`)

// insertAfterProgram returns command with word inserted after its program
// token — the first word that is not an env assignment, skipping an
// `env [-flags] [VAR=val...]` prefix (config.FirstCommandToken's rule) —
// or appended when there is none.
func insertAfterProgram(command, word string) string {
	spans := commandWord.FindAllStringIndex(command, -1)
	inEnv := false
	for _, sp := range spans {
		tok := command[sp[0]:sp[1]]
		switch {
		case config.IsEnvAssignment(tok):
			continue
		case inEnv && strings.HasPrefix(tok, "-"):
			continue
		case !inEnv && filepath.Base(tok) == "env":
			inEnv = true
			continue
		}
		return command[:sp[1]] + " " + word + command[sp[1]:]
	}
	return strings.TrimRight(command, " \t") + " " + word
}

// localPromptTail is shellCmd's prompt tail for the local login shell.
func (t cliTurn) localPromptTail() string {
	if t.promptArg {
		return " --" + localPromptArg
	}
	return localPromptRedirect
}

// remotePromptTail is shellCmd's prompt tail for the remote bash script.
func (t cliTurn) remotePromptTail() string {
	if t.promptArg {
		return ` -- "$` + remotePromptVar + `"`
	}
	return remotePromptRedirect
}

// directArgs is the argv after the executable for the direct path.
func (t cliTurn) directArgs(prompt string) []string {
	out := append([]string{}, t.args...)
	if t.promptArg {
		out = append(out, "--", prompt)
	}
	return out
}

// exportPrefix carries env across the SSH boundary.
func (t cliTurn) exportPrefix() string {
	var b strings.Builder
	for _, kv := range t.env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			continue
		}
		b.WriteString("export " + name + "=" + ShellQuote(value) + "; ")
	}
	return b.String()
}

// cliTurnOutcome is what runCLITurn learned beyond the TurnResult.
type cliTurnOutcome struct {
	// exitCode is the agent's exit status (-1 when it did not exit
	// normally: killed, never started, or the status is unknown).
	exitCode int
}

// runCLITurn runs one turn of t. Its contract matches the Claude and Codex
// runners: the turn is bounded by turnTimeoutMs (when > 0) and by an idle
// read deadline of readTimeoutMs, the process group is killed on cancel,
// a NUL in the prompt and a sandboxed SSH turn are refused before any
// process starts, and the result is finalized (FinalizeResult) on a clean
// end of stream.
func runCLITurn(ctx context.Context, log Logger, onProgress func(TurnResult), t cliTurn, prompt, workspacePath, workerHost string, readTimeoutMs, turnTimeoutMs int) (TurnResult, cliTurnOutcome, error) {
	out := cliTurnOutcome{exitCode: -1}
	if err := rejectNULPrompt(prompt); err != nil {
		return TurnResult{Failed: true, FailureText: err.Error()}, out, err
	}
	if err := refuseRemoteIsolation(ctx, workerHost); err != nil { // #258
		return TurnResult{Failed: true, FailureText: err.Error()}, out, err
	}
	// Always cancellable, so a read error can kill the subprocess at once
	// (CORE-001), with the hard turn timeout layered on when enabled.
	turnCtx, cancel := context.WithCancel(ctx)
	if turnTimeoutMs > 0 {
		var timeoutCancel context.CancelFunc
		turnCtx, timeoutCancel = context.WithTimeout(turnCtx, time.Duration(turnTimeoutMs)*time.Millisecond)
		baseCancel := cancel
		cancel = func() {
			timeoutCancel()
			baseCancel()
		}
	}
	defer cancel()

	var cmd *exec.Cmd
	var stdinFeed *remoteStdin // SSH only: the script, then held open (CORE-155)
	var promptIn *os.File      // local only: the prompt, for the CLI's stdin or fd 3 (CORE-156)
	defer func() { closePromptFile(promptIn) }()
	if workerHost != "" {
		shellCmd := remotePromptAssignment(prompt) + t.shellCmd(t.remotePromptTail())
		shellCmd = itervoxAgentExportPrefix() + t.exportPrefix() + shellCmd
		if workspacePath != "" {
			shellCmd = "cd " + ShellQuote(workspacePath) + "; " + shellCmd
		}
		// set -e (CORE-154): a failed cd aborts before the agent starts.
		shellCmd = "set -e; " + shellCmd
		// -T and the kill-on-EOF wrapper: see ClaudeRunner.RunTurn.
		sshArgs := []string{"-T"}
		sshArgs = append(sshArgs, sshStrictHostOption(workerHost)...)
		remoteArg, payload := remoteBashInvocation("-lc", shellCmd)
		sshArgs = append(sshArgs, "-o", "BatchMode=yes", workerHost, remoteArg)
		cmd = exec.CommandContext(turnCtx, "ssh", sshArgs...)
		var err error
		if stdinFeed, err = attachRemoteStdin(cmd, payload); err != nil {
			return TurnResult{Failed: true}, out, err
		}
	} else {
		shell := loginShell()
		var err error
		if name, head, direct := directInvocation(t.command, shell, t.fallback); direct {
			argv := []string{name}
			if t.subcommand != "" {
				argv = append(argv, t.subcommand)
			}
			argv = append(append(argv, head...), t.directArgs(prompt)...)
			if cmd, err = agentCommand(ctx, turnCtx, argv); err != nil { // #258: sandboxed when the run asks
				return TurnResult{Failed: true, FailureText: err.Error()}, out, err
			}
			if !t.promptArg {
				if promptIn, err = promptFile(prompt); err != nil {
					return TurnResult{Failed: true, FailureText: err.Error()}, out, err
				}
				setPromptStdin(cmd, promptIn)
			}
		} else {
			if promptIn, err = promptFile(prompt); err != nil {
				return TurnResult{Failed: true, FailureText: err.Error()}, out, err
			}
			// fd 3, not stdin: a login profile that reads stdin cannot
			// eat the prompt (see ClaudeRunner.RunTurn).
			argv := []string{shell, "-lc", RunEnvFrom(ctx).shellPrefix() + t.shellCmd(t.localPromptTail())}
			if cmd, err = agentCommand(ctx, turnCtx, argv); err != nil { // #258
				return TurnResult{Failed: true, FailureText: err.Error()}, out, err
			}
			setPromptFD3(cmd, promptIn)
		}
	}
	setProcessGroup(cmd)
	if workspacePath != "" && workerHost == "" {
		cmd.Dir = workspacePath
	}
	// Set unconditionally (see ClaudeRunner.RunTurn): a nil cmd.Env would
	// inherit the environment without the itervox marker.
	var extra []string
	if workerHost == "" {
		extra = append(RunEnvFrom(ctx).direct(), t.env...) // #204
	}
	cmd.Env = itervoxAgentEnv(extra...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return TurnResult{Failed: true}, out, fmt.Errorf("%s: stdout pipe: %w", t.name, err)
	}
	stderrBuf := newTailBuffer(maxStderrCaptureBytes) // CORE-028
	cmd.Stderr = stderrBuf

	if err := procgroup.Start(cmd); err != nil {
		return TurnResult{Failed: true}, out, fmt.Errorf("%s: start: %w", t.name, err)
	}
	stdinFeed.start()
	if workerHost == "" {
		defer procgroup.Track(ctx, cmd)() // CORE-042
	}

	result, readErr := readLines(turnCtx, log, onProgress, stdout, readTimeoutMs, t.name, t.parse)
	if readErr != nil {
		// Abnormal end of stream: kill now so Wait below is bounded.
		cancel()
		result.Failed = true
	}
	waitErr := cmd.Wait()
	stdinFeed.join()
	if waitErr != nil && readErr == nil {
		result.Failed = true
	}
	var exitErr *exec.ExitError
	switch {
	case waitErr == nil:
		out.exitCode = 0
	case errors.As(waitErr, &exitErr):
		out.exitCode = exitErr.ExitCode()
	}
	if result.Failed {
		result.FailureText = resolveFailureText(result.FailureText, stderrBuf.String(), waitErr, readErr)
	}
	if readErr != nil {
		return result, out, readErr
	}
	result.TotalTokens = result.InputTokens + result.OutputTokens
	return FinalizeResult(result), out, nil
}
