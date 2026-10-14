package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vnovick/itervox/internal/config"
)

// TestLoginShellPATHOnlyWhenNotFromShellOrMissing pins when the login shell
// is consulted: when the process was not started from an interactive shell,
// or an agent CLI is missing from the inherited PATH — never otherwise, and
// ITERVOX_LOGIN_SHELL_PATH can force or forbid it.
func TestLoginShellPATHOnlyWhenNotFromShellOrMissing(t *testing.T) {
	cases := []struct {
		name        string
		mode        string
		interactive bool
		missing     []string
		want        bool
	}{
		{"interactive, all found", "auto", true, nil, false},
		{"interactive, claude missing", "auto", true, []string{"claude"}, true},
		{"Finder/launchd start", "auto", false, nil, true},
		{"forced", "always", true, nil, true},
		{"disabled though not from a shell", "never", false, []string{"claude"}, false},
	}
	for _, c := range cases {
		if got, reason := shouldUseLoginShellPATH(c.mode, c.interactive, c.missing); got != c.want || reason == "" {
			t.Errorf("%s: use=%v (reason %q), want %v", c.name, got, reason, c.want)
		}
	}
	for raw, want := range map[string]string{"": "auto", "auto": "auto", "1": "always", "ALWAYS": "always", "0": "never", "off": "never", "bogus": "auto"} {
		if got := loginShellPATHMode(raw); got != want {
			t.Errorf("loginShellPATHMode(%q) = %q, want %q", raw, got, want)
		}
	}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if startedFromInteractiveShell(env(nil), false, false) {
		t.Error("no terminal and no SHLVL (launchd, Finder, systemd) is not an interactive start")
	}
	if !startedFromInteractiveShell(env(map[string]string{"SHLVL": "1"}), false, false) {
		t.Error("SHLVL (nohup from a shell) is an interactive start")
	}
	if !startedFromInteractiveShell(env(nil), false, true) {
		t.Error("a terminal on stderr is an interactive start")
	}

	// End to end through resolvePATH with a fake shell: an interactive start
	// whose CLIs are all present never spawns the shell.
	dir := t.TempDir()
	marker := filepath.Join(dir, "spawned")
	shell := filepath.Join(dir, "sh")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\ntouch '"+marker+"'\necho "+loginShellPATHMarker+"; echo /login; echo "+loginShellPATHMarker+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"PATH": "/usr/bin:/bin", "SHELL": shell}
	res := resolvePATH(env(vars), true, []string{"sh"}, 5*time.Second)
	if _, err := os.Stat(marker); !os.IsNotExist(err) || res.Source != pathSourceInherited {
		t.Fatalf("interactive start with every CLI present must not run the login shell (source %q)", res.Source)
	}
	res = resolvePATH(env(vars), false, []string{"sh"}, 5*time.Second)
	if _, err := os.Stat(marker); err != nil || res.Source != pathSourceLoginShell || !strings.HasPrefix(res.PATH, "/login:") {
		t.Fatalf("a non-shell start must use the login shell PATH first, got %+v", res)
	}
}

// TestLoginShellPATHIsBoundedInTime pins the deadline: an rc file that never
// returns (here: a shell that sleeps, with a background child holding its
// stdout) costs at most the timeout, and the inherited PATH is kept.
func TestLoginShellPATHIsBoundedInTime(t *testing.T) {
	dir := t.TempDir()
	shell := filepath.Join(dir, "slow-shell")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\nsleep 30 &\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"PATH": "/usr/bin:/bin", "SHELL": shell}
	started := time.Now()
	res := resolvePATH(func(k string) string { return vars[k] }, false, nil, 300*time.Millisecond)
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("login shell lookup took %s, want it bounded near the 300ms timeout", elapsed)
	}
	if res.Source != pathSourceInherited || res.PATH != "/usr/bin:/bin" || !strings.Contains(res.Err, "timed out") {
		t.Fatalf("a timed-out lookup must keep the inherited PATH and say why, got %+v", res)
	}
	if !strings.Contains(res.String(), "failed: timed out") {
		t.Fatalf("doctor line should report the failure: %q", res.String())
	}
}

// TestPathBetweenMarkersIgnoresRCNoise pins the parse of an interactive
// shell's output: banners before, between-marker PATH, prompts after.
func TestPathBetweenMarkersIgnoresRCNoise(t *testing.T) {
	out := "Welcome!\n" + loginShellPATHMarker + "\r\n/opt/homebrew/bin:/usr/bin\r\n" + loginShellPATHMarker + "\n$ "
	if got, ok := pathBetweenMarkers([]byte(out)); !ok || got != "/opt/homebrew/bin:/usr/bin" {
		t.Fatalf("pathBetweenMarkers = %q, %v", got, ok)
	}
	if _, ok := pathBetweenMarkers([]byte("no markers here")); ok {
		t.Fatal("output without markers must not parse")
	}
	merged, added := mergePATH("/a:/b", "/b:/c:relative:")
	if merged != "/a:/b:/c" || added != 1 {
		t.Fatalf("mergePATH = %q, +%d", merged, added)
	}
	cfg := &config.Config{}
	cfg.Agent.Command = "FOO=1 claude --model x"
	cfg.Agent.Profiles = map[string]config.AgentProfile{"r": {Command: "/abs/codex"}, "s": {Command: "codex exec"}}
	if got := agentCLINames(cfg); strings.Join(got, ",") != "claude,codex" {
		t.Fatalf("agentCLINames = %v, want [claude codex]", got)
	}
}
