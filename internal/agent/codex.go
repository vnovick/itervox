package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CodexRunner spawns a codex subprocess and streams its --json output.
type CodexRunner struct{}

// NewCodexRunner constructs a CodexRunner.
func NewCodexRunner() *CodexRunner {
	return &CodexRunner{}
}

// ValidateCodexCLI checks if the codex CLI is available and returns an error
// describing the problem if it cannot be found or executed.
func ValidateCodexCLI() error {
	return validateCLI("codex", "ensure 'codex' is installed and on PATH, or set OPENAI_API_KEY")
}

// ValidateCodexCLICommand is like ValidateCodexCLI but validates a specific
// command path. Falls back to ValidateCodexCLI when command is empty or "codex".
func ValidateCodexCLICommand(command string) error {
	if command == "" || command == "codex" {
		return ValidateCodexCLI()
	}
	return validateCLI(command, "ensure 'codex' is installed and on PATH, or set OPENAI_API_KEY")
}

// RunTurn runs a single codex turn as a subprocess.
//
// Fresh turn (sessionID == nil):
//
//	codex [-C <workspace>] exec --json --dangerously-bypass-approvals-and-sandbox <prompt>
//
// Continuation (sessionID != nil):
//
//	codex [-C <workspace>] exec resume --json --dangerously-bypass-approvals-and-sandbox <sessionID> <prompt>
func (c *CodexRunner) RunTurn(
	ctx context.Context,
	log Logger,
	onProgress func(TurnResult),
	sessionID *string,
	prompt, workspacePath, command, workerHost, logDir string,
	readTimeoutMs, turnTimeoutMs int,
) (TurnResult, error) {
	turnCtx, cancel := ctx, context.CancelFunc(func() {})
	if turnTimeoutMs > 0 {
		turnCtx, cancel = context.WithTimeout(ctx, time.Duration(turnTimeoutMs)*time.Millisecond)
	}
	defer cancel()

	// Generate a unique log filename per turn so multi-turn and multi-run logs
	// don't overwrite each other — matching Claude Code's per-session file behavior.
	logFileName := fmt.Sprintf("codex-%d.jsonl", time.Now().UnixMilli())

	var cmd *exec.Cmd
	if workerHost != "" {
		shellCmd := buildCodexShellCmd(command, sessionID, prompt, workspacePath)
		if logDir != "" {
			// Tee codex stdout to a file on the remote host so sshFetchLogs can read it later.
			shellCmd = shellCmd + " | tee " + shellQuote(filepath.Join(logDir, logFileName))
		}
		if workspacePath != "" {
			shellCmd = "cd " + shellQuote(workspacePath) + " && " + shellCmd
		}
		if logDir != "" {
			shellCmd = "mkdir -p " + shellQuote(logDir) + "; " + shellCmd
		}
		sshArgs := []string{"-t"}
		sshArgs = append(sshArgs, sshStrictHostOption(workerHost)...)
		sshArgs = append(sshArgs, "-o", "BatchMode=yes", workerHost, "bash", "-lc", shellCmd)
		cmd = exec.CommandContext(turnCtx, "ssh", sshArgs...)
	} else if filepath.IsAbs(command) && !strings.Contains(command, " ") {
		cmd = exec.CommandContext(turnCtx, command, buildCodexDirectArgs(sessionID, prompt, workspacePath)...)
	} else {
		cmd = exec.CommandContext(turnCtx, loginShell(), "-lc", buildCodexShellCmd(command, sessionID, prompt, workspacePath))
	}
	setProcessGroup(cmd)
	if workspacePath != "" && workerHost == "" {
		cmd.Dir = workspacePath
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return TurnResult{Failed: true}, fmt.Errorf("codex: stdout pipe: %w", err)
	}

	// For local workers, tee stdout to codex-session.jsonl so sshFetchLogs / parseSessionLogsMulti
	// can read the session transcript after the run.
	var logFile *os.File
	if logDir != "" && workerHost == "" {
		if mkErr := os.MkdirAll(logDir, 0o755); mkErr != nil {
			slog.Warn("codex: failed to create log dir", "dir", logDir, "error", mkErr)
		} else if f, createErr := os.Create(filepath.Join(logDir, logFileName)); createErr != nil {
			slog.Warn("codex: failed to create session log", "error", createErr)
		} else {
			logFile = f
		}
	}

	var reader io.Reader = stdout
	if logFile != nil {
		reader = io.TeeReader(stdout, logFile)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		return TurnResult{Failed: true}, fmt.Errorf("codex: start: %w", err)
	}

	result, readErr := readLines(turnCtx, log, onProgress, reader, readTimeoutMs, "codex", ParseCodexLine)
	if logFile != nil {
		_ = logFile.Close()
	}

	waitErr := cmd.Wait()
	if waitErr != nil && readErr == nil {
		result.Failed = true
	}

	// T-53: shared FailureText assembly across Claude and Codex runners.
	if result.Failed {
		result.FailureText = formatFailureText(result.FailureText, stderrBuf.String(), waitErr)
	}

	if readErr != nil {
		result.Failed = true
		return result, readErr
	}
	result.TotalTokens = result.InputTokens + result.OutputTokens
	result = FinalizeResult(result)
	return result, nil
}

func buildCodexDirectArgs(sessionID *string, prompt, workspacePath string) []string {
	// Same argv hazard as Claude: if the prompt begins with '-' the CLI
	// parser interprets it as an unexpected flag. safePromptArg prepends a
	// single space that Codex silently trims.
	safePrompt := safePromptArg(prompt)
	args := make([]string, 0, 8)
	if workspacePath != "" {
		args = append(args, "-C", workspacePath)
	}
	args = append(args, "exec")
	if sessionID != nil && *sessionID != "" {
		args = append(args, "resume", "--json", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", *sessionID, safePrompt)
		return args
	}
	args = append(args, "--json", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", safePrompt)
	return args
}

func buildCodexShellCmd(command string, sessionID *string, prompt, workspacePath string) string {
	safePrompt := safePromptArg(prompt)
	var b strings.Builder
	b.WriteString(command)
	if workspacePath != "" {
		b.WriteString(" -C ")
		b.WriteString(shellQuote(workspacePath))
	}
	b.WriteString(" exec")
	if sessionID != nil && *sessionID != "" {
		b.WriteString(" resume --json --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check ")
		b.WriteString(shellQuote(*sessionID))
		b.WriteString(" ")
		b.WriteString(shellQuote(safePrompt))
		return b.String()
	}
	b.WriteString(" --json --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check ")
	b.WriteString(shellQuote(safePrompt))
	return b.String()
}
