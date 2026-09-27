package logging

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Sentinel secrets of the shapes CORE-167 requires to be masked. Built by
// concatenation so this file does not itself trip secret scanners.
var (
	sentinelOpenAI    = "sk-proj-" + "Q9x2LmN4pR7tV1wY3zA6bC8dE0fG2hJ5kL7mN9pQ"
	sentinelOpenAIOld = "sk-" + "T3BlbkFJ7yUq2WnX9vZ4cR6pL1mK8jH5gF3dS0aQwErTy"
	sentinelGitHub    = "ghp_" + "aB3dE5fG7hJ9kL1mN3pQ5rS7tU9vW1xY3zA5"
	sentinelBase64    = "u7Hq2Zx9Lm4Kp8Rt1Vw6Yb3Nc5Df0Gj2Ah7Ks9Q" // 40 random base64 chars
	sentinelAWSValue  = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
)

func sentinelStderr() string {
	return strings.Join([]string{
		"Error: authentication failed",
		"export OPENAI_API_KEY=" + sentinelOpenAI,
		"legacy key " + sentinelOpenAIOld + " rejected",
		"git push https://x-access-token:" + sentinelGitHub + "@github.com/o/r.git",
		"session=" + sentinelBase64,
		"AWS_SECRET_ACCESS_KEY=" + sentinelAWSValue,
		"at commit 3f2a9c1e8b7d6054a1b2c3d4e5f60718293a4b5c in /Users/dev/project/main.go:42",
	}, "\n")
}

func allSentinels() []string {
	return []string{sentinelOpenAI, sentinelOpenAIOld, sentinelGitHub, sentinelBase64, sentinelAWSValue}
}

// TestRedactOpenAIKey (CORE-104).
func TestRedactOpenAIKey(t *testing.T) {
	proj := "sk-proj-" + "abcDEF123456ghiJKL789012mnoPQR"
	legacy := "sk-" + strings.Repeat("aB3", 16) // 48 alnum
	ant := "sk-ant-" + "api03-" + strings.Repeat("x", 40)
	cases := []struct {
		name, in, want string
	}{
		{"bare sk-proj", proj, secretMask},
		{"legacy bare sk- 40+ alnum", legacy, secretMask},
		{"bearer-wrapped masked once", "Authorization: Bearer " + proj + " done", secretMask + " done"},
		{"url query keeps prefix", "https://x/?k=" + proj, "https://x/?k=" + secretMask},
		{"anthropic still masked", ant, secretMask},
		{"short sk- left alone", "sk-abc123", "sk-abc123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, RedactString(tc.in))
		})
	}
}

// TestRedactStringMasksStderrSecretShapes (CORE-167): every sentinel shape is
// masked, while the diagnostic context (the error line, a git SHA, a path)
// survives.
func TestRedactStringMasksStderrSecretShapes(t *testing.T) {
	out := RedactString(sentinelStderr())
	for _, s := range allSentinels() {
		assert.NotContains(t, out, s)
	}
	assert.Contains(t, out, "Error: authentication failed")
	assert.Contains(t, out, "3f2a9c1e8b7d6054a1b2c3d4e5f60718293a4b5c", "a hex git SHA is not a secret shape")
	assert.Contains(t, out, "/Users/dev/project/main.go:42")
	assert.Contains(t, out, "AWS_SECRET_ACCESS_KEY="+secretMask, "the variable name stays, the value goes")
}

func TestRedactHighEntropyTokenBoundaries(t *testing.T) {
	keep := []string{
		"3f2a9c1e8b7d6054a1b2c3d4e5f60718293a4b5c",      // git SHA (lower hex)
		"123e4567-e89b-12d3-a456-426614174000",          // UUID
		"TestReadyStaysFreshDuringLongOnTickWithBudget", // identifier, no digits
		"internal/orchestrator/event_loop_budget_test.go",
		"ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", // no lowercase
		strings.Repeat("aA1", 8),               // too short (24)
		// Regressions found by the full suite: a macOS temp path and a
		// boolean flag in help text were masked.
		"/var/folders/x8/jjcw_nvn4yz651mksmn7q3fr0000gn/T/TestDashboardURLNotPrintedWithoutTTYgenerated/001/api-token",
		"cd: /tmp/TestSSHTurnMissingWorkspace2917/001/no-such-workspace: No such file or directory",
		"set ITERVOX_PRINT_TOKEN=1 to print it",
		"SOME_TOKEN=true",
	}
	for _, s := range keep {
		assert.Equal(t, s, RedactString(s), "must not be masked: %s", s)
	}
	mask := []string{
		sentinelBase64,
		"Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5MGFiY2RlZmdoaWprbG1ub3A=", // base64 of text
		"dGhpc0lzQVNlY3JldFRva2VuVmFsdWU5ODc2NTQzMjE",          // base64url-ish
	}
	for _, s := range mask {
		assert.NotContains(t, RedactString("value "+s+" end"), s)
	}
}

// TestRedactingHandlerRedactsErrorAttrs: slog stores an error attribute as a
// KindAny value, which the handler used to forward untouched — so every
// `"error", err` attribute (including the worker's "turn failed" cause,
// which carries agent stderr) bypassed redaction entirely.
func TestRedactingHandlerRedactsErrorAttrs(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(NewRedactingHandler(slog.NewJSONHandler(&buf, nil)))
	log.Warn("worker: turn failed", "error", fmt.Errorf("turn 1: %w", errors.New(sentinelStderr())))
	log.Warn("stringer", "v", stringerOf(sentinelGitHub))
	for _, s := range allSentinels() {
		assert.NotContains(t, buf.String(), s)
	}
	assert.Contains(t, buf.String(), "authentication failed")
}

type stringerOf string

func (s stringerOf) String() string { return string(s) }
