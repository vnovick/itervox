package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/profiles"
)

// runDoctor is the entrypoint for `itervox doctor`. It runs a fast preflight
// against WORKFLOW.md (yaml parse + schema-2 validation + dispatch checks)
// AND reports binary-resolution drift between the running daemon's binary
// and whatever `which itervox` would find on the user's PATH. Exits 0 on
// clean, 1 on any check failing or warning, 2 on a hard error during
// preflight itself. P0-D + P0-G.
func runDoctor(args []string) {
	workflowPath := "WORKFLOW.md"
	clearStartupError := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--workflow" && i+1 < len(args):
			workflowPath = args[i+1]
			i++
		case strings.HasPrefix(a, "--workflow="):
			workflowPath = strings.TrimPrefix(a, "--workflow=")
		case a == "--clear-startup-error":
			clearStartupError = true
		case a == "-h" || a == "--help":
			fmt.Println("usage: itervox doctor [--workflow PATH] [--clear-startup-error]")
			fmt.Println("  --clear-startup-error  remove .itervox/STARTUP_ERROR.md if present (use after fixing the root cause)")
			return
		}
	}
	if clearStartupError {
		clearStartupErrorMarker(workflowPath)
	}
	report, exitCode := runDoctorChecks(workflowPath, os.Stdout)
	if _, err := io.WriteString(os.Stdout, report); err != nil {
		fmt.Fprintf(os.Stderr, "doctor: write report: %v\n", err)
	}
	if exitCode != 0 {
		fatalExit(exitCode)
	}
}

// DoctorReport is the structured outcome of `itervox doctor`. Exposed for
// tests that want to assert specific check outcomes without parsing stdout.
type DoctorReport struct {
	Workflow      string
	SchemaPassed  bool
	WorkflowError string
	RunningBinary string
	PathBinary    string
	DriftWarning  string
	DriftIsError  bool
	// ItervoxBinEnv is the operator's $ITERVOX_BIN value at the time doctor
	// ran. Doctor consults it before flagging hook-PATH drift: when the
	// operator has pinned ITERVOX_BIN to the running binary, hooks (which
	// inherit the env via the workspace allowlist) ignore PATH entirely, so
	// drift is informational, not actionable.
	ItervoxBinEnv    string
	StartupErrorPath string
	// PortInUseWarning is the operator-facing diagnostic when WORKFLOW.md's
	// configured server.port is currently held by another process. Empty when
	// the port is free OR when no `lsof`-style probe could be performed.
	PortInUseWarning string
	// HeartbeatStaleWarning fires when .itervox/HEARTBEAT.md exists but the
	// daemon PID recorded in .itervox/daemon.pid is no longer alive — the
	// most common "I thought the daemon was running" failure mode.
	HeartbeatStaleWarning string
	// DashboardURLReachable reports whether the URL listed in
	// .itervox/dashboard_url responds to /api/v1/health. False with a
	// non-empty URL means the daemon died after writing the file.
	DashboardURL          string
	DashboardURLReachable bool
	// GitignoreMissingLines reports missing required lines in .itervox/.gitignore,
	// if the file exists but is incomplete.
	GitignoreMissingLines []string
}

func runDoctorChecks(workflowPath string, _ io.Writer) (string, int) {
	report := DoctorReport{Workflow: workflowPath}

	cfg, err := config.Load(workflowPath)
	if err == nil {
		err = config.ValidateDispatch(cfg)
	}
	report.SchemaPassed = err == nil
	if err != nil {
		report.WorkflowError = err.Error()
	}

	report.RunningBinary, _ = os.Executable()
	report.PathBinary = whichItervox()
	report.ItervoxBinEnv = os.Getenv("ITERVOX_BIN")
	switch {
	case report.RunningBinary == "" || report.PathBinary == "" || report.RunningBinary == report.PathBinary:
		// no drift to report
	case report.ItervoxBinEnv != "" && report.ItervoxBinEnv == report.RunningBinary:
		// Operator pinned ITERVOX_BIN to the running binary. Hooks
		// (via hookEnv allowlist, P0-F) will use it explicitly — the
		// PATH binary is irrelevant to runtime behaviour. Surface the
		// configuration but downgrade severity to info.
		report.DriftWarning = fmt.Sprintf(
			"two itervox binaries on this machine — ITERVOX_BIN=%s is pinned so hooks use it (PATH itervox at %s is shadowed).",
			report.ItervoxBinEnv, report.PathBinary,
		)
	default:
		report.DriftWarning = fmt.Sprintf(
			"hooks and debug-shells will invoke %s, but `itervox doctor` ran from %s. Set ITERVOX_BIN=%s or reorder PATH if hooks need the running binary.",
			report.PathBinary, report.RunningBinary, report.RunningBinary,
		)
		// Version drift is ERROR only in the "dev vs stable" case where the
		// version strings differ AND one binary is on a release tag while
		// the other is `dev`. Two stable binaries with different SHAs is
		// just "two installs" — annoying but not actionable. Doctor should
		// avoid crying wolf.
		report.DriftIsError = doctorBinaryVersionDriftIsSerious(report.RunningBinary, report.PathBinary)
	}

	if dir := filepath.Dir(workflowPath); dir != "" {
		path := filepath.Join(dir, ".itervox", "STARTUP_ERROR.md")
		if _, statErr := os.Stat(path); statErr == nil {
			report.StartupErrorPath = path
		}
	}

	// Port-collision check: when WORKFLOW.md names a port AND that port is
	// held by a process that is NOT this WORKFLOW.md's recorded daemon, the
	// operator either left a stray daemon running or has the Vite proxy /
	// `localhost:<port>` open against the wrong process.
	//
	// The holder PID is compared against this WORKFLOW.md's recorded daemon,
	// because the `cfg.Server.Port != nil` guard no longer means "the
	// operator named a port": server.port now defaults to 8090, so config
	// load ALWAYS populates it. Without the PID check every healthy daemon
	// reported its own listening socket as a collision — a warning plus
	// exit 1 from `itervox doctor` on a perfectly good setup.
	if cfg != nil && cfg.Server.Port != nil {
		port := *cfg.Server.Port
		holder, holderPID := describePortHolderWithPID(port)
		ownPID, _, _, pidErr := readPIDFile(workflowPath)
		selfHeld := pidErr == nil && holderPID != 0 && holderPID == ownPID
		if holder != "" && !selfHeld {
			report.PortInUseWarning = fmt.Sprintf(
				"port %d in use%s — if this is not your expected itervox daemon, the dashboard URL will reach the wrong process",
				port, holder)
		}
	}

	// Stale HEARTBEAT.md detection: file exists, but the recorded daemon PID
	// is gone. This is the canonical "I thought it was running" trap.
	if dir := filepath.Dir(workflowPath); dir != "" {
		hbPath := filepath.Join(dir, ".itervox", "HEARTBEAT.md")
		if _, hbErr := os.Stat(hbPath); hbErr == nil {
			pid, _, _, pidErr := readPIDFile(workflowPath)
			if pidErr != nil || !processAlive(pid) {
				report.HeartbeatStaleWarning = fmt.Sprintf(
					"%s exists but no live daemon owns it (pid in daemon.pid = %d, alive = %v) — delete the stale file or `itervox` to start fresh",
					hbPath, pid, pidErr == nil && processAlive(pid))
			}
		}
	}

	// Dashboard URL reachability: when `.itervox/dashboard_url` is present,
	// hit /api/v1/health and report whether the URL actually serves.
	if dir := filepath.Dir(workflowPath); dir != "" {
		urlPath := filepath.Join(dir, ".itervox", "dashboard_url")
		if data, urlErr := os.ReadFile(urlPath); urlErr == nil {
			report.DashboardURL = strings.TrimSpace(string(data))
			report.DashboardURLReachable = probeDashboardHealth(report.DashboardURL)
		}
	}

	// Gitignore completeness check: verify that .itervox/.gitignore contains
	// all required lines. If the file exists but is incomplete (e.g., after a
	// binary upgrade), warn the operator.
	requiredGitignoreLines := []string{".env", "HEARTBEAT.md", "daemon.pid", "dashboard_url", "STARTUP_ERROR.md", "logs/", "runtime/", "/*.json", "bin/", "*.db"}
	if dir := filepath.Dir(workflowPath); dir != "" {
		gitignorePath := filepath.Join(dir, ".itervox", ".gitignore")
		if data, gitErr := os.ReadFile(gitignorePath); gitErr == nil {
			content := string(data)
			for _, line := range requiredGitignoreLines {
				if !strings.Contains(content, line) {
					report.GitignoreMissingLines = append(report.GitignoreMissingLines, line)
				}
			}
		}
	}

	exitCode := 0
	switch {
	case !report.SchemaPassed:
		exitCode = 1
	case report.DriftIsError:
		// Only the dev-vs-stable drift case is severe enough to exit non-
		// zero. Plain "two installs with different SHAs" is reported as
		// info and does not change the exit code.
		exitCode = 1
	case report.StartupErrorPath != "":
		exitCode = 1
	case report.PortInUseWarning != "":
		exitCode = 1
	case report.HeartbeatStaleWarning != "":
		exitCode = 1
	case report.DashboardURL != "" && !report.DashboardURLReachable:
		exitCode = 1
	}
	return renderDoctorReport(report), exitCode
}

// probeDashboardHealth GETs `<url>api/v1/health` with a short timeout.
// Returns true only on a 2xx response. Used by doctor to confirm the URL
// written to `.itervox/dashboard_url` reaches a live daemon.
func probeDashboardHealth(url string) bool {
	if url == "" {
		return false
	}
	if !strings.HasSuffix(url, "/") {
		url += "/"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "curl", "-fsS", "-o", "/dev/null",
		"-w", "%{http_code}", url+"api/v1/health")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	code := strings.TrimSpace(string(out))
	return strings.HasPrefix(code, "2")
}

func renderDoctorReport(r DoctorReport) string {
	var b strings.Builder
	b.WriteString("itervox doctor\n")
	b.WriteString("==============\n\n")
	fmt.Fprintf(&b, "workflow: %s\n", r.Workflow)
	if r.SchemaPassed {
		b.WriteString("schema: OK\n")
	} else {
		fmt.Fprintf(&b, "schema: FAILED — %s\n", r.WorkflowError)
	}
	fmt.Fprintf(&b, "running binary: %s\n", r.RunningBinary)
	fmt.Fprintf(&b, "PATH itervox:   %s\n", or(r.PathBinary, "(none on system PATH)"))
	// P0-A — surface the built-in profiles so operators know what `profile:
	// merge-bot {}` expands to.
	if names := profiles.Names(); len(names) > 0 {
		fmt.Fprintf(&b, "built-in profiles: %s\n", strings.Join(names, ", "))
	}
	if r.DriftWarning != "" {
		if r.DriftIsError {
			fmt.Fprintf(&b, "ERROR: %s (running and PATH versions differ — dev vs stable)\n", r.DriftWarning)
		} else {
			fmt.Fprintf(&b, "info: %s\n", r.DriftWarning)
		}
	}
	if r.StartupErrorPath != "" {
		fmt.Fprintf(&b, "last startup error: %s — investigate the file, then `itervox doctor --clear-startup-error` once resolved\n", r.StartupErrorPath)
	}
	if r.PortInUseWarning != "" {
		fmt.Fprintf(&b, "WARNING: %s\n", r.PortInUseWarning)
	}
	if r.HeartbeatStaleWarning != "" {
		fmt.Fprintf(&b, "WARNING: %s\n", r.HeartbeatStaleWarning)
	}
	if r.DashboardURL != "" {
		if r.DashboardURLReachable {
			fmt.Fprintf(&b, "dashboard URL: %s (reachable)\n", r.DashboardURL)
		} else {
			fmt.Fprintf(&b, "dashboard URL: %s (NOT reachable — daemon may have died after writing this file)\n", r.DashboardURL)
		}
	}
	if len(r.GitignoreMissingLines) > 0 {
		fmt.Fprintf(&b, "WARNING: .itervox/.gitignore missing lines (add to prevent accidental commits): %s — run `itervox init --update --workflow %s` to fix\n",
			strings.Join(r.GitignoreMissingLines, ", "), r.Workflow)
	}
	return b.String()
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func whichItervox() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "bash", "-lc", "command -v itervox").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// doctorBinaryVersionDriftIsSerious decides whether a version-string mismatch
// between the running binary and the PATH binary warrants an ERROR (exit
// non-zero) rather than just a WARNING. Heuristic:
//
//   - identical version strings → no drift (returns false)
//   - one binary says `version=dev` (the unreleased local build marker) AND
//     the other reports a non-dev release tag → SERIOUS (returns true). This
//     is the canonical "I'm testing a dev build but my hooks invoke the
//     stable release" trap.
//   - any other shape of mismatch → WARNING only (returns false). Two stable
//     binaries with different SHAs is just "two installs", which doctor
//     should surface but not flag as a failed boot.
func doctorBinaryVersionDriftIsSerious(running, path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	getVersion := func(bin string) string {
		out, err := exec.CommandContext(ctx, bin, "--version").Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	a, b := getVersion(running), getVersion(path)
	if a == "" || b == "" || a == b {
		return false
	}
	aHasDev := strings.Contains(a, "version=dev") || strings.Contains(a, " dev ") || strings.HasSuffix(a, " dev")
	bHasDev := strings.Contains(b, "version=dev") || strings.Contains(b, " dev ") || strings.HasSuffix(b, " dev")
	// Only one side is dev → the dev/stable split case → serious.
	return aHasDev != bHasDev
}
