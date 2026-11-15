package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// osGetpid is a tiny indirection so tests can stub self-PID if needed.
func osGetpid() int { return os.Getpid() }

// discoverDaemonsByCwd returns PIDs of itervox processes whose working
// directory equals projectDir. Used by `itervox stop` / `itervox status`
// as a fallback when the canonical PID file is absent — for example:
//
//   - Pre-existing daemon started by a binary that didn't have PID-file
//     support yet.
//   - User manually deleted .itervox/daemon.pid.
//   - Multiple daemons started for the same project (not supported, but we
//     want to surface them so the user can clean up).
//
// Implementation: `pgrep -f <itervoxExeName>` enumerates candidate PIDs by
// matching the full command line, then `lsof -p <pid> -d cwd -Fn` resolves
// each process's working directory. Works on macOS and Linux. Windows is
// intentionally unsupported (this feature is for the dev-on-laptop
// workflow; tooling for cross-platform process discovery is more trouble
// than it's worth for a v1).
//
// A non-nil, empty return means "no matching processes found". Errors from
// the underlying commands are swallowed — absence of matches is valid.
func discoverDaemonsByCwd(projectDir string) []int {
	if runtime.GOOS == "windows" {
		return nil
	}
	projectAbs, err := filepath.Abs(projectDir)
	if err != nil {
		return nil
	}

	// Enumerate candidate PIDs. Using `pgrep -f` so the full command line
	// (not just the short process name) is matched — important because macOS
	// truncates comm to 15 characters and the binary may be launched via a
	// symlink with a different basename.
	out, err := exec.Command("pgrep", "-f", "itervox").Output()
	if err != nil {
		// pgrep exits 1 when no matches; Err just means "empty".
		return nil
	}

	self := findSelfPID()
	var pids []int
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		pid, convErr := strconv.Atoi(strings.TrimSpace(scanner.Text()))
		if convErr != nil || pid <= 1 || pid == self {
			continue
		}
		if !isDaemonCommandLine(processArgs(pid)) {
			continue // a guardrail shim, a tmux session, a client command, ...
		}
		cwd := processCwd(pid)
		if cwd == "" {
			continue
		}
		if pathEquals(cwd, projectAbs) {
			pids = append(pids, pid)
		}
	}
	return pids
}

// findSelfPID returns the current process PID, used to exclude ourselves
// from scan results.
func findSelfPID() int {
	return osGetpid()
}

// processCwd returns the working directory of the given PID or "" on error.
// macOS has no /proc, so we use `lsof -p <pid> -d cwd -Fn`; Linux can read
// /proc/<pid>/cwd directly (faster, no fork), but lsof also works there.
func processCwd(pid int) string {
	if runtime.GOOS == "linux" {
		if cwd, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd"); err == nil {
			return cwd
		}
	}
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		// lsof -Fn prefixes name lines with "n", e.g. "n/Users/me/repo".
		if strings.HasPrefix(line, "n") {
			return strings.TrimPrefix(line, "n")
		}
	}
	return ""
}

// pathEquals reports whether two filesystem paths refer to the same
// directory after normalisation (abs + clean). Does NOT follow symlinks —
// if the daemon was started via a symlinked path and `workflowPath` is the
// real path (or vice versa), this returns false. Acceptable: the common
// case is both sides use the same convention.
func pathEquals(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	return filepath.Clean(aa) == filepath.Clean(bb)
}

// discoverAllDaemons returns PIDs of every live itervox process on the
// system, regardless of cwd. Used by `itervox status --all` so the user
// can see a global view when they run multiple daemons across repos.
func discoverAllDaemons() []int {
	if runtime.GOOS == "windows" {
		return nil
	}
	out, err := exec.Command("pgrep", "-f", "itervox").Output()
	if err != nil {
		return nil
	}
	self := findSelfPID()
	var pids []int
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		pid, convErr := strconv.Atoi(strings.TrimSpace(scanner.Text()))
		if convErr != nil || pid <= 1 || pid == self || !isDaemonCommandLine(processArgs(pid)) {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

// isDaemonCommandLine reports whether argv is an itervox daemon: the itervox
// binary started with no subcommand (only flags), or as `itervox demo`,
// which runs the daemon on a scratch workflow. `pgrep -f itervox` also
// matches every process whose command line merely mentions itervox, and a
// tmux session started from Sessions → New session has several in the
// project directory: tmux itself (`-s itervox-…`), the session's shell
// (its PATH names the .itervox/ guardrail shims), and the guardrail shims
// and pre-push hook, which ARE the itervox binary, run as `itervox
// guardrail-shim … git`. None of them is a daemon, nor is a client command
// such as `itervox watch`. argv[0] may be a path, a symlink or a renamed
// binary whose basename starts with "itervox".
func isDaemonCommandLine(argv []string) bool {
	if len(argv) == 0 || !strings.HasPrefix(filepath.Base(argv[0]), "itervox") {
		return false
	}
	if len(argv) == 1 {
		return true
	}
	switch arg := argv[1]; arg {
	case "demo":
		return true
	case "-h", "-help", "--help", "-version", "--version":
		return false
	default:
		// main() runs the daemon when argv[1] is a flag; any other first
		// argument is a subcommand or a re-exec entrypoint.
		return strings.HasPrefix(arg, "-")
	}
}

// processArgs returns pid's argv, or nil when it cannot be read. Linux reads
// /proc/<pid>/cmdline (NUL-separated, exact). Elsewhere (macOS) `ps` prints
// the executable path (comm) and the space-joined argv (args); the path is
// split off the front so a path with spaces stays one element.
func processArgs(pid int) []string {
	if runtime.GOOS == "linux" {
		raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
		if err != nil || len(raw) == 0 {
			return nil
		}
		return strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	}
	p := strconv.Itoa(pid)
	argsOut, err := exec.Command("ps", "-ww", "-o", "args=", "-p", p).Output()
	if err != nil {
		return nil
	}
	args := strings.TrimSpace(string(argsOut))
	if commOut, err := exec.Command("ps", "-ww", "-o", "comm=", "-p", p).Output(); err == nil {
		if comm := strings.TrimSpace(string(commOut)); comm != "" && strings.HasPrefix(args, comm) {
			return append([]string{comm}, strings.Fields(args[len(comm):])...)
		}
	}
	return strings.Fields(args)
}
