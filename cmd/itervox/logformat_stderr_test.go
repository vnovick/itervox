package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// CORE-059 — --log-format json also applies to stderr when no terminal is
// attached (systemd, containers), through the same RedactingHandler as the
// file sink. With a terminal (the TUI path) stderr stays human text.

func TestHeadlessStderrHonoursJSONLogFormat(t *testing.T) {
	var stderrBuf, fileBuf bytes.Buffer
	boot, stderrOnly := bootLogHandlers(&stderrBuf, &fileBuf, slog.LevelInfo, "json", true)
	slog.New(boot).Info("boot line", "issue", "ENG-1")
	slog.New(boot).Warn("boot warning", "n", 3)
	// ttyAvailable=false: statusui will not start, the headless fanout stays.
	post := slog.New(postStartupHandler(&fileBuf, slog.LevelInfo, "json", stderrOnly, false))
	post.Info("post-startup line", "at", time.Unix(0, 0).UTC())
	post.Error("post-startup error", "err", "boom")

	lines := strings.Split(strings.TrimSpace(stderrBuf.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("stderr lines = %d, want 4:\n%s", len(lines), stderrBuf.String())
	}
	for _, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("stderr line is not JSON: %v\n%s", err, line)
		}
		if rec["msg"] == nil || rec["level"] == nil || rec["time"] == nil {
			t.Errorf("stderr JSON line lacks msg/level/time: %s", line)
		}
	}

	// A terminal keeps human text on stderr, whatever --log-format says.
	var ttyStderr, ttyFile bytes.Buffer
	bootTTY, _ := bootLogHandlers(&ttyStderr, &ttyFile, slog.LevelInfo, "json", false)
	slog.New(bootTTY).Info("interactive line")
	if strings.HasPrefix(strings.TrimSpace(ttyStderr.String()), "{") {
		t.Errorf("with a terminal stderr must stay text, got %q", ttyStderr.String())
	}
	if !strings.Contains(ttyFile.String(), `"msg":"interactive line"`) {
		t.Errorf("the file sink still honours json with a terminal: %q", ttyFile.String())
	}

	// Headless text stays text.
	var txtStderr, txtFile bytes.Buffer
	bootTxt, _ := bootLogHandlers(&txtStderr, &txtFile, slog.LevelInfo, "text", true)
	slog.New(bootTxt).Info("text line")
	if strings.HasPrefix(strings.TrimSpace(txtStderr.String()), "{") {
		t.Errorf("--log-format text must not produce JSON on stderr: %q", txtStderr.String())
	}
}

func TestHeadlessJSONStderrIsRedacted(t *testing.T) {
	const token = "abcdefghijklmnopqrstuvwxyz0123456789"
	var stderrBuf, fileBuf bytes.Buffer
	boot, stderrOnly := bootLogHandlers(&stderrBuf, &fileBuf, slog.LevelInfo, "json", true)
	slog.New(boot).Info("calling tracker", "authorization", "Bearer "+token)
	post := slog.New(postStartupHandler(&fileBuf, slog.LevelInfo, "json", stderrOnly, false))
	post.Info("header dump Authorization: Bearer "+token, "linear", "lin_api_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	out := stderrBuf.String()
	if strings.Contains(out, token) || strings.Contains(out, "lin_api_aaaa") {
		t.Fatalf("headless JSON stderr leaked a secret:\n%s", out)
	}
	if strings.Count(out, "***") < 3 {
		t.Errorf("want every secret rendered as ***:\n%s", out)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("redacted stderr line is not JSON: %v\n%s", err, line)
		}
	}
}

// TestHeadlessDaemonStderrIsJSON runs the real daemon headless with
// --log-format json: every slog record it writes to stderr is a JSON object
// (lines written with fmt.Fprint* outside slog are listed and allowed), and
// no charmlog text line appears.
func TestHeadlessDaemonStderrIsJSON(t *testing.T) {
	p := newDaemonProject(t)
	_, stderrPath := p.startHeadless(t, []string{"--log-format", "json"})
	waitFor(t, 20*time.Second, "dashboard URL written", func() bool {
		return countFileOccurrences(dashboardURLFilePath(p.Workflow), "http://") > 0
	})
	p.stopDaemonByPIDFile(t)

	jsonLines, other := 0, []string{}
	for _, line := range strings.Split(readTrim(t, stderrPath), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil && rec["msg"] != nil {
			jsonLines++
			continue
		}
		other = append(other, line)
	}
	for _, line := range other {
		// The one known non-slog write: statusui's own fmt.Fprintln when it
		// refuses to start without a terminal.
		if !strings.HasPrefix(line, "statusui: not starting TUI:") {
			t.Errorf("non-JSON stderr line: %q", line)
		}
	}
	if jsonLines < 5 {
		t.Errorf("only %d JSON slog lines on stderr", jsonLines)
	}
	t.Logf("%d JSON slog lines, %d allowed non-slog lines", jsonLines, len(other))
}

// M4-close D8 — records emitted before bootLogHandlers (the stale PID file
// reclaim in claimPIDFile, the invalid --log-format warning) used Go's
// default text logger: not JSON on a headless daemon and not behind the
// RedactingHandler. The early handler now covers them.
func TestHeadlessEarlyLogLinesAreJSON(t *testing.T) {
	p := newDaemonProject(t)
	pidPath, err := pidFilePath(p.Workflow)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(pidPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidPath, []byte("not-a-pid-record\n"), 0o600); err != nil { // stale → reclaimed
		t.Fatal(err)
	}
	_, stderrPath := p.startHeadless(t, []string{"--log-format", "json"})
	waitFor(t, 20*time.Second, "dashboard URL written", func() bool {
		return countFileOccurrences(dashboardURLFilePath(p.Workflow), "http://") > 0
	})
	p.stopDaemonByPIDFile(t)

	found := false
	for _, line := range strings.Split(readTrim(t, stderrPath), "\n") {
		if !strings.Contains(line, "reclaiming stale PID file") {
			continue
		}
		found = true
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil || rec["msg"] == nil {
			t.Fatalf("early (pre-bootLogHandlers) stderr line is not a JSON slog record: %q", line)
		}
	}
	if !found {
		t.Fatalf("no stale-PID reclaim line on stderr:\n%s", readTrim(t, stderrPath))
	}
}

// TestEarlyLogHandlerRedactsAndHonoursFormat pins the early handler itself:
// redacting, JSON when headless with json requested (flag or env; the flag
// wins), text otherwise.
func TestEarlyLogHandlerRedactsAndHonoursFormat(t *testing.T) {
	const token = "abcdefghijklmnopqrstuvwxyz0123456789"
	for _, tc := range []struct {
		args     []string
		env      string
		headless bool
		json     bool
	}{
		{[]string{"itervox", "--log-format", "json"}, "", true, true},
		{[]string{"itervox", "-log-format=json"}, "", true, true},
		{[]string{"itervox"}, "json", true, true},
		{[]string{"itervox", "--log-format=text"}, "json", true, false},
		{[]string{"itervox", "--log-format", "json"}, "", false, false},
	} {
		var buf bytes.Buffer
		slog.New(earlyLogHandler(&buf, tc.args, tc.env, tc.headless)).Info("reclaiming", "authorization", "Bearer "+token)
		out := buf.String()
		if strings.Contains(out, token) {
			t.Fatalf("%v: early handler leaked a secret: %s", tc.args, out)
		}
		var rec map[string]any
		isJSON := json.Unmarshal([]byte(strings.TrimSpace(out)), &rec) == nil
		if isJSON != tc.json {
			t.Errorf("%v env=%q headless=%v: json=%v, want %v: %s", tc.args, tc.env, tc.headless, isJSON, tc.json, out)
		}
	}
}
