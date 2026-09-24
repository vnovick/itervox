package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPermissionModeDefaultsToBypass pins that an unset field keeps the
// long-standing behaviour. Changing this default would silently alter how every
// existing deployment launches agents.
func TestPermissionModeDefaultsToBypass(t *testing.T) {
	assert.Equal(t, PermissionBypass, ParsePermissionMode(""))
	assert.Equal(t, PermissionBypass, DefaultPermissionMode)
}

// TestPermissionModeUnknownFallsBackSoftly pins fail-soft parsing: a typo must
// not stop a daemon from running queued work, and the fallback is what the
// operator was already running — never MORE permissive than what they asked for.
func TestPermissionModeUnknownFallsBackSoftly(t *testing.T) {
	assert.Equal(t, PermissionBypass, ParsePermissionMode("sandboxed"))
	assert.Equal(t, PermissionBypass, ParsePermissionMode("YOLO"))
}

func TestPermissionModeParsesKnownValues(t *testing.T) {
	assert.Equal(t, PermissionSandbox, ParsePermissionMode("sandbox"))
	assert.Equal(t, PermissionBypass, ParsePermissionMode("bypass"))
}

// TestSandboxModeEmitsNoDangerousFlagForEitherBackend is issue #66's headline
// acceptance. It is asserted on the ACTUAL command builders, not on the flag
// helpers, because a helper returning the right flags proves nothing if a
// builder still concatenates a hardcoded one.
func TestSandboxModeEmitsNoDangerousFlagForEitherBackend(t *testing.T) {
	session := "sess-1"
	cases := map[string]string{
		"claude direct":        strings.Join(buildDirectArgs(nil, "do it", PermissionSandbox), " "),
		"claude direct/resume": strings.Join(buildDirectArgs(&session, "do it", PermissionSandbox), " "),
		"claude shell":         buildShellCmd("claude", nil, "do it", PermissionSandbox),
		"claude shell/resume":  buildShellCmd("claude", &session, "do it", PermissionSandbox),
		"codex direct":         strings.Join(buildCodexDirectArgs(nil, "do it", "/ws", PermissionSandbox), " "),
		"codex direct/resume":  strings.Join(buildCodexDirectArgs(&session, "do it", "/ws", PermissionSandbox), " "),
		"codex shell":          buildCodexShellCmd("codex", nil, "do it", "/ws", PermissionSandbox),
		"codex shell/resume":   buildCodexShellCmd("codex", &session, "do it", "/ws", PermissionSandbox),
	}
	for name, got := range cases {
		assert.NotContains(t, got, "--dangerously",
			"%s: sandbox mode must emit no --dangerously-* flag", name)
	}
}

// TestSandboxModeStaysNonInteractive pins the property that makes sandbox mode
// usable at all: itervox is headless, so a mode that can pause for approval
// hangs the turn until the timeout kills it and the issue fails.
func TestSandboxModeStaysNonInteractive(t *testing.T) {
	codex := buildCodexShellCmd("codex", nil, "do it", "/ws", PermissionSandbox)
	assert.Contains(t, codex, "--sandbox workspace-write")
	assert.Contains(t, codex, "--ask-for-approval never",
		"without this a workspace-write sandbox still prompts, which hangs a headless run")

	claude := buildShellCmd("claude", nil, "do it", PermissionSandbox)
	assert.Contains(t, claude, "--permission-mode acceptEdits")
}

// TestBypassModeIsUnchanged pins that the default path emits exactly what it
// emitted before #66 — the flags every existing deployment depends on.
func TestBypassModeIsUnchanged(t *testing.T) {
	claude := buildShellCmd("claude", nil, "do it", PermissionBypass)
	assert.Contains(t, claude, "--output-format stream-json")
	assert.Contains(t, claude, "--verbose")
	assert.Contains(t, claude, "--dangerously-skip-permissions")

	codex := buildCodexShellCmd("codex", nil, "do it", "/ws", PermissionBypass)
	assert.Contains(t, codex, "--dangerously-bypass-approvals-and-sandbox")
	assert.Contains(t, codex, "--skip-git-repo-check")
}

// TestSharedFlagsStrHasLeadingSpace pins the load-bearing detail called out in
// buildShellCmd's comment: callers concatenate the string straight onto the
// command, so losing the leading space makes bash read the first flag as the
// command name ("--output-format: command not found").
func TestSharedFlagsStrHasLeadingSpace(t *testing.T) {
	for _, mode := range []PermissionMode{PermissionBypass, PermissionSandbox} {
		got := sharedFlagsStrFor(mode)
		require.NotEmpty(t, got)
		assert.True(t, strings.HasPrefix(got, " "),
			"mode %q: the flag string must keep its leading space", mode)
	}
}
