package agent

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"unicode"
)

const backendHintPrefix = "@@itervox-backend="

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

func backendFromCommand(command string) (backend, cleaned string) {
	if hinted, rest := parseBackendHint(command); hinted != "" {
		return hinted, rest
	}
	first := firstCommandToken(command)
	if first == "" {
		return "", command
	}
	switch filepath.Base(first) {
	case "claude", "codex":
		return filepath.Base(first), command
	default:
		return "", command
	}
}

func parseBackendHint(command string) (backend, cleaned string) {
	trimmed := strings.TrimSpace(command)
	if !strings.HasPrefix(trimmed, backendHintPrefix) {
		return "", command
	}
	rest := strings.TrimPrefix(trimmed, backendHintPrefix)
	if rest == "" {
		return "", command
	}
	idx := strings.IndexAny(rest, " \t")
	if idx < 0 {
		return rest, ""
	}
	return rest[:idx], strings.TrimLeft(rest[idx:], " \t")
}

func firstCommandToken(command string) string {
	fields := strings.Fields(command)
	for i := 0; i < len(fields); i++ {
		token := fields[i]
		if token == "" {
			continue
		}
		if isEnvAssignment(token) {
			continue
		}
		if filepath.Base(token) == "env" {
			for j := i + 1; j < len(fields); j++ {
				next := fields[j]
				if next == "" {
					continue
				}
				if isEnvAssignment(next) || strings.HasPrefix(next, "-") {
					continue
				}
				return next
			}
			return ""
		}
		return token
	}
	return ""
}

func isEnvAssignment(token string) bool {
	key, _, ok := strings.Cut(token, "=")
	if !ok || key == "" {
		return false
	}
	for i, r := range key {
		if i == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return false
			}
			continue
		}
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
