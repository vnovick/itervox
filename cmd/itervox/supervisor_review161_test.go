//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Regression tests for the PR #161 review of #119.

// writeRegistryFixture writes a registry record for pid in dir and a
// daemon.pid for wf naming pid, with or without the flock marker.
func writeRegistryFixture(t *testing.T, regDir, wf string, pid int, marked bool) string {
	t.Helper()
	if err := os.MkdirAll(regDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pidPath, _ := pidFilePath(wf)
	if err := os.MkdirAll(filepath.Join(filepath.Dir(pidPath), "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf("%d\t%s", pid, wf)
	if marked {
		line += "\t" + pidRecordLockMarker
	}
	if err := os.WriteFile(pidPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := daemonRecord{SchemaVersion: daemonRecordSchemaVersion, PID: pid, Workflow: wf, ProjectDir: filepath.Dir(wf)}
	data, _ := json.Marshal(rec)
	path := filepath.Join(regDir, strconv.Itoa(pid)+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return pidPath
}

// TestRegistryRejectsRecycledPID (review finding 1): after a SIGKILL leaves
// the registry record and a lock-marked daemon.pid behind, an unrelated live
// process that received the pid (here: the test process itself) must not
// make the dead daemon look alive. A marked record is kept only while its
// pid lock is held; an unmarked (pre-lock) record keeps the pid-probe rule.
func TestRegistryRejectsRecycledPID(t *testing.T) {
	if !pidLockSupported {
		t.Skip("no flock on this platform")
	}
	pid := os.Getpid()

	t.Run("marked record, lock free: pruned", func(t *testing.T) {
		regDir := filepath.Join(t.TempDir(), "daemons")
		wf := filepath.Join(t.TempDir(), "WORKFLOW.md")
		writeRegistryFixture(t, regDir, wf, pid, true)
		if got := readDaemonRegistry(regDir); len(got) != 0 {
			t.Fatalf("a recycled pid with a free lock must not be listed, got %+v", got)
		}
		if _, err := os.Stat(filepath.Join(regDir, strconv.Itoa(pid)+".json")); !os.IsNotExist(err) {
			t.Fatalf("the stale record must be pruned, stat err=%v", err)
		}
	})

	t.Run("marked record, lock held: listed", func(t *testing.T) {
		regDir := filepath.Join(t.TempDir(), "daemons")
		wf := filepath.Join(t.TempDir(), "WORKFLOW.md")
		pidPath := writeRegistryFixture(t, regDir, wf, pid, true)
		release, acquired, err := tryLockPIDFile(pidLockPath(pidPath))
		if err != nil || !acquired {
			t.Fatalf("take lock: %v %v", acquired, err)
		}
		defer release()
		if got := readDaemonRegistry(regDir); len(got) != 1 || got[0].PID != pid {
			t.Fatalf("a record whose lock is held must be listed, got %+v", got)
		}
	})

	t.Run("legacy unmarked record: pid probe", func(t *testing.T) {
		regDir := filepath.Join(t.TempDir(), "daemons")
		wf := filepath.Join(t.TempDir(), "WORKFLOW.md")
		writeRegistryFixture(t, regDir, wf, pid, false)
		if got := readDaemonRegistry(regDir); len(got) != 1 {
			t.Fatalf("a legacy record with a live pid keeps the pid-probe rule, got %+v", got)
		}
	})

	t.Run("process scan applies the same rule", func(t *testing.T) {
		dir := t.TempDir()
		wf := filepath.Join(dir, "WORKFLOW.md")
		writeRegistryFixture(t, filepath.Join(t.TempDir(), "daemons"), wf, pid, true)
		if rec, ok := scannedDaemonRecord(pid, func(int) string { return dir }); ok {
			t.Fatalf("process scan must not report a lock-free marked pid file, got %+v", rec)
		}
	})
}

// TestDaemonRecordPathsAreAbsolute (review finding 2): a daemon started with
// a relative --logs-dir publishes absolute logs_dir and token_file, resolved
// against its working directory, so a supervisor running elsewhere can read
// the generated token.
func TestDaemonRecordPathsAreAbsolute(t *testing.T) {
	p := newDaemonProject(t)
	home := filepath.Join(p.Dir, "home")
	p.startHeadless(t, []string{"--logs-dir", "relative-logs"})
	pid := p.waitForRegistered(t, home)
	var rec daemonRecord
	if err := json.Unmarshal([]byte(readTrim(t, registryFile(home, pid))), &rec); err != nil {
		t.Fatal(err)
	}
	wantLogs := filepath.Join(p.Dir, "relative-logs")
	if rec.LogsDir != wantLogs || rec.TokenFile != filepath.Join(wantLogs, apiTokenFileName) {
		t.Fatalf("logs_dir/token_file = %q/%q, want absolute under %q", rec.LogsDir, rec.TokenFile, wantLogs)
	}
	for _, path := range []string{rec.Workflow, rec.ProjectDir, rec.LogsDir, rec.TokenFile, rec.Heartbeat, rec.Executable} {
		if !filepath.IsAbs(path) {
			t.Errorf("published path %q is not absolute", path)
		}
	}
	waitFor(t, 30*time.Second, "generated token at token_file", func() bool {
		_, err := os.Stat(rec.TokenFile)
		return err == nil
	})
	p.stopDaemonByPIDFile(t)
}

// TestLoginShellPATHCompatibleShells (review finding 3): the login-shell
// lookup works for every supported account shell, each configured through
// the startup file its users put PATH in. csh/tcsh reject `-l -i -c` (macOS
// /bin/csh and /bin/tcsh are both tcsh), so they get their own invocation.
// Shells not installed here are skipped; the argv selection is pinned for
// all of them.
func TestLoginShellPATHCompatibleShells(t *testing.T) {
	for shell, want := range map[string][]string{
		"/bin/zsh":            {"-l", "-i", "-c"},
		"/bin/bash":           {"-l", "-i", "-c"},
		"/usr/local/bin/fish": {"-l", "-i", "-c"},
		"/bin/sh":             {"-l", "-i", "-c"},
		"/bin/tcsh":           {"-c"},
		"/bin/csh":            {"-c"},
		"-tcsh":               {"-c"},
	} {
		argv := loginShellArgs(shell)
		if strings.Join(argv[:len(argv)-1], " ") != strings.Join(want, " ") {
			t.Errorf("loginShellArgs(%q) = %q, want flags %q", shell, argv, want)
		}
	}
	if s := loginShellArgs("/bin/tcsh"); !strings.Contains(s[len(s)-1], ".login") {
		t.Errorf("csh-family script must source .login, got %q", s[len(s)-1])
	}

	cases := []struct {
		shell, rcFile, rcLine string
	}{
		{"zsh", ".zshrc", "export PATH=\"%s:$PATH\"\n"},
		{"bash", ".bash_profile", "export PATH=\"%s:$PATH\"\n"},
		{"dash", ".profile", "PATH=\"%s:$PATH\"; export PATH\n"},
		{"fish", ".config/fish/config.fish", "set -gx PATH \"%s\" $PATH\n"},
		{"tcsh", ".tcshrc", "setenv PATH \"%s:${PATH}\"\n"},
		{"tcsh", ".login", "setenv PATH \"%s:${PATH}\"\n"},
		{"csh", ".cshrc", "setenv PATH \"%s:${PATH}\"\n"},
		{"csh", ".login", "setenv PATH \"%s:${PATH}\"\n"},
	}
	for _, c := range cases {
		t.Run(c.shell+"/"+c.rcFile, func(t *testing.T) {
			shell, err := exec.LookPath(c.shell)
			if err != nil {
				t.Skipf("%s not installed: verify on a Mac (see the PR notes)", c.shell)
			}
			home := t.TempDir()
			bin := filepath.Join(t.TempDir(), "agent-bin")
			if err := os.MkdirAll(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			rc := filepath.Join(home, c.rcFile)
			if err := os.MkdirAll(filepath.Dir(rc), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(rc, []byte(fmt.Sprintf(c.rcLine, bin)), 0o644); err != nil {
				t.Fatal(err)
			}
			vars := map[string]string{"PATH": "/usr/bin:/bin", "SHELL": shell, "HOME": home}
			res := withEnv(t, vars, func() pathResolution {
				return resolvePATH(func(k string) string { return vars[k] }, false, []string{"claude"}, 10*time.Second)
			})
			if res.Source != pathSourceLoginShell || len(res.Missing) != 0 {
				t.Fatalf("%s: claude from %s not found via the login shell: %+v", c.shell, c.rcFile, res)
			}
		})
	}
}

// withEnv runs fn with HOME and PATH of the test process set from vars (the
// login shell inherits the process environment, not the getenv seam).
func withEnv(t *testing.T, vars map[string]string, fn func() pathResolution) pathResolution {
	t.Helper()
	for _, k := range []string{"HOME", "PATH"} {
		t.Setenv(k, vars[k])
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(vars["HOME"], ".config"))
	return fn()
}

// TestPathBetweenMarkersPreservesPATHBytes (review finding 4): the PATH
// between the markers is taken byte for byte — a directory whose name ends
// in a space, or contains a newline, survives — and only the protocol's own
// line breaks are removed.
func TestPathBetweenMarkersPreservesPATHBytes(t *testing.T) {
	for _, path := range []string{"/usr/bin:/opt/agent tools ", " /lead:/usr/bin", "/usr/bin:/odd\nname"} {
		out := "banner\n" + loginShellPATHMarker + "\n" + path + "\n" + loginShellPATHMarker + "\n"
		if got, ok := pathBetweenMarkers([]byte(out)); !ok || got != path {
			t.Errorf("pathBetweenMarkers lost bytes: got %q, want %q", got, path)
		}
	}

	// End to end: an executable under "agent tools " is found through a
	// real login shell.
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := filepath.Join(t.TempDir(), "agent tools ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".profile"), []byte("PATH=\"$PATH:"+dir+"\"; export PATH\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"PATH": "/usr/bin:/bin", "SHELL": shell, "HOME": home}
	res := withEnv(t, vars, func() pathResolution {
		return resolvePATH(func(k string) string { return vars[k] }, false, []string{"claude"}, 10*time.Second)
	})
	if res.Source != pathSourceLoginShell || len(res.Missing) != 0 {
		t.Fatalf("claude under %q not found after the login-shell lookup: %+v", dir, res)
	}
}
