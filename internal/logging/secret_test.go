package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestSecret_LogValue_RedactsValueKeepsKey(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	log.Info("auth wired", "token", Secret("sk-ant-supersecretvalue1234567890XYZ"))

	out := buf.String()
	if !strings.Contains(out, "token=***") {
		t.Errorf("expected redacted value 'token=***' in output, got: %s", out)
	}
	if strings.Contains(out, "sk-ant-supersecretvalue") {
		t.Errorf("raw secret leaked into log output: %s", out)
	}
}

func TestSecret_LogValue_EmptyDoesNotRedact(t *testing.T) {
	// An empty secret typically signals misconfiguration — operator should see
	// "this key is empty", not "this key is redacted".
	if got := Secret("").LogValue().String(); got != "" {
		t.Errorf("empty Secret.LogValue() = %q, want empty", got)
	}
}

func TestSecret_String_RedactsForFmtFormatters(t *testing.T) {
	// The fmt.Sprintf path doesn't go through slog — we still want redaction.
	got := Secret("ghp_abcdef1234567890").String()
	if got != "***" {
		t.Errorf("Secret.String() = %q, want %q", got, "***")
	}

	// Empty stays empty (mirror of LogValue behavior).
	if got := Secret("").String(); got != "" {
		t.Errorf("empty Secret.String() = %q, want empty", got)
	}
}
