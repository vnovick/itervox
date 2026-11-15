//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Real-process tests for the Mac-app groundwork (#119): host discovery via
// `itervox status --json`, the supervisor token, and login-shell PATH. They
// reuse the daemon-child harness in daemon_child_test.go.

// runOneShotChild runs `itervox <args...>` through the real main() in its
// own session (no controlling terminal), from dir, with env, and returns
// stdout, stderr and the exit error.
func runOneShotChild(t *testing.T, dir string, env []string, args ...string) (string, string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0])
	cmd.Dir = dir
	cmd.Env = append(append([]string{}, env...), daemonChildArgsEnv+"="+string(raw))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }() // test-only reaper
	select {
	case err = <-done:
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("itervox %v did not finish", args)
	}
	return stdout.String(), stderr.String(), err
}

// registryFile is the registry record a daemon with pid writes under home.
func registryFile(home string, pid int) string {
	return filepath.Join(home, ".itervox", "daemons", strconv.Itoa(pid)+".json")
}

// waitForRegistered waits until p's daemon has claimed its pid file and
// published its registry record under home, and returns its pid.
func (p *daemonProject) waitForRegistered(t *testing.T, home string) int {
	t.Helper()
	var pid int
	waitFor(t, 30*time.Second, "daemon registry record", func() bool {
		got, _, _, err := readPIDFile(p.Workflow)
		if err != nil {
			return false
		}
		pid = got
		_, err = os.Stat(registryFile(home, pid))
		return err == nil
	})
	return pid
}

func statusRecords(t *testing.T, out string) ([]daemonRecord, []map[string]any) {
	t.Helper()
	var typed []daemonRecord
	var loose []map[string]any
	if err := json.Unmarshal([]byte(out), &typed); err != nil {
		t.Fatalf("status --json is not a JSON array of records: %v\n%s", err, out)
	}
	if err := json.Unmarshal([]byte(out), &loose); err != nil {
		t.Fatal(err)
	}
	return typed, loose
}

func findRecord(recs []daemonRecord, pid int) (daemonRecord, bool) {
	for _, r := range recs {
		if r.PID == pid {
			return r, true
		}
	}
	return daemonRecord{}, false
}

// statusJSONKeys is the documented contract (docs/api.md "Supervisor
// contract"); every element must carry every key.
var statusJSONKeys = []string{
	"schema_version", "pid", "workflow", "project_dir", "url", "host", "port",
	"version", "commit", "started_at", "token_required", "token_source",
	"token_file", "logs_dir", "heartbeat", "executable", "path_source", "source",
}

// TestStatusAllJSONListsEveryRunningDaemon (#119) starts two real daemons
// sharing one home directory — one with an auto-generated token, one under a
// supervisor token — and runs the real `itervox status --all --json`: valid
// JSON with path, pid, URL, port, version, start time and token posture per
// daemon, no token value anywhere, a project-scoped `status --json`, and a
// stopped or SIGKILLed daemon's record disappearing.
func TestStatusAllJSONListsEveryRunningDaemon(t *testing.T) {
	home := t.TempDir()
	const supTok = "status-supervisor-token-0123456789abcdef"
	a := newDaemonProject(t)
	b := newDaemonProject(t)
	a.startHeadless(t, nil, "HOME="+home)
	b.startHeadless(t, nil, "HOME="+home, supervisorTokenEnv+"="+supTok)
	pidA := a.waitForRegistered(t, home)
	pidB := b.waitForRegistered(t, home)

	// A record a SIGKILLed daemon left behind: dead pid, must be pruned.
	stale := registryFile(home, 999999)
	if err := os.WriteFile(stale, []byte(`{"schema_version":1,"pid":999999,"workflow":"/nonexistent/WORKFLOW.md"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	env := append(append([]string{}, a.env...), "HOME="+home)
	out, stderr, err := runOneShotChild(t, a.Dir, env, "status", "--all", "--json")
	if err != nil {
		t.Fatalf("status --all --json failed: %v\nstderr:\n%s", err, stderr)
	}
	recs, loose := statusRecords(t, out)
	for i, m := range loose {
		for _, k := range statusJSONKeys {
			if _, ok := m[k]; !ok {
				t.Errorf("element %d lacks documented key %q: %v", i, k, m)
			}
		}
	}
	if strings.Contains(out, supTok) {
		t.Fatalf("status output carries the supervisor token:\n%s", out)
	}
	if _, ok := findRecord(recs, 999999); ok {
		t.Fatal("a dead daemon's record must not be listed")
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("a dead daemon's record must be pruned, stat err=%v", err)
	}

	for _, tc := range []struct {
		p           *daemonProject
		pid         int
		tokenSource string
	}{{a, pidA, tokenSourceGenerated}, {b, pidB, tokenSourceSupervisor}} {
		rec, ok := findRecord(recs, tc.pid)
		if !ok {
			t.Fatalf("pid %d missing from status --all --json:\n%s", tc.pid, out)
		}
		wantURL := readTrim(t, dashboardURLFilePath(tc.p.Workflow))
		switch {
		case rec.SchemaVersion != daemonRecordSchemaVersion:
			t.Errorf("pid %d: schema_version = %d", tc.pid, rec.SchemaVersion)
		case rec.Workflow != tc.p.Workflow || rec.ProjectDir != tc.p.Dir:
			t.Errorf("pid %d: workflow/project_dir = %q/%q, want %q/%q", tc.pid, rec.Workflow, rec.ProjectDir, tc.p.Workflow, tc.p.Dir)
		case rec.URL != wantURL || !strings.HasSuffix(wantURL, ":"+strconv.Itoa(rec.Port)+"/") || rec.Host != "127.0.0.1":
			t.Errorf("pid %d: url/host/port = %q/%q/%d, want %q", tc.pid, rec.URL, rec.Host, rec.Port, wantURL)
		case rec.Version != version || rec.Source != "registry":
			t.Errorf("pid %d: version/source = %q/%q", tc.pid, rec.Version, rec.Source)
		case !rec.TokenRequired || rec.TokenSource != tc.tokenSource:
			t.Errorf("pid %d: token_required/token_source = %v/%q, want true/%q", tc.pid, rec.TokenRequired, rec.TokenSource, tc.tokenSource)
		case rec.LogsDir != tc.p.LogsDir || rec.Heartbeat != heartbeatPath(tc.p.Workflow) || rec.PathSource == "":
			t.Errorf("pid %d: logs_dir/heartbeat/path_source = %q/%q/%q", tc.pid, rec.LogsDir, rec.Heartbeat, rec.PathSource)
		}
		if started, err := time.Parse(time.RFC3339, rec.StartedAt); err != nil || time.Since(started) > 10*time.Minute {
			t.Errorf("pid %d: started_at = %q (%v)", tc.pid, rec.StartedAt, err)
		}
	}
	if rec, _ := findRecord(recs, pidA); rec.TokenFile != filepath.Join(a.LogsDir, apiTokenFileName) {
		t.Errorf("generated token: token_file = %q", rec.TokenFile)
	}
	if rec, _ := findRecord(recs, pidB); rec.TokenFile != "" {
		t.Errorf("supervisor token: token_file must be empty, got %q", rec.TokenFile)
	}

	// Scoped (no --all): only this project's daemon.
	out, stderr, err = runOneShotChild(t, a.Dir, env, "status", "--json", "--workflow", a.Workflow)
	if err != nil {
		t.Fatalf("status --json failed: %v\n%s", err, stderr)
	}
	scoped, _ := statusRecords(t, out)
	if len(scoped) != 1 || scoped[0].PID != pidA {
		t.Fatalf("status --json for project A = %+v, want only pid %d", scoped, pidA)
	}

	// A clean stop removes the record; the other daemon stays listed.
	a.stopDaemonByPIDFile(t)
	if _, err := os.Stat(registryFile(home, pidA)); !os.IsNotExist(err) {
		t.Fatalf("a stopped daemon must remove its registry record, stat err=%v", err)
	}
	out, _, err = runOneShotChild(t, a.Dir, env, "status", "--all", "--json")
	if err != nil {
		t.Fatal(err)
	}
	recs, _ = statusRecords(t, out)
	if _, ok := findRecord(recs, pidA); ok {
		t.Fatal("a stopped daemon is still listed")
	}
	if _, ok := findRecord(recs, pidB); !ok {
		t.Fatal("the running daemon disappeared from the listing")
	}
	b.stopDaemonByPIDFile(t)

	// With no daemon at all the output is `[]`, not `null`.
	out, _, err = runOneShotChild(t, a.Dir, env, "status", "--json", "--workflow", a.Workflow)
	if err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("no daemons: status --json = %q (%v), want []", out, err)
	}
}

// fileTreeContains reports the first file under root whose content holds
// needle ("" when none). Symlinks are skipped: .itervox/bin/itervox links to
// the test binary, whose constants include the needle.
func fileTreeContains(t *testing.T, root, needle string) string {
	t.Helper()
	hit := ""
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&fs.ModeSymlink != 0 || hit != "" {
			return nil
		}
		if data, rerr := os.ReadFile(path); rerr == nil && bytes.Contains(data, []byte(needle)) {
			hit = path
		}
		return nil
	})
	return hit
}

// apiStatus GETs path on the daemon with the given Authorization header
// value ("" sends none).
func (p *daemonProject) apiStatus(t *testing.T, path, auth string) int {
	t.Helper()
	base := strings.TrimSuffix(readTrim(t, dashboardURLFilePath(p.Workflow)), "/")
	req, err := http.NewRequest(http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestSupervisorTokenEnforcedAndNeverPrinted (#119) runs the real daemon with
// ITERVOX_SUPERVISOR_TOKEN set, together with every knob that would otherwise
// print or relax a token: ITERVOX_PRINT_TOKEN=1, a different pinned
// ITERVOX_API_TOKEN, and server.allow_unauthenticated: true. The API must
// require exactly the supervisor token, and the value must appear nowhere
// the daemon writes: stderr (piped, and a real terminal), the log file, the
// dashboard URL line, HEARTBEAT.md, dashboard_url, the registry, api-token.
func TestSupervisorTokenEnforcedAndNeverPrinted(t *testing.T) {
	const supTok = "supervisor-token-fedcba9876543210-0123"
	const pinned = "pinned-operator-token-0123456789"
	const announced = "dashboard URL ("

	newProject := func(t *testing.T) (*daemonProject, string) {
		p := newDaemonProject(t)
		writeDaemonWorkflow(t, p.Workflow, p.Dir, filepath.Join(p.Dir, "bin", "claude"), "")
		raw, err := os.ReadFile(p.Workflow)
		if err != nil {
			t.Fatal(err)
		}
		relaxed := strings.Replace(string(raw), "  port: 0\n", "  port: 0\n  allow_unauthenticated: true\n", 1)
		if err := os.WriteFile(p.Workflow, []byte(relaxed), 0o644); err != nil {
			t.Fatal(err)
		}
		return p, filepath.Join(p.Dir, "home")
	}
	assertNoLeak := func(t *testing.T, p *daemonProject, home string, extra ...string) {
		t.Helper()
		for _, root := range append([]string{p.LogsDir, filepath.Join(p.Dir, ".itervox"), home}, extra...) {
			if hit := fileTreeContains(t, root, supTok); hit != "" {
				t.Fatalf("the supervisor token leaked into %s", hit)
			}
		}
		if _, err := os.Stat(filepath.Join(p.LogsDir, apiTokenFileName)); !os.IsNotExist(err) {
			t.Fatalf("api-token must not exist under a supervisor token, stat err=%v", err)
		}
	}

	t.Run("headless", func(t *testing.T) {
		p, home := newProject(t)
		_, stderrPath := p.startHeadless(t, nil,
			supervisorTokenEnv+"="+supTok, "ITERVOX_API_TOKEN="+pinned, printTokenEnv+"=1")
		logFile := filepath.Join(p.LogsDir, "itervox.log")
		waitFor(t, 30*time.Second, "token announcement", func() bool {
			return countFileOccurrences(logFile, announced) >= 1 && countFileOccurrences(stderrPath, announced) >= 1
		})
		p.waitForRegistered(t, home)

		for _, c := range []struct {
			auth string
			want int
		}{
			{"", http.StatusUnauthorized},
			{"Bearer " + pinned, http.StatusUnauthorized},
			{"Bearer " + supTok + "x", http.StatusUnauthorized},
			{"Bearer " + supTok, http.StatusOK},
		} {
			if got := p.apiStatus(t, "/api/v1/state", c.auth); got != c.want {
				t.Errorf("GET /api/v1/state with %q = %d, want %d", strings.Replace(c.auth, supTok, "<supervisor>", 1), got, c.want)
			}
		}
		if got := p.apiStatus(t, "/api/v1/health", ""); got != http.StatusOK {
			t.Errorf("/api/v1/health stays open, got %d", got)
		}

		stderr := readTrim(t, stderrPath)
		if !strings.Contains(stderr, "token withheld") || !strings.Contains(stderr, supervisorTokenEnv) {
			t.Fatalf("stderr should carry the token-free URL line naming the supervisor source:\n%s", stderr)
		}
		if strings.Contains(stderr, "?token=") {
			t.Fatalf("no tokenised URL may be printed under a supervisor token:\n%s", stderr)
		}
		p.stopDaemonByPIDFile(t)
		assertNoLeak(t, p, home, stderrPath, filepath.Join(p.Dir, "stdout.txt"))
	})

	t.Run("stderr is a terminal", func(t *testing.T) {
		p, home := newProject(t)
		ptyOut, _, _ := p.startUnderPTY(t, `exec "$ITERVOX_TEST_EXE" >"$ITERVOX_TEST_OUT"`, nil,
			supervisorTokenEnv+"="+supTok, printTokenEnv+"=1")
		waitFor(t, 30*time.Second, "token announcement on the terminal", func() bool {
			return countFileOccurrences(ptyOut, announced) >= 1
		})
		if got := p.dashboardStatus(t, supTok); got != http.StatusOK {
			t.Fatalf("the supervisor token must authenticate, got %d", got)
		}
		if got := readTrim(t, ptyOut); strings.Contains(got, supTok) || strings.Contains(got, "?token=") {
			t.Fatalf("a terminal stderr must not show the supervisor token:\n%s", got)
		}
		p.stopDaemonByPIDFile(t)
		assertNoLeak(t, p, home, ptyOut)
	})
}

// writeFakeLoginShell writes a stand-in login shell whose profile prepends
// loginBin to PATH, as an nvm/asdf/Homebrew line in ~/.zshrc does, and then
// runs the -c script it was given.
func writeFakeLoginShell(t *testing.T, dir, loginBin string) string {
	t.Helper()
	shell := filepath.Join(dir, "fake-login-shell")
	script := "#!/bin/sh\nPATH='" + loginBin + "':\"$PATH\"; export PATH\nfor a; do last=$a; done\nexec /bin/sh -c \"$last\"\n"
	if err := os.WriteFile(shell, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return shell
}

// minimalPATHEnv is p's child environment as launchd hands it to an app:
// PATH=/usr/bin:/bin, no SHLVL (not started from a shell), SHELL=shell.
func minimalPATHEnv(p *daemonProject, shell string, extra ...string) []string {
	var env []string
	for _, kv := range p.env {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "PATH", "SHLVL", "SHELL", loginShellPATHEnv:
			continue
		}
		env = append(env, kv)
	}
	return append(append(env, "PATH=/usr/bin:/bin", "SHELL="+shell), extra...)
}

// TestLoginShellPATHFindsBinaryOnlyOnLoginShellPATH (#119): `claude` exists
// only in a directory the login shell adds to PATH. Started with a minimal
// PATH and not from a shell, the real `itervox doctor` finds it, names the
// login shell as the PATH source, and the real daemon reports the same; with
// the lookup disabled the binary is not found, proving where it came from.
func TestLoginShellPATHFindsBinaryOnlyOnLoginShellPATH(t *testing.T) {
	p := newDaemonProject(t)
	loginBin := filepath.Join(p.Dir, "login-bin")
	if err := os.MkdirAll(loginBin, 0o755); err != nil {
		t.Fatal(err)
	}
	claude := filepath.Join(loginBin, "claude")
	if err := os.WriteFile(claude, []byte("#!/bin/sh\necho 'stub claude 0.0.0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The workflow names the bare `claude`, as a user's does.
	writeDaemonWorkflow(t, p.Workflow, p.Dir, "claude", "")
	shell := writeFakeLoginShell(t, p.Dir, loginBin)

	out, stderr, _ := runOneShotChild(t, p.Dir, minimalPATHEnv(p, shell), "doctor", "--workflow", p.Workflow)
	if !strings.Contains(out, "PATH source: login shell ("+shell) || !strings.Contains(out, "not started from an interactive shell") {
		t.Fatalf("doctor should report the login shell as the PATH source:\n%s\nstderr:\n%s", out, stderr)
	}
	if !strings.Contains(out, "agent CLI claude: "+claude) {
		t.Fatalf("doctor should find claude on the login-shell PATH:\n%s", out)
	}

	out, _, _ = runOneShotChild(t, p.Dir, minimalPATHEnv(p, shell, loginShellPATHEnv+"=never"), "doctor", "--workflow", p.Workflow)
	if !strings.Contains(out, "PATH source: inherited — "+loginShellPATHEnv+"=never") || !strings.Contains(out, "WARNING: agent CLI claude not found on PATH") {
		t.Fatalf("with the lookup disabled claude must not be found:\n%s", out)
	}

	t.Run("daemon", func(t *testing.T) {
		home := filepath.Join(p.Dir, "home")
		p.env = minimalPATHEnv(p, shell)
		p.startHeadless(t, nil)
		logFile := filepath.Join(p.LogsDir, "itervox.log")
		waitFor(t, 30*time.Second, "PATH resolution logged", func() bool {
			return countFileOccurrences(logFile, "PATH resolved") >= 1
		})
		if line := readTrim(t, logFile); !strings.Contains(line, "source=login_shell") {
			t.Fatalf("the daemon should resolve PATH from the login shell:\n%s", line)
		}
		pid := p.waitForRegistered(t, home)
		var rec daemonRecord
		if err := json.Unmarshal([]byte(readTrim(t, registryFile(home, pid))), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.PathSource != pathSourceLoginShell {
			t.Fatalf("registry path_source = %q, want %q", rec.PathSource, pathSourceLoginShell)
		}
		p.stopDaemonByPIDFile(t)
	})
}
