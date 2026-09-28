package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"
)

// runStop terminates the itervox daemon owning the current project
// directory.
//
// CORE-163: a pid alone never authorises a signal. A daemon SIGKILLed (or
// crashed) leaves its record behind, and its pid can be recycled by any
// unrelated process of the same user — the editor, a shell — which the old
// "workflow path matches, pid is alive" check would then SIGTERM and, after
// the grace period, SIGKILL. The pid lock from CORE-039 is the authority
// instead: a lock-holding daemon keeps `.itervox/runtime/daemon.pid.lock`
// flocked for its whole life and the kernel drops that lock the instant the
// daemon dies. So:
//
//   - If `stop` can take the lock, no lock-holding daemon is alive: the record
//     is stale. It is removed (while the lock is held, so a daemon starting
//     concurrently cannot lose a fresh record) and nothing is signalled.
//   - Only while another process holds the lock is the record's pid signalled,
//     and the SIGKILL escalation re-checks that the lock is still held and the
//     record still names that pid.
//   - The directory-scan fallback follows the same rule: its hits are
//     signalled only while the lock is held and no lock-verified record names
//     the daemon; when the lock is free they are not lock holders.
//   - Pre-CORE-039 (legacy two-field) records and scan hits with a free lock
//     carry no lock evidence. They are listed and NOT signalled unless the
//     operator passes --legacy, which restores the old pid-probe behaviour
//     for exactly those candidates (e.g. stopping a daemon started by a
//     pre-v0.2.1 binary across an upgrade).
//
// On a platform without flock (pidLockSupported == false) there is no lock
// to consult and the pre-CORE-039 pid probe is used, as in claimPIDFile.
//
// Multi-repo safety: the PID file lives under `<workflowDir>/.itervox/daemon.pid`.
// Each project has its own PID file, so `itervox stop` run from repo A never
// touches the daemon in repo B — even when a single user has several daemons
// running concurrently.
func runStop(args []string) {
	fs := flag.NewFlagSet("stop", flag.ExitOnError)
	workflowPath := fs.String("workflow", "WORKFLOW.md", "path to WORKFLOW.md for the project whose daemons should stop")
	grace := fs.Duration("grace", 30*time.Second, "grace period before escalating SIGTERM to SIGKILL")
	force := fs.Bool("force", false, "skip the graceful grace period and SIGKILL immediately")
	legacy := fs.Bool("legacy", false, "also signal processes the pid lock cannot vouch for (a pre-v0.2.1 daemon's two-field PID file, or directory-scan hits while no daemon holds the lock); only use after confirming the listed PIDs are itervox")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: itervox stop [flags]

Stops the itervox daemon serving the current project directory.

A process is signalled only while it provably owns this project: the daemon
holds .itervox/runtime/daemon.pid.lock for its whole life, so a PID file whose
lock is free is stale (its PID may belong to an unrelated process by now) and
is removed without signalling anything.

Resolution order:
  1. PID file at <workflowDir>/.itervox/daemon.pid (canonical), signalled
     while its lock is held.
  2. Fallback: running processes whose working directory matches the
     project directory, used only while the lock is held and the PID file
     does not name the daemon.

Daemons started by a pre-v0.2.1 binary write a PID file without the lock;
they are listed but only signalled with --legacy.

Daemons in OTHER project directories are never touched. Run this command
separately in each repo you want to stop.

Flags:
`)
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	code := stopProject(*workflowPath, stopOptions{
		grace:    *grace,
		force:    *force,
		legacy:   *legacy,
		discover: discoverDaemonsByCwd,
	}, os.Stderr)
	if code != 0 {
		fatalExit(code)
	}
}

// stopOptions parameterises stopProject; discover is injectable for tests.
type stopOptions struct {
	grace    time.Duration
	force    bool
	legacy   bool
	discover func(projectDir string) []int
}

// pidLockState is what stop learned from probing the project's pid lock.
type pidLockState int

const (
	// lockHeldByOther: a live process holds the lock — a daemon owns the project.
	lockHeldByOther pidLockState = iota
	// lockFree: nothing holds it; no lock-holding daemon is alive.
	lockFree
	// lockUnknown: the lock could not be probed (error, or no flock on this
	// platform). Only the legacy pid-probe rule applies.
	lockUnknown
)

// probePIDLock reports the lock state for pidPath. For lockFree it returns
// the release of the lock stop now holds; the caller keeps it while it
// removes a stale record. A missing runtime directory means no lock-holding
// daemon ever ran here; it is reported as lockFree without creating anything.
func probePIDLock(pidPath string) (pidLockState, func(), error) {
	if !pidLockSupported {
		return lockUnknown, func() {}, nil
	}
	lockPath := pidLockPath(pidPath)
	if _, err := os.Lstat(filepath.Dir(lockPath)); os.IsNotExist(err) {
		return lockFree, func() {}, nil
	}
	release, acquired, err := tryLockPIDFile(lockPath)
	if err != nil {
		return lockUnknown, func() {}, err
	}
	if !acquired {
		return lockHeldByOther, func() {}, nil
	}
	return lockFree, release, nil
}

// stopProject implements `itervox stop` and returns the process exit code:
// 0 when every daemon it could verify was stopped (or there was none), 1
// when it refused to signal unverified candidates or could not resolve the
// project.
func stopProject(workflowPath string, opts stopOptions, out io.Writer) int {
	projectDir, projectErr := resolveProjectDir(workflowPath)
	if projectErr != nil {
		_, _ = fmt.Fprintf(out, "itervox stop: cannot resolve project directory: %v\n", projectErr)
		return 1
	}
	pidPath, _ := pidFilePath(workflowPath)

	var rec pidRecord
	haveRecord := false
	if data, err := os.ReadFile(pidPath); err == nil {
		if parsed, perr := parsePIDRecord(data); perr == nil {
			rec, haveRecord = parsed, true
		} else {
			_, _ = fmt.Fprintf(out, "itervox stop: warning: ignoring malformed PID file %s: %v\n", pidPath, perr)
		}
	} else if !os.IsNotExist(err) {
		_, _ = fmt.Fprintf(out, "itervox stop: warning: failed to read PID file: %v\n", err)
	}
	// lockAccounted: the lock holder is already identified by a record, so a
	// directory-scan hit is someone else and needs --legacy.
	lockAccounted := false
	if haveRecord && rec.workflow != "" {
		if expected, absErr := filepath.Abs(workflowPath); absErr == nil && expected != rec.workflow {
			_, _ = fmt.Fprintf(out,
				"itervox stop: WARNING — PID file references a different WORKFLOW.md (%s). Skipping PID %d.\n",
				rec.workflow, rec.pid)
			haveRecord = false
			lockAccounted = rec.locked // that other WORKFLOW.md's daemon holds this directory's lock
		}
	}

	state, release, lerr := probePIDLock(pidPath)
	if lerr != nil {
		_, _ = fmt.Fprintf(out, "itervox stop: warning: cannot probe the pid lock (%v); only --legacy candidates can be signalled\n", lerr)
	}

	// verified: signalled because the lock proves a live daemon owns them.
	// unverified: listed, signalled only with --legacy (or on a platform
	// without flock, where the pid probe is all there is).
	var verified, unverified []int
	addUnique := func(dst *[]int, pid int) {
		if pid > 0 && !slices.Contains(verified, pid) && !slices.Contains(unverified, pid) {
			*dst = append(*dst, pid)
		}
	}

	// Record first, while stop may hold the lock (lockFree), so a stale
	// record is removed without racing a daemon that is starting now. The
	// lock is released before the (slow: pgrep + lsof) directory scan, so a
	// daemon starting meanwhile is not refused because stop held its lock.
	switch {
	case !haveRecord:
	case state == lockHeldByOther && rec.locked:
		addUnique(&verified, rec.pid)
		lockAccounted = true
	case state == lockFree && rec.locked:
		// The writer held the lock for its lifetime and we hold it now:
		// the writer is gone, whatever owns rec.pid today.
		_, _ = fmt.Fprintf(out,
			"itervox stop: PID file %s is stale (pid %d no longer holds the daemon lock; that pid may belong to an unrelated process now) — removed it, nothing signalled\n",
			pidPath, rec.pid)
		if err := os.Remove(pidPath); err != nil && !os.IsNotExist(err) {
			_, _ = fmt.Fprintf(out, "itervox stop: warning: failed to remove stale PID file: %v\n", err)
		}
		haveRecord = false
	case processAlive(rec.pid):
		// Legacy record (or a lock that could not be probed): the pid
		// probe is the only evidence, which is exactly what CORE-163 is
		// about — list it, signal it only with --legacy.
		addUnique(&unverified, rec.pid)
	case state == lockFree:
		// Legacy record whose pid is dead: stale by any rule.
		_ = os.Remove(pidPath)
		haveRecord = false
	}
	release()

	// Directory-scan fallback, same rule: its hits are lock-verified only
	// while a daemon holds the lock and no lock-marked record already names
	// it. Otherwise (lock free: they are not lock holders; or a record names
	// the holder: they are someone else) they need --legacy.
	for _, pid := range opts.discover(projectDir) {
		if state == lockHeldByOther && !lockAccounted {
			addUnique(&verified, pid)
		} else {
			addUnique(&unverified, pid)
		}
	}

	// Without flock the pid probe is the only evidence there is (the
	// pre-CORE-039 behaviour); elsewhere unverified pids need --legacy.
	legacyAllowed := opts.legacy || !pidLockSupported
	refused := 0
	if len(unverified) > 0 && !legacyAllowed {
		refused = len(unverified)
		_, _ = fmt.Fprintf(out,
			"itervox stop: NOT signalling %v: no itervox pid lock vouches for them (a pre-v0.2.1 daemon, or an unrelated process that matched the directory scan or reused a recorded pid). Confirm they are itervox daemons for %s and rerun with --legacy to signal them.\n",
			unverified, projectDir)
		unverified = nil
	}

	if len(verified) == 0 && len(unverified) == 0 {
		if refused > 0 {
			return 1
		}
		_, _ = fmt.Fprintf(out, "itervox stop: no running daemon found for %s\n", projectDir)
		return 0
	}

	targets := append(append([]int{}, verified...), unverified...)
	_, _ = fmt.Fprintf(out, "itervox stop: found %d daemon(s) for %s: %v\n", len(targets), projectDir, targets)

	sig := syscall.SIGTERM
	if opts.force {
		sig = syscall.SIGKILL
	}
	// Send the initial signal to each PID. Ignoring per-PID errors so one
	// permission failure doesn't abort the whole batch.
	for _, pid := range targets {
		signalPID(out, pid, sig)
	}

	if !opts.force {
		// Wait until every target is gone or --grace elapses. A verified
		// daemon is gone when the lock is free (not when its pid stops
		// answering: the pid could be recycled inside the grace window).
		deadline := time.Now().Add(opts.grace)
		for time.Now().Before(deadline) && !stopTargetsGone(pidPath, verified, unverified) {
			time.Sleep(50 * time.Millisecond)
		}
		if stopTargetsGone(pidPath, verified, unverified) {
			_, _ = fmt.Fprintf(out, "itervox stop: all daemons exited cleanly\n")
		} else {
			_, _ = fmt.Fprintf(out, "itervox stop: grace period elapsed, SIGKILLing survivors\n")
			if len(verified) > 0 && lockStillVouches(pidPath, rec, haveRecord) {
				for _, pid := range verified {
					signalPID(out, pid, syscall.SIGKILL)
				}
			}
			for _, pid := range filterAlive(unverified) {
				signalPID(out, pid, syscall.SIGKILL)
			}
		}
	}

	removeStalePIDRecord(out, pidPath)
	if refused > 0 {
		return 1
	}
	return 0
}

// signalPID sends sig to pid, reporting the outcome.
func signalPID(out io.Writer, pid int, sig syscall.Signal) {
	proc, err := os.FindProcess(pid)
	if err != nil {
		_, _ = fmt.Fprintf(out, "  PID %d: FindProcess failed: %v\n", pid, err)
		return
	}
	if err := proc.Signal(sig); err != nil {
		_, _ = fmt.Fprintf(out, "  PID %d: signal %s failed: %v\n", pid, sig, err)
		return
	}
	_, _ = fmt.Fprintf(out, "  PID %d: %s sent\n", pid, sig)
}

// stopTargetsGone reports whether every signalled daemon has exited: the pid
// lock is free for the lock-verified ones, and the pid probe fails for the
// --legacy ones.
func stopTargetsGone(pidPath string, verified, unverified []int) bool {
	if len(filterAlive(unverified)) > 0 {
		return false
	}
	if len(verified) == 0 {
		return true
	}
	state, release, _ := probePIDLock(pidPath)
	release()
	return state == lockFree
}

// lockStillVouches re-checks, right before a SIGKILL escalation, that the
// lock is still held and the record still names the same daemon — so a
// daemon that exited during the grace period, and whose pid was recycled or
// whose project was re-claimed by a new daemon, is not killed.
func lockStillVouches(pidPath string, rec pidRecord, haveRecord bool) bool {
	state, release, _ := probePIDLock(pidPath)
	release()
	if state != lockHeldByOther {
		return false
	}
	if !haveRecord || !rec.locked {
		return true // scan-verified: the lock is the only evidence we had
	}
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return false
	}
	now, err := parsePIDRecord(data)
	return err == nil && now.pid == rec.pid
}

// removeStalePIDRecord removes the pid file after a stop, but only while
// stop holds the lock — never under a daemon that (re)claimed the project in
// the meantime. Without flock it falls back to an unconditional remove.
func removeStalePIDRecord(out io.Writer, pidPath string) {
	state, release, _ := probePIDLock(pidPath)
	defer release()
	if state == lockHeldByOther {
		return
	}
	if err := os.Remove(pidPath); err != nil && !os.IsNotExist(err) {
		_, _ = fmt.Fprintf(out, "itervox stop: warning: failed to remove PID file: %v\n", err)
	}
}

// resolveProjectDir returns the absolute directory containing the given
// WORKFLOW.md path. Accepts both a file path and a directory path.
func resolveProjectDir(workflowPath string) (string, error) {
	abs, err := filepath.Abs(workflowPath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err == nil && info.IsDir() {
		return abs, nil
	}
	return filepath.Dir(abs), nil
}

// filterAlive returns only the PIDs still running. Used by the wait loop.
func filterAlive(pids []int) []int {
	var out []int
	for _, pid := range pids {
		if processAlive(pid) {
			out = append(out, pid)
		}
	}
	return out
}
