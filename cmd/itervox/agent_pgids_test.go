package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// groupAlive reports whether process group pgid has any member left.
func groupAlive(pgid int) bool {
	return syscall.Kill(-pgid, 0) == nil
}

// killGroupOnCleanup guarantees no test leaves a process group behind.
func killGroupOnCleanup(t *testing.T, pgid int) {
	t.Helper()
	t.Cleanup(func() {
		if pgid > 1 {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	})
}

// writeOrphanWorkflow writes a non-dry-run WORKFLOW.md whose agent is a fake
// Claude CLI that records its pid and then blocks on a long child, the shape
// of a real CLI waiting on a long tool call (a test runner, a dev server).
func writeOrphanWorkflow(t *testing.T, p *daemonProject) (pidFile string) {
	t.Helper()
	pidFile = filepath.Join(p.Dir, "agent.pid")
	fake := filepath.Join(p.Dir, "bin", "claude")
	script := `#!/bin/bash
case "$1" in --version|-v) echo "fake-claude 0.0.0-core042"; exit 0;; esac
echo "$$" > ` + pidFile + `
echo '{"type":"system","subtype":"init","session_id":"core042"}'
sleep 600
`
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	content := `---
itervox_schema_version: 2
tracker:
  kind: memory
  active_states: ["Todo", "In Progress"]
  terminal_states: ["Done"]
agent:
  command: ` + fake + `
  max_concurrent_agents: 1
  read_timeout_ms: 900000
  stall_timeout_ms: 0
  turn_timeout_ms: 0
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
	return pidFile
}

// TestReapOrphanedAgentGroups is the CORE-042 end-to-end check against the
// REAL daemon (main(), re-executed test binary): a daemon runs a fake agent
// with a long child, is kill -9'd, and the agent's process group survives it
// (the reproduction); the next daemon for the same workflow reaps the group
// at startup from .itervox/run/agent-pgids.json.
func TestReapOrphanedAgentGroups(t *testing.T) {
	p := newDaemonProject(t)
	pidFile := writeOrphanWorkflow(t, p)

	// First daemon: dispatches (dry-run off) and starts the fake agent.
	first, _ := p.startHeadless(t, nil, "ITERVOX_DRY_RUN=0")
	waitFor(t, 30*time.Second, "fake agent started", func() bool {
		data, err := os.ReadFile(pidFile)
		return err == nil && strings.TrimSpace(string(data)) != ""
	})
	agentPID, err := strconv.Atoi(readTrim(t, pidFile))
	if err != nil {
		t.Fatal(err)
	}
	pgid, err := syscall.Getpgid(agentPID)
	if err != nil {
		t.Fatalf("getpgid(%d): %v", agentPID, err)
	}
	killGroupOnCleanup(t, pgid)
	if pgid != agentPID {
		t.Fatalf("agent pid %d is not its group leader (pgid %d): Setpgid missing", agentPID, pgid)
	}

	ledgerPath := agentPGIDLedgerPath(p.Workflow)
	var ledger pgidLedgerFile
	waitFor(t, 10*time.Second, "ledger records the agent group", func() bool {
		data, err := os.ReadFile(ledgerPath)
		if err != nil || json.Unmarshal(data, &ledger) != nil {
			return false
		}
		for _, g := range ledger.Groups {
			if g.PGID == pgid {
				return true
			}
		}
		return false
	})
	for _, g := range ledger.Groups {
		if g.PGID == pgid && (g.Identifier == "" || g.DaemonPID != first.Process.Pid) {
			t.Fatalf("ledger entry %+v: want an issue identifier and daemon_pid %d", g, first.Process.Pid)
		}
	}

	// kill -9: no cmd.Cancel runs, so nothing signals the agent's group.
	if err := first.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = first.Wait()
	time.Sleep(2 * time.Second)
	if !groupAlive(pgid) {
		t.Fatalf("reproduction: agent group %d did not survive the daemon's kill -9", pgid)
	}

	// Second daemon for the same workflow (dry-run: it must not start a new
	// agent) reaps the survivor before doing anything else.
	p.startHeadless(t, nil)
	waitFor(t, 20*time.Second, "orphaned agent group reaped by the next daemon", func() bool {
		return !groupAlive(pgid)
	})
	p.stopDaemonByPIDFile(t)
}

// startGroup starts `sleep 600` as the leader of its own process group.
func startGroup(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "600")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pgid := cmd.Process.Pid
	// The test is the leader's parent, so a signalled leader stays a zombie
	// until it is waited for — and on Linux kill(-pgid, 0) still succeeds for
	// a group of zombies. Wait at once so groupAlive sees the exit. (A real
	// orphaned group is reparented to init, which reaps it.)
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	// Cleanups run last-in first-out: register the wait first so the kill
	// runs before it.
	t.Cleanup(func() { <-exited })
	killGroupOnCleanup(t, pgid)
	return pgid
}

func writeLedgerFile(t *testing.T, path string, f pgidLedgerFile) {
	t.Helper()
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// identityOf returns pid's (lstart, command) as the reaper reads them.
func identityOf(t *testing.T, pid int) procMember {
	t.Helper()
	procs, err := listProcesses()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range procs {
		if p.PID == pid {
			return p
		}
	}
	t.Fatalf("pid %d not listed by ps", pid)
	return procMember{}
}

func waitGroupGone(pgid int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for groupAlive(pgid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	return !groupAlive(pgid)
}

// TestReapOrphanedAgentGroupsSkipsReusedPGID pins the identity guard
// (M6-close V1): a live group is signalled only when its leader is the
// recorded process — same kernel start time AND same command line — or
// through recorded (pid, start) members. A recycled pgid (different start
// or command), or a ledger entry with no identity, is never signalled,
// whatever the wall clock says.
func TestReapOrphanedAgentGroupsSkipsReusedPGID(t *testing.T) {
	cases := []struct {
		name  string
		entry func(pgid int, id procMember) pgidLedgerEntry
		want  string
	}{
		{"leader identity matches", func(pgid int, id procMember) pgidLedgerEntry {
			return pgidLedgerEntry{PGID: pgid, PID: pgid, LeaderStart: id.Start, LeaderCmd: id.Command}
		}, "reaped"},
		{"recycled pgid: different leader start time", func(pgid int, id procMember) pgidLedgerEntry {
			return pgidLedgerEntry{PGID: pgid, PID: pgid, LeaderStart: "Mon Jan  1 00:00:00 2024", LeaderCmd: id.Command}
		}, "skipped_identity"},
		{"recycled pgid: different command", func(pgid int, id procMember) pgidLedgerEntry {
			return pgidLedgerEntry{PGID: pgid, PID: pgid, LeaderStart: id.Start, LeaderCmd: "claude --output-format stream-json"}
		}, "skipped_identity"},
		{"no identity recorded (older ledger)", func(pgid int, _ procMember) pgidLedgerEntry {
			return pgidLedgerEntry{PGID: pgid, PID: pgid, StartedAt: time.Now().Add(-time.Hour)}
		}, "skipped_identity"},
		{"leader gone, recorded member matches", func(pgid int, id procMember) pgidLedgerEntry {
			return pgidLedgerEntry{PGID: pgid, PID: 1 << 30, LeaderStart: id.Start, LeaderCmd: id.Command,
				Members: []pgidMember{{PID: pgid, Start: id.Start}}}
		}, "reaped_members"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pgid := startGroup(t)
			path := filepath.Join(t.TempDir(), "run", "agent-pgids.json")
			e := tc.entry(pgid, identityOf(t, pgid))
			e.Identifier, e.DaemonPID = "X-1", 999999
			writeLedgerFile(t, path, pgidLedgerFile{Version: pgidLedgerVersion, DaemonPID: 999999, SeenAt: time.Now().UTC(),
				Groups: []pgidLedgerEntry{e}})
			results, err := reapOrphanedAgentGroups(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].Action != tc.want {
				t.Fatalf("results = %+v, want one %q", results, tc.want)
			}
			reaped := tc.want == "reaped" || tc.want == "reaped_members"
			if reaped && !waitGroupGone(pgid) {
				t.Fatalf("group %d still alive after %q", pgid, tc.want)
			}
			if !reaped && !groupAlive(pgid) {
				t.Fatalf("group %d was signalled although %q", pgid, tc.want)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("ledger not removed after the reap: %v", err)
			}
		})
	}
}

// TestReapIgnoresRecycledPGIDAcrossClockSteps (M6-close V1): the verifier's
// probes. A ledger written by a daemon for ITS group — then the pgid number
// is recycled by an unrelated `sleep` a few seconds after the daemon was
// last seen, and separately after a 1 h backward clock step. The old
// seen_at window reaped both; identity matching reaps neither.
func TestReapIgnoresRecycledPGIDAcrossClockSteps(t *testing.T) {
	for _, step := range []time.Duration{0, -time.Hour} {
		pgid := startGroup(t) // the unrelated group that inherited the number
		path := filepath.Join(t.TempDir(), "run", "agent-pgids.json")
		now := time.Now().UTC().Add(step)
		// The dead daemon's own agent: recorded identity from an older process.
		writeLedgerFile(t, path, pgidLedgerFile{Version: pgidLedgerVersion, DaemonPID: 999999, SeenAt: now.Add(-8 * time.Second),
			Groups: []pgidLedgerEntry{{Identifier: "X-1", PGID: pgid, PID: pgid, StartedAt: now.Add(-time.Hour), DaemonPID: 999999,
				LeaderStart: "Sat Sep 26 10:00:00 2026", LeaderCmd: "claude --output-format stream-json --verbose -p",
				Members: []pgidMember{{PID: pgid, Start: "Sat Sep 26 10:00:00 2026"}}}}})
		results, err := reapOrphanedAgentGroups(path)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond)
		if len(results) != 1 || results[0].Action != "skipped_identity" || !groupAlive(pgid) {
			t.Fatalf("clock step %v: results=%+v alive=%v — an unrelated recycled group must survive", step, results, groupAlive(pgid))
		}
	}
}

// TestPGIDLedgerRecordsAndForgetsGroups: the observer persists a started
// group and removes the file once the last group exits.
func TestPGIDLedgerRecordsAndForgetsGroups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "agent-pgids.json")
	l := newPGIDLedger(path)
	l.GroupStarted(4242, 4242, "ABC-1")
	var f pgidLedgerFile
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Groups) != 1 || f.Groups[0].PGID != 4242 || f.Groups[0].Identifier != "ABC-1" || f.DaemonPID != os.Getpid() || f.SeenAt.IsZero() {
		t.Fatalf("ledger = %+v", f)
	}
	l.GroupExited(4242)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("ledger should be removed when empty: %v", err)
	}
}

func TestParsePSList(t *testing.T) {
	out := []byte(" 101  101 Sun Sep 27 07:52:22 2026 /bin/bash /x/claude --verbose -p\n  202 101 Sun Sep 27 07:52:23 2026 sleep 600\nbad line\n")
	got := parsePSList(out)
	want := []procMember{
		{PID: 101, PGID: 101, Start: "Sun Sep 27 07:52:22 2026", Command: "/bin/bash /x/claude --verbose -p"},
		{PID: 202, PGID: 101, Start: "Sun Sep 27 07:52:23 2026", Command: "sleep 600"},
	}
	if len(got) != len(want) {
		t.Fatalf("parsePSList = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
