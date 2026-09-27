package agent

import "fmt"

// ClaudePromptMaxUTF16Units is Claude Code's cap on piped stdin (M2-close).
// The CLI decodes stdin as UTF-8 into a JavaScript string and refuses it once
// the string's length — UTF-16 code units, not bytes — exceeds 10485760
// ("Error: piped stdin input exceeds 10MB. Pass large content as a file path
// in your prompt instead."). Measured in the shipped Claude Code 2.1.283
// bundle: `var Qi=10485760` guarding `n.length+h.length>Qi` in the stdin
// reader. Since CORE-156 the daemon hands every prompt to the CLI on stdin.
const ClaudePromptMaxUTF16Units = 10485760

// ValidatePromptSize reports, before an agent CLI is started, whether prompt
// exceeds the backend's documented input cap, with an error that names the
// cap. It never truncates: a cut prompt would silently change the task.
//
//   - claude (and "", the default runner): ClaudePromptMaxUTF16Units.
//   - codex: no client-side cap. `codex exec` reads stdin to the end
//     (codex-cli 0.157.0: "Reading prompt from stdin...", read_to_string)
//     and the model's context window is enforced by the server, which
//     reports it as a turn failure. No daemon-side guard is applied.
func ValidatePromptSize(backend, prompt string) error {
	if backend == "codex" {
		return nil
	}
	if len(prompt) <= ClaudePromptMaxUTF16Units {
		return nil // UTF-16 units never exceed UTF-8 bytes
	}
	if n := utf16Len(prompt); n > ClaudePromptMaxUTF16Units {
		return fmt.Errorf("agent: prompt is %d UTF-16 code units (%d bytes); Claude Code rejects piped input over %d (10 MiB) — "+
			"the run was not started. Shrink the prompt (issue description, profile instructions, prior handoffs) or pass large content as a file path",
			n, len(prompt), ClaudePromptMaxUTF16Units)
	}
	return nil
}

// utf16Len is the length of s as a JavaScript string decoded from UTF-8: one
// unit per BMP rune, two per astral rune, one per invalid byte (U+FFFD).
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}
