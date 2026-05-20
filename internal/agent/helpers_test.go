package agent

// White-box tests for unexported helper functions in the agent package.
// These functions contain non-trivial logic that deserves direct coverage.

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- shellQuote ---

func TestShellQuoteSimple(t *testing.T) {
	assert.Equal(t, "'hello world'", shellQuote("hello world"))
}

func TestShellQuoteWithSingleQuote(t *testing.T) {
	// Single quotes inside the string must be escaped.
	assert.Equal(t, "'it'\\''s fine'", shellQuote("it's fine"))
}

func TestShellQuoteEmpty(t *testing.T) {
	assert.Equal(t, "''", shellQuote(""))
}

func TestShellQuoteSpecialChars(t *testing.T) {
	// Backticks, $, ! etc. are safe inside single quotes.
	got := shellQuote("`echo $HOME`")
	assert.Equal(t, "'`echo $HOME`'", got)
}

// --- buildShellCmd ---

func TestBuildShellCmdNewSession(t *testing.T) {
	cmd := buildShellCmd("claude", nil, "do the thing")
	assert.Contains(t, cmd, "claude")
	assert.Contains(t, cmd, "--output-format stream-json")
	assert.Contains(t, cmd, "-p")
	assert.Contains(t, cmd, "do the thing")
	assert.NotContains(t, cmd, "--resume")
}

func TestBuildShellCmdResumeWithoutPrompt(t *testing.T) {
	id := "sess-abc"
	cmd := buildShellCmd("claude", &id, "")
	assert.Contains(t, cmd, "--resume")
	assert.Contains(t, cmd, "sess-abc")
	// Resume without a prompt should not include -p.
	assert.NotContains(t, cmd, " -p ")
}

func TestBuildShellCmdResumeWithPrompt(t *testing.T) {
	id := "sess-abc"
	cmd := buildShellCmd("claude", &id, "the user reply")
	assert.Contains(t, cmd, "--resume")
	assert.Contains(t, cmd, "sess-abc")
	// Resume WITH a prompt (input-required flow) should include both flags.
	assert.Contains(t, cmd, " -p ")
	assert.Contains(t, cmd, "the user reply")
}

func TestBuildDirectArgsResumeWithoutPrompt(t *testing.T) {
	id := "sess-abc"
	args := buildDirectArgs(&id, "")
	assert.Equal(t, []string{
		"--output-format", "stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
		"--resume", "sess-abc",
	}, args)
}

func TestBuildDirectArgsResumeWithPrompt(t *testing.T) {
	id := "sess-abc"
	args := buildDirectArgs(&id, "the user reply")
	assert.Equal(t, []string{
		"--output-format", "stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
		"--resume", "sess-abc",
		"-p", "the user reply",
	}, args)
}

// Regression: an empty command must not produce a shell line that starts with
// `--output-format`. Without the fallback, bash -lc would interpret the flag
// as the command name and print `--output-format: command not found`.
func TestBuildShellCmdEmptyCommandFallsBackToClaude(t *testing.T) {
	cmd := buildShellCmd("", nil, "do the thing")
	// Must not start with the flag (which would happen if leading whitespace
	// was the only thing before --output-format).
	assert.False(t, strings.HasPrefix(strings.TrimSpace(cmd), "--"),
		"shell command must not start with a flag; got: %q", cmd)
	assert.True(t, strings.HasPrefix(strings.TrimSpace(cmd), "claude "),
		"empty command should fall back to 'claude'; got: %q", cmd)
}

func TestBuildShellCmdWhitespaceCommandFallsBackToClaude(t *testing.T) {
	cmd := buildShellCmd("   ", nil, "do the thing")
	assert.True(t, strings.HasPrefix(strings.TrimSpace(cmd), "claude "),
		"whitespace-only command should fall back to 'claude'; got: %q", cmd)
}

func TestBuildShellCmdEmptySessionID(t *testing.T) {
	id := ""
	cmd := buildShellCmd("claude", &id, "use prompt")
	// Empty session ID should be treated as new session.
	assert.NotContains(t, cmd, "--resume")
	assert.Contains(t, cmd, "-p")
}

// --- todoItems ---

func TestTodoItemsBasic(t *testing.T) {
	raw := json.RawMessage(`{"todos":[{"content":"fix bug","status":"pending"},{"content":"add tests","status":"pending"}]}`)
	items := todoItems(raw)
	assert.Equal(t, []string{"fix bug", "add tests"}, items)
}

func TestTodoItemsSkipsEmptyContent(t *testing.T) {
	raw := json.RawMessage(`{"todos":[{"content":""},{"content":"real item"}]}`)
	items := todoItems(raw)
	assert.Equal(t, []string{"real item"}, items)
}

func TestTodoItemsNilInput(t *testing.T) {
	assert.Nil(t, todoItems(nil))
}

func TestTodoItemsEmptyJSON(t *testing.T) {
	assert.Nil(t, todoItems(json.RawMessage(`{}`)))
}

func TestTodoItemsInvalidJSON(t *testing.T) {
	assert.Nil(t, todoItems(json.RawMessage(`not json`)))
}

func TestTodoItemsNoTodosKey(t *testing.T) {
	assert.Nil(t, todoItems(json.RawMessage(`{"other":"field"}`)))
}

// --- buildCodexShellCmd ---

func TestBuildCodexShellCmdNewSession(t *testing.T) {
	cmd := buildCodexShellCmd("codex", nil, "do the work", "/workspace")
	assert.Contains(t, cmd, "codex")
	assert.Contains(t, cmd, "-C")
	assert.Contains(t, cmd, "/workspace")
	assert.Contains(t, cmd, " exec")
	assert.Contains(t, cmd, "--json")
	assert.Contains(t, cmd, "do the work")
	assert.NotContains(t, cmd, "resume")
}

func TestBuildCodexShellCmdResume(t *testing.T) {
	id := "sess-xyz"
	cmd := buildCodexShellCmd("codex", &id, "continue", "")
	assert.Contains(t, cmd, "resume")
	assert.Contains(t, cmd, "sess-xyz")
	assert.Contains(t, cmd, "continue")
}

func TestBuildCodexShellCmdNoWorkspace(t *testing.T) {
	cmd := buildCodexShellCmd("codex", nil, "prompt", "")
	assert.NotContains(t, cmd, "-C")
}

func TestBuildCodexShellCmdEmptySessionID(t *testing.T) {
	id := ""
	cmd := buildCodexShellCmd("codex", &id, "prompt text", "")
	assert.NotContains(t, cmd, "resume")
}

// --- sshFetchClaude session stamping via tar ---
//
// sshFetchClaude streams a tar archive over SSH and derives session IDs from tar
// header filenames — the same source as the local readJSONLFileMultiWith path. These tests
// verify the two primitives the tar loop relies on: streamLineToEntry propagating
// the session ID it receives, and the filename→sessionID extraction formula.

func TestStreamLineToEntryStampsSessionID(t *testing.T) {
	// A minimal Claude Code assistant event with one text block.
	line := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]},"session_id":"abc123"}`)
	entry, ok := streamLineToEntry(line, "abc123")
	assert.True(t, ok)
	assert.Equal(t, "abc123", entry.SessionID)
}

func TestStreamLineToEntryEmptySessionID(t *testing.T) {
	// When no session ID is provided, SessionID is "".
	line := []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]},"session_id":""}`)
	entry, ok := streamLineToEntry(line, "")
	assert.True(t, ok)
	assert.Equal(t, "", entry.SessionID)
}

func TestSSHSessionIDFromTarHeader(t *testing.T) {
	// The tar header name → session ID formula must match the local readJSONLFileMultiWith formula.
	cases := []struct{ name, want string }{
		{"abc123.jsonl", "abc123"},
		{"./abc123.jsonl", "abc123"},      // tar may include "./" prefix
		{"subdir/abc123.jsonl", "abc123"}, // tar -C strips dir but test robustness
	}
	for _, c := range cases {
		got := strings.TrimSuffix(filepath.Base(c.name), ".jsonl")
		assert.Equal(t, c.want, got, "header name: %s", c.name)
	}
}

func TestSSHFetchClaudeViaInMemoryTar(t *testing.T) {
	// Build an in-memory tar archive with two session files and verify that
	// streamLineToEntriesWith correctly stamps each entry with its file's session ID.
	// This exercises the exact loop body used by sshFetchClaude.
	file1Line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"thinking"}]},"session_id":"sess-001"}`
	file2Line := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]},"session_id":"sess-002"}`

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range []struct{ name, body string }{
		{"sess-001.jsonl", file1Line + "\n"},
		{"sess-002.jsonl", file2Line + "\n"},
	} {
		_ = tw.WriteHeader(&tar.Header{Name: f.name, Size: int64(len(f.body)), Mode: 0o644})
		_, _ = tw.Write([]byte(f.body))
	}
	_ = tw.Close()

	entries := parseRemoteJSONLTar(&buf, ParseLine, "test-host", "claude")

	assert.Len(t, entries, 2)
	assert.Equal(t, "sess-001", entries[0].SessionID)
	assert.Equal(t, "sess-002", entries[1].SessionID)
}

func TestParseRemoteJSONLTarSupportsCodexParser(t *testing.T) {
	codexLine := `{"type":"item.completed","item":{"id":"i1","type":"agent_message","text":"hello from codex"}}`

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := codexLine + "\n"
	_ = tw.WriteHeader(&tar.Header{Name: "codex-abc.jsonl", Size: int64(len(body)), Mode: 0o644})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()

	entries := parseRemoteJSONLTar(&buf, ParseCodexLine, "test-host", "codex")

	assert.Len(t, entries, 1)
	assert.Equal(t, "codex-abc", entries[0].SessionID)
	assert.Contains(t, entries[0].Message, "hello from codex")
}

// TestSafePromptArg guards against the argv hazard that surfaced in
// production: a rendered prompt beginning with '-' (markdown list, YAML, or
// accidental paste) was interpreted by Claude's CLI parser as an unknown
// flag. The fix prepends a single space so the parser sees a non-flag value
// token. Claude trims leading prompt whitespace, so the agent behavior is
// unchanged.
func TestSafePromptArg(t *testing.T) {
	t.Run("prompts starting with '-' get a leading space", func(t *testing.T) {
		assert.Equal(t, " - Step 1\n- Step 2", safePromptArg("- Step 1\n- Step 2"))
		assert.Equal(t, " -help", safePromptArg("-help"))
		assert.Equal(t, " --foo", safePromptArg("--foo"))
		assert.Equal(t, " - id: input-responder\n  enabled: true",
			safePromptArg("- id: input-responder\n  enabled: true"))
	})
	t.Run("prompts not starting with '-' are returned verbatim", func(t *testing.T) {
		assert.Equal(t, "Continue the work.", safePromptArg("Continue the work."))
		assert.Equal(t, "", safePromptArg(""))
		assert.Equal(t, "  - leading whitespace is fine", safePromptArg("  - leading whitespace is fine"))
		assert.Equal(t, "## Heading then - dash", safePromptArg("## Heading then - dash"))
	})
}

// TestBuildDirectArgs_SafeForLeadingDashPrompt verifies the integration
// between buildDirectArgs and safePromptArg — the slice passed to
// exec.CommandContext must never contain a raw token starting with '-' as
// the value slot after '-p'.
func TestBuildDirectArgs_SafeForLeadingDashPrompt(t *testing.T) {
	sid := "sess-123"
	args := buildDirectArgs(&sid, "- id: x")
	// Expect: [...sharedFlags, "--resume", "sess-123", "-p", " - id: x"]
	require := func(cond bool, msg string) {
		if !cond {
			t.Fatalf("%s: args=%v", msg, args)
		}
	}
	require(len(args) >= 2 && args[len(args)-2] == "-p", "expected -p flag before prompt")
	require(args[len(args)-1] == " - id: x", "expected prompt to be prefixed with a space to neutralise leading dash")
}
