package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M2-close: Claude Code rejects piped stdin whose decoded JavaScript string
// length (UTF-16 code units) exceeds 10485760 — "Error: piped stdin input
// exceeds 10MB" — measured in the shipped 2.1.283 bundle
// (`var Qi=10485760` … `if(n.length+h.length>Qi)`). The daemon checks the
// same measure before it starts the CLI.
func TestValidatePromptSizeClaudeCap(t *testing.T) {
	require.Equal(t, 10485760, ClaudePromptMaxUTF16Units)

	assert.NoError(t, ValidatePromptSize("claude", strings.Repeat("a", ClaudePromptMaxUTF16Units)))
	err := ValidatePromptSize("claude", strings.Repeat("a", ClaudePromptMaxUTF16Units+1))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "10485760")
	assert.Contains(t, err.Error(), "Claude Code")
	assert.Contains(t, err.Error(), "10485761")

	// UTF-16 units, not bytes: 4 MiB of a 3-byte BMP rune is 12 MiB of UTF-8
	// but only ~4.2M units — accepted, as Claude Code accepts it.
	assert.NoError(t, ValidatePromptSize("claude", strings.Repeat("€", 4<<20)))
	// An astral rune is 2 units: 5,242,881 of them is 10,485,762 units.
	assert.Error(t, ValidatePromptSize("claude", strings.Repeat("😀", ClaudePromptMaxUTF16Units/2+1)))
	// Empty backend means Claude (the default runner).
	assert.Error(t, ValidatePromptSize("", strings.Repeat("a", ClaudePromptMaxUTF16Units+1)))
}

// Codex publishes no client-side prompt cap: codex exec reads stdin to the
// end (read_to_string) and the model's context window is enforced by the
// server. No daemon-side guard is applied for it.
func TestValidatePromptSizeCodexHasNoClientCap(t *testing.T) {
	assert.NoError(t, ValidatePromptSize("codex", strings.Repeat("a", ClaudePromptMaxUTF16Units+1)))
}

func TestUTF16Len(t *testing.T) {
	assert.Equal(t, 0, utf16Len(""))
	assert.Equal(t, 3, utf16Len("abc"))
	assert.Equal(t, 1, utf16Len("€"))
	assert.Equal(t, 2, utf16Len("😀"))
	assert.Equal(t, 2, utf16Len("\xff\xfe"), "each invalid byte decodes to one U+FFFD")
}
