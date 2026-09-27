package main

import (
	"encoding/json"
	"errors"
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

// CORE-057 — real-signal drain tests. Each test re-executes the test binary
// as a real daemon (see daemon_child_test.go) with dispatch ENABLED (no
// ITERVOX_DRY_RUN) against the memory tracker, and a fake `claude` that
// records when it starts and finishes a turn. The daemon receives a real
// SIGTERM from this process; nothing inside the daemon knows it is a test.

// drainProject is a daemonProject whose fake agent sleeps turnSeconds per
// turn and appends its pid to <dir>/started and <dir>/finished.
type drainProject struct {
	*daemonProject
	startedPath  string
	finishedPath string
}

func newDrainProject(t *testing.T, turnSeconds int) *drainProject {
	t.Helper()
	p := newDaemonProject(t)
	started := filepath.Join(p.Dir, "started")
	finished := filepath.Join(p.Dir, "finished")
	agent := filepath.Join(p.Dir, "bin", "claude")
	script := "#!/bin/sh\n" +
		"case \"$*\" in *--version*) echo 'stub claude 0.0.0'; exit 0;; esac\n" +
		"echo $$ >> " + started + "\n" +
		"sleep " + strconv.Itoa(turnSeconds) + "\n" +
		"echo $$ >> " + finished + "\n" +
		"exit 0\n"
	if err := os.WriteFile(agent, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `---
itervox_schema_version: 2
tracker:
  kind: memory
  active_states: ["Todo", "In Progress"]
  terminal_states: ["Done"]
agent:
  command: ` + agent + `
  max_concurrent_agents: 1
workspace:
  root: ` + filepath.Join(p.Dir, "workspaces") + `
server:
  host: 127.0.0.1
  port: 0
---

You are working on {{ issue.identifier }}.
`
	if err := os.WriteFile(p.Workflow, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// Dispatch must actually run the fake agent.
	env := p.env[:0:0]
	for _, kv := range p.env {
		if kv == "ITERVOX_DRY_RUN=1" {
			continue
		}
		env = append(env, kv)
	}
	p.env = append(env, "ITERVOX_API_TOKEN=drain-test-token")
	return &drainProject{daemonProject: p, startedPath: started, finishedPath: finished}
}

func countLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return len(strings.Fields(string(data)))
}

// readyProbe GETs /api/v1/ready and returns the status and decoded body.
func readyProbe(base string) (int, map[string]any, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(strings.TrimSuffix(base, "/") + "/api/v1/ready")
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body, nil
}

// waitExit waits for cmd to exit within timeout and returns its exit error.
func waitExit(t *testing.T, cmd *exec.Cmd, timeout time.Duration) (time.Duration, error) {
	t.Helper()
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }() // test-only reaper
	select {
	case err := <-done:
		return time.Since(start), err
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("daemon did not exit within %s of the signal", timeout)
		return 0, nil
	}
}

func pidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// TestSIGTERMDrainLetsRunningWorkerFinishWithinGrace: SIGTERM while one turn
// is in flight. The daemon must stop admitting work, report /ready 503 with
// draining:true while it waits, let the running agent finish its turn (it is
// not killed), start no second turn although nine more issues are eligible,
// and exit 0 once the turn is done — well inside --shutdown-grace.
func TestSIGTERMDrainLetsRunningWorkerFinishWithinGrace(t *testing.T) {
	p := newDrainProject(t, 4)
	cmd, stderrPath := p.startHeadless(t, []string{"--shutdown-grace", "40s"})

	waitFor(t, 20*time.Second, "first agent turn started", func() bool { return countLines(p.startedPath) >= 1 })
	base := readTrim(t, dashboardURLFilePath(p.Workflow))
	if status, _, err := readyProbe(base); err != nil || status != http.StatusOK {
		t.Fatalf("before SIGTERM /ready = %d, %v; want 200", status, err)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	// While the turn is still running, the load balancer must see "not ready".
	sawDraining := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && countLines(p.finishedPath) == 0 {
		status, body, err := readyProbe(base)
		if err == nil && status == http.StatusServiceUnavailable && body["draining"] == true {
			sawDraining = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !sawDraining {
		t.Errorf("/api/v1/ready never answered 503 draining:true during the drain; stderr:\n%s", readTrim(t, stderrPath))
	}

	took, err := waitExit(t, cmd, 30*time.Second)
	if err != nil {
		t.Errorf("daemon exit = %v, want 0; stderr:\n%s", err, readTrim(t, stderrPath))
	}
	if got := countLines(p.finishedPath); got != 1 {
		t.Errorf("finished turns = %d, want 1 (the in-flight turn must run to completion, not be killed); stderr:\n%s",
			got, readTrim(t, stderrPath))
	}
	if got := countLines(p.startedPath); got != 1 {
		t.Errorf("started turns = %d, want 1 (no new admission after SIGTERM)", got)
	}
	if took > 20*time.Second {
		t.Errorf("exit took %s after SIGTERM; the drain should end when the 4 s turn does", took)
	}
	if !strings.Contains(readTrim(t, stderrPath), "drain complete") {
		t.Errorf("stderr has no 'drain complete' line:\n%s", readTrim(t, stderrPath))
	}
}

// TestSecondSignalForcesImmediateStop: a second SIGTERM during the drain
// cancels the in-flight turn (its process group is killed) and the daemon
// exits promptly instead of waiting out the grace.
func TestSecondSignalForcesImmediateStop(t *testing.T) {
	p := newDrainProject(t, 120)
	cmd, stderrPath := p.startHeadless(t, []string{"--shutdown-grace", "5m"})

	waitFor(t, 20*time.Second, "first agent turn started", func() bool { return countLines(p.startedPath) >= 1 })
	agentPID, _ := strconv.Atoi(strings.Fields(readTrim(t, p.startedPath))[0])
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "drain began", func() bool {
		return strings.Contains(readTrim(t, stderrPath), "draining")
	})
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	took, err := waitExit(t, cmd, 40*time.Second)
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("wait: %v", err)
	}
	if took > 30*time.Second {
		t.Errorf("forced stop took %s; want well under the 5m grace", took)
	}
	if countLines(p.finishedPath) != 0 {
		t.Errorf("the 120 s turn finished; a forced stop must cancel it")
	}
	waitFor(t, 5*time.Second, "fake agent process killed", func() bool { return !pidAlive(agentPID) })
	if !strings.Contains(readTrim(t, stderrPath), "forcing") {
		t.Errorf("stderr has no forced-stop line:\n%s", readTrim(t, stderrPath))
	}
}

// TestOperatorEditDrainsBeforeReload pins the CORE-057 reload decision: an
// operator edit to WORKFLOW.md (not a daemon self-write — those never
// reload, CORE-116) drains first. The in-flight turn runs to completion, no
// new turn starts during the drain, and only then is the edit applied: the
// next generation dispatches again.
func TestOperatorEditDrainsBeforeReload(t *testing.T) {
	p := newDrainProject(t, 6)
	cmd, stderrPath := p.startHeadless(t, []string{"--shutdown-grace", "60s"})

	waitFor(t, 20*time.Second, "first agent turn started", func() bool { return countLines(p.startedPath) >= 1 })
	data, err := os.ReadFile(p.Workflow)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data), "You are working on", "Operator edit: you are working on", 1)
	if err := os.WriteFile(p.Workflow, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 15*time.Second, "reload drain began", func() bool {
		return strings.Contains(readTrim(t, stderrPath), "reload: WORKFLOW.md changed — draining")
	})
	if countLines(p.finishedPath) != 0 {
		t.Fatalf("the turn finished before the drain was observed; lengthen the turn")
	}
	waitFor(t, 30*time.Second, "reload applied after the drain", func() bool {
		return strings.Contains(readTrim(t, stderrPath), "reload: drain complete, applying WORKFLOW.md")
	})
	if got := countLines(p.finishedPath); got != 1 {
		t.Errorf("finished turns when the reload applied = %d, want 1 (the edit must not kill the turn)", got)
	}
	if got := countLines(p.startedPath); got != 1 {
		t.Errorf("started turns during the reload drain = %d, want 1 (no admission while draining)", got)
	}
	// The new generation admits work again.
	waitFor(t, 20*time.Second, "next generation dispatches", func() bool { return countLines(p.startedPath) >= 2 })
	if cmd.ProcessState != nil {
		t.Fatalf("daemon exited during the reload")
	}
}

// TestSIGTERMDrainGraceExpiryCancelsTurn: when --shutdown-grace expires
// before the turn ends, the daemon cancels the turn (its agent process is
// killed, not orphaned) and exits.
func TestSIGTERMDrainGraceExpiryCancelsTurn(t *testing.T) {
	p := newDrainProject(t, 120)
	cmd, stderrPath := p.startHeadless(t, []string{"--shutdown-grace", "2s"})

	waitFor(t, 20*time.Second, "first agent turn started", func() bool { return countLines(p.startedPath) >= 1 })
	agentPID, _ := strconv.Atoi(strings.Fields(readTrim(t, p.startedPath))[0])
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	took, err := waitExit(t, cmd, 40*time.Second)
	if err != nil {
		t.Errorf("daemon exit = %v, want 0; stderr:\n%s", err, readTrim(t, stderrPath))
	}
	if took < 2*time.Second {
		t.Errorf("exited after %s, before the 2 s grace", took)
	}
	if countLines(p.finishedPath) != 0 {
		t.Errorf("the 120 s turn finished; grace expiry must cancel it")
	}
	waitFor(t, 5*time.Second, "fake agent process killed", func() bool { return !pidAlive(agentPID) })
	if !strings.Contains(readTrim(t, stderrPath), "drain grace expired") {
		t.Errorf("stderr has no grace-expired line:\n%s", readTrim(t, stderrPath))
	}
}

// TestSIGTERMWhileConfigInvalidExits: after an operator edit makes
// WORKFLOW.md invalid, no generation runs and the loop waits for a fix. A
// SIGTERM in that state must still stop the daemon (it used to sleep through
// the retry backoff without reading the signal channel, forever).
func TestSIGTERMWhileConfigInvalidExits(t *testing.T) {
	p := newDaemonProject(t)
	cmd, stderrPath := p.startHeadless(t, nil)
	waitFor(t, 20*time.Second, "dashboard URL written", func() bool {
		return countFileOccurrences(dashboardURLFilePath(p.Workflow), "http://") > 0
	})
	if err := os.WriteFile(p.Workflow, []byte("---\nitervox_schema_version: 99\n---\nbroken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Second, "config-invalid state", func() bool {
		return strings.Contains(readTrim(t, stderrPath), "config invalid")
	})
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	took, err := waitExit(t, cmd, 15*time.Second)
	if err != nil {
		t.Errorf("exit = %v, want 0", err)
	}
	t.Logf("exited %s after SIGTERM in the config-invalid state", took)
}
