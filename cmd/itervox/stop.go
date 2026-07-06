package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"
)

// runStop terminates the itervox daemon owning the current project
// directory. It reads the project's PID file (written at daemon startup),
// verifies the recorded process is live and references the expected
// WORKFLOW.md (so a recycled PID can never be killed by mistake), then
// sends SIGTERM and waits up to `--grace` for a clean exit. If the process
// is still alive after the grace period, a SIGKILL is sent.
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
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: itervox stop [flags]

Stops all itervox daemons serving the current project directory.

Resolution order:
  1. PID file at <workflowDir>/.itervox/daemon.pid (canonical).
  2. Fallback: scan running processes whose working directory matches the
     project directory — handles pre-upgrade daemons that never wrote a PID
     file and daemons started outside the normal launch path.

Daemons in OTHER project directories are never touched. Run this command
separately in each repo you want to stop.

Flags:
`)
		fs.PrintDefaults()
	}
	_ = fs.Parse(args)

	projectDir, projectErr := resolveProjectDir(*workflowPath)
	if projectErr != nil {
		fmt.Fprintf(os.Stderr, "itervox stop: cannot resolve project directory: %v\n", projectErr)
		fatalExit(1)
	}

	// Start with the PID file, then union-in the directory scan. The scan
	// catches pre-upgrade daemons and redundant instances; the file catches
	// daemons whose cwd has changed since launch (unusual but possible).
	var pids []int
	pidFilePath := ""
	if pid, recordedWorkflow, path, err := readPIDFile(*workflowPath); err == nil {
		pidFilePath = path
		if recordedWorkflow != "" {
			if expected, absErr := filepath.Abs(*workflowPath); absErr == nil && expected != recordedWorkflow {
				fmt.Fprintf(os.Stderr,
					"itervox stop: WARNING — PID file references a different WORKFLOW.md (%s). Skipping PID %d.\n",
					recordedWorkflow, pid)
			} else if processAlive(pid) {
				pids = append(pids, pid)
			}
		} else if processAlive(pid) {
			pids = append(pids, pid)
		}
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "itervox stop: warning: failed to read PID file: %v\n", err)
	}

	for _, scanned := range discoverDaemonsByCwd(projectDir) {
		if !slices.Contains(pids, scanned) {
			pids = append(pids, scanned)
		}
	}

	if len(pids) == 0 {
		fmt.Fprintf(os.Stderr, "itervox stop: no running daemon found for %s\n", projectDir)
		if pidFilePath != "" {
			_ = os.Remove(pidFilePath)
		}
		fatalExit(0)
	}

	fmt.Fprintf(os.Stderr, "itervox stop: found %d daemon(s) for %s: %v\n", len(pids), projectDir, pids)

	sig := syscall.SIGTERM
	if *force {
		sig = syscall.SIGKILL
	}

	// Send the initial signal to each PID. Ignoring per-PID errors so one
	// permission failure doesn't abort the whole batch.
	for _, pid := range pids {
		proc, err := os.FindProcess(pid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  PID %d: FindProcess failed: %v\n", pid, err)
			continue
		}
		if err := proc.Signal(sig); err != nil {
			fmt.Fprintf(os.Stderr, "  PID %d: signal %s failed: %v\n", pid, sig, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "  PID %d: %s sent\n", pid, sig)
	}

	if *force {
		if pidFilePath != "" {
			_ = os.Remove(pidFilePath)
		}
		return
	}

	// Poll all PIDs until each is dead or --grace elapses.
	deadline := time.Now().Add(*grace)
	for time.Now().Before(deadline) {
		pids = filterAlive(pids)
		if len(pids) == 0 {
			fmt.Fprintf(os.Stderr, "itervox stop: all daemons exited cleanly\n")
			if pidFilePath != "" {
				_ = os.Remove(pidFilePath)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	fmt.Fprintf(os.Stderr, "itervox stop: grace period elapsed, SIGKILLing survivors: %v\n", pids)
	for _, pid := range pids {
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(syscall.SIGKILL)
		}
	}
	if pidFilePath != "" {
		_ = os.Remove(pidFilePath)
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
