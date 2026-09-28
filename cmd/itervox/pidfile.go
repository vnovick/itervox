package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/vnovick/itervox/internal/config"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// PID-file conventions
// --------------------
//
// Path: `<workflowDir>/.itervox/daemon.pid`
//
// The file holds a single line: `<pid>\t<workflowPath>\tflock\n`. The
// workflowPath lets `itervox stop` skip a record written for a different
// WORKFLOW.md in the same directory; it does NOT protect against a recycled
// pid — only the lock does (CORE-163, see stop.go).
// The trailing `flock` marker (CORE-039) says the writer held the pid lock
// (`.itervox/runtime/daemon.pid.lock`, see pidLockPath) for its lifetime, so
// the lock — not the pid — decides whether the writer is still alive.
// Pre-CORE-039 daemons wrote the two-field `<pid>\t<workflowPath>\n` record
// without a lock; readPIDFile accepts both shapes and claimPIDFile applies a
// migration rule to the legacy one.
//
// Scoping: each project directory (where WORKFLOW.md lives) owns its own PID
// file, so multiple itervox daemons for different repos coexist without
// colliding. `itervox stop` is therefore scoped to the current project —
// other running daemons in other repos are untouched.
//
// Lifecycle: written on daemon startup, removed on clean shutdown. A stale
// PID file (daemon crashed or was SIGKILLed) is tolerated: `itervox stop`
// finds the pid lock free, removes the record and signals nothing — even when
// the recorded pid has since been reused by an unrelated process (CORE-163).
// The lock (0600) and its .itervox/runtime/ directory (0700) are private to
// the daemon's user; the record itself is created 0600 by linkClaim.

// pidRecordLockMarker is the third field of a pid record written by a daemon
// that holds the pid lock (CORE-039).
const pidRecordLockMarker = "flock"

// pidLockPath returns the sidecar flock path for a pid file. It lives under
// .itervox/runtime/, which the nested .itervox/.gitignore already ignores, and
// is never deleted (see tryLockPIDFile).
func pidLockPath(pidPath string) string {
	return filepath.Join(filepath.Dir(pidPath), "runtime", "daemon.pid.lock")
}

// heldPIDLocks maps a pid-file path to the release func of the lock this
// process holds for it. The lock must outlive claimPIDFile (it is the claim),
// and it must be reachable from removePIDFile, which every shutdown and
// fatalExit path already calls — so it is kept here rather than threaded
// through main's several cleanup sites. Keeping the *os.File reachable also
// stops a finalizer from closing it, which would silently drop the lock.
var heldPIDLocks = struct {
	mu sync.Mutex
	m  map[string]func()
}{m: map[string]func(){}}

// pidRecord is a parsed pid file.
type pidRecord struct {
	pid      int
	workflow string
	// locked is true for records written by a lock-holding daemon (the
	// `flock` marker). For those, a free lock proves the writer is gone.
	locked bool
}

// parsePIDRecord parses either record shape. The marker is recognised only
// as the LAST of three or more tab-separated fields, so a legacy record whose
// workflow path happens to contain a tab still parses as before.
func parsePIDRecord(data []byte) (pidRecord, error) {
	line := strings.TrimSpace(string(data))
	parts := strings.Split(line, "\t")
	p, err := strconv.Atoi(parts[0])
	if err != nil {
		return pidRecord{}, fmt.Errorf("pid file malformed (expected <pid>\\t<path>): %w", err)
	}
	rec := pidRecord{pid: p}
	switch {
	case len(parts) >= 3 && parts[len(parts)-1] == pidRecordLockMarker:
		rec.locked = true
		rec.workflow = strings.Join(parts[1:len(parts)-1], "\t")
	case len(parts) >= 2:
		rec.workflow = strings.Join(parts[1:], "\t")
	}
	return rec, nil
}

// pidFilePath returns the canonical PID-file path for a given WORKFLOW.md.
func pidFilePath(workflowPath string) (string, error) {
	// Canonicalised through the SAME derivation as every other per-project
	// path, so the pid file and the rest of .itervox cannot land in different
	// directories when a checkout is addressed by two spellings.
	//
	// Note this is a consistency cleanup, NOT a fix for a broken guard: both
	// spellings of a symlinked checkout open the same inode, so
	// requireNoLiveDaemon fired correctly before this change too. Verified by
	// probing the pre-change code.
	dir := filepath.Dir(config.CanonicalWorkflowPath(workflowPath))
	return filepath.Join(dir, ".itervox", "daemon.pid"), nil
}

// readPIDFile parses the canonical PID file and returns the PID plus the
// workflow path that was recorded at daemon startup. Returns os.ErrNotExist
// when no file is present — caller should interpret that as "no daemon".
func readPIDFile(workflowPath string) (pid int, recordedWorkflow string, path string, err error) {
	path, perr := pidFilePath(workflowPath)
	if perr != nil {
		return 0, "", "", perr
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		return 0, "", path, rerr
	}
	rec, perr := parsePIDRecord(data)
	if perr != nil {
		return 0, "", path, perr
	}
	return rec.pid, rec.workflow, path, nil
}

// removePIDFile deletes the canonical PID file and then releases the pid lock
// this process holds for it, if any. Idempotent: main registers it on several
// cleanup paths. Logs a warning on errors but does not return them — PID-file
// cleanup is best-effort and must not fail a graceful shutdown.
//
// The file is removed BEFORE the lock is released, so a claimant arriving in
// between is refused by the lock rather than seeing a free lock over a file
// that is still about to disappear.
func removePIDFile(workflowPath string) {
	path, err := pidFilePath(workflowPath)
	if err != nil {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Warn("itervox: failed to remove PID file", "path", path, "error", err)
	}
	heldPIDLocks.mu.Lock()
	release := heldPIDLocks.m[path]
	delete(heldPIDLocks.m, path)
	heldPIDLocks.mu.Unlock()
	if release != nil {
		release()
	}
}

// recordHolderAlive decides whether the process that wrote rec may still hold
// the pid file. It is only consulted AFTER this process acquired the pid lock.
//
//   - locked record: the writer held the lock for its lifetime, and we now
//     hold it, so the writer is gone — whatever process owns rec.pid today
//     (a recycled pid, or this very process after a same-pid container
//     restart) is not it. Stale.
//   - legacy record (pre-CORE-039 writer, never locked): fall back to the pid
//     probe, except that our own pid is stale by construction — no other live
//     process can hold it, and every in-process claimant of this binary goes
//     through the lock. A live foreign pid is still refused: an older daemon
//     may be running across an upgrade. The residual limit is a legacy record
//     whose pid was recycled by an unrelated process; that still refuses until
//     the operator removes the file, exactly as before CORE-039.
//
// On a platform without flock (pidLockSupported == false) every record takes
// the legacy pid-probe path, own pid included, i.e. the pre-CORE-039 guard.
func recordHolderAlive(rec pidRecord) bool {
	if !pidLockSupported {
		return processAlive(rec.pid)
	}
	if rec.locked || rec.pid == os.Getpid() {
		return false
	}
	return processAlive(rec.pid)
}

// processAlive reports whether a process with the given PID exists and the
// calling user can signal it. Implemented with signal 0, which is the
// canonical "is it alive" probe on POSIX systems.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 performs no delivery but returns an error if the target is
	// gone or not owned by the caller. On Windows os.FindProcess only
	// succeeds for live processes, so Signal(nil)-like semantics aren't
	// needed there — but Signal(syscall.Signal(0)) is still a no-op.
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return true
}

// linkClaim creates target containing content, atomically, failing with
// os.ErrExist if any file is already there.
//
// The obvious implementation — O_CREATE|O_EXCL then write — is NOT sufficient,
// and got this wrong on the first attempt. O_EXCL makes the file appear
// atomically, but it appears EMPTY, and the content lands a moment later. A
// racing claimant that hits EEXIST inside that window reads a zero-length file,
// fails to parse a pid from it, correctly concludes "malformed, therefore
// stale", deletes it, and claims. Under a loaded test run that produced NINE
// simultaneous winners out of 24.
//
// Writing to a temp file in the same directory and hard-linking it into place
// closes the window: link(2) is atomic and fails with EEXIST if the destination
// exists, so the file is fully written at the instant it becomes visible under
// its final name. There is no state in which another process can observe a
// half-formed claim.
//
// The temp file is created in filepath.Dir(target) rather than TMPDIR because
// hard links cannot cross filesystems.
func linkClaim(target, content string) error {
	tmp, err := os.CreateTemp(filepath.Dir(target), ".daemon.pid.claim-*")
	if err != nil {
		return fmt.Errorf("create pid claim temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once linked and removed below

	if _, werr := tmp.WriteString(content); werr != nil {
		_ = tmp.Close()
		return fmt.Errorf("write pid claim temp: %w", werr)
	}
	if cerr := tmp.Close(); cerr != nil {
		return fmt.Errorf("close pid claim temp: %w", cerr)
	}
	if lerr := os.Link(tmpName, target); lerr != nil {
		// os.Link wraps the syscall error; errors.Is(..., os.ErrExist) still
		// matches, which is what the caller distinguishes on.
		return lerr
	}
	return nil
}

// claimPIDFile atomically claims ownership of this project's PID file, or
// reports the live daemon that already holds it.
//
// This replaces the read-check-write sequence of requireNoLiveDaemon followed
// by writePIDFile (issue #64). That sequence left a window between deciding
// "nobody else is here" and recording "I am here" — 73 lines of startup in
// main() — in which a second daemon could make the same decision. Both would
// then proceed and share one .itervox/ directory, interleaving HEARTBEAT.md,
// the outbox, and the automation queue: precisely the failure the guard exists
// to prevent.
//
// CORE-039: the claim first takes a non-blocking flock on the sidecar
// pidLockPath and keeps it for the daemon's lifetime. A busy lock means a live
// claimant, and we refuse without touching the file. A free lock means no
// lock-holding daemon is alive, which is what makes a same-pid restart (a
// container entrypoint whose previous run was SIGKILLed) and a recycled pid
// recoverable: a pid probe alone reports both as "alive".
//
// The file itself is still claimed with an atomic exclusive create, so the
// filesystem also decides between this binary and a pre-CORE-039 daemon that
// does not know about the lock. EEXIST is not a failure by itself — it means
// someone holds the file, and only then do we ask recordHolderAlive:
//
//   - alive        -> refuse, returning their pid for the operator message
//   - dead/stale   -> remove and retry the exclusive create ONCE
//   - malformed    -> treated as stale; readPIDFile rejects it, and a file we
//     cannot parse cannot identify a process to defer to
//
// The retry is bounded at one. Two daemons racing to reclaim the same stale
// file both attempt the remove, but only one can win the following O_EXCL
// create; the loser's second attempt sees the winner's live pid and refuses.
// Bounding it also means a pathological reclaim loop cannot spin.
//
// atomicfs.WriteFile is deliberately NOT used here: it is rename-based, which
// replaces any existing file unconditionally — the exact clobber this is
// preventing. A crash mid-write leaves a short malformed line, which the
// stale path above reclaims.
func claimPIDFile(workflowPath string) (livePid int, recordedWorkflow string, pidPath string, err error) {
	target, perr := pidFilePath(workflowPath)
	if perr != nil {
		return 0, "", "", perr
	}
	if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr != nil {
		return 0, "", target, fmt.Errorf("create .itervox dir: %w", mkErr)
	}
	abs, aerr := filepath.Abs(workflowPath)
	if aerr != nil {
		return 0, "", target, aerr
	}
	content := fmt.Sprintf("%d\t%s\t%s\n", os.Getpid(), abs, pidRecordLockMarker)

	lockPath := pidLockPath(target)
	if mkErr := ensurePrivateDir(filepath.Dir(lockPath)); mkErr != nil {
		return 0, "", target, fmt.Errorf("create pid lock dir: %w", mkErr)
	}
	release, acquired, lerr := tryLockPIDFile(lockPath)
	if lerr != nil {
		return 0, "", target, fmt.Errorf("claim pid file: %w", lerr)
	}
	if !acquired {
		// The holder may still be between taking the lock and linking its
		// record, in which case there is no pid to report yet.
		pid, recorded, _, _ := readPIDFile(workflowPath)
		return pid, recorded, target, fmt.Errorf(
			"itervox: another daemon already running for this WORKFLOW.md (pid=%d, recorded_workflow=%q; it holds %s). Stop it with `itervox stop`",
			pid, recorded, lockPath)
	}
	claimed := false
	defer func() {
		if !claimed {
			release()
		}
	}()

	const attempts = 2 // initial claim + one stale reclaim
	for i := 0; i < attempts; i++ {
		oerr := linkClaim(target, content)
		if oerr == nil {
			heldPIDLocks.mu.Lock()
			heldPIDLocks.m[target] = release
			heldPIDLocks.mu.Unlock()
			claimed = true
			return 0, "", target, nil
		}
		if !errors.Is(oerr, os.ErrExist) {
			return 0, "", target, fmt.Errorf("claim pid file: %w", oerr)
		}

		data, rerr := os.ReadFile(target)
		var rec pidRecord
		if rerr == nil {
			rec, rerr = parsePIDRecord(data)
		}
		if rerr == nil && recordHolderAlive(rec) {
			return rec.pid, rec.workflow, target, fmt.Errorf(
				"itervox: another daemon already running for this WORKFLOW.md (pid=%d, recorded_workflow=%q). Either stop it with `itervox stop` or remove %s after confirming the process is gone",
				rec.pid, rec.workflow, target)
		}
		slog.Info("itervox: reclaiming stale PID file", "path", target, "stale_pid", rec.pid, "locked_record", rec.locked)
		if rmErr := os.Remove(target); rmErr != nil && !os.IsNotExist(rmErr) {
			return 0, "", target, fmt.Errorf("remove stale pid file: %w", rmErr)
		}
	}

	// Lost the reclaim race: another daemon won the exclusive create between
	// our remove and our retry. Report it as busy rather than proceeding.
	pid, recorded, _, _ := readPIDFile(workflowPath)
	return pid, recorded, target, fmt.Errorf(
		"itervox: another daemon claimed this WORKFLOW.md while starting (pid=%d, recorded_workflow=%q). Retry, or remove %s after confirming the process is gone",
		pid, recorded, target)
}

// ensurePrivateDir creates dir 0700 (the pid lock's .itervox/runtime/), and
// tightens an existing one — e.g. created 0755 by a pre-v0.2.1 daemon — to
// 0700 when we own it. When we do not own it (chmod fails with EPERM) the
// claim still proceeds with a warning instead of failing startup: the lock
// file's own 0600 mode is what keeps other users from opening it, and the
// operator can fix the directory without the daemon being down. A symlink in
// place of the directory is refused.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory (symlink refused)", dir)
	}
	if info.Mode().Perm() != 0o700 {
		if chErr := os.Chmod(dir, 0o700); chErr != nil {
			slog.Warn("itervox: pid lock directory is accessible to other users and cannot be tightened",
				"path", dir, "mode", info.Mode().Perm().String(), "error", chErr)
		}
	}
	return nil
}

// dashboardURLFilePath returns the canonical .itervox/dashboard_url file
// path. The file holds a single line — the dashboard URL the daemon bound
// to. Vite's dev proxy reads this so the proxy target follows the running
// daemon's actual bound port (mitigates the silent auto-shift / hard-coded
// 8090 failure mode).
func dashboardURLFilePath(workflowPath string) string {
	base := filepath.Dir(workflowPath)
	if base == "" {
		base = "."
	}
	return filepath.Join(base, ".itervox", "dashboard_url")
}

// writeDashboardURLFile atomically persists the bound dashboard URL so the
// Vite dev proxy can discover it without operator configuration.
func writeDashboardURLFile(workflowPath, dashboardURL string) error {
	if dashboardURL == "" {
		return nil
	}
	path := dashboardURLFilePath(workflowPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	return atomicfs.WriteFile(path, []byte(dashboardURL+"\n"), 0o644)
}

// removeDashboardURLFile is the shutdown counterpart to writeDashboardURLFile.
// Best-effort — operators reading the file after a crash see whatever the
// last daemon wrote; doctor's "is the URL responding?" probe catches the
// stale case.
func removeDashboardURLFile(workflowPath string) {
	path := dashboardURLFilePath(workflowPath)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Warn("itervox: failed to remove dashboard URL file", "path", path, "error", err)
	}
}

// removeHeartbeatFile removes the .itervox/HEARTBEAT.md file on shutdown so
// a clean exit leaves no stale liveness signal. The heartbeat writer is the
// authoritative path for refresh during steady state; this is purely a
// shutdown courtesy that prevents an operator from being misled by a
// post-crash HEARTBEAT.md (mitigates the "looks alive but the daemon is
// gone" failure mode).
func removeHeartbeatFile(workflowPath string) {
	base := filepath.Dir(workflowPath)
	if base == "" {
		base = "."
	}
	path := filepath.Join(base, ".itervox", "HEARTBEAT.md")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Warn("itervox: failed to remove HEARTBEAT.md", "path", path, "error", err)
	}
}

// warnIfDaemonRunning prints a warning when a live daemon owns workflowPath.
//
// Used by the one-shot subcommands that write `.itervox/` state — `init
// --update`, `deps analyze` — which run outside the daemon and so bypass
// requireNoLiveDaemon entirely. Their writes race the running daemon's own:
// a migration rewrites WORKFLOW.md under a daemon that has already parsed it,
// and an analyzer pass rewrites the dependency sidecar the daemon is reading.
//
// A warning rather than a refusal: both commands are legitimate against a live
// daemon (a migration is often exactly what you want before a reload), and
// hard-failing would break established workflows. The point is that the
// operator learns about the overlap instead of debugging its symptoms.
func warnIfDaemonRunning(workflowPath, action string) {
	pid, _, path, err := readPIDFile(workflowPath)
	if err != nil || !processAlive(pid) {
		return
	}
	fmt.Fprintf(os.Stderr,
		"warning: a daemon is running for this WORKFLOW.md (pid=%d, %s).\n"+
			"  %s writes .itervox state that the daemon also owns; restart it afterwards so it picks up the change.\n",
		pid, path, action)
}
