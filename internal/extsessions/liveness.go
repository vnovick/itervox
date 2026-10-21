package extsessions

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// RecentWriteWindow: a transcript written to more recently than this is
// treated as possibly in use even when no process can be tied to it.
const RecentWriteWindow = 2 * time.Minute

// Process is one process as the liveness check sees it. Inspection is
// read-only: /proc on Linux, ps and lsof elsewhere. Nothing is sent to a
// process and nothing is attached to it.
type Process struct {
	PID  int
	PPID int    // the parent's PID; 0 when unknown
	Name string // the executable's base name
	Args []string
	// Cwd is the process's working directory; "" when it could not be read.
	Cwd string
	// Files are the regular files it has open; nil when not read.
	Files []string
}

// Liveness says whether the CLI that wrote a session may still be running.
type Liveness struct {
	// Running is true when the session may still be in use: a process is
	// tied to it, or the check could not rule one out.
	Running bool
	// Certain is false when Running is a precaution (nothing proves the
	// session is in use, but nothing rules it out).
	Certain bool
	Reason  string
	// PID is the process the decision rests on (0: none).
	PID int
}

// ProcessBackend is "claude" or "codex" when p runs that CLI — its
// executable, or the script an interpreter (node, bun, deno) runs — and ""
// otherwise.
func ProcessBackend(p Process) string {
	candidates := []string{p.Name}
	if len(p.Args) > 0 {
		candidates = append(candidates, p.Args[0])
		switch filepath.Base(p.Args[0]) {
		case "node", "bun", "deno", "nodejs":
			for _, a := range p.Args[1:min(len(p.Args), 4)] {
				if !strings.HasPrefix(a, "-") {
					candidates = append(candidates, a)
					break
				}
			}
		}
	}
	for _, c := range candidates {
		base := filepath.Base(c)
		switch {
		case base == "claude" || strings.Contains(c, "claude-code"):
			return BackendClaude
		case base == "codex" || strings.Contains(c, "@openai/codex") || base == "codex.js":
			return BackendCodex
		}
	}
	return ""
}

// CheckLiveness decides whether s may still be in use, from a process list
// (listErr: the list could not be read) and the transcript's last write.
// When unsure it answers Running, and Reason says why.
func CheckLiveness(s Session, procs []Process, listErr error, now time.Time) Liveness {
	if listErr != nil {
		return Liveness{Running: true, Reason: fmt.Sprintf(
			"could not inspect running processes (%v), so it is treated as still running", listErr)}
	}
	path := canonical(s.Path)
	cwd := canonical(s.Cwd)
	var maybe *Liveness
	for _, p := range procs {
		if ProcessBackend(p) != s.Backend {
			continue
		}
		if slices.ContainsFunc(p.Files, func(f string) bool { return canonical(f) == path }) {
			return Liveness{Running: true, Certain: true, PID: p.PID, Reason: fmt.Sprintf(
				"%s process %d has the transcript open", s.Backend, p.PID)}
		}
		if slices.ContainsFunc(p.Args, func(a string) bool { return strings.Contains(a, s.ID) }) {
			return Liveness{Running: true, Certain: true, PID: p.PID, Reason: fmt.Sprintf(
				"%s process %d was started for this session", s.Backend, p.PID)}
		}
		if maybe != nil {
			continue
		}
		switch {
		case p.Cwd == "":
			maybe = &Liveness{Running: true, Reason: fmt.Sprintf(
				"a %s process (%d) is running whose directory could not be read; it may be this session", s.Backend, p.PID)}
		case cwd != "" && canonical(p.Cwd) == cwd:
			maybe = &Liveness{Running: true, PID: p.PID, Reason: fmt.Sprintf(
				"a %s process (%d) is running in the session's directory; it may be this session", s.Backend, p.PID)}
		}
	}
	if maybe != nil {
		return *maybe
	}
	if age := now.Sub(s.UpdatedAt); age < RecentWriteWindow {
		return Liveness{Running: true, Reason: fmt.Sprintf(
			"the transcript was written to %s ago; wait until it has been idle for %s",
			age.Round(time.Second), RecentWriteWindow)}
	}
	return Liveness{Certain: true, Reason: fmt.Sprintf(
		"no %s process is running in the session's directory", s.Backend)}
}

// canonical cleans p and resolves its symlinks when it can.
func canonical(p string) string {
	if p == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(real)
	}
	return filepath.Clean(p)
}

// Scanner discovers sessions and checks their liveness.
type Scanner struct {
	Options Options
	// Processes lists the host's processes; nil uses SystemProcesses.
	Processes func() ([]Process, error)
}

func (sc Scanner) processes() ([]Process, error) {
	if sc.Processes != nil {
		return sc.Processes()
	}
	return SystemProcesses()
}

// Scan lists the sessions in scope with their liveness, plus the process
// list liveness was decided from (nil when there were no sessions or it
// could not be read), for callers that match sessions to tmux panes by
// process ancestry.
func (sc Scanner) Scan(now time.Time) ([]Session, []Process, error) {
	sessions, err := Discover(sc.Options, now)
	if len(sessions) == 0 {
		return sessions, nil, err
	}
	procs, listErr := sc.processes()
	for i := range sessions {
		l := CheckLiveness(sessions[i], procs, listErr, now)
		sessions[i].Running, sessions[i].RunningReason, sessions[i].PID = l.Running, l.Reason, l.PID
	}
	if listErr != nil {
		procs = nil
	}
	return sessions, procs, err
}

// Ancestors returns pid's parent chain in procs, nearest first, stopping at
// an unknown parent, PID 1 or a loop.
func Ancestors(procs []Process, pid int) []int {
	parent := make(map[int]int, len(procs))
	for _, p := range procs {
		parent[p.PID] = p.PPID
	}
	var chain []int
	seen := map[int]bool{pid: true}
	for cur := parent[pid]; cur > 1 && !seen[cur]; cur = parent[cur] {
		seen[cur] = true
		chain = append(chain, cur)
	}
	return chain
}

// FindProcess returns the process pid from procs.
func FindProcess(procs []Process, pid int) (Process, bool) {
	for _, p := range procs {
		if p.PID == pid {
			return p, true
		}
	}
	return Process{}, false
}

func atoiOK(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	return n, err == nil
}
