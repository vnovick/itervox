package agent

import (
	"context"
	"io"
	"os/exec"
)

// SetValidateCLIShellFallback toggles the interactive-login-shell fallback
// used by validateCLI. Tests disable it so PATH manipulation is sufficient
// to exercise the "not found" path without the user's real ~/.zshrc
// re-adding the tool onto PATH.
//
// It returns the previous value so the caller can restore it.
func SetValidateCLIShellFallback(v bool) (prev bool) {
	prev = validateCLIShellFallback
	validateCLIShellFallback = v
	return prev
}

// ItervoxAgentEnv / ItervoxAgentExportPrefix expose the marker helpers to the
// blackbox agent_test package.
func ItervoxAgentEnv(extra ...string) []string { return itervoxAgentEnv(extra...) }
func ItervoxAgentExportPrefix() string         { return itervoxAgentExportPrefix() }

// ReadLinesForTest drives the unexported stream reader (readLines) over r
// with a no-op logger, so blackbox tests can measure its allocations without
// a subprocess (CORE-002 peak-allocation rows).
func ReadLinesForTest(r io.Reader, parseFn func([]byte) (StreamEvent, error)) (TurnResult, error) {
	return readLines(context.Background(), nopLogger{}, nil, r, 30000, "test", parseFn)
}

type nopLogger struct{}

func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Debug(string, ...any) {}
func (nopLogger) Warn(string, ...any)  {}

// Stream-reader limits (CORE-002), exported for the blackbox allocation rows.
const (
	StreamDetectBudgetBytes   = streamDetectBudgetBytes
	MaxNonTerminalLineBytes   = maxNonTerminalLineBytes
	StreamReaderOverheadBytes = streamReaderOverheadBytes
)

// FailureTextTruncationMarker is the marker a truncated FailureText carries
// (CORE-028), exported for the blackbox runner rows.
const FailureTextTruncationMarker = failureTextTruncationMarker

// RemoteBashInvocationForTest exposes remoteBashInvocation to the blackbox
// package so a test can drive the wrapper through the faithful fake ssh and
// capture its stderr directly (M1-B2 round 3, m1). It returns the ssh
// argument and the stdin payload.
func RemoteBashInvocationForTest(flags, script string) (string, string) {
	return remoteBashInvocation(flags, script)
}

// StartWithRemoteStdinForTest starts cmd with payload on a stdin held open
// until cmd exits, exactly as the SSH runners do (CORE-155), and returns the
// func to call after cmd.Wait.
func StartWithRemoteStdinForTest(cmd *exec.Cmd, payload string) (join func(), err error) {
	feed, err := attachRemoteStdin(cmd, payload)
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	feed.start()
	return feed.join, nil
}
