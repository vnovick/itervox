package agent

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/vnovick/itervox/internal/logging"
)

// FailureText bounds (CORE-028). FailureText becomes the worker exit cause,
// the retry row's error in the snapshot/SSE JSON, and the body of the
// exhausted-retries tracker comment, so it must stay small no matter how much
// a failing agent printed.
const (
	// maxFailureTextBytes caps the whole assembled FailureText, markers
	// included.
	maxFailureTextBytes = 64 << 10
	// maxAgentFailureBytes caps the agent-reported (parsed stream-json)
	// diagnostic; a terminal result line may be up to maxStreamLineBytes.
	// The head is kept — it is the message itself.
	maxAgentFailureBytes = 16 << 10
	// failureTextTruncationMarker marks every cut, in FailureText and in the
	// runners' tail-bounded stderr capture (tailBuffer).
	failureTextTruncationMarker = "[...truncated...]"
)

// resolveFailureText is the single shared home (CORE-001, fix round 2) for
// classifying a failed turn's FailureText across both the Claude and Codex
// runners. It is called only when result.Failed is already true; callers
// pass the TurnResult's own parsed diagnostic (if any), the raw stderr
// buffer, the cmd.Wait() error, and the readLines() error so the three-way
// classification below cannot diverge between runners (T-53's original
// intent for formatFailureText, restated here now that the CORE-001 fix
// added a second axis of classification on top of it):
//
//   - A prior parsed stream event already produced a diagnostic
//     (parsedFailure != ""): assemble normally via formatFailureText,
//     regardless of how the turn subsequently ended.
//   - readErr wraps context.Canceled (operator pause or daemon shutdown,
//     propagated from the caller's ctx) with no prior diagnostic: return ""
//     — this preserves the pre-CORE-001 behavior byte-for-byte, including
//     NOT falling back to stderr or the exit error from cmd.Wait()
//     (typically "signal: killed", an artifact of our own cancel()-triggered
//     kill rather than a real failure to report — the leak fix-round-1 found
//     when this logic still lived separately in each runner).
//   - Everything else (idle read timeout, scanner error, or a
//     turn_timeout_ms expiry / context.DeadlineExceeded) falls back to the
//     read error's own text before assembling: a turn silently succeeding
//     at the clean-end check (worker.go:577) when it actually hit the hard
//     deadline, an idle timeout, or a scanner error is a real
//     misclassification that must surface.
//
// Every input is passed through logging.RedactString BEFORE the byte bounds
// cut it (CORE-167): agent stderr can carry exported secrets, and FailureText
// reaches the daemon log, the dashboard and a tracker comment. Redacting
// first also means a cut can never leave half a secret that no longer
// matches a pattern.
func resolveFailureText(parsedFailure, rawStderr string, waitErr, readErr error) string {
	parsedFailure = logging.RedactString(parsedFailure)
	rawStderr = logging.RedactString(rawStderr)
	switch {
	case parsedFailure != "":
		return formatFailureText(parsedFailure, rawStderr, waitErr)
	case readErr != nil && errors.Is(readErr, context.Canceled):
		return ""
	default:
		failureDetail := parsedFailure
		if readErr != nil {
			failureDetail = logging.RedactString(readErr.Error())
		}
		return formatFailureText(failureDetail, rawStderr, waitErr)
	}
}

// FailureText layout tokens shared by formatFailureText (which writes them)
// and SplitFailureText (which reads them back).
const (
	failureTextSeparator   = " | "
	failureTextStderrLabel = "stderr: "
)

// SplitFailureText splits text that embeds a runner FailureText (for example
// a worker exit cause "turn 1: <FailureText>") into the agent-reported part
// and the CLI stderr part, inverting formatFailureText's layout (CORE-030).
// The stderr part starts at the first "stderr: " label that opens a segment
// — at the very start, after " | ", or after ": " (the "turn N: " prefix) —
// and runs to the end, because formatFailureText always places stderr last.
// Text without such a label is all agent-side. The label itself is dropped.
func SplitFailureText(s string) (agentFailure, stderr string) {
	for from := 0; ; {
		i := strings.Index(s[from:], failureTextStderrLabel)
		if i < 0 {
			return s, ""
		}
		at := from + i
		before := s[:at]
		switch {
		case at == 0:
			return "", s[len(failureTextStderrLabel):]
		case strings.HasSuffix(before, failureTextSeparator):
			return strings.TrimSuffix(before, failureTextSeparator), s[at+len(failureTextStderrLabel):]
		case strings.HasSuffix(before, ": "):
			return before, s[at+len(failureTextStderrLabel):]
		}
		from = at + len(failureTextStderrLabel)
	}
}

// formatFailureText assembles the FailureText emitted by both Claude and
// Codex runners when the agent process fails. Combines the agent-reported
// failure message, the trimmed stderr buffer, and the wait error into a
// single pipe-separated string so the dashboard / TUI can show all
// available diagnostic context. T-53 (gaps_280426 04.G-07).
//
// Rules (preserve the prior runner-specific behavior):
//   - The agent-reported failure (parsed from stream-json) goes first.
//   - Trimmed stderr is included next when non-empty.
//   - The exit-status error from cmd.Wait is appended ONLY when no
//     higher-quality diagnostic is available (no parsed failure, no stderr).
//   - Bounded (CORE-028): the agent-reported failure keeps its head up to
//     maxAgentFailureBytes, stderr keeps its TAIL (the root cause is at the
//     end) in whatever remains of maxFailureTextBytes, and each cut carries
//     failureTextTruncationMarker. The result never exceeds
//     maxFailureTextBytes.
func formatFailureText(agentFailure, rawStderr string, waitErr error) string {
	const sep, stderrLabel = failureTextSeparator, failureTextStderrLabel
	agentFailure = keepHead(agentFailure, maxAgentFailureBytes)
	stderr := strings.TrimSpace(rawStderr)
	parts := make([]string, 0, 3)
	if agentFailure != "" {
		parts = append(parts, agentFailure)
	}
	if stderr != "" {
		budget := maxFailureTextBytes - len(stderrLabel)
		if agentFailure != "" {
			budget -= len(agentFailure) + len(sep)
		}
		parts = append(parts, stderrLabel+keepTail(stderr, budget))
	}
	if waitErr != nil && agentFailure == "" && stderr == "" {
		parts = append(parts, keepHead("exit: "+logging.RedactString(waitErr.Error()), maxFailureTextBytes))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, sep)
}

// keepHead returns s unchanged when it fits in n bytes; otherwise its first
// bytes, cut on a rune boundary, followed by failureTextTruncationMarker, in
// at most n bytes total.
func keepHead(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := max(n-len(failureTextTruncationMarker), 0)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + failureTextTruncationMarker
}

// keepTail returns s unchanged when it fits in n bytes; otherwise
// failureTextTruncationMarker followed by s's last bytes, cut on a rune
// boundary, in at most n bytes total. A marker already leading s (from the
// tail-bounded stderr capture) is dropped by the cut, so the result carries
// exactly one.
func keepTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	start := len(s) - max(n-len(failureTextTruncationMarker), 0)
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return failureTextTruncationMarker + s[start:]
}
