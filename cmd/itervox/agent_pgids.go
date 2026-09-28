package main

// Orphaned agent process-group ledger (CORE-042).
//
// Agents and workspace hooks run in their own process group (Setpgid, via
// procgroup.Configure) so a cancelled turn can kill the whole tree. The flip
// side: when the daemon dies without running cmd.Cancel — kill -9, an OOM
// kill, a hard crash — nothing signals those groups. The CORE-042
// reproduction (kill -9 of a real daemon, 30 s wait) left survivors for a
// Claude-shaped agent, a Codex-shaped agent and a before_run hook alike.
//
// The ledger records every LOCAL group the daemon starts in
// .itervox/run/agent-pgids.json and the next daemon for the same workflow
// reaps what the dead one left behind, before it dispatches anything.
//
// A pgid is only a number, so the reaper never trusts it alone, and it
// compares no wall-clock windows (M6-close V1: a group recycled within
// seen_at+12 s, or a backward clock step, used to be killed). Identity is
// the kernel's own record of each process: its start time as ps prints it
// (lstart, fixed at fork, unaffected by later clock steps — both sides are
// read from ps, the same clock source) plus its command line.
//   - The leader's (lstart, command) is recorded when the group starts. If a
//     live process with that pid still leads that pgid with the same lstart
//     AND command, the whole group is signalled: a pgid cannot be reused
//     while its leader lives, so every member is the agent's descendant.
//   - Otherwise (the usual orphan: the CLI died on the broken stdout pipe
//     and its children live on) only processes whose exact (pid, lstart) was
//     recorded as a member of the group are signalled. Members are recorded
//     at every refresh (pgidLedgerRefresh); a descendant started after the
//     last refresh before the daemon died is missed — fail-safe, never an
//     over-kill.
// Anything else — no identity recorded (an older ledger), a recycled pid, a
// changed command — is left alone.
//
// SSH workers are out of scope: their local process is the ssh client and
// the agent's group lives on the remote host, which CORE-155's kill-on-EOF
// wrapper stops. The runners therefore Track local groups only.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/procgroup"
)

const (
	// agentPGIDLedgerRel is the ledger path relative to the workflow's
	// .itervox directory. run/ is gitignored by ensureItervoxGitignore.
	agentPGIDLedgerRel = "run/agent-pgids.json"
	// pgidLedgerRefresh is how often a daemon with live groups re-records
	// their members (and seen_at, informational).
	pgidLedgerRefresh = 10 * time.Second
	// pgidReapGrace is how long a SIGTERMed group gets before the SIGKILL
	// sweep.
	pgidReapGrace     = 2 * time.Second
	pgidLedgerVersion = 2
)

// pgidLedgerEntry is one tracked process group.
type pgidLedgerEntry struct {
	Identifier string    `json:"identifier,omitempty"`
	PGID       int       `json:"pgid"`
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"started_at"`
	DaemonPID  int       `json:"daemon_pid"`
	// LeaderStart and LeaderCmd are the leader's kernel start time (ps
	// lstart, LC_ALL=C) and command line when the group started (M6-close V1).
	LeaderStart string `json:"leader_start,omitempty"`
	LeaderCmd   string `json:"leader_cmd,omitempty"`
	// Members are the group's processes seen at the last refresh.
	Members []pgidMember `json:"members,omitempty"`
}

// pgidMember identifies one process exactly: pid plus kernel start time.
type pgidMember struct {
	PID   int    `json:"pid"`
	Start string `json:"start"`
}

// pgidLedgerFile is the on-disk shape.
type pgidLedgerFile struct {
	Version   int               `json:"version"`
	DaemonPID int               `json:"daemon_pid"`
	SeenAt    time.Time         `json:"seen_at"`
	Groups    []pgidLedgerEntry `json:"groups"`
}

// agentPGIDLedgerPath returns <workflow dir>/.itervox/run/agent-pgids.json.
func agentPGIDLedgerPath(workflowPath string) string {
	return filepath.Join(filepath.Dir(workflowPath), ".itervox", filepath.FromSlash(agentPGIDLedgerRel))
}

// pgidLedger is the procgroup.Observer the daemon installs. Its mutex
// serialises the in-memory map AND the file write, so the file always
// reflects a consistent set; it is not orchestrator State and is never
// touched by the event loop.
type pgidLedger struct {
	path      string
	daemonPID int
	now       func() time.Time

	mu      sync.Mutex
	entries map[int]pgidLedgerEntry
}

func newPGIDLedger(path string) *pgidLedger {
	return &pgidLedger{path: path, daemonPID: os.Getpid(), now: time.Now, entries: map[int]pgidLedgerEntry{}}
}

var _ procgroup.Observer = (*pgidLedger)(nil)

// GroupStarted implements procgroup.Observer.
func (l *pgidLedger) GroupStarted(pgid, pid int, label string) {
	// The leader's identity, read before taking the lock (one ps).
	var start, cmd string
	if procs, err := listProcesses(); err == nil {
		for _, p := range procs {
			if p.PID == pid {
				start, cmd = p.Start, p.Command
				break
			}
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	e := pgidLedgerEntry{Identifier: label, PGID: pgid, PID: pid, StartedAt: l.now().UTC(), DaemonPID: l.daemonPID,
		LeaderStart: start, LeaderCmd: cmd}
	if start != "" {
		e.Members = []pgidMember{{PID: pid, Start: start}}
	}
	l.entries[pgid] = e
	l.persistLocked()
}

// GroupExited implements procgroup.Observer.
func (l *pgidLedger) GroupExited(pgid int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.entries[pgid]; !ok {
		return
	}
	delete(l.entries, pgid)
	l.persistLocked()
}

// refresh re-records each live group's members (pid + kernel start time)
// and re-stamps seen_at.
func (l *pgidLedger) refresh() {
	l.mu.Lock()
	n := len(l.entries)
	l.mu.Unlock()
	if n == 0 {
		return
	}
	procs, err := listProcesses()
	l.mu.Lock()
	defer l.mu.Unlock()
	if err == nil {
		byGroup := map[int][]pgidMember{}
		for _, p := range procs {
			byGroup[p.PGID] = append(byGroup[p.PGID], pgidMember{PID: p.PID, Start: p.Start})
		}
		for pgid, e := range l.entries {
			if m := byGroup[pgid]; len(m) > 0 {
				e.Members = m
				l.entries[pgid] = e
			}
		}
	}
	if len(l.entries) > 0 {
		l.persistLocked()
	}
}

// Run refreshes seen_at every pgidLedgerRefresh until ctx ends.
func (l *pgidLedger) Run(ctx context.Context) {
	t := time.NewTicker(pgidLedgerRefresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.refresh()
		}
	}
}

// persistLocked writes the ledger; an empty ledger removes the file.
func (l *pgidLedger) persistLocked() {
	if len(l.entries) == 0 {
		if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
			slog.Warn("agent-pgids: remove ledger failed", "path", l.path, "error", err)
		}
		return
	}
	f := pgidLedgerFile{Version: pgidLedgerVersion, DaemonPID: l.daemonPID, SeenAt: l.now().UTC()}
	for _, e := range l.entries {
		f.Groups = append(f.Groups, e)
	}
	sort.Slice(f.Groups, func(i, j int) bool { return f.Groups[i].PGID < f.Groups[j].PGID })
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		slog.Warn("agent-pgids: encode ledger failed", "error", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		slog.Warn("agent-pgids: create ledger dir failed", "path", l.path, "error", err)
		return
	}
	if err := atomicfs.WriteFile(l.path, data, 0o600); err != nil {
		slog.Warn("agent-pgids: write ledger failed", "path", l.path, "error", err)
	}
}

// procMember is one live process as ps reports it.
type procMember struct {
	PID     int
	PGID    int
	Start   string // kernel start time, ps lstart under LC_ALL=C
	Command string // command line, whitespace-normalised
}

// listProcesses enumerates every process. A seam for tests; production
// reads `ps`.
var listProcesses = psListProcesses

// psListProcesses runs `ps -A -o pid=,pgid=,lstart=,command=` with LC_ALL=C
// (portable across darwin and Linux procps). lstart is five fields
// ("Sun Sep 27 07:52:22 2026") and is the kernel's start time of the process,
// not derived from the current clock.
func psListProcesses() ([]procMember, error) {
	cmd := exec.Command("ps", "-A", "-o", "pid=,pgid=,lstart=,command=")
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("agent-pgids: ps: %w", err)
	}
	return parsePSList(out), nil
}

// parsePSList parses psListProcesses' output.
func parsePSList(out []byte) []procMember {
	var procs []procMember
	for _, line := range bytes.Split(out, []byte("\n")) {
		f := strings.Fields(string(line))
		if len(f) < 8 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		pgid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		procs = append(procs, procMember{PID: pid, PGID: pgid, Start: strings.Join(f[2:7], " "), Command: strings.Join(f[7:], " ")})
	}
	return procs
}

// reapResult reports what reapOrphanedAgentGroups did with one entry.
type reapResult struct {
	Entry   pgidLedgerEntry
	Members int
	Action  string // "reaped", "reaped_members", "gone", "skipped_identity", "skipped_self"
}

// reapOrphanedAgentGroups reads the ledger a previous daemon left at path,
// signals every recorded group whose identity still matches (see the file
// comment: leader lstart + command, or recorded (pid, lstart) members), and
// removes the ledger. It is called once at startup,
// after the pid-file claim guarantees no other daemon owns the workflow and
// before this daemon starts any group of its own.
func reapOrphanedAgentGroups(path string) ([]reapResult, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("agent-pgids: read %s: %w", path, err)
	}
	var f pgidLedgerFile
	if err := json.Unmarshal(data, &f); err != nil {
		// A corrupt ledger cannot identify anything safely; drop it.
		_ = os.Remove(path)
		return nil, fmt.Errorf("agent-pgids: decode %s: %w", path, err)
	}
	procs, err := listProcesses()
	if err != nil {
		// Keep the ledger: the next start can try again.
		return nil, err
	}
	byPID := make(map[int]procMember, len(procs))
	byGroup := map[int][]procMember{}
	for _, p := range procs {
		byPID[p.PID] = p
		byGroup[p.PGID] = append(byGroup[p.PGID], p)
	}
	self, selfGroup := os.Getpid(), syscall.Getpgrp()
	var results []reapResult
	for _, e := range f.Groups {
		members := byGroup[e.PGID]
		r := reapResult{Entry: e, Members: len(members)}
		switch {
		case len(members) == 0:
			r.Action = "gone"
		case e.PGID <= 1 || e.PGID == selfGroup || containsPID(members, self):
			r.Action = "skipped_self"
		case leaderMatches(e, byPID):
			reapGroup(e.PGID)
			r.Action = "reaped"
		default:
			if n := reapRecordedMembers(e, byPID); n > 0 {
				r.Action = "reaped_members"
			} else {
				r.Action = "skipped_identity"
			}
		}
		results = append(results, r)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return results, fmt.Errorf("agent-pgids: remove %s: %w", path, err)
	}
	return results, nil
}

func containsPID(members []procMember, pid int) bool {
	for _, m := range members {
		if m.PID == pid {
			return true
		}
	}
	return false
}

// leaderMatches reports whether the recorded leader is still alive, still
// leads the group, and is the same process: same kernel start time AND same
// command line (M6-close V1).
func leaderMatches(e pgidLedgerEntry, byPID map[int]procMember) bool {
	if e.LeaderStart == "" || e.LeaderCmd == "" {
		return false
	}
	p, ok := byPID[e.PID]
	return ok && p.PGID == e.PGID && p.Start == e.LeaderStart && p.Command == e.LeaderCmd
}

// reapRecordedMembers signals each recorded member that is still the same
// process (pid + kernel start time) in the same group, and returns how many
// it signalled.
func reapRecordedMembers(e pgidLedgerEntry, byPID map[int]procMember) int {
	n := 0
	for _, m := range e.Members {
		if m.Start == "" || m.PID <= 1 {
			continue
		}
		if p, ok := byPID[m.PID]; ok && p.PGID == e.PGID && p.Start == m.Start {
			reapProcess(m.PID)
			n++
		}
	}
	return n
}

// reapProcess SIGTERMs pid, then SIGKILLs it after pgidReapGrace.
func reapProcess(pid int) {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return
	}
	deadline := time.Now().Add(pgidReapGrace)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// reapGroup SIGTERMs the group, gives it pgidReapGrace to exit, then runs
// the procgroup SIGKILL sweep for whatever is left.
func reapGroup(pgid int) {
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
		return // ESRCH: already gone
	}
	deadline := time.Now().Add(pgidReapGrace)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pgid, 0) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = procgroup.Kill(pgid)
}

// startAgentPGIDLedger reaps what a previous daemon left behind and installs
// this daemon's ledger as the process-wide procgroup observer.
func startAgentPGIDLedger(workflowPath string) *pgidLedger {
	path := agentPGIDLedgerPath(workflowPath)
	results, err := reapOrphanedAgentGroups(path)
	if err != nil {
		slog.Warn("agent-pgids: orphan reap failed", "path", path, "error", err)
	}
	for _, r := range results {
		lvl := slog.LevelInfo
		if r.Action == "reaped" || r.Action == "reaped_members" {
			lvl = slog.LevelWarn
		}
		slog.Log(context.Background(), lvl, "agent-pgids: previous daemon's process group",
			"action", r.Action, "pgid", r.Entry.PGID, "identifier", r.Entry.Identifier,
			"members", r.Members, "started_at", r.Entry.StartedAt, "previous_daemon_pid", r.Entry.DaemonPID)
	}
	l := newPGIDLedger(path)
	procgroup.SetObserver(l)
	return l
}
