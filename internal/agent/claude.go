package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// ClaudeRunner spawns a real claude subprocess and streams its output.
type ClaudeRunner struct{}

// NewClaudeRunner constructs a ClaudeRunner.
func NewClaudeRunner() *ClaudeRunner {
	return &ClaudeRunner{}
}

// validateCLIShellFallback controls whether validateCLI falls back to
// spawning an interactive login shell when exec.LookPath fails. Tests flip
// this to false so they can assert the "not found" path without having the
// user's real ~/.zshrc re-introduce the tool onto PATH.
var validateCLIShellFallback = true

// validateCLI checks whether the named CLI tool is available.
//
// It first tries the inherited PATH directly (via exec.LookPath + `<name> --version`),
// which succeeds in the common case where the user ran itervox from an
// interactive shell that already has the tool on PATH.
//
// If that fails, it falls back to spawning an interactive login shell
// ("<shell> -ilc ...") so tools installed via nvm/volta/asdf/aliases in
// ~/.zshrc or ~/.bashrc are still discoverable. Note: zsh's `-l` alone does
// NOT source ~/.zshrc — `-i` is required for that.
//
// hint is appended to the "not available" error message (e.g. installation advice).
func validateCLI(name, hint string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Attempt 1: use the inherited PATH directly. Skip when name is a
	// pre-resolved absolute path — exec.CommandContext handles that fine.
	if !filepath.IsAbs(name) {
		if _, err := exec.LookPath(name); err == nil {
			if err := exec.CommandContext(ctx, name, "--version").Run(); err == nil {
				return nil
			}
			if ctx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("%s CLI validation timed out after 5s", name)
			}
		}
	} else {
		if err := exec.CommandContext(ctx, name, "--version").Run(); err == nil {
			return nil
		}
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("%s CLI validation timed out after 5s", name)
		}
	}

	if !validateCLIShellFallback {
		return fmt.Errorf("%s CLI not available: not found on PATH (%s)", name, hint)
	}

	// Attempt 2: fall back to an interactive login shell so ~/.zshrc /
	// ~/.bashrc PATH additions and shell aliases/functions are picked up.
	shell := loginShell()
	var stderr bytes.Buffer
	cmd := buildValidationShellCommand(ctx, shell, name+" --version")
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("%s CLI validation timed out after 5s", name)
		}
		if stderr.Len() > 0 {
			return fmt.Errorf("%s CLI not available: %s (stderr: %s)", name, err, strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("%s CLI not available: %s (%s)", name, err, hint)
	}
	return nil
}

func buildValidationShellCommand(ctx context.Context, shell, invocation string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, shell, "-ilc", invocation)
	// Detach the fallback login shell from the caller's controlling TTY so an
	// interactive rc file cannot steal or leave behind the foreground process
	// group just before Bubble Tea starts.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}

// ValidateClaudeCLI checks if the claude CLI is available and returns an error
// describing the problem if it cannot be found or executed.
func ValidateClaudeCLI() error {
	return validateCLI("claude", "ensure 'claude' is installed and on PATH")
}

// ValidateClaudeCLICommand is like ValidateClaudeCLI but validates a specific
// command path (e.g. an absolute path resolved from a shell alias). Falls back
// to ValidateClaudeCLI when command is empty or "claude".
func ValidateClaudeCLICommand(command string) error {
	if command == "" || command == "claude" {
		return ValidateClaudeCLI()
	}
	return validateCLI(command, "ensure 'claude' is installed and on PATH")
}

// RunTurn runs a single claude turn as a subprocess.
//
// First turn (sessionID == nil): claude -p <prompt> --output-format stream-json
// Continuation (sessionID != nil): claude --resume <sessionID> --output-format stream-json
//
// readTimeoutMs is the per-line idle deadline; if no output arrives within that
// window the turn is aborted as a stall. turnTimeoutMs is the hard wall-clock
// limit for the entire turn.
func (c *ClaudeRunner) RunTurn(
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

	var cmd *exec.Cmd
	if workerHost != "" {
		// Remote execution: SSH to host and run command in a login shell.
		// The workspace path is expected to exist on the remote host (e.g. NFS share).
		// Use -t to allocate a PTY so remote processes receive SIGHUP when SSH exits.
		shellCmd := buildShellCmd(command, sessionID, prompt)
		if logDir != "" {
			shellCmd = "export CLAUDE_CODE_LOG_DIR=" + shellQuote(logDir) + "; mkdir -p " + shellQuote(logDir) + "; " + shellCmd
		}
		if workspacePath != "" {
			shellCmd = "cd " + shellQuote(workspacePath) + " && " + shellCmd
		}
		sshArgs := []string{"-t"}
		sshArgs = append(sshArgs, sshStrictHostOption(workerHost)...)
		sshArgs = append(sshArgs, "-o", "BatchMode=yes", workerHost, "bash", "-lc", shellCmd)
		cmd = exec.CommandContext(turnCtx, "ssh", sshArgs...)
	} else if filepath.IsAbs(command) && !strings.Contains(command, " ") {
		// Clean absolute path with no flags — run the binary directly, no shell needed.
		cmd = exec.CommandContext(turnCtx, command, buildDirectArgs(sessionID, prompt)...)
	} else {
		// Bare name — wrap in login shell so PATH is resolved at runtime.
		cmd = exec.CommandContext(turnCtx, loginShell(), "-lc", buildShellCmd(command, sessionID, prompt))
	}
	setProcessGroup(cmd)
	if workspacePath != "" && workerHost == "" {
		cmd.Dir = workspacePath
	}
	if logDir != "" && workerHost == "" {
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			slog.Warn("agent: failed to create log dir", "dir", logDir, "error", err)
		}
		cmd.Env = append(os.Environ(), "CLAUDE_CODE_LOG_DIR="+logDir)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return TurnResult{Failed: true}, fmt.Errorf("agent: stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return TurnResult{Failed: true}, fmt.Errorf("agent: start: %w", err)
	}

	result, readErr := readLines(turnCtx, log, onProgress, stdout, readTimeoutMs, "claude", ParseLine)

	// Wait regardless of readErr so we don't leave zombie processes.
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

// sharedFlags are the CLI flags used by every claude invocation regardless of
// execution mode (direct binary or shell). Centralised here so adding a new
// flag only requires one edit.
const sharedFlagsStr = " --output-format stream-json --verbose --dangerously-skip-permissions"

var sharedFlagsSlice = []string{"--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"}

// safePromptArg prevents Claude's CLI argparse from interpreting a prompt
// that starts with '-' as another flag. The parser treats `-<anything>` as a
// flag even when it appears in the value slot of a preceding flag like `-p`.
// Any prompt that opens with '-' (e.g. a markdown list — "- Step 1…", or
// YAML — "- id: foo") would otherwise surface as `error: unknown option`.
// Claude trims leading whitespace in prompt content, so prepending a single
// space is observably a no-op for the agent.
func safePromptArg(prompt string) string {
	if strings.HasPrefix(prompt, "-") {
		return " " + prompt
	}
	return prompt
}

// buildDirectArgs returns CLI args for direct (non-shell) invocation.
func buildDirectArgs(sessionID *string, prompt string) []string {
	base := append([]string{}, sharedFlagsSlice...)
	if sessionID != nil && *sessionID != "" {
		args := append(base, "--resume", *sessionID)
		if prompt != "" {
			args = append(args, "-p", safePromptArg(prompt))
		}
		return args
	}
	return append(base, "-p", safePromptArg(prompt))
}

// buildShellCmd returns the full shell command string for bash/zsh -lc.
// The prompt is passed via a shell variable to avoid quoting issues with
// special characters (backticks, $, !, quotes) in the rendered template.
//
// Defensive: if command is empty or whitespace, fall back to "claude" and
// log a warning. Without this, sharedFlagsStr's leading space would produce
// " --output-format ..." which bash interprets as `--output-format` being
// the command name, surfacing as `--output-format: command not found`.
func buildShellCmd(command string, sessionID *string, prompt string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		slog.Warn("agent: empty command resolved at dispatch — falling back to 'claude'. Check WORKFLOW.md agent.command and any profile.command fields.")
		command = "claude"
	}
	base := command + sharedFlagsStr
	if sessionID != nil && *sessionID != "" {
		// Resume an existing session. When a prompt is also provided (e.g.,
		// the user's reply to an input-required question), append it with -p
		// so Claude receives the message. Without -p, Claude expects a
		// deferred-tool marker in the session and errors with "No deferred
		// tool marker found" if it's not there.
		resumed := base + " --resume " + shellQuote(*sessionID)
		if prompt != "" {
			resumed += " -p " + shellQuote(safePromptArg(prompt))
		}
		return resumed
	}
	return base + " -p " + shellQuote(safePromptArg(prompt))
}

// todoItems parses a TodoWrite input and returns the content of each todo.
func todoItems(rawInput json.RawMessage) []string {
	if len(rawInput) == 0 {
		return nil
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(rawInput, &input); err != nil {
		return nil
	}
	v, ok := input["todos"]
	if !ok {
		return nil
	}
	var todos []struct {
		Content string `json:"content"`
	}
	if json.Unmarshal(v, &todos) != nil {
		return nil
	}
	items := make([]string, 0, len(todos))
	for _, t := range todos {
		if t.Content != "" {
			items = append(items, t.Content)
		}
	}
	return items
}

// toolDescription extracts a short human-readable summary from a tool's JSON input,
// making log lines informative — e.g. "Glob — *.go in src/" instead of just "Glob".
func toolDescription(name string, rawInput json.RawMessage) string {
	if len(rawInput) == 0 {
		return ""
	}
	var input map[string]json.RawMessage
	if err := json.Unmarshal(rawInput, &input); err != nil {
		return ""
	}
	str := func(key string) string {
		v, ok := input[key]
		if !ok {
			return ""
		}
		var s string
		if json.Unmarshal(v, &s) != nil {
			return ""
		}
		return s
	}
	arrLen := func(key string) int {
		v, ok := input[key]
		if !ok {
			return 0
		}
		var items []any
		if json.Unmarshal(v, &items) != nil {
			return 0
		}
		return len(items)
	}
	trunc := func(s string, n int) string {
		if len([]rune(s)) <= n {
			return s
		}
		return string([]rune(s)[:n]) + "…"
	}
	switch strings.ToLower(name) {
	case "bash", "shell":
		cmd := trunc(str("command"), 560)
		// Include non-zero exit code in the description so it survives into the
		// INFO-level issue log buffer (the raw input is only logged at DEBUG).
		if v, ok := input["exit_code"]; ok {
			var code *int
			if json.Unmarshal(v, &code) == nil && code != nil && *code != 0 {
				return fmt.Sprintf("%s (exit:%d)", cmd, *code)
			}
		}
		// For Codex shell events, lift common single-file/directory operations
		// into a more readable form so they appear alongside Claude tool descriptions.
		if semantic := shellSemanticDesc(cmd); semantic != "" {
			return semantic
		}
		return cmd
	case "spawn_agent":
		if d := str("description"); d != "" {
			return trunc(d, 300)
		}
		return trunc(str("prompt"), 300)
	case "send_input", "resume_agent":
		return trunc(str("prompt"), 300)
	case "wait":
		if n := arrLen("receiver_thread_ids"); n > 0 {
			return fmt.Sprintf("waiting on %d sub-agent(s)", n)
		}
		return ""
	case "read":
		return str("file_path")
	case "write":
		return str("file_path")
	case "edit", "multiedit":
		return str("file_path")
	case "glob":
		p := str("pattern")
		if d := str("path"); d != "" {
			return p + " in " + d
		}
		return p
	case "grep":
		p := str("pattern")
		if d := str("path"); d != "" {
			return p + " in " + d
		}
		return p
	case "agent", "task":
		if d := str("description"); d != "" {
			return trunc(d, 300)
		}
		return trunc(str("prompt"), 200)
	case "webfetch":
		return trunc(str("url"), 200)
	case "websearch":
		return trunc(str("query"), 200)
	case "todowrite":
		var todos []struct {
			Content string `json:"content"`
		}
		if v, ok := input["todos"]; ok {
			if json.Unmarshal(v, &todos) == nil && len(todos) > 0 {
				if len(todos) == 1 {
					return trunc(todos[0].Content, 100)
				}
				return fmt.Sprintf("%d tasks: %s", len(todos), trunc(todos[0].Content, 60))
			}
		}
		return ""
	case "todoread":
		return ""
	default:
		// Fall back to first non-empty string field value.
		for _, v := range input {
			var s string
			if json.Unmarshal(v, &s) == nil && s != "" {
				return trunc(s, 120)
			}
		}
		return ""
	}
}

// logShellDetail emits an INFO action_detail line for Codex shell completions.
// This promotes exit_code, status, and output_size into the INFO-level log so
// they reach the per-issue log buffer and the web API (tool_input is DEBUG-only).
func logShellDetail(log Logger, prefix, sessionID string, input json.RawMessage) {
	var m map[string]json.RawMessage
	if json.Unmarshal(input, &m) != nil {
		return
	}
	var exitCode *int
	var status string
	var outputSize int
	if v, ok := m["exit_code"]; ok {
		_ = json.Unmarshal(v, &exitCode)
	}
	if v, ok := m["status"]; ok {
		_ = json.Unmarshal(v, &status)
	}
	if v, ok := m["output"]; ok {
		var out string
		if json.Unmarshal(v, &out) == nil {
			outputSize = len(out)
		}
	}
	// Only emit when there is meaningful detail to surface.
	if exitCode == nil && status == "" && outputSize == 0 {
		return
	}
	args := []any{"session_id", sessionID, "tool", "shell", "status", status, "output_size", outputSize}
	if exitCode != nil {
		args = append(args, "exit_code", *exitCode)
	}
	log.Info(prefix+": action_detail", args...)
}

// shellSemanticDesc maps simple single-operand shell commands to a more readable
// description. Returns "" when the command is too complex to summarise cleanly.
// This is used for Codex shell events so that common file operations surface
// with the same style as Claude tool descriptions (e.g. "cat main.go" → "main.go").
func shellSemanticDesc(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) < 2 {
		return ""
	}
	verb := filepath.Base(fields[0])
	// Skip env-var prefixes like `VAR=x cmd arg`.
	for strings.ContainsRune(fields[0], '=') && len(fields) > 1 {
		fields = fields[1:]
		verb = filepath.Base(fields[0])
	}
	// Only handle the single-operand case to avoid misclassifying pipelines.
	if strings.ContainsAny(cmd, "|&;`$(){}") {
		return ""
	}
	// Strip flags so `cat -n file.go` still maps to `file.go`.
	var operands []string
	for _, f := range fields[1:] {
		if !strings.HasPrefix(f, "-") {
			operands = append(operands, f)
		}
	}
	if len(operands) != 1 {
		return ""
	}
	operand := operands[0]
	switch verb {
	case "cat", "head", "tail", "less", "more", "bat":
		return operand
	case "ls", "find":
		return operand
	case "mkdir", "rmdir", "rm", "cp", "mv", "touch":
		return verb + " " + operand
	default:
		return ""
	}
}

// loginShell returns the user's login shell from $SHELL, falling back to bash.
// setProcessGroup configures cmd to run in its own process group and to kill
// the entire group (including child processes) when the context is cancelled.
// Without this, cancelling a "bash -lc 'codex ...'" only kills bash, not codex.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Kill the entire process group (negative PID).
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// WaitDelay ensures cmd.Wait returns promptly after Cancel fires, even if
	// child processes inherited the stdout pipe and are still alive. Without
	// this, Wait blocks until all pipe readers close — which may never happen
	// if the agent spawned background subprocesses.
	cmd.WaitDelay = 5 * time.Second
}

func loginShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "bash"
}

// shellQuote wraps s in single quotes, escaping any single quotes within it.
// This is the POSIX-safe way to pass arbitrary strings to bash -c.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// readLines reads stream-json lines from r, accumulating a TurnResult.
// log is used for INFO-level output (assistant messages, turn result) so that
// Claude's live activity appears in the log stream with the caller's context.
// logPrefix is the backend name used in log messages (e.g. "claude", "codex").
// Returns on EOF, context cancellation, or readTimeoutMs idle expiry.
func readLines(ctx context.Context, log Logger, onProgress func(TurnResult), r io.Reader, readTimeoutMs int, logPrefix string, parseFn func([]byte) (StreamEvent, error)) (TurnResult, error) {
	type scanResult struct {
		line []byte
		err  error
		done bool
	}
	lineCh := make(chan scanResult, 1)
	// done is closed when readLines returns (for any reason) so the scanner
	// goroutine can unblock from its channel send and exit promptly. Without
	// this, a context-cancel while the goroutine is blocked on lineCh <- ...
	// would leak the goroutine until the underlying pipe closes independently
	// (particularly visible in SSH-hosted worker mode).
	done := make(chan struct{})
	defer close(done)

	go func() {
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB max line to handle large prompts
		for scanner.Scan() {
			b := make([]byte, len(scanner.Bytes()))
			copy(b, scanner.Bytes())
			select {
			case lineCh <- scanResult{line: b}:
			case <-done:
				return
			}
		}
		select {
		case lineCh <- scanResult{done: true, err: scanner.Err()}:
		case <-done:
		}
	}()

	readDeadline := time.Duration(readTimeoutMs) * time.Millisecond
	var result TurnResult

	for {
		timer := time.NewTimer(readDeadline)
		select {
		case <-ctx.Done():
			timer.Stop()
			return result, ctx.Err()

		case <-timer.C:
			return result, fmt.Errorf("agent: read timeout after %dms idle", readTimeoutMs)

		case sr := <-lineCh:
			timer.Stop()
			if sr.done {
				return result, sr.err
			}
			ev, err := parseFn(sr.line)
			if err != nil {
				slog.Debug("agent: raw line", "data", string(sr.line))
				continue
			}
			switch ev.Type {
			case "assistant":
				for _, text := range ev.TextBlocks {
					log.Info(logPrefix+": text", "session_id", ev.SessionID, "text", text)
				}
				for _, tc := range ev.ToolCalls {
					desc := toolDescription(tc.Name, tc.Input)
					nameLower := strings.ToLower(tc.Name)
					if ev.InProgress {
						// item.started: log as action_started so operators can see
						// long-running shell/collab work immediately, not only on completion.
						log.Info(logPrefix+": action_started", "session_id", ev.SessionID, "tool", tc.Name, "description", desc)
					} else if nameLower == "agent" || nameLower == "task" || nameLower == "spawn_agent" {
						log.Info(logPrefix+": subagent", "session_id", ev.SessionID, "tool", tc.Name, "description", desc)
					} else {
						log.Info(logPrefix+": action", "session_id", ev.SessionID, "tool", tc.Name, "description", desc)
						if nameLower == "shell" {
							// Surface exit_code / status / output_size at INFO level so
							// the per-issue log buffer (which only stores INFO+) captures them.
							logShellDetail(log, logPrefix, ev.SessionID, tc.Input)
						}
					}
					if nameLower == "todowrite" && !ev.InProgress {
						for _, item := range todoItems(tc.Input) {
							log.Info(logPrefix+": todo", "session_id", ev.SessionID, "task", item)
						}
					}
					log.Debug(logPrefix+": tool_input", "session_id", ev.SessionID, "tool", tc.Name, "input", string(tc.Input))
				}
			case "result":
				if ev.IsError {
					log.Warn(logPrefix+": result error", "session_id", ev.SessionID, "text", ev.ResultText)
				} else {
					log.Info(logPrefix+": turn done", "session_id", ev.SessionID,
						"input_tokens", ev.Usage.InputTokens, "output_tokens", ev.Usage.OutputTokens)
				}
			case "system":
				log.Info(logPrefix+": session started", "session_id", ev.SessionID)
			}
			result = ApplyEvent(result, ev)
			// Only broadcast progress for meaningful state changes.
			// InProgress events (item.started) just log action_started and don't
			// advance the turn result; broadcasting them causes spurious dashboard churn.
			if onProgress != nil && (ev.Type == "assistant" || ev.Type == "system") && !ev.InProgress {
				onProgress(result)
			}
		}
	}
}
