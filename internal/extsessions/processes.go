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
	"strconv"
	"strings"
	"syscall"
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

// sameUID reports whether st (a /proc/<pid> directory) belongs to uid.
// Unknown ownership counts as the same user (it is inspected, not skipped).
func sameUID(st os.FileInfo, uid int) bool {
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return true
	}
	return int(sys.Uid) == uid
}

// psProcesses lists this user's processes with ps, then reads the cwd and
// open files of each agent CLI with lsof.
func psProcesses() ([]Process, error) {
	ctx, cancel := context.WithTimeout(context.Background(), inspectTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-x", "-ww", "-o", "pid=,args=", "-U", strconv.Itoa(os.Getuid()))
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("extsessions: ps: %w", err)
	}
	procs := parsePS(raw, os.Getpid())
	lsof := lsofPath()
	for i := range procs {
		if ProcessBackend(procs[i]) == "" {
			continue
		}
		if lsof == "" {
			continue // cwd unknown: CheckLiveness treats the process as possibly this session
		}
		procs[i].Cwd, procs[i].Files = lsofFiles(lsof, procs[i].PID)
	}
	return procs, nil
}

// parsePS parses `ps -o pid=,args=` output, skipping self. Arguments are
// split on spaces (ps does not quote them), which is enough to recognise
// the executable and a session ID argument.
func parsePS(raw []byte, self int) []Process {
	var out []Process
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		pidText, args, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		pid, isNum := atoiOK(pidText)
		if !ok || !isNum || pid == self {
			continue
		}
		fields := strings.Fields(args)
		if len(fields) == 0 {
			continue
		}
		out = append(out, Process{PID: pid, Name: filepath.Base(fields[0]), Args: fields})
	}
	return out
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
