package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/vnovick/itervox/internal/logging"
)

func TestLogFormatSelection(t *testing.T) {
	cases := []struct {
		name    string
		flagVal string
		envVal  string
		want    string
	}{
		{name: "flag json", flagVal: "json", envVal: "", want: "json"},
		{name: "env json", flagVal: "", envVal: "json", want: "json"},
		{name: "flag beats env", flagVal: "text", envVal: "json", want: "text"},
		{name: "flag json beats env text", flagVal: "json", envVal: "text", want: "json"},
		{name: "neither set defaults to text", flagVal: "", envVal: "", want: "text"},
		{name: "invalid flag falls back to text", flagVal: "yaml", envVal: "", want: "text"},
		{name: "invalid env falls back to text", flagVal: "", envVal: "xml", want: "text"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveLogFormat(tc.flagVal, tc.envVal)
			if got != tc.want {
				t.Errorf("resolveLogFormat(%q, %q) = %q; want %q", tc.flagVal, tc.envVal, got, tc.want)
			}
		})
	}
}

func TestNewFileLogHandlerJSON(t *testing.T) {
	var buf bytes.Buffer
	h := newFileLogHandler(&buf, slog.LevelInfo, "json")
	log := slog.New(h)
	log.Info("hello world", "issue", "ENG-1")

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("newFileLogHandler(json) output did not parse as JSON: %v\noutput: %s", err, buf.String())
	}
	if decoded["msg"] != "hello world" {
		t.Errorf("decoded msg = %v; want %q", decoded["msg"], "hello world")
	}
	if decoded["level"] != "INFO" {
		t.Errorf("decoded level = %v; want %q", decoded["level"], "INFO")
	}
	if decoded["issue"] != "ENG-1" {
		t.Errorf("decoded issue = %v; want %q", decoded["issue"], "ENG-1")
	}
}

func TestNewFileLogHandlerText(t *testing.T) {
	var buf bytes.Buffer
	h := newFileLogHandler(&buf, slog.LevelInfo, "text")
	log := slog.New(h)
	log.Info("hello world")

	out := buf.String()
	if !strings.Contains(out, "msg=\"hello world\"") {
		t.Errorf("newFileLogHandler(text) output = %q; want logfmt msg attr", out)
	}
	// Must not be valid JSON — this is the format differentiator.
	var decoded map[string]any
	if err := json.Unmarshal([]byte(out), &decoded); err == nil {
		t.Errorf("newFileLogHandler(text) output unexpectedly parsed as JSON: %s", out)
	}
}

func TestPostStartupHandler(t *testing.T) {
	cases := []struct {
		name         string
		ttyAvailable bool
		wantStderr   bool
	}{
		{name: "tty available redirects to file only", ttyAvailable: true, wantStderr: false},
		{name: "no tty keeps stderr+file fanout", ttyAvailable: false, wantStderr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var fileBuf, stderrBuf bytes.Buffer
			stderrHandler := slog.NewTextHandler(&stderrBuf, &slog.HandlerOptions{Level: slog.LevelInfo})

			h := postStartupHandler(&fileBuf, slog.LevelInfo, "text", stderrHandler, tc.ttyAvailable)
			log := slog.New(h)
			log.Info("post-startup line", "issue", "ENG-1")

			if !strings.Contains(fileBuf.String(), "post-startup line") {
				t.Errorf("file sink missing post-startup line in both modes; got %q", fileBuf.String())
			}
			gotStderr := strings.Contains(stderrBuf.String(), "post-startup line")
			if gotStderr != tc.wantStderr {
				t.Errorf("ttyAvailable=%v: stderr got line=%v; want %v (stderr=%q)", tc.ttyAvailable, gotStderr, tc.wantStderr, stderrBuf.String())
			}
		})
	}
}

func TestPostStartupHandler_RedactsBothSinksWhenHeadless(t *testing.T) {
	var fileBuf, stderrBuf bytes.Buffer
	stderrHandler := slog.NewTextHandler(&stderrBuf, &slog.HandlerOptions{Level: slog.LevelInfo})

	h := postStartupHandler(&fileBuf, slog.LevelInfo, "text", stderrHandler, false)
	log := slog.New(h)
	log.Info("saw token sk-ant-supersecret-1234567890XYZABCD0123456789 in env")

	if strings.Contains(fileBuf.String(), "sk-ant-supersecret") {
		t.Errorf("headless file sink leaked secret: %s", fileBuf.String())
	}
	if strings.Contains(stderrBuf.String(), "sk-ant-supersecret") {
		t.Errorf("headless stderr sink leaked secret: %s", stderrBuf.String())
	}
}

func TestJSONSinkIsRedacted(t *testing.T) {
	var buf bytes.Buffer
	h := logging.NewRedactingHandler(newFileLogHandler(&buf, slog.LevelInfo, "json"))
	log := slog.New(h)
	log.Info("saw token sk-ant-supersecret-1234567890XYZABCD0123456789 in env",
		"raw", "carries lin_api_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa here")

	out := buf.String()
	if strings.Contains(out, "sk-ant-supersecret") {
		t.Errorf("redacted json sink leaked anthropic key: %s", out)
	}
	if strings.Contains(out, "lin_api_aaaa") {
		t.Errorf("redacted json sink leaked linear key: %s", out)
	}

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("redacted json sink output did not parse as JSON: %v\noutput: %s", err, out)
	}
}
