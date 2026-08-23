package main

import (
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

// writePIDFile writes the current process PID (plus the absolute workflow
// path, for later verification) to the canonical location. Creates the
// `.itervox/` directory if needed. Overwrites an existing file — callers
// must decide whether to allow that; see requireNoRunningDaemon.
func writePIDFile(workflowPath string) (string, error) {
	path, err := pidFilePath(workflowPath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create .itervox dir: %w", err)
	}
	abs, err := filepath.Abs(workflowPath)
	if err != nil {
		return "", err
	}
	content := fmt.Sprintf("%d\t%s\n", os.Getpid(), abs)
	if err := atomicfs.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write pid file: %w", err)
	}
	return path, nil
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

// requireNoLiveDaemon refuses to start if a previous PID file points at a
// live process. Returns the running PID + recorded workflow when found so the
// caller can print an operator-friendly error before exiting.
//
// Mitigates the "two itervox daemons fight over .itervox state" failure mode
// where the previous daemon's HEARTBEAT.md, automation_queue.json, etc. would
// be silently clobbered by a second daemon for the same WORKFLOW.md.
func requireNoLiveDaemon(workflowPath string) (livePid int, recordedWorkflow string, pidPath string, err error) {
	pid, recorded, path, readErr := readPIDFile(workflowPath)
	if readErr != nil {
		// No pidfile or unreadable → treat as no previous daemon.
		return 0, "", path, nil
	}
	if !processAlive(pid) {
		// Stale pidfile — log and let the caller overwrite.
		slog.Info("itervox: previous PID file is stale; overwriting",
			"path", path, "stale_pid", pid)
		return 0, "", path, nil
	}
	return pid, recorded, path, fmt.Errorf(
		"itervox: another daemon already running for this WORKFLOW.md (pid=%d, recorded_workflow=%q). Either stop it with `itervox stop` or remove %s after confirming the process is gone",
		pid, recorded, path)
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
