package agent

// CORE-001 fix round 2 (finding G4): resolveFailureText is the single
// shared home for the FailureText classification rule that both
// ClaudeRunner and CodexRunner now call, instead of each carrying its own
// copy. These tests pin all three branches directly at the helper so the
// guarantee cannot regress in one runner without a test catching it here,
// independent of the two runners' own (real-subprocess) tests in
// turn_cancel_test.go.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveFailureText_PriorDiagnosticAssemblesNormally(t *testing.T) {
	// A prior parsed stream event (e.g. a Claude/Codex "result"/"turn.failed"
	// event with IsError=true) already produced a diagnostic. It must be
	// used regardless of how the turn subsequently ended, and stderr/waitErr
	// are still appended per formatFailureText's normal rules.
	got := resolveFailureText("model reported: context length exceeded", "warning: low disk", errors.New("exit status 1"), nil)
	assert.Equal(t, "model reported: context length exceeded | stderr: warning: low disk", got)
}

func TestResolveFailureText_PriorDiagnosticSurvivesCancellation(t *testing.T) {
	// A prior diagnostic takes precedence even when the turn was ALSO
	// cancelled afterward (e.g. a parsed failure followed by an operator
	// pause before cmd.Wait() returns) — the cancellation carve-out below
	// only applies when there is no prior diagnostic.
	got := resolveFailureText("rate limited", "", nil, context.Canceled)
	assert.Equal(t, "rate limited", got)
}

func TestResolveFailureText_ContextCanceledWithNoPriorDiagnosticIsEmpty(t *testing.T) {
	// The exact regression fix-round-1 found: cancel()'s SIGKILL makes
	// cmd.Wait() return a "signal: killed"-shaped error, and formatFailureText
	// would otherwise fall back to it (its own waitErr rule) once agentFailure
	// and stderr are both empty. An operator pause / daemon shutdown
	// (context.Canceled) with no prior diagnostic must still produce an
	// empty FailureText — matching the pre-CORE-001 behavior byte-for-byte —
	// even though waitErr is non-nil here.
	waitErr := errors.New("signal: killed")
	got := resolveFailureText("", "", waitErr, context.Canceled)
	assert.Empty(t, got, "context.Canceled with no prior diagnostic must produce an empty FailureText even when waitErr reports the kill")
}

func TestResolveFailureText_ContextCanceledIgnoresStderrToo(t *testing.T) {
	// The carve-out is a full skip of formatFailureText, not just of the
	// waitErr fallback: stderr noise incidental to the cancellation is also
	// discarded, matching the pre-CORE-001 behavior where the whole
	// assembly block was skipped for a bare readErr.
	got := resolveFailureText("", "some stderr chatter", errors.New("signal: killed"), context.Canceled)
	assert.Empty(t, got)
}

func TestResolveFailureText_WrappedContextCanceledIsRecognized(t *testing.T) {
	// readLines can return a wrapped error; errors.Is must still match.
	wrapped := fmt.Errorf("agent: turn aborted: %w", context.Canceled)
	got := resolveFailureText("", "", nil, wrapped)
	assert.Empty(t, got)
}

func TestResolveFailureText_DeadlineExceededFallsBackToReadErrText(t *testing.T) {
	// A turn_timeout_ms expiry (context.DeadlineExceeded) must NOT be
	// silently classified as a clean session end (worker.go:577) — it falls
	// back to the read error's own text, which mentions "deadline".
	got := resolveFailureText("", "", nil, context.DeadlineExceeded)
	assert.Equal(t, "context deadline exceeded", got)
	assert.Contains(t, got, "deadline")
}

func TestResolveFailureText_IdleReadTimeoutFallsBackToReadErrText(t *testing.T) {
	readErr := errors.New("agent: read timeout after 1500ms idle")
	got := resolveFailureText("", "", nil, readErr)
	assert.Equal(t, "agent: read timeout after 1500ms idle", got)
}

func TestResolveFailureText_ScannerErrorFallsBackToReadErrText(t *testing.T) {
	readErr := errors.New("bufio.Scanner: token too long")
	got := resolveFailureText("", "", nil, readErr)
	assert.Equal(t, "bufio.Scanner: token too long", got)
}

func TestResolveFailureText_NoReadErrFallsBackToWaitErrViaFormatFailureText(t *testing.T) {
	// result.Failed can also become true purely from cmd.Wait() failing with
	// no read error at all (e.g. the agent exited non-zero after a clean
	// EOF). There is no readErr to fall back to, so formatFailureText's own
	// waitErr rule takes over, unaffected by the CORE-001 classification.
	got := resolveFailureText("", "", errors.New("exit status 1"), nil)
	assert.Equal(t, "exit: exit status 1", got)
}

func TestResolveFailureText_NothingAtAllIsEmpty(t *testing.T) {
	got := resolveFailureText("", "", nil, nil)
	assert.Empty(t, got)
}
