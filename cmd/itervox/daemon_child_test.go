package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Real-daemon harness (M0-close fix-B, gaps G4/G8/G10).
//
// When re-executed with daemonChildArgsEnv set, the test binary replaces
// os.Args and calls the REAL main() — flag parsing, the logging/crash-output
// bootstrap, the WORKFLOW.md reload loop, run(), the token announcement and
// the HTTP listener — against a throwaway project using the memory tracker in
// dry-run mode. It runs from init(), before the testing package registers its
// flags on flag.CommandLine, so main()'s flag.Parse sees only itervox flags.
// Nothing in production code knows about this hook.
const (
	daemonChildArgsEnv = "ITERVOX_TEST_DAEMON_CHILD_ARGS"
	// daemonChildPanicEnv makes the child panic on a non-main goroutine once
	// main() has bootstrapped (the dashboard_url file exists), to prove the
	// crash output main() arms captures it.
	daemonChildPanicEnv = "ITERVOX_TEST_DAEMON_CHILD_PANIC"
	daemonChildPanicMsg = "daemon child fixture panic"
	// daemonChildArmFailEnv makes the child's setCrashOutput seam fail, so
	// main() takes armCrashOutput's error path (M0-close fix-G).
	daemonChildArmFailEnv = "ITERVOX_TEST_DAEMON_CHILD_ARM_FAIL"
)

func init() {
	raw := os.Getenv(daemonChildArgsEnv)
	if raw == "" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		_, _ = os.Stderr.WriteString("daemon child: bad args: " + err.Error() + "\n")
		os.Exit(5)
	}
	os.Args = append([]string{"itervox"}, args...)
	if os.Getenv(daemonChildArmFailEnv) == "1" {
		setCrashOutput = func(*os.File, debug.CrashOptions) error {
			return errors.New("daemon child fixture: set crash output refused")
		}
	}
	if os.Getenv(daemonChildPanicEnv) == "1" {
		wf := "WORKFLOW.md"
		for i, a := range args {
			if a == "--workflow" && i+1 < len(args) {
				wf = args[i+1]
			}
		}
		go func() { // test-only child fixture; never compiled into the daemon
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(dashboardURLFilePath(wf)); err == nil {
					panic(daemonChildPanicMsg)
				}
				time.Sleep(20 * time.Millisecond)
			}
		}()
	}
	main()
	os.Exit(0)
}

// daemonProject is a throwaway project directory a real daemon child runs in.
type daemonProject struct {
	Dir      string
	Workflow string
	LogsDir  string
	env      []string
}

// newDaemonProject writes a schema-2 WORKFLOW.md using the memory tracker,
// an ephemeral port, and a stub `claude` (so backend validation passes
// without the real CLI). The child environment strips every ITERVOX_* token
// knob so each test states its own.
func newDaemonProject(t *testing.T) *daemonProject {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(bin, "claude")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho 'stub claude 0.0.0'\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	wf := filepath.Join(dir, "WORKFLOW.md")
	writeDaemonWorkflow(t, wf, dir, stub, "")
	p := &daemonProject{Dir: dir, Workflow: wf, LogsDir: filepath.Join(dir, "logs")}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "ITERVOX_API_TOKEN", printTokenEnv, "ITERVOX_DRY_RUN", "HOME",
			daemonChildArgsEnv, daemonChildPanicEnv, "PATH",
			// No model-discovery network calls from a test daemon.
			"ANTHROPIC_API_KEY", "OPENAI_API_KEY",
			// CORE-058: a developer shell's PORT must not move the bind.
			"PORT", "ITERVOX_SERVER_HOST", "ITERVOX_SERVER_PORT":
			continue
		}
		p.env = append(p.env, kv)
	}
	p.env = append(p.env,
		"ITERVOX_DRY_RUN=1",
		"HOME="+filepath.Join(dir, "home"),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	return p
}

func writeDaemonWorkflow(t *testing.T, wf, dir, claude, promptSuffix string) {
	t.Helper()
	content := `---
itervox_schema_version: 2
tracker:
  kind: memory
  active_states: ["Todo", "In Progress"]
  terminal_states: ["Done"]
agent:
  command: ` + claude + `
workspace:
  root: ` + filepath.Join(dir, "workspaces") + `
server:
  host: 127.0.0.1
  port: 0
---

You are working on {{ issue.identifier }}.` + promptSuffix + `
`
	if err := os.WriteFile(wf, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// childCommand returns the argv (test binary + nothing else) and environment
// for a daemon child with the given extra itervox flags and env.
func (p *daemonProject) childCommand(t *testing.T, flags []string, extraEnv ...string) (string, []string) {
	t.Helper()
	args := append([]string{"--workflow", p.Workflow, "--logs-dir", p.LogsDir}, flags...)
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	env := append(append([]string{}, p.env...), daemonChildArgsEnv+"="+string(raw))
	env = append(env, extraEnv...)
	return os.Args[0], env
}

// startHeadless starts a daemon child whose stdin is /dev/null and whose
// stdout/stderr are regular files — the systemd/container/`2>file` shape.
func (p *daemonProject) startHeadless(t *testing.T, flags []string, extraEnv ...string) (*exec.Cmd, string) {
	t.Helper()
	exe, env := p.childCommand(t, flags, extraEnv...)
	stderrPath := filepath.Join(p.Dir, "stderr.txt")
	errf, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	outf, err := os.Create(filepath.Join(p.Dir, "stdout.txt"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe)
	cmd.Dir = p.Dir
	cmd.Env = env
	cmd.Stdout = outf
	cmd.Stderr = errf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }() // test-only reaper
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		_ = errf.Close()
		_ = outf.Close()
	})
	return cmd, stderrPath
}

// stopDaemonByPIDFile SIGTERMs the daemon recorded in the project's pid file
// and waits for the file to disappear (the daemon's clean-shutdown cleanup).
func (p *daemonProject) stopDaemonByPIDFile(t *testing.T) {
	t.Helper()
	pid, _, pidPath, err := readPIDFile(p.Workflow)
	if err != nil {
		return // never claimed, or already cleaned up
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	waitFor(t, 15*time.Second, "daemon pid file removed on shutdown", func() bool {
		_, err := os.Stat(pidPath)
		return os.IsNotExist(err)
	})
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for: %s", timeout, what)
}

func readTrim(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.TrimSpace(string(data))
}

// dashboardStatus GETs /api/v1/state on the live daemon with the bearer
// token and returns the HTTP status.
func (p *daemonProject) dashboardStatus(t *testing.T, token string) int {
	t.Helper()
	base := readTrim(t, dashboardURLFilePath(p.Workflow))
	req, err := http.NewRequest(http.MethodGet, strings.TrimSuffix(base, "/")+"/api/v1/state", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/v1/state: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// countFileOccurrences counts how many times needle appears in path (0 when
// the file does not exist yet).
func countFileOccurrences(path, needle string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), needle)
}

// TestGeneratedAPITokenFileSurvivesConfigReload (G4, CORE-012) drives the
// real main() reload loop across two run() generations: the daemon
// auto-generates a token, writes <logs-dir>/api-token, then WORKFLOW.md is
// edited on disk (as an operator — or a dashboard settings save — does),
// which cancels the first generation and starts a second. The token lives on
// in the process environment and still authenticates, so the discovery file
// must survive the reload and keep naming it.
func TestGeneratedAPITokenFileSurvivesConfigReload(t *testing.T) {
	p := newDaemonProject(t)
	p.startHeadless(t, nil)
	tokenFile := filepath.Join(p.LogsDir, apiTokenFileName)
	logFile := filepath.Join(p.LogsDir, "itervox.log")
	const announce = "dashboard URL (token withheld from logs)"

	waitFor(t, 30*time.Second, "first generation announces the token", func() bool {
		return countFileOccurrences(logFile, announce) >= 1
	})
	token := readTrim(t, tokenFile)
	if len(token) != 64 {
		t.Fatalf("api-token should hold the 64-hex generated token, got %q", token)
	}
	if got := p.dashboardStatus(t, token); got != http.StatusOK {
		t.Fatalf("generation 1: GET /api/v1/state with the file's token = %d, want 200", got)
	}

	// Operator edits WORKFLOW.md by hand -> watcher -> reload -> run() #2.
	writeDaemonWorkflow(t, p.Workflow, p.Dir, filepath.Join(p.Dir, "bin", "claude"), "\nEdited by hand.")
	waitFor(t, 30*time.Second, "second generation announces the token", func() bool {
		return countFileOccurrences(logFile, announce) >= 2
	})

	got, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatalf("api-token was removed by the reload while the generated token is still live: %v", err)
	}
	if strings.TrimSpace(string(got)) != token {
		t.Fatalf("api-token after reload = %q, want the still-live generated token %q", got, token)
	}
	if code := p.dashboardStatus(t, token); code != http.StatusOK {
		t.Fatalf("generation 2: the file's token must still authenticate, got %d", code)
	}
	p.stopDaemonByPIDFile(t)
}

// TestPinnedAPITokenRemovesStaleFileAcrossReload is the other half of G4: a
// token the operator pinned via ITERVOX_API_TOKEN was never generated by
// this process, so a stale api-token from an earlier generated-token boot is
// removed at startup and stays absent across a reload.
func TestPinnedAPITokenRemovesStaleFileAcrossReload(t *testing.T) {
	p := newDaemonProject(t)
	tokenFile := filepath.Join(p.LogsDir, apiTokenFileName)
	if err := os.MkdirAll(p.LogsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenFile, []byte("stale-generated-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const pinned = "pinned-operator-token-0123456789"
	p.startHeadless(t, nil, "ITERVOX_API_TOKEN="+pinned)
	logFile := filepath.Join(p.LogsDir, "itervox.log")
	const announce = "dashboard URL (token withheld from logs)"
	waitFor(t, 30*time.Second, "first generation announces the token", func() bool {
		return countFileOccurrences(logFile, announce) >= 1
	})
	if _, err := os.Stat(tokenFile); !os.IsNotExist(err) {
		t.Fatalf("a pinned token must remove the stale api-token file, stat err=%v", err)
	}
	writeDaemonWorkflow(t, p.Workflow, p.Dir, filepath.Join(p.Dir, "bin", "claude"), "\nEdited by hand.")
	waitFor(t, 30*time.Second, "second generation announces the token", func() bool {
		return countFileOccurrences(logFile, announce) >= 2
	})
	if _, err := os.Stat(tokenFile); !os.IsNotExist(err) {
		t.Fatalf("a pinned token must never be written to api-token, stat err=%v", err)
	}
	if code := p.dashboardStatus(t, pinned); code != http.StatusOK {
		t.Fatalf("pinned token must authenticate, got %d", code)
	}
	p.stopDaemonByPIDFile(t)
}

// startUnderPTY runs a daemon child under script(1), which allocates a real
// pseudo-terminal for the child's stdin and stderr — the interactive
// operator's shape — with no Go pty dependency. inner is the /bin/sh
// command script(1) runs; it may redirect descriptors away from the pty
// ($ITERVOX_TEST_EXE is the child, $ITERVOX_TEST_OUT / $ITERVOX_TEST_ERR are
// files in the project dir). It returns the path holding everything the
// child wrote to the terminal. Skips when script(1) is unavailable.
func (p *daemonProject) startUnderPTY(t *testing.T, inner string, flags []string, extraEnv ...string) (ptyOut, outPath, errPath string) {
	t.Helper()
	scriptBin, err := exec.LookPath("script")
	if err != nil {
		t.Skip("script(1) not available: the real-TTY case is covered only by the unit test")
	}
	exe, env := p.childCommand(t, flags, extraEnv...)
	outPath = filepath.Join(p.Dir, "child-stdout.txt")
	errPath = filepath.Join(p.Dir, "child-stderr.txt")
	// TERM=dumb stops the charm renderers from querying the terminal's
	// colours (OSC 10/11 + DSR), which script(1)'s unattended pty never
	// answers, costing a multi-second timeout per query. It does not affect
	// term.IsTerminal, which only asks the kernel about the descriptor.
	env = append(env, "TERM=dumb", "ITERVOX_TEST_EXE="+exe, "ITERVOX_TEST_OUT="+outPath, "ITERVOX_TEST_ERR="+errPath)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin", "freebsd", "netbsd", "openbsd":
		cmd = exec.Command(scriptBin, "-q", "/dev/null", "/bin/sh", "-c", inner)
	case "linux":
		cmd = exec.Command(scriptBin, "-q", "-e", "-c", inner, "/dev/null")
		env = append(env, "SHELL=/bin/sh")
	default:
		t.Skipf("no script(1) invocation known for %s", runtime.GOOS)
	}
	ptyOut = filepath.Join(p.Dir, "pty.txt")
	f, err := os.Create(ptyOut)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Dir = p.Dir
	cmd.Env = env
	cmd.Stdout = f
	cmd.Stderr = f
	// Keep script's stdin open (and silent) so it never forwards EOF to the
	// pty while the daemon runs.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		p.stopDaemonByPIDFile(t)
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }() // test-only reaper
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		_ = f.Close()
	})
	return ptyOut, outPath, errPath
}

// TestDashboardTokenAnnounceUsesRealStderrDetection (G10, CORE-012) runs the
// real main() — --no-print-token flag parsing, ITERVOX_PRINT_TOKEN, and the
// production term.IsTerminal(os.Stderr) probe on the child's REAL descriptor
// 2 — in each operator shape. The "stdin is a terminal but stderr is piped"
// row is the one that fails if detection is moved from stderr to stdin; the
// plain piped row fails if detection is hard-coded true; the terminal row
// fails if it is hard-coded false.
func TestDashboardTokenAnnounceUsesRealStderrDetection(t *testing.T) {
	const withheld = "dashboard URL (token withheld from logs)"
	const shown = "dashboard URL (carries token"
	// Every row waits for EITHER announcement, then asserts which one it
	// got, so a wrong decision fails with the leak itself, not a timeout.
	const announced = "dashboard URL ("

	t.Run("stderr piped to a file, stdin /dev/null", func(t *testing.T) {
		p := newDaemonProject(t)
		_, stderrPath := p.startHeadless(t, nil)
		waitFor(t, 30*time.Second, "token announcement on stderr", func() bool {
			return countFileOccurrences(stderrPath, announced) >= 1
		})
		token := readTrim(t, filepath.Join(p.LogsDir, apiTokenFileName))
		stderr := readTrim(t, stderrPath)
		if strings.Contains(stderr, token) {
			t.Fatalf("piped stderr must not carry the token:\n%s", stderr)
		}
		if !strings.Contains(stderr, withheld) || !strings.Contains(stderr, tokenFingerprint(token)) || !strings.Contains(stderr, apiTokenFileName) {
			t.Fatalf("piped stderr should name the fingerprint and the api-token file:\n%s", stderr)
		}
	})

	t.Run("stderr piped, ITERVOX_PRINT_TOKEN=1 forces the URL", func(t *testing.T) {
		p := newDaemonProject(t)
		_, stderrPath := p.startHeadless(t, nil, printTokenEnv+"=1")
		waitFor(t, 30*time.Second, "tokenised URL on stderr", func() bool {
			return countFileOccurrences(stderrPath, announced) >= 1
		})
		token := readTrim(t, filepath.Join(p.LogsDir, apiTokenFileName))
		if got := readTrim(t, stderrPath); !strings.Contains(got, shown) || !strings.Contains(got, "?token="+token) {
			t.Fatalf("ITERVOX_PRINT_TOKEN=1 must print the tokenised URL on piped stderr")
		}
	})

	t.Run("stdin is a terminal, stderr piped to a file", func(t *testing.T) {
		p := newDaemonProject(t)
		_, _, errPath := p.startUnderPTY(t, `exec "$ITERVOX_TEST_EXE" 2>"$ITERVOX_TEST_ERR"`, nil)
		waitFor(t, 30*time.Second, "token announcement on piped stderr", func() bool {
			return countFileOccurrences(errPath, announced) >= 1
		})
		token := readTrim(t, filepath.Join(p.LogsDir, apiTokenFileName))
		if stderr := readTrim(t, errPath); strings.Contains(stderr, token) {
			t.Fatalf("stderr is piped (only stdin is a terminal), so the token must be withheld:\n%s", stderr)
		}
	})

	t.Run("stderr is a terminal", func(t *testing.T) {
		p := newDaemonProject(t)
		ptyOut, _, _ := p.startUnderPTY(t, `exec "$ITERVOX_TEST_EXE" >"$ITERVOX_TEST_OUT"`, nil)
		tokenFile := filepath.Join(p.LogsDir, apiTokenFileName)
		waitFor(t, 30*time.Second, "tokenised URL on the terminal", func() bool {
			return countFileOccurrences(ptyOut, announced) >= 1
		})
		token := readTrim(t, tokenFile)
		if got := readTrim(t, ptyOut); !strings.Contains(got, shown) || !strings.Contains(got, "?token="+token) {
			t.Fatalf("stderr is a terminal: the tokenised URL must be printed there")
		}
	})

	t.Run("stderr is a terminal, --no-print-token", func(t *testing.T) {
		p := newDaemonProject(t)
		ptyOut, _, _ := p.startUnderPTY(t, `exec "$ITERVOX_TEST_EXE" >"$ITERVOX_TEST_OUT"`, []string{"--no-print-token"})
		waitFor(t, 30*time.Second, "withheld announcement on the terminal", func() bool {
			return countFileOccurrences(ptyOut, announced) >= 1
		})
		token := readTrim(t, filepath.Join(p.LogsDir, apiTokenFileName))
		if got := readTrim(t, ptyOut); strings.Contains(got, token) || !strings.Contains(got, withheld) {
			t.Fatalf("--no-print-token must keep the token off a terminal stderr")
		}
	})
}

// TestDaemonStartupArmsCrashOutput (G8, CORE-007) goes through the real
// main() startup, not the armCrashOutput helper: boot 1 panics on a
// non-main goroutine once it is serving, and the dump must land in
// <logs-dir>/crash.log — which only happens if main() armed
// debug.SetCrashOutput. Boot 2 must then report that crash in HEARTBEAT.md
// with the path of the file holding the dump and without the panic text.
func TestDaemonStartupArmsCrashOutput(t *testing.T) {
	p := newDaemonProject(t)
	crashPath := filepath.Join(p.LogsDir, crashLogName)

	cmd, stderrPath := p.startHeadless(t, nil, daemonChildPanicEnv+"=1")
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }() // test-only reaper
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("boot 1 should have crashed; stderr:\n%s", readTrim(t, stderrPath))
		}
	case <-time.After(60 * time.Second):
		t.Fatal("boot 1 did not crash")
	}
	if !strings.Contains(readTrim(t, stderrPath), "panic: "+daemonChildPanicMsg) {
		t.Fatalf("boot 1 should have died of the fixture panic; stderr:\n%s", readTrim(t, stderrPath))
	}
	dump, err := os.ReadFile(crashPath)
	if err != nil {
		t.Fatalf("main() did not arm crash output: %s missing after an unrecovered goroutine panic: %v", crashPath, err)
	}
	if !strings.Contains(string(dump), "panic: "+daemonChildPanicMsg) || !strings.Contains(string(dump), "goroutine ") {
		t.Fatalf("crash.log should hold the full runtime dump, got:\n%s", dump)
	}

	// Boot 2 (a fresh daemon on the same logs dir) reports it once.
	p.startHeadless(t, nil)
	hb := heartbeatPath(p.Workflow)
	waitFor(t, 30*time.Second, "HEARTBEAT.md reports the last crash", func() bool {
		return countFileOccurrences(hb, "- Last crash:") >= 1
	})
	content := readTrim(t, hb)
	if !strings.Contains(content, crashPath) {
		t.Fatalf("HEARTBEAT.md should name %s:\n%s", crashPath, content)
	}
	if strings.Contains(content, daemonChildPanicMsg) {
		t.Fatalf("panic text must never reach HEARTBEAT.md:\n%s", content)
	}
	p.stopDaemonByPIDFile(t)
}

// TestDaemonStartupReportsCrashWhenArmFails (M0-close fix-G item 1) goes
// through the real main() startup with debug.SetCrashOutput failing. The
// previous run's crash is still detected (prepareCrashLog runs, and moves the
// marker, before arming), so it must reach HEARTBEAT.md on this boot: the
// next boot sees an unchanged marker and would never report it.
func TestDaemonStartupReportsCrashWhenArmFails(t *testing.T) {
	p := newDaemonProject(t)
	crashPath := filepath.Join(p.LogsDir, crashLogName)
	if err := os.MkdirAll(p.LogsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A previous run's dump with no marker: prepareCrashLog reports it.
	if err := os.WriteFile(crashPath, []byte("panic: "+daemonChildPanicMsg+"\n\ngoroutine 1 [running]:\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, stderrPath := p.startHeadless(t, nil, daemonChildArmFailEnv+"=1")
	hb := heartbeatPath(p.Workflow)
	waitFor(t, 30*time.Second, "HEARTBEAT.md written", func() bool {
		_, err := os.Stat(hb)
		return err == nil
	})
	if !strings.Contains(readTrim(t, stderrPath), "crash output not armed") {
		t.Fatalf("the arm-failure path was not exercised; stderr:\n%s", readTrim(t, stderrPath))
	}
	content := readTrim(t, hb)
	if !strings.Contains(content, "- Last crash:") || !strings.Contains(content, crashPath) {
		t.Fatalf("a failed arm must not drop the previous run's crash; HEARTBEAT.md:\n%s", content)
	}
	if _, err := os.Stat(crashPath + crashMarkerSuffix); err != nil {
		t.Fatalf("the marker should have moved (so no later boot reports this crash): %v", err)
	}
	p.stopDaemonByPIDFile(t)
}
