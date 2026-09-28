package agent

import (
	"context"
	"log/slog"
	"strings"

	"github.com/vnovick/itervox/internal/config"
)

const backendHintPrefix = config.BackendHintPrefix

type MultiRunner struct {
	defaultRunner Runner
	runners       map[string]Runner
}

func NewMultiRunner(defaultRunner Runner, runners map[string]Runner) *MultiRunner {
	return &MultiRunner{
		defaultRunner: defaultRunner,
		runners:       runners,
	}
}

func (m *MultiRunner) RunTurn(
	ctx context.Context,
	log Logger,
	onProgress func(TurnResult),
	sessionID *string,
	prompt, workspacePath, command, workerHost, logDir string,
	readTimeoutMs, turnTimeoutMs int,
	permissionMode PermissionMode,
) (TurnResult, error) {
	backend, cleanedCommand := backendFromCommand(command)
	cleanedPrompt := stripBackendHintFromPrompt(prompt)

	if backend != "" && backend != "claude" && backend != "codex" {
		slog.Warn("multi-runner: unsupported backend, falling back to default",
			"backend", backend, "command", cleanedCommand)
	}

	if r, ok := m.runners[backend]; ok {
		return r.RunTurn(ctx, log, onProgress, sessionID, cleanedPrompt, workspacePath, cleanedCommand, workerHost, logDir, readTimeoutMs, turnTimeoutMs, permissionMode)
	}
	return m.defaultRunner.RunTurn(ctx, log, onProgress, sessionID, cleanedPrompt, workspacePath, cleanedCommand, workerHost, logDir, readTimeoutMs, turnTimeoutMs, permissionMode)
}

func stripBackendHintFromPrompt(prompt string) string {
	if strings.HasPrefix(prompt, backendHintPrefix) {
		rest := strings.TrimPrefix(prompt, backendHintPrefix)
		if idx := strings.IndexAny(rest, " \t\n"); idx >= 0 {
			return rest[idx+1:]
		}
		return ""
	}
	return prompt
}

func BackendFromCommand(command string) string {
	backend, _ := backendFromCommand(command)
	return backend
}

func CommandWithBackendHint(command, backend string) string {
	if backend == "" {
		return command
	}
	if BackendFromCommand(command) == backend {
		return command
	}
	current, cleaned := parseBackendHint(command)
	if current == backend {
		return command
	}
	if current != "" {
		command = cleaned
	}
	return backendHintPrefix + backend + " " + command
}

// The pure command-token parser moved to internal/config (CORE-010) so that
// config validation can derive a command's backend without importing agent.
// These unexported names delegate so existing call sites are unchanged.

func backendFromCommand(command string) (backend, cleaned string) {
	return config.SplitBackendFromCommand(command)
}

func parseBackendHint(command string) (backend, cleaned string) {
	return config.ParseBackendHint(command)
}
