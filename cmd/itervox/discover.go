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
		if convErr != nil || pid <= 1 || pid == self {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}
