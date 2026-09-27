package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/vnovick/itervox/internal/procgroup"
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
	// Bounded stderr (fix round 1, M7): an rc file can print arbitrarily
	// much, and only the tail explains a failure.
	stderr := newTailBuffer(maxValidationStderrBytes)
	cmd := buildValidationShellCommand(ctx, shell, name+" --version")
	cmd.Stderr = stderr

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

// maxValidationStderrBytes bounds the fallback login shell's stderr kept
// for validateCLI's error message.
const maxValidationStderrBytes = 4 << 10

// validationWaitDelay bounds how long validateCLI's fallback waits on the
// shell's stderr pipe after the shell exits (or after the 5s context kills
// its group). Without it, a background process started by an rc file that
// inherited stderr held cmd.Wait — and daemon startup — open until that
// process exited (fix round 1, M7).
const validationWaitDelay = 2 * time.Second

func buildValidationShellCommand(ctx context.Context, shell, invocation string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, shell, "-ilc", invocation)
	// Group kill on the 5s deadline plus a WaitDelay (M7). Configure sets
	// Setpgid; it is replaced below by Setsid, which ALSO makes the shell
	// the leader of a new process group (pgid == pid), so procgroup's
	// Cancel — Kill(cmd.Process.Pid) — still reaches the whole group.
	// Setsid and Setpgid cannot be combined: setpgid after setsid fails
	// with EPERM for a session leader.
	procgroup.Configure(cmd, validationWaitDelay)
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
// First turn (sessionID == nil): claude --output-format stream-json ... -p
// Continuation (sessionID != nil): claude --output-format stream-json ... --resume <sessionID> [-p]
//
// The prompt is never an argument (CORE-156): with -p and no prompt
// argument, claude reads the prompt from its stdin until EOF (see
// promptFile and remotePromptRedirect).
//
// readTimeoutMs is the per-line idle deadline; if no output arrives within
// that window — or the stream ends abnormally, e.g. a read error from a terminal line
// over the 16 MiB maxStreamLineBytes cap — RunTurn cancels the turn context, kills the subprocess
// (its whole process group), and returns a failed TurnResult with a
// non-empty FailureText. This is independent of, and faster than, the
// orchestrator's separate stall detection. turnTimeoutMs is the hard
// wall-clock limit for the entire turn.
func (c *ClaudeRunner) RunTurn(
	ctx context.Context,
	log Logger,
	onProgress func(TurnResult),
	sessionID *string,
	prompt, workspacePath, command, workerHost, logDir string,
	readTimeoutMs, turnTimeoutMs int,
	permissionMode PermissionMode,
) (TurnResult, error) {
	if err := rejectNULPrompt(prompt); err != nil {
		return TurnResult{Failed: true, FailureText: err.Error()}, err
	}
	// turnCtx is always cancellable, even when the hard turn timeout is
	// disabled (turnTimeoutMs <= 0): on a read error we must be able to kill
	// the subprocess immediately rather than relying on a no-op cancel that
	// leaves it running until the caller's ctx eventually ends (CORE-001).
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
	var promptIn *os.File      // local only: the prompt, for the CLI's stdin (CORE-156)
	defer func() { closePromptFile(promptIn) }()
	if workerHost != "" {
		// Remote execution: SSH to host and run command in a login shell.
		// The workspace path is expected to exist on the remote host (e.g. NFS share).
		// -T: never allocate a PTY (round 3, m3). ssh's stdin is the script
		// pipe, which must not pass through a terminal line discipline (echo,
		// CRLF); -T also overrides an ssh_config `RequestTTY force`. Without a
		// PTY a cancelled turn does not SIGHUP the remote agent. CORE-155:
		// the remote wrapper (remoteBashInvocation) kills the agent's process
		// group when ssh's stdin reaches EOF, and attachRemoteStdin keeps that
		// stdin open until ssh exits — so the CORE-001 group kill of the local
		// ssh client on cancel/shutdown is what stops the remote agent.
		shellCmd := remotePromptAssignment(prompt) + buildShellCmd(command, sessionID, prompt, remotePromptRedirect, permissionMode)
		shellCmd = itervoxAgentExportPrefix() + shellCmd
		if logDir != "" {
			shellCmd = "export CLAUDE_CODE_LOG_DIR=" + ShellQuote(logDir) + "; mkdir -p " + ShellQuote(logDir) + " || true; " + shellCmd
		}
		if workspacePath != "" {
			shellCmd = "cd " + ShellQuote(workspacePath) + "; " + shellCmd
		}
		// set -e (CORE-154): a failed cd aborts before the agent starts,
		// instead of running it in the remote HOME.
		shellCmd = "set -e; " + shellCmd
		sshArgs := []string{"-T"}
		sshArgs = append(sshArgs, sshStrictHostOption(workerHost)...)
		remoteArg, payload := remoteBashInvocation("-lc", shellCmd)
		sshArgs = append(sshArgs, "-o", "BatchMode=yes", workerHost, remoteArg)
		cmd = exec.CommandContext(turnCtx, "ssh", sshArgs...)
		var err error
		if stdinFeed, err = attachRemoteStdin(cmd, payload); err != nil {
			return TurnResult{Failed: true}, err
		}
	} else {
		if claudeReadsPrompt(sessionID, prompt) {
			var err error
			if promptIn, err = promptFile(prompt); err != nil {
				return TurnResult{Failed: true, FailureText: err.Error()}, err
			}
		}
		if filepath.IsAbs(command) && !strings.Contains(command, " ") {
			// Clean absolute path with no flags — run the binary directly,
			// no shell needed; the prompt file is its stdin.
			cmd = exec.CommandContext(turnCtx, command, buildDirectArgs(sessionID, prompt, permissionMode)...)
			setPromptStdin(cmd, promptIn)
		} else {
			// Bare name — wrap in login shell so PATH is resolved at
			// runtime. The prompt file is fd 3, redirected to the CLI's
			// stdin by the command itself, so a login profile that reads
			// stdin cannot consume it.
			cmd = exec.CommandContext(turnCtx, loginShell(), "-lc",
				buildShellCmd(command, sessionID, prompt, localPromptRedirectFor(promptIn), permissionMode))
			setPromptFD3(cmd, promptIn)
		}
	}
	setProcessGroup(cmd)
	if workspacePath != "" && workerHost == "" {
		cmd.Dir = workspacePath
	}
	logDirEnv := ""
	if logDir != "" && workerHost == "" {
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			slog.Warn("agent: failed to create log dir", "dir", logDir, "error", err)
		}
		logDirEnv = "CLAUDE_CODE_LOG_DIR=" + logDir
	}
	// Set unconditionally: Go inherits the parent environment when cmd.Env is
	// nil, so leaving it unset on any path silently drops the marker. For the
	// SSH path cmd.Env applies to the local ssh process only — the marker
	// travels in the shell command instead (see below).
	cmd.Env = itervoxAgentEnv(logDirEnv)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return TurnResult{Failed: true}, fmt.Errorf("agent: stdout pipe: %w", err)
	}
	// Tail-bounded (CORE-028): only the last maxStderrCaptureBytes are
	// kept, however long the turn runs and however much the agent prints.
	stderrBuf := newTailBuffer(maxStderrCaptureBytes)
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		return TurnResult{Failed: true}, fmt.Errorf("agent: start: %w", err)
	}
	stdinFeed.start()
	if workerHost == "" {
		// CORE-042: record the local group in the orphan ledger until Wait
		// has returned (the deferred call runs after cmd.Wait below).
		defer procgroup.Track(ctx, cmd)()
	}

	result, readErr := readLines(turnCtx, log, onProgress, stdout, readTimeoutMs, "claude", ParseLine)

	if readErr != nil {
		// The stream ended abnormally (idle read timeout or a read error such
		// as a line over maxStreamLineBytes)
		// rather than a clean EOF/result event. Cancel the turn context now —
		// before cmd.Wait() — so cmd.Cancel (SIGKILL to the process group,
		// configured in setProcessGroup) and WaitDelay bound the wait below
		// instead of leaving the subprocess running unobserved until the
		// caller's context or the hard turn timeout eventually fires.
		cancel()
		result.Failed = true
	}

	// Wait regardless of readErr so we don't leave zombie processes.
	waitErr := cmd.Wait()
	stdinFeed.join() // Wait closed ssh's stdin pipe; the writer has returned
	if waitErr != nil && readErr == nil {
		result.Failed = true
	}

	// resolveFailureText (internal/agent/failure_text.go) is the single
	// shared classification home for both Claude and Codex — see its doc
	// comment for the three-way rule (T-53 / CORE-001).
	if result.Failed {
		result.FailureText = resolveFailureText(result.FailureText, stderrBuf.String(), waitErr, readErr)
	}

	if readErr != nil {
		return result, readErr
	}
	result.TotalTokens = result.InputTokens + result.OutputTokens
	result = FinalizeResult(result)
	return result, nil
}

// sharedFlagsBase are the CLI flags used by every claude invocation regardless
// of execution mode (direct binary or shell) AND regardless of permission
// mode. The approval flags are appended per-turn by claudePermissionFlags,
// because they are the one part that varies by profile (issue #66).
var sharedFlagsBase = []string{"--output-format", "stream-json", "--verbose"}

// sharedFlagsSliceFor returns the full flag slice for a permission mode.
func sharedFlagsSliceFor(mode PermissionMode) []string {
	out := append([]string{}, sharedFlagsBase...)
	return append(out, claudePermissionFlags(mode)...)
}

// sharedFlagsStrFor is the shell-command form. The leading space is load
// bearing — callers concatenate it directly onto the command.
func sharedFlagsStrFor(mode PermissionMode) string {
	out := ""
	for _, f := range sharedFlagsSliceFor(mode) {
		out += " " + f
	}
	return out
}

// rejectNULPrompt refuses a prompt holding a NUL byte (round 3, m2). Over
// SSH the prompt travels inside the NUL-terminated script payload and is
// held in a bash variable, which cannot hold a NUL, so such a prompt can
// never reach the remote agent intact (the wrapper's byte count would fail
// with a misleading exit 97). Local turns refuse it too, so a prompt runs on
// every worker or on none. Both paths call this first and fail the turn with
// an error naming the byte. The NUL is not stripped: silently changing the
// issue text could change what the agent is asked to do.
func rejectNULPrompt(prompt string) error {
	if i := strings.IndexByte(prompt, 0); i >= 0 {
		return fmt.Errorf("agent: prompt contains a NUL byte at offset %d; process arguments cannot carry NUL, so the agent cannot be started — remove it from the issue text or prompt template", i)
	}
	return nil
}

// claudeReadsPrompt reports whether a claude turn takes a prompt: every
// first turn, and a continuation with a message (the user's reply to an
// input-required question). A bare resume passes no -p and reads nothing.
func claudeReadsPrompt(sessionID *string, prompt string) bool {
	return sessionID == nil || *sessionID == "" || prompt != ""
}

// buildDirectArgs returns CLI args for direct (non-shell) invocation. The
// prompt is not among them (CORE-156): `-p` with no prompt argument makes
// claude read it from stdin, which RunTurn wires to the prompt file. That
// also retires the argv hazard of a prompt starting with '-' (parsed as a
// flag), which used to need a leading space.
func buildDirectArgs(sessionID *string, prompt string, mode PermissionMode) []string {
	args := sharedFlagsSliceFor(mode)
	if sessionID != nil && *sessionID != "" {
		args = append(args, "--resume", *sessionID)
	}
	if claudeReadsPrompt(sessionID, prompt) {
		args = append(args, "-p")
	}
	return args
}

// buildShellCmd returns the full shell command string for bash/zsh -lc.
// The prompt is never in it (CORE-156): when the turn takes a prompt, the
// command ends in `-p` followed by promptRedirect, the redirection that
// puts the prompt on claude's stdin — localPromptRedirect for the local
// login shell, remotePromptRedirect for the remote bash script.
//
// Defensive: if command is empty or whitespace, fall back to "claude" and
// log a warning. Without this, the flag string's leading space would produce
// " --output-format ..." which bash interprets as `--output-format` being
// the command name, surfacing as `--output-format: command not found`.
func buildShellCmd(command string, sessionID *string, prompt, promptRedirect string, mode PermissionMode) string {
	command = strings.TrimSpace(command)
	if command == "" {
		slog.Warn("agent: empty command resolved at dispatch — falling back to 'claude'. Check WORKFLOW.md agent.command and any profile.command fields.")
		command = "claude"
	}
	cmd := command + sharedFlagsStrFor(mode)
	if sessionID != nil && *sessionID != "" {
		// Resume an existing session. When a prompt is also provided (e.g.,
		// the user's reply to an input-required question), -p makes Claude
		// read the message. Without -p, Claude expects a deferred-tool
		// marker in the session and errors with "No deferred tool marker
		// found" if it's not there.
		cmd += " --resume " + ShellQuote(*sessionID)
	}
	if claudeReadsPrompt(sessionID, prompt) {
		cmd += " -p" + promptRedirect
	}
	return cmd
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
		// Fall back to the first non-empty string field value, by sorted
		// key: ranging over the map directly made the description vary
		// from call to call for identical input (CORE-137).
		for _, k := range slices.Sorted(maps.Keys(input)) {
			var s string
			if json.Unmarshal(input[k], &s) == nil && s != "" {
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

// setProcessGroup configures cmd to run in its own process group and to kill
// the entire group (including child processes) when the context is cancelled.
// Without this, cancelling a "bash -lc 'codex ...'" only kills bash, not codex.
func setProcessGroup(cmd *exec.Cmd) {
	// procgroup.Configure (CORE-150) is the single shared home for the
	// Setpgid + group-kill Cancel + WaitDelay wiring and for the
	// retry-until-ESRCH SIGKILL sweep (procgroup.Kill: 10 ms re-send, 1 s
	// cap). WaitDelay ensures cmd.Wait returns promptly after Cancel fires,
	// even if child processes inherited the stdout pipe and are still alive.
	// Without it, Wait blocks until all pipe readers close — which may never
	// happen if the agent spawned background subprocesses.
	procgroup.Configure(cmd, 5*time.Second)
}

// loginShell returns the user's login shell from $SHELL, falling back to bash.
func loginShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "bash"
}

// ShellQuote wraps s in single quotes for bash. Its output is parsed only by
// bash: the local `bash -lc` of a bare command name, and, for SSH workers,
// the remote `bash` that evaluates the script received on stdin — since fix
// round 2 no ShellQuote output reaches the remote login shell (see
// remoteBashInvocation in ssh.go). A single quote or backslash is emitted
// outside the quoted segments as \' or \\; for bash that is equivalent to
// the classic '\” form, and it also keeps the result fish-safe should it
// ever be parsed there. It is also the one quoter for orchestrator's POSIX
// sh shim and env exports (CORE-111), where \' and \\ outside quotes mean
// the same thing.
func ShellQuote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\'', '\\':
			b.WriteByte('\'')
			b.WriteByte('\\')
			b.WriteByte(c)
			b.WriteByte('\'')
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// Stream line reader (CORE-002).
//
// readLines reads agent stdout one '\n'-terminated line at a time through a
// streamLineReader, which never buffers more of a line than it has decided
// to keep:
//
//  1. Every line of at most maxNonTerminalLineBytes (1 MiB, the old
//     bufio.Scanner cap) is buffered and handed to the parser in full, with
//     no byte-level type detection — so every line the pre-CORE-002 reader
//     decoded still decodes, including a terminal event whose "type" key is
//     JSON-escaped ("ty\u0070e") or comes late in the object (M0-close
//     re-check G6, CORE-002 amendment of 2026-09-25).
//  2. Only a line that crosses 1 MiB triggers type detection, over its first
//     streamDetectBudgetBytes (64 KiB) bytes (detectStreamLineType — an
//     allocation-free, key-order and whitespace independent scan of the
//     top-level object for its "type" member). The detector is a
//     bounded-memory shortcut for lines the old reader could not parse at
//     all; it never decides the fate of a line of 1 MiB or less.
//  3. Terminal type (Claude "result"; Codex "turn.completed"/"turn.failed"):
//     the line keeps buffering and is decoded in full, up to the
//     maxStreamLineBytes (16 MiB) hard cap, over which the reader fails
//     closed with errStreamLineTooLong — the turn fails rather than guessing
//     past a result it cannot hold. RunTurn then cancels the turn and kills
//     the subprocess, so nothing is left to drain.
//  4. Any other line over 1 MiB — a non-terminal "type", or no "type"
//     within the detection budget — is skipped: the rest of it is read and
//     discarded chunk by chunk, never buffered past the 1 MiB already held.
//
// Every skipped line increments TurnResult.OversizeLines and logs a Warn
// with its byte length.

// maxStreamLineBytes is the documented hard cap on a single TERMINAL stdout
// line from an agent CLI: 16 MiB. Over it the turn fails closed.
const maxStreamLineBytes = 16 << 20

// streamReadBufferBytes is the bufio.Reader size readLines reads through;
// it is also the largest chunk one ReadSlice call appends to a line.
const streamReadBufferBytes = 64 << 10

// streamDetectBudgetBytes is the detection budget: the most of a line the
// reader buffers before it must know the line's top-level "type".
const streamDetectBudgetBytes = 64 << 10

// maxNonTerminalLineBytes is the documented cap for a non-terminal line:
// longer non-terminal lines are skipped and counted, not decoded.
const maxNonTerminalLineBytes = 1 << 20

// streamReaderOverheadBytes bounds what one readLines call allocates besides
// line content: the bufio.Reader buffer, one read chunk of line-buffer
// headroom, and fixed per-call bookkeeping (goroutine, channel, timers,
// small-line copies). The CORE-002 peak-allocation rows assert against it.
const streamReaderOverheadBytes = 2*streamReadBufferBytes + 128<<10

// errStreamLineTooLong is the fail-closed read error for a terminal line over
// maxStreamLineBytes. Prefixed so FailureText identifies the read-error path.
var errStreamLineTooLong = fmt.Errorf("agent: stream line exceeds %d MiB cap", maxStreamLineBytes>>20)

// isTerminalStreamType reports whether a top-level "type" ends a turn for
// either parser: Claude "result" (ParseLine) or Codex "turn.completed" /
// "turn.failed" (ParseCodexLine). One set serves both runners — a
// Claude-only type on the Codex stream merely buffers and is then ignored by
// ParseCodexLine.
func isTerminalStreamType(t string) bool {
	switch t {
	case "result", "turn.completed", "turn.failed":
		return true
	}
	return false
}

// streamLineReader reads stdout lines under the rules documented above.
type streamLineReader struct {
	br *bufio.Reader
	// buf is the reusable line buffer. Its initial capacity holds the
	// detection budget plus one read chunk, so the decision point never
	// reallocates it.
	buf []byte
}

func newStreamLineReader(r io.Reader) *streamLineReader {
	return &streamLineReader{
		br:  bufio.NewReaderSize(r, streamReadBufferBytes),
		buf: make([]byte, 0, streamDetectBudgetBytes+streamReadBufferBytes),
	}
}

// streamLine is one result of streamLineReader.next.
type streamLine struct {
	line []byte // a caller-owned copy of the line; nil when skipped
	// skippedBytes > 0 marks an oversized line that was skipped; it is the
	// line's full length. skippedType is its detected "type" ("" if none
	// was found within the detection budget).
	skippedBytes int
	skippedType  string
}

// next returns the next line, with the '\n' terminator (and a trailing
// '\r') stripped. A final unterminated line is returned normally; the call
// after it returns io.EOF.
func (lr *streamLineReader) next() (streamLine, error) {
	buf := lr.buf[:0]
	// Content limit before a decision: every line up to the non-terminal
	// cap is decoded in full, whatever its "type" (rule 1).
	limit := maxNonTerminalLineBytes
	decided := false
	typ := ""
	for {
		chunk, err := lr.br.ReadSlice('\n')
		content := len(chunk)
		if err == nil {
			content-- // the '\n' terminator is not part of the line
		}
		if len(buf)+content > limit && !decided {
			// Decision point: the line is longer than 1 MiB. content ≤ one
			// read chunk (64 KiB), so buf already holds well over the
			// detection budget of the line's head.
			var found bool
			typ, found = detectStreamLineType(buf[:min(len(buf), streamDetectBudgetBytes)])
			decided = true
			if found && isTerminalStreamType(typ) {
				limit = maxStreamLineBytes
			} else {
				limit = 0 // non-terminal or type unknown within budget: skip
			}
		}
		if decided && len(buf)+content > limit {
			if limit == maxStreamLineBytes {
				return streamLine{}, errStreamLineTooLong
			}
			n, derr := lr.discardRest(len(buf)+content, err)
			if derr != nil {
				return streamLine{}, derr
			}
			return streamLine{skippedBytes: n, skippedType: typ}, nil
		}
		if len(buf)+len(chunk) > cap(buf) && !decided {
			// Grow once to the non-terminal cap (plus the terminator)
			// instead of geometrically.
			grown := make([]byte, len(buf), maxNonTerminalLineBytes+1)
			copy(grown, buf)
			buf = grown
		}
		buf = append(buf, chunk...)
		switch {
		case err == nil:
			return lr.finish(buf[:len(buf)-1]), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(buf) > 0:
			return lr.finish(buf), nil
		default:
			return streamLine{}, err
		}
	}
}

// finish copies line out of the reusable buffer and keeps that buffer for the
// next line unless it grew past the non-terminal cap (a large terminal line).
func (lr *streamLineReader) finish(line []byte) streamLine {
	if n := len(line); n > 0 && line[n-1] == '\r' {
		line = line[:n-1]
	}
	out := make([]byte, len(line))
	copy(out, line)
	if c := cap(line); c > cap(lr.buf) && c <= maxNonTerminalLineBytes+1 {
		lr.buf = line[:0]
	}
	return streamLine{line: out}
}

// discardRest reads and drops the remainder of a line being skipped. seen is
// the content length consumed so far and lastErr the error of the ReadSlice
// that returned the final chunk seen. It returns the line's full length.
func (lr *streamLineReader) discardRest(seen int, lastErr error) (int, error) {
	for {
		switch {
		case lastErr == nil, errors.Is(lastErr, io.EOF):
			return seen, nil // line ended (or stream ended mid-line)
		case !errors.Is(lastErr, bufio.ErrBufferFull):
			return 0, lastErr
		}
		var chunk []byte
		chunk, lastErr = lr.br.ReadSlice('\n')
		seen += len(chunk)
		if lastErr == nil {
			seen-- // terminator
		}
	}
}

// detectStreamLineType scans p — a possibly truncated prefix of one JSON
// line — for the top-level object's "type" member and returns its string
// value. found is false when p ends, or is not a JSON object, before that
// member is complete. It is order- and whitespace-independent, skips nested
// values and strings (including escapes) without materialising them, and
// allocates nothing but the returned string.
func detectStreamLineType(p []byte) (typ string, found bool) {
	i := skipJSONSpace(p, 0)
	if i >= len(p) || p[i] != '{' {
		return "", false
	}
	i++
	for {
		i = skipJSONSpace(p, i)
		if i >= len(p) || p[i] != '"' {
			return "", false
		}
		keyEnd, ok := scanJSONString(p, i)
		if !ok {
			return "", false
		}
		isType := string(p[i+1:keyEnd-1]) == "type"
		i = skipJSONSpace(p, keyEnd)
		if i >= len(p) || p[i] != ':' {
			return "", false
		}
		i = skipJSONSpace(p, i+1)
		if isType {
			if i >= len(p) || p[i] != '"' {
				return "", false
			}
			end, ok := scanJSONString(p, i)
			if !ok {
				return "", false
			}
			return string(p[i+1 : end-1]), true
		}
		if i, ok = skipJSONValue(p, i); !ok {
			return "", false
		}
		i = skipJSONSpace(p, i)
		if i >= len(p) || p[i] != ',' {
			return "", false
		}
		i++
	}
}

func skipJSONSpace(p []byte, i int) int {
	for i < len(p) && (p[i] == ' ' || p[i] == '\t' || p[i] == '\n' || p[i] == '\r') {
		i++
	}
	return i
}

// scanJSONString returns the index just past the string starting at p[i]
// (which must be '"').
func scanJSONString(p []byte, i int) (int, bool) {
	for j := i + 1; j < len(p); j++ {
		switch p[j] {
		case '\\':
			j++
		case '"':
			return j + 1, true
		}
	}
	return 0, false
}

// skipJSONValue returns the index just past the value starting at p[i].
func skipJSONValue(p []byte, i int) (int, bool) {
	if i >= len(p) {
		return 0, false
	}
	switch p[i] {
	case '"':
		return scanJSONString(p, i)
	case '{', '[':
		depth := 0
		for j := i; j < len(p); j++ {
			switch p[j] {
			case '"':
				end, ok := scanJSONString(p, j)
				if !ok {
					return 0, false
				}
				j = end - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1, true
				}
			}
		}
		return 0, false
	default: // number, true, false, null
		for j := i; j < len(p); j++ {
			switch p[j] {
			case ',', '}', ']', ' ', '\t', '\n', '\r':
				return j, true
			}
		}
		return 0, false
	}
}

// readLines reads stream-json lines from r, accumulating a TurnResult.
// log is used for INFO-level output (assistant messages, turn result) so that
// Claude's live activity appears in the log stream with the caller's context.
// logPrefix is the backend name used in log messages (e.g. "claude", "codex").
// Returns on EOF, context cancellation, or readTimeoutMs idle expiry.
func readLines(ctx context.Context, log Logger, onProgress func(TurnResult), r io.Reader, readTimeoutMs int, logPrefix string, parseFn func([]byte) (StreamEvent, error)) (TurnResult, error) {
	type scanResult struct {
		streamLine
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
		lr := newStreamLineReader(r)
		for {
			sl, err := lr.next()
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = nil // clean end of stream
				}
				select {
				case lineCh <- scanResult{done: true, err: err}:
				case <-done:
				}
				return
			}
			select {
			case lineCh <- scanResult{streamLine: sl}:
			case <-done:
				return
			}
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
			if sr.skippedBytes > 0 {
				result.OversizeLines++
				log.Warn(logPrefix+": skipped oversized stream line",
					"bytes", sr.skippedBytes, "type", sr.skippedType, "oversize_lines", result.OversizeLines)
				continue
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
				// Only init (or a Codex thread.started, which has no
				// subtype) starts a session; other subtypes used to be
				// logged as "session started" too (CORE-050).
				switch ev.Subtype {
				case "", "init":
					log.Info(logPrefix+": session started", "session_id", ev.SessionID)
				case "api_retry":
					if ev.Limit != nil {
						log.Warn(logPrefix+": api retry (vendor limit)", "session_id", ev.SessionID,
							"kind", string(ev.Limit.Kind), "http_status", ev.Limit.HTTPStatus,
							"category", ev.Limit.ErrorCategory, "retry_after", ev.Limit.RetryAfter.String())
					} else {
						log.Debug(logPrefix+": api retry", "session_id", ev.SessionID)
					}
				default:
					log.Debug(logPrefix+": system event", "session_id", ev.SessionID, "subtype", ev.Subtype)
				}
			case "rate_limit_event":
				if ev.Limit != nil {
					log.Warn(logPrefix+": vendor limit reached", "session_id", ev.SessionID,
						"limit_type", ev.Limit.LimitType, "resets_at", formatLimitReset(ev.Limit))
				}
			case EventError:
				log.Warn(logPrefix+": error event", "text", ev.Message)
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
