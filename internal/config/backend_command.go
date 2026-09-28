package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// BackendHintPrefix is the leading token the orchestrator prepends to a
// runner command to force a backend ("@@itervox-backend=codex <cmd>") when the
// backend cannot be derived from the command's binary name.
//
// The pure command-token parser lives here, not in internal/agent, so that
// config validation (ValidateAutomationsWithDefaults) can derive a command's
// backend without config importing agent — the package order is agent →
// config, never the reverse (CORE-010). internal/agent delegates to these.
const BackendHintPrefix = "@@itervox-backend="

// IsSupportedBackend reports whether backend is exactly one of the agent
// backends itervox can run ("claude", "codex"). The comparison is exact — no
// trimming or case folding — because callers feed the value into a runner
// command as a backend hint that is split at the first whitespace (CORE-040).
// The empty string is NOT a backend; callers that accept "" as "clear the
// override" must check for it separately.
//
// It lives in config, not internal/agent, for the same package-order reason
// as the parser below: internal/server (and config validation) must not
// import agent.
func IsSupportedBackend(backend string) bool {
	switch backend {
	case "claude", "codex":
		return true
	}
	return false
}

// BackendFromCommand returns "claude" or "codex" when the command either
// carries a backend hint or its first real token (after env assignments and
// an optional `env [-flags]` prefix) has that basename, and "" for anything
// else — e.g. an opaque wrapper script. No I/O.
func BackendFromCommand(command string) string {
	backend, _ := SplitBackendFromCommand(command)
	return backend
}

// SplitBackendFromCommand is BackendFromCommand plus the command with any
// backend hint stripped (unchanged when there is no hint).
func SplitBackendFromCommand(command string) (backend, cleaned string) {
	if hinted, rest := ParseBackendHint(command); hinted != "" {
		return hinted, rest
	}
	first := FirstCommandToken(command)
	if first == "" {
		return "", command
	}
	if base := filepath.Base(first); IsSupportedBackend(base) {
		return base, command
	}
	return "", command
}

// ParseBackendHint splits a leading BackendHintPrefix token off command.
func ParseBackendHint(command string) (backend, cleaned string) {
	trimmed := strings.TrimSpace(command)
	if !strings.HasPrefix(trimmed, BackendHintPrefix) {
		return "", command
	}
	rest := strings.TrimPrefix(trimmed, BackendHintPrefix)
	if rest == "" {
		return "", command
	}
	idx := strings.IndexAny(rest, " \t")
	if idx < 0 {
		return rest, ""
	}
	return rest[:idx], strings.TrimLeft(rest[idx:], " \t")
}

// FirstCommandToken returns the first token of command that is the program
// to run: env assignments are skipped, and `env [-flags] [VAR=val...] prog`
// resolves to prog.
func FirstCommandToken(command string) string {
	fields := strings.Fields(command)
	for i := 0; i < len(fields); i++ {
		token := fields[i]
		if token == "" {
			continue
		}
		if IsEnvAssignment(token) {
			continue
		}
		if filepath.Base(token) == "env" {
			for j := i + 1; j < len(fields); j++ {
				next := fields[j]
				if next == "" {
					continue
				}
				if IsEnvAssignment(next) || strings.HasPrefix(next, "-") {
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

// IsEnvAssignment reports whether token has the shell NAME=value form.
func IsEnvAssignment(token string) bool {
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

// validateSwitchBackendCommand rejects a rate_limited rule whose
// switch_to_backend disagrees with the backend derived from the switch
// profile's effective command. Effective = profile.Command when non-empty,
// else defaultCommand (agent.command) — the same inheritance
// the orchestrator's resolveDispatchTarget applies at dispatch. A command with no recognisable
// backend (an opaque wrapper) is accepted: that is what the backend hint the
// orchestrator prepends exists for.
func validateSwitchBackendCommand(id, switchProfileName string, switchProfile AgentProfile, switchToBackend, defaultCommand string) error {
	if switchToBackend == "" {
		return nil
	}
	effective := strings.TrimSpace(switchProfile.Command)
	source := "command"
	if effective == "" {
		effective = strings.TrimSpace(defaultCommand)
		source = "inherited agent.command"
	}
	derived := BackendFromCommand(effective)
	if derived == "" || derived == switchToBackend {
		return nil
	}
	// Deliberately does not contain the substring "switch_to_profile": the
	// dashboard maps validation errors to a form field by substring
	// (internal/server writeAutomationValidationError), and this error
	// belongs to the switchToBackend field.
	return fmt.Errorf("automation %q: policy.switch_to_backend %q does not match switch profile %q, whose %s %q runs %q; use a profile whose command runs %s, or drop switch_to_backend",
		id, switchToBackend, switchProfileName, source, effective, derived, switchToBackend)
}
