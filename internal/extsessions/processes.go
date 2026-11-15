package extsessions

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// procRoot is the Linux process filesystem.
const procRoot = "/proc"

// inspectTimeout bounds one ps / lsof call.
const inspectTimeout = 5 * time.Second

// SystemProcesses lists this user's claude and codex processes with their
// working directory and open files. It only reads: /proc where it exists
// (Linux), else `ps` and `lsof` (macOS, BSD), neither of which needs a
// privacy (TCC) permission for the user's own processes. Other processes
// are listed without cwd or files.
func SystemProcesses() ([]Process, error) {
	if err := processInspectionUnavailable(); err != nil {
		return nil, err
	}
	if st, err := os.Stat(filepath.Join(procRoot, "self")); err == nil && st.IsDir() {
		return procProcesses(procRoot)
	}
	return psProcesses()
}

// procProcesses reads root (a /proc tree). Processes of other users are
// skipped: they cannot have written this user's transcripts.
func procProcesses(root string) ([]Process, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("extsessions: read %s: %w", root, err)
	}
	uid := os.Getuid()
	self := os.Getpid()
	var out []Process
	for _, e := range entries {
		pid, ok := atoiOK(e.Name())
		if !ok || pid == self {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if st, err := os.Stat(dir); err != nil || !sameUID(st, uid) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		p := Process{PID: pid, Args: strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")}
		if stat, err := os.ReadFile(filepath.Join(dir, "stat")); err == nil {
			p.PPID = parseStatPPID(stat)
		}
		if comm, err := os.ReadFile(filepath.Join(dir, "comm")); err == nil {
			p.Name = strings.TrimSpace(string(comm))
		}
		if ProcessBackend(p) == "" {
			out = append(out, p) // not an agent CLI: no need for its cwd or files
			continue
		}
		p.Cwd, _ = os.Readlink(filepath.Join(dir, "cwd"))
		fdDir := filepath.Join(dir, "fd")
		if fds, err := os.ReadDir(fdDir); err == nil {
			p.Files = []string{}
			for _, fd := range fds {
				target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
				if err == nil && filepath.IsAbs(target) {
					p.Files = append(p.Files, target)
				}
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// parseStatPPID reads the parent PID from /proc/<pid>/stat: the fourth
// field, counted after the parenthesised command name (which may contain
// spaces and parentheses itself).
func parseStatPPID(stat []byte) int {
	s := string(stat)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0
	}
	n, _ := atoiOK(f[1])
	return n
}

// lsofCacheTTL bounds how long one lsof result of a process is reused
// (#212). Within it, a transcript the process opened since is not seen in
// Files, which only lowers a "has the transcript open" answer to the
// "running in the session's directory" one; it never rules a session out.
const lsofCacheTTL = 2 * time.Minute

// startTolerance is how far two scans' start-time estimates (scan time
// minus ps's whole-second etime) may differ for the same process.
const startTolerance = 2 * time.Second

// lsofCache remembers the lsof result of each agent process, keyed by pid
// and start time, so the 15-second scan runs lsof only for processes it has
// not inspected yet, or not within lsofCacheTTL (#212). A pid reused by a
// new process has a different start time and is inspected afresh.
type lsofCache struct {
	mu      sync.Mutex
	entries map[int]lsofEntry
}

type lsofEntry struct {
	started time.Time
	checked time.Time
	cwd     string
	files   []string
}

// lookup returns the cached result for pid started at started, if fresh.
func (c *lsofCache) lookup(pid int, started, now time.Time) (lsofEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[pid]
	if !ok || now.Sub(e.checked) >= lsofCacheTTL {
		return lsofEntry{}, false
	}
	if d := e.started.Sub(started); d > startTolerance || d < -startTolerance {
		return lsofEntry{}, false
	}
	return e, true
}

// store records the result for pid.
func (c *lsofCache) store(pid int, e lsofEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[int]lsofEntry{}
	}
	c.entries[pid] = e
}

// prune forgets the processes no longer listed.
func (c *lsofCache) prune(live map[int]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for pid := range c.entries {
		if !live[pid] {
			delete(c.entries, pid)
		}
	}
}

// systemLsofCache is the cache of SystemProcesses.
var systemLsofCache lsofCache

// psProcesses lists this user's processes with ps, then reads the cwd and
// open files of each agent CLI with lsof (cached per process).
func psProcesses() ([]Process, error) {
	var lsof func(int) (string, []string)
	if path := lsofPath(); path != "" {
		lsof = func(pid int) (string, []string) { return lsofFiles(path, pid) }
	}
	return inspectPS(runPS, lsof, &systemLsofCache, time.Now())
}

// runPS lists this user's processes: pid, ppid, elapsed time and arguments.
// LC_ALL=C keeps the columns' format independent of the user's locale.
func runPS() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), inspectTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-x", "-ww", "-o", "pid=,ppid=,etime=,args=", "-U", strconv.Itoa(os.Getuid()))
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return cmd.Output()
}

// inspectPS is psProcesses with its process lister, lsof runner (nil: no
// lsof on this host), cache and clock injected.
func inspectPS(ps func() ([]byte, error), lsof func(int) (string, []string), cache *lsofCache, now time.Time) ([]Process, error) {
	raw, err := ps()
	if err != nil {
		return nil, fmt.Errorf("extsessions: ps: %w", err)
	}
	rows := parsePS(raw, os.Getpid())
	live := make(map[int]bool, len(rows))
	for _, r := range rows {
		live[r.PID] = true
	}
	procs := make([]Process, len(rows))
	for i, r := range rows {
		procs[i] = r.Process
		if ProcessBackend(r.Process) == "" || lsof == nil {
			continue // no lsof: cwd unknown, CheckLiveness treats the process as possibly this session
		}
		started := now.Add(-r.elapsed)
		if r.elapsed >= 0 {
			if e, ok := cache.lookup(r.PID, started, now); ok {
				procs[i].Cwd, procs[i].Files = e.cwd, slices.Clone(e.files)
				continue
			}
		}
		procs[i].Cwd, procs[i].Files = lsof(r.PID)
		if r.elapsed >= 0 && procs[i].Cwd != "" {
			cache.store(r.PID, lsofEntry{started: started, checked: now, cwd: procs[i].Cwd, files: slices.Clone(procs[i].Files)})
		}
	}
	cache.prune(live)
	return procs, nil
}

// psRow is one parsed ps line: the process and how long it has run
// (negative when the etime column could not be read).
type psRow struct {
	Process
	elapsed time.Duration
}

// parsePS parses `ps -o pid=,ppid=,etime=,args=` output, skipping self.
// Arguments are split on spaces (ps does not quote them), which is enough
// to recognise the executable and a session ID argument.
func parsePS(raw []byte, self int) []psRow {
	var out []psRow
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		all := strings.Fields(sc.Text())
		if len(all) < 4 {
			continue
		}
		pid, isNum := atoiOK(all[0])
		ppid, ppidNum := atoiOK(all[1])
		if !isNum || !ppidNum || pid == self {
			continue
		}
		fields := all[3:]
		out = append(out, psRow{
			Process: Process{PID: pid, PPID: ppid, Name: filepath.Base(fields[0]), Args: fields},
			elapsed: parseEtime(all[2]),
		})
	}
	return out
}

// parseEtime reads ps's etime, [[dd-]hh:]mm:ss; -1 when malformed.
func parseEtime(s string) time.Duration {
	var days int
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, isNum := atoiOK(d)
		if !isNum {
			return -1
		}
		days, s = n, rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return -1
	}
	secs := 0
	for _, p := range parts {
		n, isNum := atoiOK(p)
		if !isNum {
			return -1
		}
		secs = secs*60 + n
	}
	return time.Duration(days)*24*time.Hour + time.Duration(secs)*time.Second
}

func lsofPath() string {
	if p, err := exec.LookPath("lsof"); err == nil {
		return p
	}
	if _, err := os.Stat("/usr/sbin/lsof"); err == nil {
		return "/usr/sbin/lsof"
	}
	return ""
}

// lsofFiles reads one process's cwd and open regular files with
// `lsof -p <pid> -F ftn`. lsof exits non-zero when it could not stat some
// file; what it printed is still used.
func lsofFiles(lsof string, pid int) (cwd string, files []string) {
	ctx, cancel := context.WithTimeout(context.Background(), inspectTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, lsof, "-w", "-n", "-P", "-p", strconv.Itoa(pid), "-F", "ftn")
	raw, err := cmd.Output()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return "", nil
	}
	return parseLsof(raw)
}

// parseLsof parses lsof -F ftn output: per file an f (descriptor), t (type)
// and n (name) line.
func parseLsof(raw []byte) (cwd string, files []string) {
	files = []string{}
	var fd, typ string
	for _, line := range strings.Split(string(raw), "\n") {
		if line == "" {
			continue
		}
		v := line[1:]
		switch line[0] {
		case 'f':
			fd, typ = v, ""
		case 't':
			typ = v
		case 'n':
			switch {
			case fd == "cwd":
				cwd = v
			case typ == "REG" && filepath.IsAbs(v):
				files = append(files, v)
			}
		}
	}
	return cwd, files
}
