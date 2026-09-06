package main

import (
	"errors"
	"fmt"
	"github.com/vnovick/itervox/internal/config"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/vnovick/itervox/internal/atomicfs"
)

// PID-file conventions
// --------------------
//
// Path: `<workflowDir>/.itervox/daemon.pid`
//
// The file holds a single line: `<pid>\t<workflowPath>\n`. The trailing
// workflowPath is a sanity-check used by `itervox stop` so we don't
// accidentally kill a daemon whose PID was recycled by an unrelated process.
//
// Scoping: each project directory (where WORKFLOW.md lives) owns its own PID
// file, so multiple itervox daemons for different repos coexist without
// colliding. `itervox stop` is therefore scoped to the current project —
// other running daemons in other repos are untouched.
//
// Lifecycle: written on daemon startup, removed on clean shutdown. A stale
// PID file (daemon crashed or was SIGKILLed) is tolerated — `itervox stop`
// verifies the PID is live with `os.FindProcess` + signal 0 and reports
// "no running daemon found" when the file points at a dead PID.

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
	line := strings.TrimSpace(string(data))
	parts := strings.SplitN(line, "\t", 2)
	p, perr := strconv.Atoi(parts[0])
	if perr != nil {
		return 0, "", path, fmt.Errorf("pid file malformed (expected <pid>\\t<path>): %w", perr)
	}
	recorded := ""
	if len(parts) == 2 {
		recorded = parts[1]
	}
	return p, recorded, path, nil
}

// removePIDFile deletes the canonical PID file. Returns nil if the file was
// already absent. Logs a warning on other errors but does not return them —
// PID-file cleanup is best-effort and must not fail a graceful shutdown.
func removePIDFile(workflowPath string) {
	path, err := pidFilePath(workflowPath)
	if err != nil {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Warn("itervox: failed to remove PID file", "path", path, "error", err)
	}
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
// The claim is O_CREATE|O_EXCL, so the filesystem decides the winner rather
// than the gap between two syscalls being short. EEXIST is not a failure by
// itself — it means someone holds the file, and only then do we ask whether
// that someone is alive:
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
	content := fmt.Sprintf("%d\t%s\n", os.Getpid(), abs)

	const attempts = 2 // initial claim + one stale reclaim
	for i := 0; i < attempts; i++ {
		oerr := linkClaim(target, content)
		if oerr == nil {
			return 0, "", target, nil
		}
		if !errors.Is(oerr, os.ErrExist) {
			return 0, "", target, fmt.Errorf("claim pid file: %w", oerr)
		}

		pid, recorded, _, rerr := readPIDFile(workflowPath)
		if rerr == nil && processAlive(pid) {
			return pid, recorded, target, fmt.Errorf(
				"itervox: another daemon already running for this WORKFLOW.md (pid=%d, recorded_workflow=%q). Either stop it with `itervox stop` or remove %s after confirming the process is gone",
				pid, recorded, target)
		}
		slog.Info("itervox: reclaiming stale PID file", "path", target, "stale_pid", pid)
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
