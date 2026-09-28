package agent

// White-box tests for unexported helper functions in the agent package.
// These functions contain non-trivial logic that deserves direct coverage.

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- ShellQuote ---

func TestShellQuoteSimple(t *testing.T) {
	assert.Equal(t, "'hello world'", ShellQuote("hello world"))
}

func TestShellQuoteWithSingleQuote(t *testing.T) {
	// Single quotes inside the string must be escaped.
	assert.Equal(t, "'it'\\''s fine'", ShellQuote("it's fine"))
}

func TestShellQuoteEmpty(t *testing.T) {
	assert.Equal(t, "''", ShellQuote(""))
}

func TestShellQuoteSpecialChars(t *testing.T) {
	// Backticks, $, ! etc. are safe inside single quotes.
	got := ShellQuote("`echo $HOME`")
	assert.Equal(t, "'`echo $HOME`'", got)
}

// --- buildShellCmd ---

func TestBuildShellCmdNewSession(t *testing.T) {
	cmd := buildShellCmd("claude", nil, "do the thing", localPromptRedirect, PermissionBypass)
	assert.Contains(t, cmd, "claude")
	assert.Contains(t, cmd, "--output-format stream-json")
	// CORE-156: the prompt is never in the command; -p reads it from stdin.
	assert.True(t, strings.HasSuffix(cmd, " -p < /dev/fd/3"), "got %q", cmd)
	assert.NotContains(t, cmd, "do the thing")
	assert.NotContains(t, cmd, "--resume")
}

func TestBuildShellCmdResumeWithoutPrompt(t *testing.T) {
	id := "sess-abc"
	cmd := buildShellCmd("claude", &id, "", "", PermissionBypass)
	assert.Contains(t, cmd, "--resume")
	assert.Contains(t, cmd, "sess-abc")
	// Resume without a prompt should not include -p.
	assert.NotContains(t, cmd, " -p")
}

func TestBuildShellCmdResumeWithPrompt(t *testing.T) {
	id := "sess-abc"
	cmd := buildShellCmd("claude", &id, "the user reply", localPromptRedirect, PermissionBypass)
	assert.Contains(t, cmd, "--resume")
	assert.Contains(t, cmd, "sess-abc")
	// Resume WITH a prompt (input-required flow) should include both flags;
	// the reply itself arrives on stdin (CORE-156).
	assert.True(t, strings.HasSuffix(cmd, " -p < /dev/fd/3"), "got %q", cmd)
	assert.NotContains(t, cmd, "the user reply")
}

func TestBuildDirectArgsResumeWithoutPrompt(t *testing.T) {
	id := "sess-abc"
	args := buildDirectArgs(&id, "", PermissionBypass)
	assert.Equal(t, []string{
		"--output-format", "stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
		"--resume", "sess-abc",
	}, args)
}

func TestBuildDirectArgsResumeWithPrompt(t *testing.T) {
	id := "sess-abc"
	args := buildDirectArgs(&id, "the user reply", PermissionBypass)
	assert.Equal(t, []string{
		"--output-format", "stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
		"--resume", "sess-abc",
		"-p",
	}, args, "the reply arrives on stdin, never in argv (CORE-156)")
}

// Regression: an empty command must not produce a shell line that starts with
// `--output-format`. Without the fallback, bash -lc would interpret the flag
// as the command name and print `--output-format: command not found`.
func TestBuildShellCmdEmptyCommandFallsBackToClaude(t *testing.T) {
	cmd := buildShellCmd("", nil, "do the thing", localPromptRedirect, PermissionBypass)
	// Must not start with the flag (which would happen if leading whitespace
	// was the only thing before --output-format).
	assert.False(t, strings.HasPrefix(strings.TrimSpace(cmd), "--"),
		"shell command must not start with a flag; got: %q", cmd)
	assert.True(t, strings.HasPrefix(strings.TrimSpace(cmd), "claude "),
		"empty command should fall back to 'claude'; got: %q", cmd)
}

func TestBuildShellCmdWhitespaceCommandFallsBackToClaude(t *testing.T) {
	cmd := buildShellCmd("   ", nil, "do the thing", localPromptRedirect, PermissionBypass)
	assert.True(t, strings.HasPrefix(strings.TrimSpace(cmd), "claude "),
		"whitespace-only command should fall back to 'claude'; got: %q", cmd)
}

func TestBuildShellCmdEmptySessionID(t *testing.T) {
	id := ""
	cmd := buildShellCmd("claude", &id, "use prompt", localPromptRedirect, PermissionBypass)
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
	cmd := buildCodexShellCmd("codex", nil, "/workspace", localPromptRedirect, PermissionBypass)
	assert.Contains(t, cmd, "codex")
	assert.Contains(t, cmd, "-C")
	assert.Contains(t, cmd, "/workspace")
	assert.Contains(t, cmd, " exec")
	assert.Contains(t, cmd, "--json")
	// CORE-156: the prompt argument is "-" (read stdin).
	assert.True(t, strings.HasSuffix(cmd, " --skip-git-repo-check - < /dev/fd/3"), "got %q", cmd)
	assert.NotContains(t, cmd, "resume")
}

func TestBuildCodexShellCmdResume(t *testing.T) {
	id := "sess-xyz"
	cmd := buildCodexShellCmd("codex", &id, "", localPromptRedirect, PermissionBypass)
	assert.Contains(t, cmd, "resume")
	assert.True(t, strings.HasSuffix(cmd, " 'sess-xyz' - < /dev/fd/3"), "got %q", cmd)
}

func TestBuildCodexShellCmdNoWorkspace(t *testing.T) {
	cmd := buildCodexShellCmd("codex", nil, "", localPromptRedirect, PermissionBypass)
	assert.NotContains(t, cmd, "-C")
}

func TestBuildCodexShellCmdEmptySessionID(t *testing.T) {
	id := ""
	cmd := buildCodexShellCmd("codex", &id, "", localPromptRedirect, PermissionBypass)
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
	// The tar header name → session ID formula must match the local
	// readJSONLFileMultiWith formula. Call the shared production function
	// (sessionIDFromFilename) directly, rather than re-implementing its
	// formula here, and assert fixed literal expected values.
	cases := []struct{ name, want string }{
		{"abc123.jsonl", "abc123"},
		{"./abc123.jsonl", "abc123"},      // tar may include "./" prefix
		{"subdir/abc123.jsonl", "abc123"}, // tar -C strips dir but test robustness
	}
	for _, c := range cases {
		got := sessionIDFromFilename(c.name)
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

// TestBuildArgsCarryNoPrompt (CORE-156) replaces the safePromptArg tests:
// the prompt is no longer an argument at all, so a prompt that starts with
// '-' (a markdown list, YAML) can no longer be parsed as a flag, and no
// argument can reach Linux's MAX_ARG_STRLEN whatever the prompt's size.
func TestBuildArgsCarryNoPrompt(t *testing.T) {
	sid := "sess-123"
	prompt := "- id: x\n" + strings.Repeat("y", 200<<10)
	for name, args := range map[string][]string{
		"claude first":  buildDirectArgs(nil, prompt, PermissionBypass),
		"claude resume": buildDirectArgs(&sid, prompt, PermissionBypass),
		"codex first":   buildCodexDirectArgs(nil, "/ws", PermissionBypass),
		"codex resume":  buildCodexDirectArgs(&sid, "/ws", PermissionBypass),
	} {
		for _, a := range args {
			assert.Less(t, len(a), 100, "%s: argument %q is prompt-sized", name, a[:min(len(a), 40)])
			assert.NotContains(t, a, "id: x", "%s: prompt bytes in argv", name)
		}
		if strings.HasPrefix(name, "claude") {
			assert.Equal(t, "-p", args[len(args)-1], "%s: -p with no prompt argument reads stdin", name)
		} else {
			assert.Equal(t, "-", args[len(args)-1], "%s: the \"-\" prompt argument reads stdin", name)
		}
	}
}

// TestShellQuoteKeepsBackslashAndQuoteOutsideQuotedSegments pins the
// fish-safe form (fix round 1, C1): fish interprets \' and \\ inside single
// quotes, so both characters are emitted outside every quoted segment.
func TestShellQuoteKeepsBackslashAndQuoteOutsideQuotedSegments(t *testing.T) {
	assert.Equal(t, `'a'\\'b'\''c'`, ShellQuote(`a\b'c`))
	assert.Equal(t, `'x'\\''\\'y'`, ShellQuote(`x\\y`))
}

// TestShellQuoteWorkerCasesUnion (CORE-111): ShellQuote replaced
// orchestrator/worker.go's private copy ("'" + ReplaceAll(v, "'", `'\”`) +
// "'"), used for the itervox shim's exec path and the env exports. For every
// input without a backslash the output is byte-identical to that copy, and
// for every input — backslashes included — POSIX sh reads back the original
// value.
func TestShellQuoteWorkerCasesUnion(t *testing.T) {
	oldWorkerQuote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'" }
	cases := []string{
		"", "plain", "/usr/local/bin/itervox", "/Users/me/My Apps/itervox",
		"it's", "'", "''", "a'b'c", "$HOME `id` $(id)", "tab\tand\nnewline",
		`back\slash`, `trailing\`, `\'mixed'\`,
	}
	for _, v := range cases {
		got := ShellQuote(v)
		if !strings.Contains(v, `\`) && got != oldWorkerQuote(v) {
			t.Errorf("ShellQuote(%q) = %s; worker copy gave %s", v, got, oldWorkerQuote(v))
		}
		out, err := exec.Command("sh", "-c", "printf %s "+got).Output()
		if err != nil {
			t.Fatalf("sh rejected ShellQuote(%q) = %s: %v", v, got, err)
		}
		if string(out) != v {
			t.Errorf("sh read back %q for ShellQuote(%q) = %s", out, v, got)
		}
	}
}
