package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"

	charmlog "github.com/charmbracelet/log"
	"github.com/charmbracelet/x/term"
	"github.com/joho/godotenv"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/app"
	"github.com/vnovick/itervox/internal/atomicfs"
	"github.com/vnovick/itervox/internal/automationconfig"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/skills"
	"github.com/vnovick/itervox/internal/statusui"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/github"
	"github.com/vnovick/itervox/internal/tracker/linear"
	"github.com/vnovick/itervox/internal/workflow"
	"github.com/vnovick/itervox/internal/workspace"
	"gopkg.in/lumberjack.v2"
)

// Set by GoReleaser via ldflags — empty when built with `go build`
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func newAppSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: itervox [command] [flags]

Commands:
  init    Scan a repository and generate a WORKFLOW.md starter file
             --tracker  linear|github  (required)
             --runner   claude|codex    (default: claude)
             --output   output file path (default: WORKFLOW.md)
             --dir      directory to scan (default: .)
             --force    overwrite existing output file

  clear   Remove workspace directories created by itervox
             --workflow path to WORKFLOW.md (default: WORKFLOW.md)
             [identifier ...]  specific issues to clear; omit for all

  stop    Stop all daemons serving the current project (uses PID file +
          process scan as fallback, so pre-upgrade daemons are caught)
             --workflow path to WORKFLOW.md (default: WORKFLOW.md)
             --grace    SIGTERM → SIGKILL grace period (default: 30s)
             --force    skip the grace period and SIGKILL immediately

  status  List running itervox daemons for the current project
             --workflow path to WORKFLOW.md (default: WORKFLOW.md)
             --all      also list daemons from other projects

  --version  Print version information

Run mode (default when no command given):
`)
	flag.PrintDefaults()
}

// defaultLogsDir returns a per-project logs directory under ~/.itervox/logs/
// derived from the tracker kind and project slug in the WORKFLOW.md at path.
// Falls back to ~/.itervox/logs if the config can't be read or has no slug.
func defaultLogsDir(workflowPath string) string {
	base := filepath.Join("~", ".itervox", "logs")
	if home, err := os.UserHomeDir(); err == nil {
		base = filepath.Join(home, ".itervox", "logs")
	}
	cfg, err := config.Load(workflowPath)
	if err != nil || cfg.Tracker.Kind == "" || cfg.Tracker.ProjectSlug == "" {
		return base
	}
	// Encode the slug so it is safe as a directory name component.
	safe := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_").Replace(cfg.Tracker.ProjectSlug)
	return filepath.Join(base, cfg.Tracker.Kind, safe)
}

func convertModelsForSnapshot(models map[string][]config.ModelOption) map[string][]server.ModelOption {
	if len(models) == 0 {
		return nil
	}
	result := make(map[string][]server.ModelOption, len(models))
	for backend, opts := range models {
		converted := make([]server.ModelOption, len(opts))
		for i, m := range opts {
			converted[i] = server.ModelOption{ID: m.ID, Label: m.Label}
		}
		result[backend] = converted
	}
	return result
}

func configuredBackend(command, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if backend := agent.BackendFromCommand(command); backend != "" {
		return backend
	}
	return "claude"
}

// validateBackend checks that the CLI for the requested agent backend is
// present and accessible. validatedBackends is a dedup set so each backend
// is validated at most once per startup. profileName is "" for the default
// agent and non-empty for named profiles (affects log messages only).
// Returns a non-nil error when the default backend fails validation so the
// caller can abort startup rather than silently waiting until dispatch time.
func validateBackend(backend, profileName string, validatedBackends map[string]struct{}, cfg *config.Config) error {
	switch backend {
	case "", "claude":
		if _, ok := validatedBackends["claude"]; ok {
			return nil
		}
		validatedBackends["claude"] = struct{}{}
		// Use the already-resolved command (absolute path or bare name) so
		// validation runs the same binary that will actually be executed,
		// not the bare name which may not be on PATH in a login shell.
		resolvedCmd := cfg.Agent.Command
		if profileName != "" {
			if p, ok := cfg.Agent.Profiles[profileName]; ok && p.Command != "" {
				resolvedCmd = p.Command
			}
		}
		if err := agent.ValidateClaudeCLICommand(resolvedCmd); err != nil {
			if profileName != "" {
				slog.Warn("claude CLI validation failed for profile", "profile", profileName, "error", err)
				return nil // profile failures are non-fatal
			}
			return fmt.Errorf("claude CLI not found or not executable: %w", err)
		}
		if profileName != "" {
			slog.Info("claude CLI validated successfully for profile", "profile", profileName)
		} else {
			slog.Info("claude CLI validated successfully")
		}
	case "codex":
		if _, ok := validatedBackends["codex"]; ok {
			return nil
		}
		validatedBackends["codex"] = struct{}{}
		resolvedCmd := cfg.Agent.Command
		if profileName != "" {
			if p, ok := cfg.Agent.Profiles[profileName]; ok && p.Command != "" {
				resolvedCmd = p.Command
			}
		}
		if err := agent.ValidateCodexCLICommand(resolvedCmd); err != nil {
			if profileName != "" {
				slog.Warn("codex CLI validation failed for profile", "profile", profileName, "error", err)
				return nil // profile failures are non-fatal
			}
			return fmt.Errorf("codex CLI not found or not executable: %w", err)
		}
		if profileName != "" {
			slog.Info("codex CLI validated successfully for profile", "profile", profileName)
		} else {
			slog.Info("codex CLI validated successfully")
		}
	default:
		if profileName != "" {
			slog.Warn("unsupported backend in profile, will fall back to default runner", "profile", profileName, "backend", backend)
		} else {
			slog.Warn("unsupported default backend, will fall back to default runner", "backend", backend)
		}
	}
	return nil
}

// generateAPIToken returns a cryptographically random 32-byte hex token
// suitable for use as an ephemeral ITERVOX_API_TOKEN. Matches the entropy
// of `openssl rand -hex 32`.
func generateAPIToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// secretEnvKeys names environment variables whose presence is interesting
// from a security/auth posture standpoint. When loadDotEnv populates one of
// these, an additional INFO line is emitted naming the keys (NEVER values)
// so an operator skimming stderr can confirm bearer-auth or tracker auth
// was wired up by the dotenv. The routine "dotenv: loaded" line stays at
// DEBUG to avoid log spam at the default verbosity level.
var secretEnvKeys = []string{
	"ITERVOX_API_TOKEN",
	"LINEAR_API_KEY",
	"GITHUB_TOKEN",
	"ANTHROPIC_API_KEY",
}

// loadDotEnv silently loads .itervox/.env then .env from the current working
// directory, injecting missing variables into the process environment.
// Existing environment variables are never overwritten.
func loadDotEnv() {
	candidates := []string{
		filepath.Join(".itervox", ".env"),
		".env",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		// Snapshot which sensitive keys are absent BEFORE the load so we can
		// diff after and report only newly-set keys. godotenv.Load doesn't
		// overwrite existing vars, so a key already present in the env was
		// not contributed by this file and we shouldn't credit it.
		absentBefore := make(map[string]struct{}, len(secretEnvKeys))
		for _, k := range secretEnvKeys {
			if _, present := os.LookupEnv(k); !present {
				absentBefore[k] = struct{}{}
			}
		}

		if err := godotenv.Load(p); err != nil {
			slog.Warn("dotenv: failed to load", "path", p, "err", err)
			return
		}
		slog.Debug("dotenv: loaded", "path", p)

		var setKeys []string
		for k := range absentBefore {
			if _, present := os.LookupEnv(k); present {
				setKeys = append(setKeys, k)
			}
		}
		if len(setKeys) > 0 {
			slog.Info("env: bearer auth / API key configured from dotenv",
				"path", p, "keys", setKeys)
		}
		return // stop at first file found
	}
}

func main() {
	// TTY recovery safety net (T-12). All current panic sources fire BEFORE
	// `go statusui.Run` (which puts the terminal into the alt-screen / raw
	// mode), so this defer is a guard against a future regression where a
	// post-statusui-Run goroutine panics. See internal/statusui/statusui.go
	// for the cooked-mode restoration the TUI does on its own clean exit.
	defer func() {
		if r := recover(); r != nil {
			if term.IsTerminal(os.Stdin.Fd()) {
				_ = exec.Command("stty", "sane").Run()
			}
			panic(r) // re-raise so the stack trace surfaces.
		}
	}()

	loadDotEnv() // must run before config.LoadConfig / os.Getenv calls
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			runInit(os.Args[2:])
			return
		case "clear":
			runClear(os.Args[2:])
			return
		case "action":
			runAction(os.Args[2:])
			return
		case "stop":
			runStop(os.Args[2:])
			return
		case "status":
			runStatus(os.Args[2:])
			return
		case "--version", "-version":
			fmt.Printf("itervox %s (commit: %s, built: %s)\n", version, commit, date)
			return
		case "help", "--help", "-help", "-h":
			printUsage()
			return
		}
	}

	flag.Usage = printUsage
	workflowPath := flag.String("workflow", "WORKFLOW.md", "path to WORKFLOW.md")
	logsDir := flag.String("logs-dir", "", "directory for rotating log files (default: ~/.itervox/logs/<kind>/<project>)")
	verbose := flag.Bool("verbose", false, "enable DEBUG-level logging (includes Claude output)")
	shutdownGrace := flag.Duration("shutdown-grace", 30*time.Second, "grace period for active workers on SIGINT/SIGTERM before force exit")
	flag.Parse()

	logLevel := slog.LevelInfo
	if *verbose {
		logLevel = slog.LevelDebug
	}

	// Resolve the logs directory.  When --logs-dir is not set we derive a
	// per-project path under ~/.itervox/logs/<kind>/<slug> so that logs are
	// co-located with workspaces and automatically scoped to the project.
	// We do a lightweight early config read solely to get the tracker kind and
	// project slug; failures are non-fatal and fall back to a shared default.
	resolvedLogsDir := *logsDir
	if resolvedLogsDir == "" {
		resolvedLogsDir = defaultLogsDir(*workflowPath)
	}

	// Tee logs to stderr and a rotating file under <logs-dir>/itervox.log.
	if err := os.MkdirAll(resolvedLogsDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create logs dir %s: %v\n", resolvedLogsDir, err)
		fatalExit(1)
	}
	rotatingFile := &lumberjack.Logger{
		Filename:   filepath.Join(resolvedLogsDir, "itervox.log"),
		MaxSize:    10, // MB
		MaxBackups: 5,
		Compress:   true,
	}
	// Colored handler for stderr (auto-detects TTY for ANSI colors).
	charmLevel := charmlog.InfoLevel
	if logLevel == slog.LevelDebug {
		charmLevel = charmlog.DebugLevel
	}
	stderrHandler := charmlog.NewWithOptions(os.Stderr, charmlog.Options{
		ReportTimestamp: true,
		TimeFormat:      time.TimeOnly,
		Level:           charmLevel,
	})
	// Plain text handler for the rotating log file (no colors).
	fileHandler := slog.NewTextHandler(rotatingFile, &slog.HandlerOptions{
		Level: logLevel,
	})
	// Wrap the fanout in a RedactingHandler so any string attr or msg that
	// matches a known secret pattern (Bearer tokens, lin_api_*, ghp_*, etc.)
	// is rewritten to "***" before reaching either sink. Pairs with the
	// logging.Secret LogValuer for the structured-attr path; this layer
	// catches secrets that slip through as plain strings (stderr dumps,
	// panic stacks, third-party library output). T-29 / F-NEW-A.
	slog.SetDefault(slog.New(logging.NewRedactingHandler(logging.NewFanoutHandler(stderrHandler, fileHandler))))
	// stderrOnly bypasses the rotating-file sink. Use it for any record that
	// must NEVER hit disk — e.g. the dashboard URL that intentionally carries
	// the bearer token for copy/paste once at startup. NOT wrapped in
	// RedactingHandler because that one emit is the explicit secret-display
	// path; redacting it would defeat the purpose of showing the URL to the
	// operator. Every other slog default goes through the redacting wrapper.
	stderrOnly := slog.New(stderrHandler)
	slog.Info("itervox starting", "version", version, "commit", commit, "date", date)
	slog.Info("logging to file", "path", rotatingFile.Filename)

	// Top-level context: cancelled on first SIGINT/SIGTERM to begin graceful drain.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	// Write a per-project PID file so `itervox stop` can find and terminate
	// this daemon. Cleaned up on graceful shutdown (see defer below). A
	// previous PID file from an unclean shutdown is silently overwritten;
	// `itervox stop` validates liveness before signalling so a stale file
	// cannot cause harm.
	if path, err := writePIDFile(*workflowPath); err != nil {
		slog.Warn("itervox: failed to write PID file — `itervox stop` will not find this daemon", "error", err)
	} else {
		slog.Info("itervox: wrote PID file", "path", path, "pid", os.Getpid())
		defer removePIDFile(*workflowPath)
	}

	// Outer loop: restart when WORKFLOW.md changes.
	// firstIter gates the os.Exit on config-load/validation failure: a typo at
	// boot is fatal (the user has no good config to fall back on), but a typo
	// during a live edit must NOT kill the daemon — the watcher will fire
	// again when the user fixes the file, and we'll retry on the next tick.
	// reloadAttempt feeds reloadBackoff for exponential retry timing (T-26):
	// resets to 0 on every successful load so transient errors don't compound.
	firstIter := true
	reloadAttempt := 0
	for {
		loaded, err := config.Load(*workflowPath)
		if err == nil {
			err = config.ValidateDispatch(loaded)
		}
		if err != nil {
			if firstIter {
				slog.Error("startup: config invalid", "path", *workflowPath, "error", err)
				fatalExit(1)
			}
			wait := reloadBackoff(reloadAttempt)
			retryAt := time.Now().Add(wait)
			publishConfigInvalid(&server.ConfigInvalidStatus{
				Path:         *workflowPath,
				Error:        err.Error(),
				RetryAttempt: reloadAttempt + 1,
				RetryAt:      retryAt.Format(time.RFC3339),
			})
			slog.Warn("reload: config invalid, keeping daemon alive — fix WORKFLOW.md to resume",
				"path", *workflowPath, "error", err, "retry_attempt", reloadAttempt+1, "retry_in", wait.String())
			time.Sleep(wait)
			reloadAttempt++
			continue
		}
		cfg := loaded
		firstIter = false
		reloadAttempt = 0         // reset on every successful load
		publishConfigInvalid(nil) // clear the banner

		// Auto-discover models at startup when WORKFLOW.md doesn't have available_models.
		// This ensures the dashboard model dropdown is populated even for pre-existing configs.
		if len(cfg.Agent.AvailableModels) == 0 {
			claudeModels := agent.ListClaudeModels()
			codexModels := agent.ListCodexModels()
			cfg.Agent.AvailableModels = map[string][]config.ModelOption{
				"claude": make([]config.ModelOption, len(claudeModels)),
				"codex":  make([]config.ModelOption, len(codexModels)),
			}
			for i, m := range claudeModels {
				cfg.Agent.AvailableModels["claude"][i] = config.ModelOption{ID: m.ID, Label: m.Label}
			}
			for i, m := range codexModels {
				cfg.Agent.AvailableModels["codex"][i] = config.ModelOption{ID: m.ID, Label: m.Label}
			}
			slog.Info("models auto-discovered", "claude", len(claudeModels), "codex", len(codexModels))
		}

		runCtx, runCancel := context.WithCancel(ctx)

		// Watch WORKFLOW.md; cancel runCtx to trigger reload on change.
		go func() {
			if err := workflow.Watch(runCtx, *workflowPath, runCancel); err != nil && runCtx.Err() == nil {
				slog.Warn("workflow watcher stopped", "error", err)
			}
		}()

		runDone := make(chan error, 1)
		go func() {
			runDone <- run(runCtx, cancel, cfg, *workflowPath, rotatingFile.Filename, rotatingFile, logLevel, stderrOnly)
		}()

		var runErr error
		// Wait for run to finish or a signal to arrive.
		select {
		case err := <-runDone:
			runCancel()
			if ctx.Err() != nil {
				return // top-level shutdown already in progress
			}
			runErr = err
		case sig := <-sigCh:
			slog.Info("shutting down gracefully, waiting for active workers...", "signal", sig, "grace", shutdownGrace.String())
			cancel()    // cancel top-level ctx → stops dispatching new work
			runCancel() // also cancel runCtx

			// Wait for run to finish within grace period, or force-exit on second signal / timeout.
			graceTimer := time.NewTimer(*shutdownGrace)
			defer graceTimer.Stop()
			select {
			case <-runDone:
				slog.Info("all workers finished, exiting")
			case <-graceTimer.C:
				slog.Warn("grace period expired, forcing exit")
			case sig2 := <-sigCh:
				slog.Warn("received second signal, forcing exit", "signal", sig2)
			}
			return
		}

		if ctx.Err() != nil {
			return // top-level shutdown
		}

		reloadMsg, reloadDelay := reloadPlanForRunExit(runErr)
		// Real run errors WARN; a clean reload (nil or wrapped context.Canceled)
		// is Debug-level — matches internal/workflow/watcher.go's "file changed"
		// signal. Promoting it to Info would spam stderr on every save.
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			slog.Warn(reloadMsg, "error", runErr, "delay", reloadDelay.String())
		} else {
			slog.Debug(reloadMsg)
		}
		time.Sleep(reloadDelay)
	}
}

// run starts the orchestrator (and optionally the HTTP server) and blocks until
// runCtx is cancelled. logFile is passed to the HTTP server for the /api/v1/logs endpoint.
// fileWriter is the rotating log file writer; logLevel is the configured log level.
// Both are used to redirect slog away from stderr once the TUI takes the terminal.
func run(ctx context.Context, quitApp func(), cfg *config.Config, workflowPath string, logFile string, fileWriter io.Writer, logLevel slog.Level, stderrOnly *slog.Logger) error {
	tr, err := buildTracker(cfg)
	if err != nil {
		return fmt.Errorf("build tracker: %w", err)
	}

	var runner agent.Runner = agent.NewMultiRunner(
		agent.NewClaudeRunner(),
		map[string]agent.Runner{
			"codex": agent.NewCodexRunner(),
		},
	)
	runner = commandResolverRunner{inner: runner}

	// T-32: apply SSH StrictHostKeyChecking config. The agent package keeps a
	// safe TOFU default ("accept-new") at startup; only override when the
	// user has set a value in WORKFLOW.md. Per-host overrides are applied
	// alongside (nil clears any prior overrides on reload).
	if cfg.Agent.SSHStrictHostChecking != "" {
		agent.SetSSHStrictHostDefault(cfg.Agent.SSHStrictHostChecking)
	}
	agent.SetSSHStrictHostOverrides(cfg.Agent.SSHStrictHostByHost)

	// Validate CLI availability for the default agent command and all profiles.
	// A missing default binary is a hard error — fail before entering the
	// dispatch loop so the user sees it immediately rather than at dispatch time.
	validatedBackends := make(map[string]struct{})
	if err := validateBackend(configuredBackend(cfg.Agent.Command, cfg.Agent.Backend), "", validatedBackends, cfg); err != nil {
		return fmt.Errorf("agent startup: %w", err)
	}
	for name, profile := range cfg.Agent.Profiles {
		if err := validateBackend(configuredBackend(profile.Command, profile.Backend), name, validatedBackends, cfg); err != nil {
			slog.Warn("agent startup: profile validation failed", "profile", name, "error", err)
		}
	}
	wm := workspace.NewManager(cfg)

	// Remove workspaces for issues that were terminal when we last shut down.
	// T-49: capture the wait closure so shutdown can ensure cleanup finished
	// before the daemon exits (otherwise an in-flight tracker.FetchIssuesByStates
	// could be aborted mid-call when ctx is cancelled).
	cleanupWait := orchestrator.StartupTerminalCleanup(ctx, tr, cfg.Tracker.TerminalStates, func(id string) error {
		return wm.RemoveWorkspace(ctx, id, "")
	})
	defer cleanupWait()

	refreshChan := make(chan struct{}, 1)
	logBuf := logbuffer.New()
	// Persist per-issue logs to disk alongside the main log file so they
	// survive restarts and remain viewable after an issue completes.
	if logFile != "" {
		logBuf.SetLogDir(filepath.Join(filepath.Dir(logFile), "issues"))
	}
	orch := orchestrator.New(cfg, tr, runner, wm)
	if os.Getenv("ITERVOX_DRY_RUN") == "1" {
		orch.DryRun = true
		slog.Info("itervox: dry-run mode enabled — agents will not be dispatched")
	}
	orch.SetLogBuffer(logBuf)
	if logFile != "" {
		logDir := filepath.Dir(logFile)
		orch.SetHistoryFile(filepath.Join(logDir, "history.json"))
		orch.SetPausedFile(filepath.Join(logDir, "paused.json"))
		orch.SetInputRequiredFile(filepath.Join(logDir, "input_required.json"))
		// Gap §5.3 — persist rate_limited auto-switch overrides so a daemon
		// crash mid-flight doesn't lose them and re-dispatch under the
		// original (rate-limited) profile.
		orch.SetAutoSwitchedFile(filepath.Join(logDir, "auto_switched.json"))
		orch.SetAgentLogDir(filepath.Join(logDir, "sessions"))
	}
	if cfg.Tracker.Kind != "" && cfg.Tracker.ProjectSlug != "" {
		orch.SetHistoryKey(cfg.Tracker.Kind + ":" + cfg.Tracker.ProjectSlug)
	}

	appSessionID := newAppSessionID()
	orch.SetAppSessionID(appSessionID)

	snap := buildSnapFunc(orch, tr, cfg, appSessionID, logBuf, workflowPath)

	// HTTP server — bind listener early so we know the actual port before
	// starting the TUI (the TUI needs the correct dashboard URL for 'w' key).
	var srvDone <-chan error
	var srvListener net.Listener
	var actualAddr string
	var actionTokenStore *agentactions.Store
	if cfg.Server.Port != nil {
		var err error
		srvListener, actualAddr, err = listenWithFallback(cfg.Server.Host, *cfg.Server.Port, 10)
		if err != nil {
			return fmt.Errorf("server: %w", err)
		}
		slog.Info("HTTP server listening", "addr", actualAddr)
		// Secure-by-default for non-loopback binds: if no token is set and the
		// user hasn't explicitly opted into unauthenticated LAN access, we
		// auto-generate an ephemeral token and install the bearer middleware.
		// Regenerated on every restart unless the user pins one via env var.
		if host := cfg.Server.Host; host != "127.0.0.1" && host != "localhost" && host != "::1" && host != "" {
			if os.Getenv("ITERVOX_API_TOKEN") == "" {
				if cfg.Server.AllowUnauthenticatedLAN {
					slog.Warn("server: binding to non-loopback address with no authentication (allow_unauthenticated_lan: true)",
						"host", host)
				} else {
					generated, err := generateAPIToken()
					if err != nil {
						return fmt.Errorf("server: auto-generating API token: %w", err)
					}
					if err := os.Setenv("ITERVOX_API_TOKEN", generated); err != nil {
						return fmt.Errorf("server: setting ITERVOX_API_TOKEN: %w", err)
					}
					slog.Info("server: auto-generated ephemeral API token for non-loopback bind",
						"host", host,
						"hint", "set ITERVOX_API_TOKEN in .itervox/.env to pin a stable token, or set server.allow_unauthenticated_lan: true to opt out")
				}
			}
		}
		// When a token is set (user-provided OR auto-generated above), print a
		// dashboard URL that carries it as a query parameter. AuthGate captures
		// ?token= on first load, persists it in sessionStorage, and strips it
		// from the URL via history.replaceState. All subsequent requests attach
		// it as an Authorization: Bearer header.
		if tok := os.Getenv("ITERVOX_API_TOKEN"); tok != "" {
			// Token must NEVER hit the rotating log file. Use the stderr-only
			// logger built in main(), bypassing the slog default (which fans
			// out to disk). A future PR moving back to plain slog.Info(...)
			// would silently start writing the bearer token to ~/.itervox/logs/.
			stderrOnly.Info("dashboard URL (carries token — copy/paste once)",
				"url", fmt.Sprintf("http://%s/?token=%s", actualAddr, tok))
		}
	}
	if actualAddr != "" {
		actionTokenStore = agentactions.NewStore()
		orch.SetAgentActionBaseURL(agentActionBaseURL(actualAddr))
		orch.SetAgentActionTokens(actionTokenStore)
	}

	// Redirect slog to file-only before the TUI takes the alt-screen.
	// Without this, concurrent slog writes to stderr corrupt the bubbletea display.
	// The TUI log pane reads directly from logBuf instead of stderr.
	// Wrapped in RedactingHandler so secrets-in-msg/attrs are scrubbed before
	// hitting ~/.itervox/logs/ even after the TUI takes over (T-29 / F-NEW-A).
	slog.SetDefault(slog.New(logging.NewRedactingHandler(slog.NewTextHandler(fileWriter, &slog.HandlerOptions{Level: logLevel}))))
	tuiCfg, tuiCancel := buildTUIConfig(orch, tr, cfg, workflowPath, quitApp)
	if actualAddr != "" {
		if tok := os.Getenv("ITERVOX_API_TOKEN"); tok != "" {
			tuiCfg.DashboardURL = fmt.Sprintf("http://%s/?token=%s", actualAddr, tok)
		} else {
			tuiCfg.DashboardURL = fmt.Sprintf("http://%s/", actualAddr)
		}
	}
	tuiDone := statusui.Run(ctx, snap, logBuf, tuiCfg, tuiCancel)
	// Wait for the TUI to fully restore the terminal (stty sane) before run()
	// returns. Without this, the process can exit while the terminal is still
	// in raw mode, leaving the user's shell broken (no Ctrl-C, no echo).
	defer func() { <-tuiDone }()

	// Start serving on the already-bound listener.
	if srvListener != nil {
		fetchIssue := func(ctx context.Context, identifier string) (*server.TrackerIssue, error) {
			issue, err := tr.FetchIssueByIdentifier(ctx, identifier)
			if err != nil {
				return nil, err
			}
			if issue == nil {
				return nil, nil
			}
			ti := app.EnrichIssue(*issue, orch.Snapshot(), time.Now(), cfg)
			return &ti, nil
		}

		var pm server.ProjectManager
		if tpm, ok := tr.(tracker.ProjectManager); ok {
			pm = &linearProjectManager{pm: tpm, workflowPath: workflowPath}
		}

		adapter := &orchestratorAdapter{
			orch:         orch,
			logBuf:       logBuf,
			cfg:          cfg,
			tr:           tr,
			workflowPath: workflowPath,
		}
		adapter.initSkillsCache()
		srv := server.New(server.Config{
			Snapshot:         snap,
			RefreshChan:      refreshChan,
			LogFile:          logFile,
			Client:           adapter,
			FetchIssue:       fetchIssue,
			ProjectManager:   pm,
			APIToken:         os.Getenv("ITERVOX_API_TOKEN"),
			ActionTokenStore: actionTokenStore,
			SkillsClient:     adapter,
		})
		adapter.notify = srv.Notify
		if err := srv.Validate(); err != nil {
			return fmt.Errorf("server configuration error: %w", err)
		}
		orch.OnStateChange = srv.Notify
		srvDone = serveOnListener(ctx, srvListener, actualAddr, srv)
	}

	// Forward web dashboard refresh signals to the orchestrator for an immediate re-poll.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-refreshChan:
				slog.Debug("manual refresh requested")
				orch.Refresh()
			}
		}
	}()

	startAutomations(ctx, cfg, tr, orch)

	orchDone := make(chan error, 1)
	go func() { orchDone <- orch.Run(ctx) }()

	if srvDone != nil {
		select {
		case err := <-orchDone:
			return err
		case err := <-srvDone:
			return err
		}
	}
	return <-orchDone
}

// resolveAgentCommand resolves a bare command name (e.g. "claude") to its full
// absolute path using the user's interactive login shell, which sources .zshrc
// and therefore picks up PATH additions from nvm, volta, homebrew, etc.
// If the command is already absolute, or resolution fails, the original value
// is returned unchanged.
// buildSnapFunc returns the StateSnapshot function wired to the live orchestrator,
// tracker, and config. Extracted from run() to keep that function scannable.
func sortedRetryRows(retries map[string]*orchestrator.RetryEntry) []server.RetryRow {
	rows := make([]server.RetryRow, 0, len(retries))
	for _, r := range retries {
		row := server.RetryRow{
			Identifier: r.Identifier,
			Attempt:    r.Attempt,
			DueAt:      r.DueAt,
		}
		if r.Error != nil {
			row.Error = *r.Error
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].Identifier < rows[j].Identifier
	})
	return rows
}

func sortedPausedIdentifiers(paused map[string]string) []string {
	identifiers := make([]string, 0, len(paused))
	for identifier := range paused {
		identifiers = append(identifiers, identifier)
	}
	sort.Strings(identifiers)
	return identifiers
}

// resolveProjectName returns a short, human-readable label for the project
// this daemon is serving. Preference order:
//  1. `tracker.project_slug` from WORKFLOW.md (when the user has declared
//     one — most Linear/GitHub setups do).
//  2. The basename of the WORKFLOW.md directory (e.g. `/Users/me/acme/WORKFLOW.md`
//     → "acme"), which works for unslugged local scaffolds.
//  3. "itervox" as a last-resort fallback so the header never renders empty.
func resolveProjectName(cfg *config.Config, workflowPath string) string {
	if cfg != nil && strings.TrimSpace(cfg.Tracker.ProjectSlug) != "" {
		return cfg.Tracker.ProjectSlug
	}
	if abs, err := filepath.Abs(workflowPath); err == nil {
		if base := filepath.Base(filepath.Dir(abs)); base != "." && base != "/" && base != "" {
			return base
		}
	}
	return "itervox"
}

func buildSnapFunc(orch *orchestrator.Orchestrator, tr tracker.Tracker, cfg *config.Config, appSessionID string, logBuf *logbuffer.Buffer, workflowPath string) func() server.StateSnapshot {
	// projectName is computed once at construction time (not per snapshot) —
	// neither the tracker slug nor the workflow path change within a daemon
	// run (config reload restarts the process, so this closure is recreated).
	projectName := resolveProjectName(cfg, workflowPath)
	return func() server.StateSnapshot {
		s := orch.Snapshot()
		now := time.Now()

		running := make([]server.RunningRow, 0, len(s.Running))
		for _, r := range s.Running {
			msg := r.LastMessage
			if len(msg) > 120 {
				msg = msg[:120] + "…"
			}
			var lastEvAt string
			if r.LastEventAt != nil {
				lastEvAt = r.LastEventAt.Format(time.RFC3339)
			}
			// Count subagent markers in the log buffer for this issue.
			var subCount int
			if logBuf != nil {
				for _, line := range logBuf.Get(r.Issue.Identifier) {
					if strings.Contains(line, `"claude: subagent"`) || strings.Contains(line, `"codex: subagent"`) {
						subCount++
					}
				}
			}
			// T-6: prefer the live counter (incremented from the HTTP handler
			// goroutine) over RunEntry.CommentCount because the latter is only
			// updated when the run terminates. If both are zero we omit the
			// field via omitempty.
			liveComments := r.CommentCount
			if c := orch.CommentCountFor(r.Issue.Identifier); c > liveComments {
				liveComments = c
			}
			running = append(running, server.RunningRow{
				Identifier:    r.Issue.Identifier,
				State:         r.Issue.State,
				TurnCount:     r.TurnCount,
				Tokens:        r.TotalTokens,
				InputTokens:   r.InputTokens,
				OutputTokens:  r.OutputTokens,
				LastEvent:     msg,
				LastEventAt:   lastEvAt,
				SessionID:     r.SessionID,
				WorkerHost:    r.WorkerHost,
				Backend:       r.Backend,
				Kind:          r.Kind,
				AutomationID:  r.AutomationID,
				TriggerType:   r.TriggerType,
				CommentCount:  liveComments,
				ElapsedMs:     now.Sub(r.StartedAt).Milliseconds(),
				StartedAt:     r.StartedAt,
				SubagentCount: subCount,
			})
		}
		sort.Slice(running, func(i, j int) bool {
			return running[i].StartedAt.Before(running[j].StartedAt)
		})

		retrying := sortedRetryRows(s.RetryAttempts)
		paused := sortedPausedIdentifiers(s.PausedIdentifiers)

		var rateLimits *server.RateLimitInfo
		var activeProjectFilter []string
		if rl, ok := tr.(tracker.RateLimiter); ok {
			if snap := rl.RateLimitSnapshot(); snap != nil {
				rateLimits = &server.RateLimitInfo{
					RequestsLimit:       snap.RequestsLimit,
					RequestsRemaining:   snap.RequestsRemaining,
					RequestsReset:       snap.Reset,
					ComplexityLimit:     snap.ComplexityLimit,
					ComplexityRemaining: snap.ComplexityRemaining,
				}
			}
		}
		if tpm, ok := tr.(tracker.ProjectManager); ok {
			activeProjectFilter = tpm.GetProjectFilter()
		}
		// When no runtime filter is set but WORKFLOW.md has project_slug,
		// surface it so the TUI picker shows it as checked.
		if activeProjectFilter == nil && cfg.Tracker.ProjectSlug != "" {
			activeProjectFilter = []string{cfg.Tracker.ProjectSlug}
		}
		profiles := orch.ProfilesCfg()
		autoClearWorkspace := orch.AutoClearWorkspaceCfg()
		activeStates, terminalStates, completionState := orch.TrackerStatesCfg()

		var availableProfiles []string
		for name, profile := range profiles {
			if config.ProfileEnabled(profile) {
				availableProfiles = append(availableProfiles, name)
			}
		}
		sort.Strings(availableProfiles)

		var profileDefs map[string]server.ProfileDef
		if len(profiles) > 0 {
			profileDefs = make(map[string]server.ProfileDef, len(profiles))
			for n, p := range profiles {
				profileDefs[n] = server.ProfileDef{
					Command:          p.Command,
					Prompt:           p.Prompt,
					Backend:          p.Backend,
					Enabled:          config.ProfileEnabled(p),
					AllowedActions:   config.NormalizeAllowedActions(p.AllowedActions),
					CreateIssueState: p.CreateIssueState,
				}
			}
		}

		completedRuns := orch.RunHistory()
		history := make([]server.HistoryRow, 0, len(completedRuns))
		for _, r := range completedRuns {
			history = append(history, server.HistoryRow{
				Identifier:   r.Identifier,
				Title:        r.Title,
				StartedAt:    r.StartedAt,
				FinishedAt:   r.FinishedAt,
				ElapsedMs:    r.ElapsedMs,
				TurnCount:    r.TurnCount,
				TotalTokens:  r.TotalTokens,
				InputTokens:  r.InputTokens,
				OutputTokens: r.OutputTokens,
				Status:       r.Status,
				WorkerHost:   r.WorkerHost,
				Backend:      r.Backend,
				Kind:         r.Kind,
				SessionID:    r.SessionID,
				AppSessionID: r.AppSessionID,
				AutomationID: r.AutomationID,
				TriggerType:  r.TriggerType,
				CommentCount: r.CommentCount,
			})
		}

		sshHostAddrs, sshHostDescs := orch.SSHHostsCfg()
		sshHostInfos := make([]server.SSHHostInfo, 0, len(sshHostAddrs))
		for _, h := range sshHostAddrs {
			sshHostInfos = append(sshHostInfos, server.SSHHostInfo{
				Host:        h,
				Description: sshHostDescs[h],
			})
		}

		pausedWithPR := orch.GetPausedOpenPRs()
		snap := server.StateSnapshot{
			GeneratedAt:                  now,
			Counts:                       server.Counts{Running: len(running), Retrying: len(retrying), Paused: len(paused)},
			Running:                      running,
			History:                      history,
			Retrying:                     retrying,
			Paused:                       paused,
			PausedWithPR:                 pausedWithPR,
			MaxConcurrentAgents:          orch.MaxWorkers(),
			MaxRetries:                   orch.MaxRetriesCfg(),
			FailedState:                  orch.FailedStateCfg(),
			MaxSwitchesPerIssuePerWindow: orch.MaxSwitchesPerIssuePerWindowCfg(),
			SwitchWindowHours:            orch.SwitchWindowHoursCfg(),
			RateLimits:                   rateLimits,
			TrackerKind:                  cfg.Tracker.Kind,
			ProjectName:                  projectName,
			ActiveProjectFilter:          activeProjectFilter,
			AvailableProfiles:            availableProfiles,
			ProfileDefs:                  profileDefs,
			ActiveStates:                 activeStates,
			TerminalStates:               terminalStates,
			CompletionState:              completionState,
			BacklogStates:                cfg.Tracker.BacklogStates,
			PollIntervalMs:               cfg.Polling.IntervalMs,
			AutoClearWorkspace:           autoClearWorkspace,
			CurrentAppSessionID:          appSessionID,
			SSHHosts:                     sshHostInfos,
			DispatchStrategy:             orch.DispatchStrategyCfg(),
			DefaultBackend:               configuredBackend(cfg.Agent.Command, cfg.Agent.Backend),
			InlineInput:                  orch.InlineInputCfg(),
			Automations:                  automationconfig.DefinitionsFromConfigs(orch.AutomationsCfg()),
			AvailableModels:              convertModelsForSnapshot(cfg.Agent.AvailableModels),
			SupportedAgentActions:        config.SupportedAgentActions(),
			ReviewerProfile:              func() string { p, _ := orch.ReviewerCfg(); return p }(),
			AutoReview:                   func() bool { _, a := orch.ReviewerCfg(); return a }(),
		}
		// Stale threshold for the dashboard badge: pick the longest
		// MaxAgeMinutes across all enabled input_required automations. If no
		// rule configures one, fall back to 24h so abandoned entries still
		// surface visually even when no automation guards against them.
		staleAfter := longestInputRequiredMaxAge(orch.AutomationsCfg(), 24*time.Hour)
		snap.InputRequired = sortedInputRequiredRows(s.InputRequiredIssues, s.PendingInputResumes, staleAfter, now)
		// Surface in-flight WORKFLOW.md reload failures (T-26). nil when valid.
		snap.ConfigInvalid = loadConfigInvalid()
		return snap
	}
}

// buildTUIConfig wires the terminal status-UI config and returns the cancel
// function (used as the 'x' key handler in statusui.Run). Extracted from run().
func buildTUIConfig(
	orch *orchestrator.Orchestrator,
	tr tracker.Tracker,
	cfg *config.Config,
	workflowPath string,
	quitApp func(),
) (statusui.Config, func(string) bool) {
	tuiCfg := statusui.Config{
		MaxAgents:     cfg.Agent.MaxConcurrentAgents,
		TodoStates:    cfg.Tracker.ActiveStates,
		BacklogStates: cfg.Tracker.BacklogStates,
		QuitApp:       quitApp,
	}
	if cfg.Server.Port != nil {
		if tok := os.Getenv("ITERVOX_API_TOKEN"); tok != "" {
			tuiCfg.DashboardURL = fmt.Sprintf("http://%s:%d/?token=%s", cfg.Server.Host, *cfg.Server.Port, tok)
		} else {
			tuiCfg.DashboardURL = fmt.Sprintf("http://%s:%d/", cfg.Server.Host, *cfg.Server.Port)
		}
	}
	if tpm, ok := tr.(tracker.ProjectManager); ok {
		tuiCfg.FetchProjects = func() ([]statusui.ProjectItem, error) {
			fetchCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			projects, err := tpm.FetchProjects(fetchCtx)
			if err != nil {
				return nil, err
			}
			items := make([]statusui.ProjectItem, len(projects))
			for i, p := range projects {
				items[i] = statusui.ProjectItem{ID: p.ID, Name: p.Name, Slug: p.Slug}
			}
			return items, nil
		}
		tuiCfg.SetProjectFilter = func(slugs []string) {
			tpm.SetProjectFilter(slugs)
			if err := updateWorkflowProjectSlug(workflowPath, slugs); err != nil {
				slog.Warn("tui: project_slug persist failed; runtime filter applied but next reload will see the old value", "error", err)
			}
		}
	}
	tuiCfg.AdjustWorkers = func(delta int) {
		next := orch.MaxWorkers() + delta
		orch.SetMaxWorkers(next)
		if err := workflow.PatchIntField(workflowPath, "max_concurrent_agents", orch.MaxWorkers()); err != nil {
			slog.Warn("failed to persist max_concurrent_agents to WORKFLOW.md", "error", err)
		}
	}
	{
		backlogAndActive := append(append([]string{}, cfg.Tracker.BacklogStates...), cfg.Tracker.ActiveStates...)
		tuiCfg.FetchBacklog = func() ([]statusui.BacklogIssueItem, error) {
			fetchCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			issues, err := tr.FetchIssuesByStates(fetchCtx, backlogAndActive)
			if err != nil {
				return nil, err
			}
			items := make([]statusui.BacklogIssueItem, len(issues))
			for i, iss := range issues {
				pri := 0
				if iss.Priority != nil {
					pri = *iss.Priority
				}
				var desc string
				if iss.Description != nil {
					desc = *iss.Description
				}
				var comments []statusui.CommentItem
				for _, c := range iss.Comments {
					comments = append(comments, statusui.CommentItem{Author: c.AuthorName, Body: c.Body})
				}
				items[i] = statusui.BacklogIssueItem{
					Identifier:  iss.Identifier,
					Title:       iss.Title,
					State:       iss.State,
					Priority:    pri,
					Description: desc,
					Comments:    comments,
				}
			}
			return items, nil
		}
		if len(cfg.Tracker.ActiveStates) > 0 {
			targetState := cfg.Tracker.ActiveStates[0]
			tuiCfg.DispatchIssue = func(identifier string) error {
				dispCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				allStates := append(append([]string{}, cfg.Tracker.BacklogStates...), cfg.Tracker.ActiveStates...)
				issues, err := tr.FetchIssuesByStates(dispCtx, allStates)
				if err != nil {
					return err
				}
				for _, iss := range issues {
					if iss.Identifier == identifier {
						return tr.UpdateIssueState(dispCtx, iss.ID, targetState)
					}
				}
				return fmt.Errorf("issue %s not found", identifier)
			}
		}
	}
	tuiCfg.ResumeIssue = func(identifier string) bool {
		ok := orch.ResumeIssue(identifier)
		if ok {
			orch.Refresh()
		}
		return ok
	}
	tuiCfg.TerminateIssue = func(identifier string) bool {
		ok := orch.TerminateIssue(identifier)
		if ok {
			orch.Refresh()
		}
		return ok
	}
	tuiCfg.SetIssueProfile = func(identifier, profile string) {
		orch.SetIssueProfile(identifier, profile)
	}
	tuiCfg.IssueProfiles = func() map[string]string {
		s := orch.Snapshot()
		return s.IssueProfiles
	}
	tuiCfg.TriggerPoll = orch.Refresh
	tuiCfg.FetchIssueDetail = func(identifier string) (*statusui.BacklogIssueItem, error) {
		fetchCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		issue, err := tr.FetchIssueByIdentifier(fetchCtx, identifier)
		if err != nil {
			return nil, err
		}
		pri := 0
		if issue.Priority != nil {
			pri = *issue.Priority
		}
		var desc string
		if issue.Description != nil {
			desc = *issue.Description
		}
		var comments []statusui.CommentItem
		for _, c := range issue.Comments {
			comments = append(comments, statusui.CommentItem{Author: c.AuthorName, Body: c.Body})
		}
		return &statusui.BacklogIssueItem{
			Identifier:  issue.Identifier,
			Title:       issue.Title,
			State:       issue.State,
			Priority:    pri,
			Description: desc,
			Comments:    comments,
		}, nil
	}

	tuiCancel := func(identifier string) bool {
		issue := orch.GetRunningIssue(identifier)
		if issue == nil {
			return false
		}
		if !orch.CancelIssue(identifier) {
			return false
		}
		return true
	}
	return tuiCfg, tuiCancel
}

// orchestratorAdapter implements server.OrchestratorClient using the live
// orchestrator, log buffer, tracker, and WORKFLOW.md persistence helpers.
// notify must be set after server construction (adapter.notify = srv.Notify).
type orchestratorAdapter struct {
	orch         *orchestrator.Orchestrator
	logBuf       *logbuffer.Buffer
	cfg          *config.Config
	tr           tracker.Tracker
	workflowPath string
	notify       func()
	skillsCache  *skills.Cache
}

func (a *orchestratorAdapter) FetchIssues(ctx context.Context) ([]server.TrackerIssue, error) {
	allStates := deduplicateStates(a.cfg.Tracker.BacklogStates, a.cfg.Tracker.ActiveStates, a.cfg.Tracker.TerminalStates, a.cfg.Tracker.CompletionState)
	issues, err := a.tr.FetchIssuesByStates(ctx, allStates)
	if err != nil {
		return nil, err
	}
	snap := a.orch.Snapshot()
	now := time.Now()
	result := make([]server.TrackerIssue, len(issues))
	for i, issue := range issues {
		result[i] = app.EnrichIssue(issue, snap, now, a.cfg)
	}
	return result, nil
}

func (a *orchestratorAdapter) CancelIssue(identifier string) bool {
	return a.orch.CancelIssue(identifier)
}

func (a *orchestratorAdapter) ResumeIssue(identifier string) bool {
	ok := a.orch.ResumeIssue(identifier)
	if ok {
		a.orch.Refresh()
	}
	return ok
}

func (a *orchestratorAdapter) TerminateIssue(identifier string) bool {
	ok := a.orch.TerminateIssue(identifier)
	if ok {
		a.orch.Refresh()
	}
	return ok
}

func (a *orchestratorAdapter) ReanalyzeIssue(identifier string) bool {
	return a.orch.ReanalyzeIssue(identifier)
}

func (a *orchestratorAdapter) FetchLogs(identifier string) []string {
	return a.logBuf.Get(identifier)
}

func (a *orchestratorAdapter) FetchLogIdentifiers() []string {
	return a.logBuf.Identifiers()
}

func (a *orchestratorAdapter) ClearLogs(identifier string) error {
	return a.logBuf.Clear(identifier)
}

func (a *orchestratorAdapter) ClearAllLogs() error {
	return a.logBuf.ClearAll()
}

func (a *orchestratorAdapter) ClearIssueSubLogs(identifier string) error {
	logDir := a.orch.AgentLogDir()
	if logDir == "" {
		return nil
	}
	issueDir := filepath.Join(logDir, workspace.SanitizeKey(identifier))
	if err := workspace.AssertContained(logDir, issueDir); err != nil {
		return fmt.Errorf("clear sublogs: %w", err)
	}
	entries, err := os.ReadDir(issueDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		_ = os.Remove(filepath.Join(issueDir, e.Name()))
	}
	return nil
}

func (a *orchestratorAdapter) ClearSessionSublog(identifier, sessionID string) error {
	logDir := a.orch.AgentLogDir()
	if logDir == "" {
		return nil
	}
	// Sanitize both path components to prevent directory traversal.
	safeID := workspace.SanitizeKey(identifier)
	safeSess := workspace.SanitizeKey(sessionID)
	p := filepath.Join(logDir, safeID, safeSess+".jsonl")
	if err := workspace.AssertContained(logDir, p); err != nil {
		return fmt.Errorf("clear session sublog: %w", err)
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// FetchSubLogs returns parsed Claude Code session logs from CLAUDE_CODE_LOG_DIR.
// The fetcher is selected based on where the issue was last run:
//   - SSH host → SSHSublogFetcher (tar-over-SSH, session IDs from filenames)
//   - local    → LocalSublogFetcher (direct disk read)
//   - Docker   → DockerSublogFetcher (planned)
func (a *orchestratorAdapter) FetchSubLogs(ctx context.Context, identifier string) ([]domain.IssueLogEntry, error) {
	logDir := a.orch.AgentLogDir()
	if logDir == "" {
		return nil, nil
	}
	issueLogDir := filepath.Join(logDir, workspace.SanitizeKey(identifier))
	return a.sublogFetcher(identifier).FetchSubLogs(ctx, issueLogDir)
}

// sublogFetcher resolves the correct SublogFetcher for identifier by inspecting
// run history and live running sessions. Returns LocalSublogFetcher when no
// remote host is found.
func (a *orchestratorAdapter) sublogFetcher(identifier string) agent.SublogFetcher {
	// Check currently-running sessions first (most recent wins).
	// Running is keyed by issue ID, not identifier — iterate values.
	snap := a.orch.Snapshot()
	for _, entry := range snap.Running {
		if entry.Issue.Identifier == identifier && entry.WorkerHost != "" {
			return agent.SSHSublogFetcher{Host: entry.WorkerHost}
		}
	}
	// Fall back to run history.
	for _, run := range a.orch.RunHistory() {
		if run.Identifier == identifier && run.WorkerHost != "" {
			return agent.SSHSublogFetcher{Host: run.WorkerHost}
		}
	}
	return agent.LocalSublogFetcher{}
}

func (a *orchestratorAdapter) DispatchReviewer(identifier string) error {
	return a.orch.DispatchReviewer(identifier)
}

func (a *orchestratorAdapter) CommentOnIssue(ctx context.Context, identifier, body string) error {
	issue, err := a.tr.FetchIssueByIdentifier(ctx, identifier)
	if err != nil {
		return fmt.Errorf("fetch issue: %w", err)
	}
	if issue == nil {
		return fmt.Errorf("issue %s not found", identifier)
	}
	_, err = a.tr.CreateComment(ctx, issue.ID, tracker.MarkManagedComment(body))
	return err
}

func (a *orchestratorAdapter) CreateIssue(
	ctx context.Context,
	identifier, title, body, stateName string,
) (*domain.Issue, error) {
	issue, err := a.tr.FetchIssueByIdentifier(ctx, identifier)
	if err != nil {
		return nil, fmt.Errorf("fetch issue: %w", err)
	}
	if issue == nil {
		return nil, fmt.Errorf("issue %s not found", identifier)
	}
	return a.tr.CreateIssue(ctx, issue.ID, title, body, stateName)
}

func (a *orchestratorAdapter) UpdateIssueState(ctx context.Context, identifier, stateName string) error {
	issue, err := a.tr.FetchIssueByIdentifier(ctx, identifier)
	if err != nil {
		return fmt.Errorf("fetch issue: %w", err)
	}
	if issue == nil {
		return fmt.Errorf("issue %s not found", identifier)
	}
	return a.tr.UpdateIssueState(ctx, issue.ID, stateName)
}

// deduplicateStates concatenates backlog, active, terminal states and the
// completion state (if non-empty), removing duplicates while preserving order.
func deduplicateStates(backlog, active, terminal []string, completion string) []string {
	base := append(append(append([]string{}, backlog...), active...), terminal...)
	if completion != "" {
		base = append(base, completion)
	}
	seen := make(map[string]bool, len(base))
	out := make([]string, 0, len(base))
	for _, s := range base {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

type commandResolverRunner struct {
	inner   agent.Runner
	resolve func(string) string
}

func (r commandResolverRunner) RunTurn(
	ctx context.Context,
	log agent.Logger,
	onProgress func(agent.TurnResult),
	sessionID *string,
	prompt, workspacePath, command, workerHost, logDir string,
	readTimeoutMs, turnTimeoutMs int,
) (agent.TurnResult, error) {
	resolver := r.resolve
	if resolver == nil {
		resolver = resolveAgentCommand
	}
	if workerHost == "" {
		command = resolveCommandLine(command, resolver)
	}
	return r.inner.RunTurn(ctx, log, onProgress, sessionID, prompt, workspacePath, command, workerHost, logDir, readTimeoutMs, turnTimeoutMs)
}

func resolveCommandLine(command string, resolver func(string) string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	tokenStart, tokenEnd, ok := resolveCommandTokenSpan(command)
	if !ok {
		return command
	}
	token := command[tokenStart:tokenEnd]
	resolved := resolver(token)
	if resolved == token {
		return command
	}
	return command[:tokenStart] + resolved + command[tokenEnd:]
}

func resolveCommandTokenSpan(command string) (int, int, bool) {
	searchFrom := 0
	firstToken := true
	for {
		start, end, ok := nextShellTokenSpan(command, searchFrom)
		if !ok {
			return 0, 0, false
		}
		token := command[start:end]
		if firstToken && strings.HasPrefix(token, "@@itervox-backend=") {
			firstToken = false
			searchFrom = end
			continue
		}
		firstToken = false
		if isShellEnvAssignmentToken(token) {
			searchFrom = end
			continue
		}
		return start, end, true
	}
}

func nextShellTokenSpan(command string, searchFrom int) (int, int, bool) {
	start := searchFrom
	for start < len(command) {
		if !unicode.IsSpace(rune(command[start])) {
			break
		}
		start++
	}
	if start >= len(command) {
		return 0, 0, false
	}

	end := start
	var quote byte
	for end < len(command) {
		ch := command[end]
		switch {
		case quote != 0:
			if ch == quote {
				quote = 0
				end++
				continue
			}
			if ch == '\\' && quote == '"' && end+1 < len(command) {
				end += 2
				continue
			}
			end++
		case ch == '\'' || ch == '"':
			quote = ch
			end++
		case ch == '\\' && end+1 < len(command):
			end += 2
		case unicode.IsSpace(rune(ch)):
			return start, end, true
		default:
			end++
		}
	}
	return start, end, true
}

func isShellEnvAssignmentToken(token string) bool {
	key, _, ok := strings.Cut(token, "=")
	if !ok || key == "" {
		return false
	}
	for i, ch := range key {
		if i == 0 {
			if ch != '_' && !unicode.IsLetter(ch) {
				return false
			}
			continue
		}
		if ch != '_' && !unicode.IsLetter(ch) && !unicode.IsDigit(ch) {
			return false
		}
	}
	return true
}

func resolveAgentCommand(command string) string {
	if filepath.IsAbs(command) {
		return command
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "bash"
	}
	// -ilc: interactive (-i, sources .zshrc) + login (-l, sources .zprofile) + command (-c)
	out, err := exec.Command(shell, "-ilc", "command -v "+command).Output()
	if err != nil {
		slog.Warn("agent command resolution failed — using bare name; set agent.command to the full path if it fails",
			"command", command, "shell", shell, "error", err)
		return command
	}
	// Interactive shells may print init messages. Scan every line for either:
	//   /absolute/path             (binary on PATH)
	//   alias name=/abs/path       (bash-style alias — Claude Code installs this way)
	//   alias name='/abs/path'
	//   name: aliased to /abs/path (zsh-style alias — `command -v` output on zsh)
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		l := strings.TrimSpace(line)
		if filepath.IsAbs(l) {
			slog.Info("agent command resolved", "command", l)
			return l
		}
		// bash: alias foo=/path  or  alias foo='/path'  or  alias foo="/path"
		if strings.HasPrefix(l, "alias ") {
			if _, val, ok := strings.Cut(l, "="); ok {
				val = strings.Trim(val, `'"`)
				if filepath.IsAbs(val) {
					slog.Info("agent command resolved from alias", "command", val)
					return val
				}
			}
		}
		// zsh: "claude: aliased to /Users/x/.claude/local/claude"
		if strings.Contains(l, ": aliased to ") {
			if _, val, ok := strings.Cut(l, ": aliased to "); ok {
				val = strings.Trim(strings.TrimSpace(val), `'"`)
				if filepath.IsAbs(val) {
					slog.Info("agent command resolved from zsh alias", "command", val)
					return val
				}
			}
		}
	}
	slog.Warn("could not resolve agent command; using bare name — set agent.command to the full path if this fails",
		"command", command, "shell_output", strings.TrimSpace(string(out)))
	return command
}

// linearProjectManager adapts tracker.ProjectManager to server.ProjectManager,
// converting domain.Project → server.Project and persisting filter changes to WORKFLOW.md.
type linearProjectManager struct {
	pm           tracker.ProjectManager
	workflowPath string
}

// FetchProjects implements server.ProjectManager.
func (m *linearProjectManager) FetchProjects(ctx context.Context) ([]server.Project, error) {
	projects, err := m.pm.FetchProjects(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]server.Project, len(projects))
	for i, p := range projects {
		result[i] = server.Project{ID: p.ID, Name: p.Name, Slug: p.Slug}
	}
	return result, nil
}

// SetProjectFilter implements server.ProjectManager and persists the filter to WORKFLOW.md.
// T-55: persist failures slog.Warn; rollback isn't modeled by ProjectManager.
func (m *linearProjectManager) SetProjectFilter(slugs []string) {
	m.pm.SetProjectFilter(slugs)
	if m.workflowPath != "" {
		if err := updateWorkflowProjectSlug(m.workflowPath, slugs); err != nil {
			slog.Warn("project_slug persist failed; runtime filter applied but next reload will see the old value", "error", err, "path", m.workflowPath)
		}
	}
}

// GetProjectFilter implements server.ProjectManager.
func (m *linearProjectManager) GetProjectFilter() []string { return m.pm.GetProjectFilter() }

// buildTracker constructs the correct tracker adapter from config.
func buildTracker(cfg *config.Config) (tracker.Tracker, error) {
	switch cfg.Tracker.Kind {
	case "linear":
		return linear.NewClient(linear.ClientConfig{
			APIKey:         cfg.Tracker.APIKey,
			ProjectSlug:    cfg.Tracker.ProjectSlug,
			ActiveStates:   cfg.Tracker.ActiveStates,
			TerminalStates: cfg.Tracker.TerminalStates,
			Endpoint:       cfg.Tracker.Endpoint,
		}), nil
	case "github":
		return github.NewClient(github.ClientConfig{
			APIKey:         cfg.Tracker.APIKey,
			ProjectSlug:    cfg.Tracker.ProjectSlug,
			ActiveStates:   cfg.Tracker.ActiveStates,
			TerminalStates: cfg.Tracker.TerminalStates,
			BacklogStates:  cfg.Tracker.BacklogStates,
			Endpoint:       cfg.Tracker.Endpoint,
		}), nil
	case "memory":
		issues := tracker.GenerateDemoIssues(10)
		return tracker.NewMemoryTracker(issues, cfg.Tracker.ActiveStates, cfg.Tracker.TerminalStates), nil
	default:
		return nil, fmt.Errorf("unknown tracker kind %q (supported: linear, github, memory)", cfg.Tracker.Kind)
	}
}

// runClear removes workspace directories for one or more issues, or all workspaces
// under workspace.root when no identifiers are given.
//
// Usage:
//
//	itervox clear [--workflow WORKFLOW.md] [identifier ...]
//
// With no identifiers, all subdirectories under workspace.root are removed.
// With identifiers, only those specific workspace directories are removed.
func runClear(args []string) {
	fs := flag.NewFlagSet("clear", flag.ExitOnError)
	workflowPath := fs.String("workflow", "WORKFLOW.md", "path to WORKFLOW.md (to read workspace.root)")
	_ = fs.Parse(args)
	identifiers := fs.Args()

	cfg, err := config.Load(*workflowPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "itervox clear: load config %s: %v\n", *workflowPath, err)
		fatalExit(1)
	}

	root := cfg.Workspace.Root

	// T-43 (05.G-12): refuse to delete from a workspace.root that resolves to
	// a system or user-home directory. A misconfigured WORKFLOW.md (e.g.
	// `workspace.root: /` or `workspace.root: ~`) would otherwise let
	// `itervox clear` recursively remove everything in the user's home dir.
	// Belt-and-suspenders: the WORKFLOW.md schema doesn't currently validate
	// this either.
	if reason := unsafeWorkspaceRoot(root); reason != "" {
		fmt.Fprintf(os.Stderr, "itervox clear: refusing to clear %q (%s) — set workspace.root to a project-specific path\n", root, reason)
		fatalExit(1)
	}

	if len(identifiers) == 0 {
		// Remove all entries under workspace.root.
		entries, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Printf("itervox clear: workspace root %s does not exist — nothing to clear\n", root)
				return
			}
			fmt.Fprintf(os.Stderr, "itervox clear: read dir %s: %v\n", root, err)
			fatalExit(1)
		}
		removed := 0
		for _, e := range entries {
			path := filepath.Join(root, e.Name())
			if err := os.RemoveAll(path); err != nil {
				fmt.Fprintf(os.Stderr, "itervox clear: remove %s: %v\n", path, err)
			} else {
				fmt.Printf("  removed %s\n", path)
				removed++
			}
		}
		fmt.Printf("itervox clear: removed %d workspace(s) from %s\n", removed, root)
		return
	}

	// Remove only the specified identifiers.
	wm := workspace.NewManager(cfg)
	for _, id := range identifiers {
		path := workspace.WorkspacePath(root, id)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			fmt.Printf("  skip %s (not found)\n", path)
			continue
		}
		if err := wm.RemoveWorkspace(context.Background(), id, ""); err != nil {
			fmt.Fprintf(os.Stderr, "itervox clear: remove %s: %v\n", path, err)
		} else {
			fmt.Printf("  removed %s\n", path)
		}
	}
}

func runAction(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "itervox action: expected subcommand: comment | create-issue | move-state | provide-input")
		fatalExit(1)
	}

	daemonURL := strings.TrimRight(os.Getenv("ITERVOX_DAEMON_URL"), "/")
	token := os.Getenv("ITERVOX_ACTION_TOKEN")
	identifier := os.Getenv("ITERVOX_ISSUE_IDENTIFIER")
	if daemonURL == "" || token == "" || identifier == "" {
		fmt.Fprintln(os.Stderr, "itervox action: missing worker action environment; this command only works inside an active itervox worker")
		fatalExit(2)
	}

	var endpoint string
	var body any

	switch args[0] {
	case "comment":
		fs := flag.NewFlagSet("action comment", flag.ExitOnError)
		commentBody := fs.String("body", "", "tracker comment body")
		_ = fs.Parse(args[1:])
		if strings.TrimSpace(*commentBody) == "" {
			fmt.Fprintln(os.Stderr, "itervox action comment: --body is required")
			fatalExit(2)
		}
		endpoint = "/api/v1/agent-actions/" + url.PathEscape(identifier) + "/comment"
		body = map[string]string{"body": *commentBody}
	case "create-issue":
		fs := flag.NewFlagSet("action create-issue", flag.ExitOnError)
		title := fs.String("title", "", "title for the follow-up issue")
		issueBody := fs.String("body", "", "body/description for the follow-up issue")
		_ = fs.Parse(args[1:])
		if strings.TrimSpace(*title) == "" {
			fmt.Fprintln(os.Stderr, "itervox action create-issue: --title is required")
			fatalExit(2)
		}
		if strings.TrimSpace(os.Getenv("ITERVOX_CREATE_ISSUE_STATE")) == "" {
			fmt.Fprintln(os.Stderr, "itervox action create-issue: create_issue_state is not configured for this profile")
			fatalExit(2)
		}
		endpoint = "/api/v1/agent-actions/" + url.PathEscape(identifier) + "/create-issue"
		body = map[string]string{"title": *title, "body": *issueBody}
	case "move-state":
		fs := flag.NewFlagSet("action move-state", flag.ExitOnError)
		state := fs.String("state", "", "target tracker state")
		_ = fs.Parse(args[1:])
		if strings.TrimSpace(*state) == "" {
			fmt.Fprintln(os.Stderr, "itervox action move-state: --state is required")
			fatalExit(2)
		}
		endpoint = "/api/v1/agent-actions/" + url.PathEscape(identifier) + "/move-state"
		body = map[string]string{"state": *state}
	case "provide-input":
		fs := flag.NewFlagSet("action provide-input", flag.ExitOnError)
		message := fs.String("message", "", "input message to resume the blocked run")
		_ = fs.Parse(args[1:])
		if strings.TrimSpace(*message) == "" {
			fmt.Fprintln(os.Stderr, "itervox action provide-input: --message is required")
			fatalExit(2)
		}
		endpoint = "/api/v1/agent-actions/" + url.PathEscape(identifier) + "/provide-input"
		body = map[string]string{"message": *message}
	default:
		fmt.Fprintf(os.Stderr, "itervox action: unknown subcommand %q\n", args[0])
		fatalExit(1)
	}

	if err := invokeAgentAction(daemonURL+endpoint, token, body); err != nil {
		fmt.Fprintf(os.Stderr, "itervox action: %v\n", err)
		fatalExit(1)
	}
	fmt.Println("ok")
}

func invokeAgentAction(endpoint, token string, body any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(bodyBytes))
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("%s: %s", resp.Status, msg)
	}
	return nil
}

// updateWorkflowProjectSlug rewrites the project_slug line in the YAML frontmatter
// of the given WORKFLOW.md path. If slugs is nil or empty, the line is commented out.
// T-55: returns an error so callers can decide whether to surface a persistence
// failure to the user (the in-memory filter is applied regardless of write
// outcome, but a silent disk-write failure used to leave the next reload with
// the old value while the UI claimed "saved").
func updateWorkflowProjectSlug(path string, slugs []string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("project_slug: read %s: %w", path, err)
	}
	lines := strings.Split(string(data), "\n")
	inFrontmatter := false
	fmCount := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			fmCount++
			if fmCount == 1 {
				inFrontmatter = true
				continue
			}
			break // second --- ends frontmatter
		}
		if !inFrontmatter {
			continue
		}
		// Match both commented and uncommented project_slug lines.
		stripped := strings.TrimLeft(line, " #")
		if !strings.HasPrefix(stripped, "project_slug:") {
			continue
		}
		// Determine indentation (spaces before # or p).
		indent := strings.Repeat(" ", len(line)-len(strings.TrimLeft(line, " ")))
		if len(slugs) == 0 {
			lines[i] = indent + "# project_slug:  # Optional — select interactively via TUI (p) or web dashboard"
		} else {
			lines[i] = indent + "project_slug: " + strings.Join(slugs, ", ")
		}
		break
	}
	if err := atomicfs.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return fmt.Errorf("project_slug: write %s: %w", path, err)
	}
	return nil
}

func agentActionBaseURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// listenWithFallback tries to listen on the given host:port. If the port is
// already in use, it tries up to maxPortRetries successive ports. Returns the
// listener and the actual address it bound to.
func listenWithFallback(host string, port, maxPortRetries int) (net.Listener, string, error) {
	for i := 0; i <= maxPortRetries; i++ {
		tryPort := port + i
		addr := fmt.Sprintf("%s:%d", host, tryPort)
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			if i > 0 {
				slog.Warn("server: configured port in use, using next available",
					"configured_port", port, "actual_port", tryPort)
			}
			return ln, addr, nil
		}
		if !isAddrInUse(err) {
			return nil, "", fmt.Errorf("http listen %s: %w", addr, err)
		}
	}
	return nil, "", fmt.Errorf("ports %d–%d all in use — is another itervox instance running?",
		port, port+maxPortRetries)
}

// serveOnListener starts an HTTP server on an already-bound listener and
// returns a channel that receives its exit error.
func serveOnListener(ctx context.Context, ln net.Listener, addr string, handler http.Handler) <-chan error {
	errCh := make(chan error, 1)

	srv := &http.Server{
		Addr:        addr,
		Handler:     handler,
		ReadTimeout: 5 * time.Second,
		// WriteTimeout is intentionally 0 (no deadline) so the SSE /api/v1/events
		// endpoint can stream indefinitely. Per-route write timeouts should use
		// http.TimeoutHandler for non-SSE handlers if needed in future.
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			errCh <- err
		} else {
			errCh <- nil
		}
	}()

	return errCh
}

// isAddrInUse reports whether err indicates the address is already in use.
func isAddrInUse(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		var sysErr *os.SyscallError
		if errors.As(opErr.Err, &sysErr) {
			return sysErr.Err == syscall.EADDRINUSE
		}
	}
	return false
}
