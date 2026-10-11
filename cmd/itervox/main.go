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
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/charmbracelet/x/term"
	"github.com/joho/godotenv"
	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agentactions"
	"github.com/vnovick/itervox/internal/app"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/depsanalysis"
	"github.com/vnovick/itervox/internal/gitexec"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/outbox"
	"github.com/vnovick/itervox/internal/server"
	"github.com/vnovick/itervox/internal/statusui"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/github"
	"github.com/vnovick/itervox/internal/tracker/linear"
	"github.com/vnovick/itervox/internal/workflow"
	"github.com/vnovick/itervox/internal/workspace"
	"gopkg.in/natefinch/lumberjack.v2"
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
  quickstart  From a repository to a running board in one command:
          detect the tracker and agent CLI, write (or migrate) WORKFLOW.md,
          set up the tracker token and GitHub labels (asks first), run
          doctor, start the daemon in the background and print the URL.
             --tracker  github|linear  (default: detected)
             --runner   claude|codex   (default: detected)
             --dir      repository directory (default: .)
             --workflow workflow path (default: <dir>/WORKFLOW.md)
             --yes, -y  answer yes to every confirmation
             --no-start run the checks only; do not start the daemon
             --ready-timeout  how long to wait for the daemon (default: 90s)

  init    Scan a repository and generate a WORKFLOW.md starter file,
          or migrate an existing one with --update.
             --tracker  linear|github  (required for new workflows)
             --runner   claude|codex    (default: claude)
             --output   output file path (default: WORKFLOW.md)
             --dir      directory to scan (default: .)
             --force    overwrite existing output file
             --update   migrate an existing WORKFLOW.md to the latest
                        schema (writes WORKFLOW.md.bak, extracts inline
                        profile prompts to .itervox/agents/<name>/
                        INSTRUCTIONS.md, seeds SOUL.md, stamps
                        itervox_schema_version)
             --workflow path to the WORKFLOW.md to migrate when using
                        --update (default: WORKFLOW.md)

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
		// No slug to namespace by — fall back to a directory derived from the
		// workflow's own path rather than the shared base.
		//
		// The shared base was a cross-project state collision: this directory
		// holds automation_queue.json, which carries the DEPENDENCY AUDIT, so
		// every project without a project_slug read and wrote one another's
		// audit rows. Observed live — a Linear project inherited ten
		// `demo-id-*` rows written by an unrelated `kind: memory` run, then
		// asked Linear about issues that had never existed there on every
		// refresh cycle.
		return filepath.Join(base, "workflow", config.WorkspaceProjectKey(workflowPath))
	}
	// Encode the slug so it is safe as a directory name component, and append
	// the workflow key.
	//
	// The slug ALONE is not a project identity: two checkouts of one repo (a
	// git worktree dev setup), or several repos driven by one Linear project,
	// share a slug and would share this directory — which holds
	// automation_queue.json and therefore the dependency audit, plus
	// history.json, paused.json and input_required.json. The PID guard does
	// not stop it: it keys on the workflow's directory, so both checkouts pass.
	//
	// The slug stays in the path so the directory remains recognisable to a
	// human browsing ~/.itervox/logs.
	safe := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_").Replace(cfg.Tracker.ProjectSlug)
	return filepath.Join(base, cfg.Tracker.Kind, safe+"-"+config.WorkspaceProjectKey(workflowPath))
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

// shouldGenerateToken decides whether the daemon should auto-generate an
// ephemeral API token before binding. It is deliberately host-independent
// (see #48): bind address is not a security boundary — a loopback daemon
// sitting behind a tunnel or reverse proxy is exactly as exposed to the
// public internet as one bound to a non-loopback address, and the daemon
// has no way to detect that from inside the process. So generation is now
// gated purely on "is there already a token" and "did the operator
// explicitly opt out", regardless of what cfg.Server.Host is.
func shouldGenerateToken(allowUnauthenticated bool, envToken string) bool {
	if envToken != "" {
		return false
	}
	return !allowUnauthenticated
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
	// CORE-104: the Codex and Claude Code subscription credentials.
	"OPENAI_API_KEY",
	"CLAUDE_CODE_OAUTH_TOKEN",
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

// bootCrash is the crash left by the previous run, detected once by
// armCrashOutput in main() before any run() generation starts (so it is
// written before any reader exists and read-only afterwards). Every run()
// generation of this boot reports it in HEARTBEAT.md (CORE-007).
var bootCrash crashReport

// noPrintToken is --no-print-token, set once in main() from flag.Parse before
// any run() generation starts and read-only afterwards (same lifecycle as
// bootCrash). It suppresses the tokenised dashboard URL on stderr in every
// mode (CORE-012); see announceDashboardToken.
var noPrintToken bool

func main() {
	// TTY recovery safety net (T-12) for panics on the MAIN goroutine only.
	// recover() cannot see a panic on any other goroutine: those are handled
	// where they start — orchestrator.RecoverGoroutine for one-shot tasks and
	// failFastOnPanic (which restores the terminal before re-panicking) for
	// long-lived loops, enforced by `make no-bare-go` (CORE-008) — and every
	// unrecovered crash dump is also written to <logs-dir>/crash.log by
	// debug.SetCrashOutput (armCrashOutput, CORE-007). See
	// internal/statusui/statusui.go for the cooked-mode restoration the TUI
	// does on its own clean exit.
	defer func() {
		if r := recover(); r != nil {
			if term.IsTerminal(os.Stdin.Fd()) {
				_ = exec.Command("stty", "sane").Run()
			}
			panic(r) // re-raise so the stack trace surfaces.
		}
	}()

	// M4-close D8: redacting, format-aware stderr logging from the first
	// line, until bootLogHandlers installs the full stderr+file handler.
	slog.SetDefault(slog.New(earlyLogHandler(os.Stderr, os.Args, os.Getenv("ITERVOX_LOG_FORMAT"), !statusui.TerminalAvailable())))
	loadDotEnv() // must run before config.LoadConfig / os.Getenv calls
	// Drop GIT_DIR / GIT_WORK_TREE / … that git exports into hooks (and that
	// a .env could carry): itervox may be started from a git hook, and every
	// subprocess it spawns — git, gh, hooks, agent CLIs — must act on the
	// repository its working directory names, never the enclosing one.
	// gitexec.Command scrubs per call as well; this covers every other spawn.
	gitexec.UnsetInProcess()
	// Register a pre-set ITERVOX_API_TOKEN (from the real environment or
	// loaded above via .itervox/.env) for exact-value log redaction. See
	// logging.RegisterSecret's doc comment: agent subprocesses inherit this
	// env var, and its bare-hex shape doesn't match any pattern-based
	// redaction rule. No-ops when unset (loadDotEnv leaves it empty in that
	// case) — the auto-generated-token path below registers separately once
	// a value actually exists.
	logging.RegisterSecret(os.Getenv("ITERVOX_API_TOKEN"))
	setItervoxBinEnv()
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
		case "doctor":
			runDoctor(os.Args[2:])
			return
		case "quickstart":
			runQuickstart(os.Args[2:])
			return
		case "models":
			runModels(os.Args[2:])
			return
		case "deps":
			runDeps(os.Args[2:])
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
	logsDir := flag.String("logs-dir", "", "directory for rotating log files (default: a per-project dir under ~/.itervox/logs)")
	verbose := flag.Bool("verbose", false, "enable DEBUG-level logging (includes Claude output)")
	shutdownGrace := flag.Duration("shutdown-grace", 30*time.Second, "how long a drain waits for in-flight agent turns on SIGINT/SIGTERM (or before applying an operator edit to WORKFLOW.md) before cancelling them; a second signal stops at once")
	logFormatFlag := flag.String("log-format", "", "log format: text|json for the log file, and for stderr when no terminal is attached (systemd, containers); with a terminal stderr stays text (default: text; env: ITERVOX_LOG_FORMAT; flag wins over env)")
	flag.BoolVar(&noPrintToken, "no-print-token", false, "never print the tokenised dashboard URL on stderr (by default it is printed only when stderr is a terminal; ITERVOX_PRINT_TOKEN=1 forces it; an auto-generated token is always written to <logs-dir>/api-token, mode 0600)")
	flag.Parse()

	logLevel := slog.LevelInfo
	if *verbose {
		logLevel = slog.LevelDebug
	}

	// Resolve the file-sink log format. requestedLogFormat mirrors the
	// picked-before-validation value so we can warn exactly once when it
	// falls back to text because of an unrecognized value — resolveLogFormat
	// itself is pure and silently normalizes, so the warn lives here instead.
	// envLogFormat is captured once (wave-1 polish nit) rather than calling
	// os.Getenv("ITERVOX_LOG_FORMAT") twice for what must be the same value
	// within a single process invocation.
	envLogFormat := os.Getenv("ITERVOX_LOG_FORMAT")
	requestedLogFormat := *logFormatFlag
	if requestedLogFormat == "" {
		requestedLogFormat = envLogFormat
	}
	logFormat := resolveLogFormat(*logFormatFlag, envLogFormat)
	if requestedLogFormat != "" && requestedLogFormat != logFormat {
		slog.Warn("invalid --log-format/ITERVOX_LOG_FORMAT value, falling back to text",
			"value", requestedLogFormat, "valid_values", "text, json")
	}

	// Resolve the logs directory.  When --logs-dir is not set we derive a
	// per-project path under ~/.itervox/logs/<kind>/<slug> so that logs are
	// co-located with workspaces and automatically scoped to the project.
	// We do a lightweight early config read solely to get the tracker kind and
	// project slug; failures are non-fatal and fall back to a per-project default.
	resolvedLogsDir := *logsDir
	if resolvedLogsDir == "" {
		resolvedLogsDir = defaultLogsDir(*workflowPath)
	}

	// Refuse a second daemon BEFORE touching any shared state.
	//
	// This check used to run after the logs directory was created and the
	// rotating file handler installed, so a daemon that was about to be
	// rejected had already written to the log file the live daemon owns —
	// interleaving two daemons' output in one file and rotating it out from
	// under the running one. Nothing below this point may precede the guard.
	if pid, recorded, pidPath, err := claimPIDFile(*workflowPath); err != nil {
		// Deliberately NOT writeStartupErrorMarker: that writes
		// STARTUP_ERROR.md into the directory the LIVE daemon owns, and the
		// heartbeat writer reads it to report "Daemon: degraded". A refused
		// second start would therefore flip a perfectly healthy daemon to
		// degraded with someone else's error, until an operator ran
		// `itervox doctor --clear-startup-error`.
		//
		// This is also not a startup CONFIG error — the config is fine; the
		// operator simply started twice. stderr is the right and only channel.
		fmt.Fprintln(os.Stderr, err.Error())
		_ = pid
		_ = recorded
		_ = pidPath
		fatalExit(1)
	}
	// The claim above WROTE the pid file, so this process now owns it from
	// here rather than from the later write. Every exit path between this
	// point and the fuller cleanup block below must release it, or a refused
	// start would strand a claim no live process backs (issue #64).
	defer removePIDFile(*workflowPath)
	onFatalExit = func() { removePIDFile(*workflowPath) }

	// Tee logs to stderr and a rotating file under <logs-dir>/itervox.log.
	if err := os.MkdirAll(resolvedLogsDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create logs dir %s: %v\n", resolvedLogsDir, err)
		fatalExit(1)
	}
	// CORE-114: retention is operator-tunable through the environment:
	//   ITERVOX_LOG_MAX_SIZE_MB   rotate at this size (default 10)
	//   ITERVOX_LOG_MAX_BACKUPS   rotated files kept (default 5)
	//   ITERVOX_LOG_MAX_AGE_DAYS  delete rotated files older than this (default 0 = off)
	logRetention, retentionErr := daemonLogRetentionFromEnv(os.Getenv)
	if retentionErr != nil {
		// Logging is not set up yet: stderr is the only channel (M6-close V2).
		fmt.Fprintf(os.Stderr, "itervox: %v\n", retentionErr)
		fatalExit(1)
	}
	rotatingFile := &lumberjack.Logger{
		Filename:   filepath.Join(resolvedLogsDir, "itervox.log"),
		MaxSize:    logRetention.MaxSizeMB,
		MaxBackups: logRetention.MaxBackups,
		MaxAge:     logRetention.MaxAgeDays,
		Compress:   true,
	}
	// CORE-059: with no terminal attached (systemd, containers) stderr uses
	// the resolved --log-format, so `json` yields one JSON object per slog
	// record on stderr too; with a terminal it stays human (charmlog) text.
	// Both sinks sit behind the RedactingHandler (T-29 / F-NEW-A): any
	// string attr or msg matching a known secret pattern (Bearer tokens,
	// lin_api_*, ghp_*, …) is rewritten to "***" before reaching either.
	bootHandler, stderrHandler := bootLogHandlers(os.Stderr, rotatingFile, logLevel, logFormat, !statusui.TerminalAvailable())
	slog.SetDefault(slog.New(bootHandler))
	// stderrOnly bypasses the rotating-file sink. Use it for any record that
	// must NEVER hit disk — e.g. the dashboard URL that intentionally carries
	// the bearer token for copy/paste once at startup (and only when stderr
	// is a terminal or ITERVOX_PRINT_TOKEN=1 — see announceDashboardToken,
	// CORE-012). NOT wrapped in
	// RedactingHandler because that one emit is the explicit secret-display
	// path; redacting it would defeat the purpose of showing the URL to the
	// operator. Every other slog default goes through the redacting wrapper.
	stderrOnly := slog.New(stderrHandler)
	slog.Info("itervox starting", "version", version, "commit", commit, "date", date)
	slog.Info("logging to file", "path", rotatingFile.Filename,
		"max_size_mb", logRetention.MaxSizeMB, "max_backups", logRetention.MaxBackups, "max_age_days", logRetention.MaxAgeDays)
	// CORE-007: capture crash dumps from every goroutine in crash.log and
	// report a crash left by the previous run once, in HEARTBEAT.md.
	// The report is kept on the error path too: prepareCrashLog has already
	// moved the marker by the time arming can fail, so dropping it here
	// would lose the previous run's crash for good (M0-close fix-G).
	report, err := armCrashOutput(resolvedLogsDir)
	if err != nil {
		slog.Warn("crash: crash output not armed", "error", err)
	}
	bootCrash = report

	// Top-level context: cancelled on first SIGINT/SIGTERM to begin graceful drain.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	// CORE-042: reap the process groups a kill -9'd previous daemon left
	// behind (the pid-file claim above makes us the only owner), then record
	// this daemon's local agent/hook groups for the next start.
	pgidLedger := startAgentPGIDLedger(*workflowPath)
	go func() {
		defer failFastOnPanic("agent-pgid-ledger")
		pgidLedger.Run(ctx)
	}()

	// Refuse to start when a previous daemon is still running for this
	// workflow. Without this guard a second daemon would silently stomp the
	// first's HEARTBEAT.md, automation_queue.json, and per-issue logs,
	// producing the symptom triad: "stale HEARTBEAT.md", "lost queue
	// entries", "dashboard URL points at a daemon I can't find".

	// Write a per-project PID file so `itervox stop` can find and terminate
	// this daemon. Cleaned up on graceful shutdown (see defer below).
	// The pid file was already written by claimPIDFile's atomic claim above;
	// re-writing it here would reintroduce the unconditional overwrite that
	// made the guard racy. This block now only registers cleanup.
	if pidPath, err := pidFilePath(*workflowPath); err != nil {
		slog.Warn("itervox: could not resolve PID file path", "error", err)
	} else {
		slog.Info("itervox: holding PID file claim", "path", pidPath, "pid", os.Getpid())
		// Clean shutdown removes pidfile, dashboard_url, and HEARTBEAT.md
		// together so a future operator never sees stale state from this
		// run. Doctor / `itervox status` rely on these files being either
		// fresh or absent to give accurate "is it alive" verdicts.
		defer removePIDFile(*workflowPath)
		defer removeDashboardURLFile(*workflowPath)
		defer removeHeartbeatFile(*workflowPath)
		// os.Exit skips the defers above, so every fatalExit path — a failed
		// rebind, an invalid config on the first iteration — used to leave
		// these files behind and make doctor report a daemon that had died.
		// Same cleanup, reachable from fatalExit. See exit.go's onFatalExit.
		onFatalExit = func() {
			removePIDFile(*workflowPath)
			removeDashboardURLFile(*workflowPath)
			removeHeartbeatFile(*workflowPath)
		}
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
	// The HTTP socket outlives run(): it is bound once here and handed to each
	// run() as a per-generation view, so a config reload keeps the port and
	// never races EADDRINUSE against the previous server's shutdown. Rebinding
	// happens only when a reload actually changes server.host/server.port.
	var srvPersist *persistentListener
	var srvAddr string
	for {
		// M1-close C2: load under the settings lock and advance the settings
		// generation in the same critical section, so a settings save still
		// in flight from the previous generation either lands before this
		// load reads the file or is refused (503, nothing written) after it.
		loaded, _, err := loadSettingsGeneration(*workflowPath)
		if err == nil {
			err = config.ValidateDispatch(loaded)
		}
		if err != nil {
			if firstIter {
				slog.Error("startup: config invalid", "path", *workflowPath, "error", err)
				writeStartupErrorMarker(*workflowPath, err)
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
			// CORE-057: no generation runs here, so nothing to drain — but the
			// signal must still be honoured (a bare Sleep ignored SIGTERM for
			// as long as the file stayed invalid).
			select {
			case <-time.After(wait):
			case sig := <-sigCh:
				slog.Info("shutdown: signal while WORKFLOW.md is invalid; no generation is running, exiting", "signal", sig)
				return
			}
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

		// Bind — or rebind, when a reload changed server.host/server.port —
		// the shared HTTP socket. A bind failure is fatal on the first
		// iteration and on reload alike: the operator must free the port or
		// change `server.port`; retrying every second would just spam.
		host := cfg.Server.Host
		port := 0
		if cfg.Server.Port != nil {
			port = *cfg.Server.Port
		}
		// Rebind only when the RESOLVED socket actually changes. Comparing
		// the literal "host:port" treated `127.0.0.1` -> `localhost` as a
		// move, even though it is the same socket.
		if srvPersist == nil || !sameBindTarget(srvAddr, host, port) {
			// Release the old socket BEFORE binding the new one. The previous
			// order (bind, then close) assumed a changed key implied a
			// different socket, so the two could never collide; the alias
			// case above disproves that, and the daemon hit EADDRINUSE
			// against its own listener and called fatalExit -> os.Exit,
			// skipping the deferred cleanup of daemon.pid / dashboard_url /
			// HEARTBEAT.md and leaving `itervox doctor` reporting a live
			// daemon that had died. Closing first cannot regress the failure
			// path: a failed bind was already fatal either way.
			if srvPersist != nil {
				_ = srvPersist.Close()
				srvPersist = nil
			}
			raw, addr, err := listenStrict(host, port)
			if err != nil {
				fmt.Fprintln(os.Stderr, err.Error())
				fmt.Fprintln(os.Stderr,
					"hint: to run two itervox daemons in parallel, set a distinct `server.port` per repo in WORKFLOW.md, or `server.port: 0` to let the OS pick a free port.")
				writeStartupErrorMarker(*workflowPath, err)
				fatalExit(1)
			}
			srvPersist = newPersistentListener(raw)
			srvAddr = addr
			// CORE-058: say which source chose the bind (env vars beat WORKFLOW.md).
			slog.Info("HTTP server listening", "addr", addr,
				"host_source", cfg.Server.HostSource, "port_source", cfg.Server.PortSource)
		}

		runCtx, runCancel := context.WithCancel(ctx)
		// CORE-057: one drain control per generation; reloadReq carries an
		// operator edit (self-writes are suppressed by CORE-116) to the loop
		// below, which drains before reloading instead of cancelling turns.
		dc := newDrainControl()
		reloadReq := make(chan struct{}, 1)

		// Watch WORKFLOW.md; request a drain-then-reload on change.
		go func() {
			defer failFastOnPanic("workflow-watcher")
			// Baseline = the bytes cfg was parsed from, not the watcher's own
			// first reading: an edit between the load above and this start
			// still reloads (M1-close C2).
			onChange := func() {
				select {
				case reloadReq <- struct{}{}:
				default: // a reload is already pending; it re-reads the file
				}
			}
			if err := workflow.WatchFrom(runCtx, *workflowPath, cfg.WorkflowHash, onChange); err != nil && runCtx.Err() == nil {
				slog.Warn("workflow watcher stopped", "error", err)
			}
		}()

		runDone := make(chan error, 1)
		go func() {
			defer failFastOnPanic("run-generation")
			runDone <- run(runCtx, cancel, cfg, *workflowPath, rotatingFile.Filename, rotatingFile, logLevel, logFormat, stderrOnly, srvPersist.generation(), srvAddr, dc)
		}()

		var runErr error
		// Wait for run to finish, an operator edit, or a signal.
		select {
		case err := <-runDone:
			runCancel()
			if ctx.Err() != nil {
				return // top-level shutdown already in progress
			}
			runErr = err
		case <-reloadReq:
			var shutdown bool
			runErr, shutdown = reloadDrain(ctx, dc, *shutdownGrace, sigCh, runDone, runCancel, cancel)
			if shutdown {
				return
			}
		case sig := <-sigCh:
			shutdownDrain(sig, dc, *shutdownGrace, sigCh, runDone, cancel)
			return
		}

		if ctx.Err() != nil {
			return // top-level shutdown
		}

		// Bind failures — the one class of error the retry loop must never
		// spin on — are handled fatally at bind time above, before run() is
		// even started. Everything run() can return is either a clean reload
		// or something the loop can patiently re-attempt: the loop re-reads
		// WORKFLOW.md each iteration, so operator fixes take effect.
		reloadMsg, reloadDelay := reloadPlanForRunExit(runErr)
		// Real run errors WARN; a clean reload (nil or wrapped context.Canceled)
		// is Debug-level — matches internal/workflow/watcher.go's "file changed"
		// signal. Promoting it to Info would spam stderr on every save.
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			slog.Warn(reloadMsg, "error", runErr, "delay", reloadDelay.String())
		} else {
			slog.Debug(reloadMsg)
		}
		if waitBeforeReload(ctx, reloadDelay, sigCh) {
			cancel()
			return
		}
	}
}

// run starts the orchestrator (and optionally the HTTP server) and blocks until
// runCtx is cancelled. logFile is passed to the HTTP server for the /api/v1/logs endpoint.
// fileWriter is the rotating log file writer; logLevel is the configured log level;
// logFormat ("text" or "json", see resolveLogFormat) selects the file sink's
// slog.Handler via newFileLogHandler. All three are used to redirect slog away
// from stderr once the TUI takes the terminal.
//
// srvListener is a per-run view of the daemon's shared HTTP socket (see
// persistentListener) and actualAddr its bound host:port. run() does not bind:
// the socket outlives it, so reloads keep the port. run() must not return
// while its HTTP server could still accept — the next run() serves on the
// same socket.
func run(ctx context.Context, quitApp func(), cfg *config.Config, workflowPath string, logFile string, fileWriter io.Writer, logLevel slog.Level, logFormat string, stderrOnly *slog.Logger, srvListener net.Listener, actualAddr string, dc *drainControl) error {
	// The settings generation this run's config was loaded under (the main
	// loop's loadSettingsGeneration bumped it just before calling run, and
	// nothing bumps it again until run has returned). Saves made by this
	// generation's adapter and TUI are refused once a reload supersedes it.
	settingsGen := currentSettingsGeneration(workflowPath)
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

	// T-32: apply SSH StrictHostKeyChecking config. run() executes once per
	// generation, i.e. at startup AND on every WORKFLOW.md reload.
	applySSHStrictHostConfig(cfg)

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
	// M4-close BH-M4-2: keep the worktrees of reviews a drain refused.
	pendingReviewsPath := ""
	if logFile != "" {
		pendingReviewsPath = filepath.Join(filepath.Dir(logFile), "pending_reviews.json")
	}
	keepForReview := orchestrator.LoadPendingReviewIdentifiers(pendingReviewsPath)
	cleanupWait := orchestrator.StartupTerminalCleanup(ctx, tr, cfg.Tracker.TerminalStates, func(id string) error {
		if _, keep := keepForReview[id]; keep {
			slog.Info("startup cleanup: keeping workspace for a pending review", "identifier", id)
			return nil
		}
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
	orch.SetDepsSidecarPath(depsanalysis.SidecarPath(filepath.Dir(workflowPath)))
	// unified-dependency-graph Task 6 — operator deps-override dismissals live
	// under the project's .itervox/ dir (not logDir) so they persist alongside
	// the sidecar they dismiss edges from, independent of --log placement.
	orch.SetDepsOverridesFile(filepath.Join(filepath.Dir(workflowPath), ".itervox", "deps_overrides.json"))

	// outbox Task 3 — write-ahead outbox for tracker state transitions and
	// comments (docs/superpowers/specs/2026-08-06-write-ahead-outbox-design.md).
	// outbox.New never fails the daemon: a missing or corrupt outbox.json
	// starts an empty in-memory outbox and logs a Warn (see its doc
	// comment) rather than blocking startup, same posture as
	// deps_overrides.json above. cfg.Tracker.Outbox (default true,
	// load-time only, not cfgMu-guarded) is the kill switch: when false,
	// the orchestrator keeps its New()-constructed directWriteSink and no
	// flusher goroutine starts, so behavior is byte-identical to
	// pre-outbox itervox — the outbox handle is still constructed (cheap,
	// and lets an operator flip the flag on a later reload without a
	// restart... though today the flag is load-time-only, so a reload
	// still requires a restart to take effect) but never wired in.
	ob, err := outbox.New(filepath.Join(filepath.Dir(workflowPath), ".itervox", "outbox.json"))
	if err != nil {
		return fmt.Errorf("build outbox: %w", err)
	}
	if cfg.Tracker.Outbox {
		orch.SetWriteSink(orchestrator.NewOutboxWriteSink(ob))
		orch.SetOutbox(ob)
	}

	var agentSessionsDir string
	if logFile != "" {
		logDir := filepath.Dir(logFile)
		orch.SetHistoryFile(filepath.Join(logDir, "history.json"))
		orch.SetPausedFile(filepath.Join(logDir, "paused.json"))
		orch.SetInputRequiredFile(filepath.Join(logDir, "input_required.json"))
		orch.SetAutomationQueueFile(filepath.Join(logDir, "automation_queue.json"))
		// Gap §5.3 — persist rate_limited auto-switch overrides so a daemon
		// crash mid-flight doesn't lose them and re-dispatch under the
		// original (rate-limited) profile.
		orch.SetAutoSwitchedFile(filepath.Join(logDir, "auto_switched.json"))
		// CORE-053 — the backend circuit breakers survive a restart.
		orch.SetBackendHealthFile(filepath.Join(logDir, "backend_health.json"))
		orch.SetPendingReviewsFile(pendingReviewsPath) // M4-close BH-M4-2
		agentSessionsDir = filepath.Join(logDir, "sessions")
		orch.SetAgentLogDir(agentSessionsDir)
	}
	if cfg.Tracker.Kind != "" && cfg.Tracker.ProjectSlug != "" {
		orch.SetHistoryKey(cfg.Tracker.Kind + ":" + cfg.Tracker.ProjectSlug)
	}

	appSessionID := newAppSessionID()
	orch.SetAppSessionID(appSessionID)

	// Phase 1.3 advisory — when deps_analyzer_profile is set but no sidecar
	// has been written, surface a single info-level line so operators
	// understand why the Deps tab shows tracker-only relations. The
	// dashboard's "Analyze dependencies" button (or `itervox deps analyze`
	// from the shell) closes the gap.
	advertiseMissingDepsSidecar(cfg, workflowPath)

	// depsSvc is constructed here — before buildSnapFunc, not after — because
	// runner/orch/agentSessionsDir are all already available (set above) and
	// newDepsAnalyzerService starts no goroutines of its own (work begins
	// lazily on EnqueueAnalysis); there is no real construction-order
	// problem to solve, so depsSvc is passed to buildSnapFunc as a plain,
	// always-non-nil parameter rather than threaded in via a second-phase
	// bind. bindDepsNotify is the one piece that genuinely needs two-phase
	// wiring — srv.Notify doesn't exist until `server.New` runs inside the
	// `srvListener != nil` block below — so it stays called from there.
	depsSvc, bindDepsNotify := wireDepsAnalyzerService(ctx, orch, cfg, tr, runner, workflowPath, agentSessionsDir)

	snap := buildSnapFunc(orch, tr, cfg, appSessionID, logBuf, workflowPath, depsSvc, ob)

	// HTTP server setup. The listener was bound by main's reload loop —
	// which owns the socket across reloads — before the TUI starts, so the
	// dashboard URL for the 'w' key is already accurate.
	//
	// Explicit port from WORKFLOW.md: NEVER auto-shift on EADDRINUSE. Silent
	// shifting is the canonical "Vite proxies to 8090 but the daemon is on
	// 8091" trap — operators trust WORKFLOW.md to be the source of truth
	// for the port. If the explicit port is taken, the operator must change
	// WORKFLOW.md OR stop the conflicting process. That failure is handled
	// (loudly, fatally) at bind time in main.
	var srvDone <-chan error
	var actionTokenStore *agentactions.Store
	{
		// Persist the bound URL for Vite's dev proxy and for `itervox doctor`.
		dashboardURL := "http://" + actualAddr + "/"
		if err := writeDashboardURLFile(workflowPath, dashboardURL); err != nil {
			slog.Warn("itervox: failed to write dashboard URL file", "error", err)
		}
		// Secure-by-default, regardless of bind address (#48): if no token is
		// set and the user hasn't explicitly opted out via
		// server.allow_unauthenticated, auto-generate an ephemeral token and
		// install the bearer middleware. Bind address is NOT a signal here —
		// a loopback bind behind a tunnel/reverse proxy is exactly as
		// internet-exposed as a non-loopback bind, and the daemon can't tell
		// the difference from inside the process. Regenerated on every
		// restart unless the user pins one via env var.
		// tokenGenerated is PROCESS-scoped, not per-generation: a reload
		// finds the token this process generated still in the environment
		// and must keep treating it as generated, or announceDashboardToken
		// would delete <logs-dir>/api-token while that token stays live
		// (M0-close G4). See ensureAPIToken.
		tokenGenerated, freshlyGenerated, err := ensureAPIToken(cfg.Server.AllowUnauthenticatedLAN)
		if err != nil {
			return err
		}
		if freshlyGenerated {
			slog.Info("server: auto-generated ephemeral API token",
				"host", cfg.Server.Host,
				"hint", "set ITERVOX_API_TOKEN in .itervox/.env to pin a stable token, or set server.allow_unauthenticated: true to opt out")
		} else if cfg.Server.AllowUnauthenticatedLAN && os.Getenv("ITERVOX_API_TOKEN") == "" {
			slog.Warn("server: starting with no authentication (server.allow_unauthenticated: true) — anyone who can reach this bind address, including through a tunnel or reverse proxy, has full API access",
				"host", cfg.Server.Host)
		}
		// When a token is set (user-provided OR auto-generated above), tell the
		// operator how to reach the dashboard. The tokenised URL (AuthGate
		// captures ?token= on first load, persists it in sessionStorage, and
		// strips it via history.replaceState) goes to stderr ONLY when stderr
		// is a terminal or ITERVOX_PRINT_TOKEN=1, and never with
		// --no-print-token — under systemd/containers stderr is a log sink
		// (journald, CloudWatch, …), and anyone who can read it AND reach the
		// bind address would hold a valid bearer token (CORE-012). Headless,
		// an auto-generated token is discoverable at <logs-dir>/api-token
		// (0600); the log line carries only its path and a fingerprint.
		if tok := os.Getenv("ITERVOX_API_TOKEN"); tok != "" {
			announceDashboardToken(stderrOnly, slog.Default(), dashboardTokenAnnounce{
				Addr:        actualAddr,
				Token:       tok,
				Generated:   tokenGenerated,
				LogsDir:     filepath.Dir(logFile),
				NoPrintFlag: noPrintToken,
				PrintEnv:    os.Getenv(printTokenEnv),
				StderrIsTTY: term.IsTerminal(os.Stderr.Fd()),
			})
		}
	}
	if actualAddr != "" {
		actionTokenStore = agentactions.NewStore()
		orch.SetAgentActionBaseURL(agentActionBaseURL(actualAddr))
		orch.SetAgentActionTokens(actionTokenStore)
		// v0.2.0 audit P1-3 — periodic janitor for expired action grants.
		// Validate() opportunistically deletes tokens it sees, but most
		// grants are issued to workers that never call the action endpoint
		// (create_issue is rare), so the map would otherwise grow forever
		// on a long-running daemon. 15-minute cadence is conservative
		// relative to the 1-hour default TTL — any expired entry is gone
		// within one cleanup interval after expiry. Goroutine self-exits
		// on ctx cancel; one trailing Cleanup pass on shutdown drains
		// what the final tick missed.
		actionStoreCleanupTicker := time.NewTicker(15 * time.Minute)
		go func() {
			defer failFastOnPanic("action-token-cleanup")
			defer actionStoreCleanupTicker.Stop()
			for {
				select {
				case <-ctx.Done():
					actionTokenStore.Cleanup(time.Now())
					return
				case t := <-actionStoreCleanupTicker.C:
					if removed := actionTokenStore.Cleanup(t); removed > 0 {
						slog.Debug("agentactions: expired grants pruned", "removed", removed)
					}
				}
			}
		}()
	}

	// Redirect slog to file-only before the TUI takes the alt-screen — but
	// only when the TUI is actually about to start. Without this redirect,
	// concurrent slog writes to stderr corrupt the bubbletea display; the TUI
	// log pane reads directly from logBuf instead of stderr, so nothing is
	// lost by going file-only. But statusui.Run refuses to start the TUI at
	// all when there is no controlling terminal (systemd, container, CI,
	// detached stdio — issue #49-2), and an unconditional redirect paid the
	// cost (stderr goes dark) for none of the benefit in that case. TTY
	// availability is decided here, before the redirect, via the same
	// TTY-ownership check statusui.Run performs internally (TerminalAvailable
	// wraps checkForegroundTTYOwnership with no retry/sleep so the two checks
	// cannot drift). Headless keeps the pre-TUI stderr+file fanout so
	// journalctl et al. keep working.
	// Wrapped in RedactingHandler (both branches, via postStartupHandler) so
	// secrets-in-msg/attrs are scrubbed before hitting ~/.itervox/logs/ and,
	// in headless mode, stderr too (T-29 / F-NEW-A).
	// newFileLogHandler keeps this in sync with the pre-TUI file handler above
	// so --log-format=json survives the redirect instead of silently reverting
	// to text once the TUI starts.
	slog.SetDefault(slog.New(postStartupHandler(fileWriter, logLevel, logFormat, stderrOnly.Handler(), statusui.TerminalAvailable())))
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

	if err := ensureItervoxGitignore(filepath.Join(filepath.Dir(workflowPath), ".itervox")); err != nil {
		slog.Warn("heartbeat: gitignore update failed", "error", err)
	}
	if err := refreshItervoxBinSymlink(filepath.Dir(workflowPath)); err != nil {
		slog.Warn("itervox: bin symlink refresh failed", "error", err)
	}
	heartbeatDashboardURL := ""
	if actualAddr != "" {
		heartbeatDashboardURL = fmt.Sprintf("http://%s/", actualAddr)
	}
	heartbeat := newHeartbeatWriter(heartbeatPath(workflowPath), heartbeatOptions{
		WorkflowPath:  workflowPath,
		SchemaVersion: cfg.SchemaVersion,
		DashboardURL:  heartbeatDashboardURL,
		LastCrash:     bootCrash,
	}, snap, heartbeatMinInterval)
	if err := heartbeat.WriteNow(time.Now().UTC()); err != nil {
		slog.Warn("heartbeat: startup write failed", "path", heartbeat.path, "error", err)
	}
	orch.OnStateChange = heartbeat.Request
	go func() {
		defer failFastOnPanic("heartbeat")
		heartbeat.Run(ctx)
	}()

	// Start serving on the already-bound listener.
	//
	// #52 deferral — startDepsAutoAnalyze (below) is wired inside this
	// `srvListener != nil` block purely because that's where depsSvc,
	// adapter, and the other server-only wiring already live, not because
	// auto-analysis has any actual dependency on the HTTP server being up.
	// srvListener is always non-nil on every code path today, so the
	// scheduler always starts — but a future headless mode (no dashboard/API
	// listener) would silently disable auto-analysis as a side effect of
	// this placement. Hoisting it out is deliberately NOT done here (v0.2.0
	// scope); this comment exists so the coupling is visible instead of
	// silently assumed.
	if srvListener != nil {
		// CORE-043: anchors /ready's startup grace for this generation.
		generationStartedAt := time.Now()
		fetchIssue := func(ctx context.Context, identifier string) (*server.TrackerIssue, error) {
			issue, err := tr.FetchIssueByIdentifier(ctx, identifier)
			if err != nil {
				return nil, err
			}
			if issue == nil {
				return nil, nil
			}
			snap := orch.Snapshot()
			ti := app.EnrichIssue(*issue, snap, time.Now(), cfg)
			ti.StatusChanges = statusChangeRows(snap.IssueStatusHistory[issue.Identifier])
			return &ti, nil
		}

		var pm server.ProjectManager
		if tpm, ok := tr.(tracker.ProjectManager); ok {
			pm = &linearProjectManager{pm: tpm, workflowPath: workflowPath, settingsGen: settingsGen}
		}

		// logFile is rotatingFile.Filename, i.e. filepath.Join(resolvedLogsDir,
		// "itervox.log") — so its directory IS the resolved logs dir, with
		// --logs-dir already applied. Deriving it here rather than widening
		// run's signature keeps the one place that resolves the path (main)
		// authoritative (issue #65).
		adapterLogsDir := ""
		if logFile != "" {
			adapterLogsDir = filepath.Dir(logFile)
		}
		adapter := &orchestratorAdapter{
			orch:         orch,
			logBuf:       logBuf,
			cfg:          cfg,
			tr:           tr,
			workflowPath: workflowPath,
			settingsGen:  settingsGen,
			logsDir:      adapterLogsDir,
			ob:           ob,
			// CORE-120: gate new operator writes on the switch, not on ob.
			outboxEnabled: cfg.Tracker.Outbox,
		}
		adapter.initSkillsCache()
		// analyzer-autonomy Task 4 — periodic unattended dependency analysis.
		// depsSvc satisfies depsAutoAnalyzeEnqueuer; a second SidecarCache
		// instance is created here (mtime-cached, so a second instance is
		// cheap) rather than reusing buildSnapFunc's — that cache is private
		// to buildSnapFunc's closure and not returned/exported.
		// wave-2 polish Task 4 / #52 — resolveProfile used to read
		// DepsAnalyzerProfileCfg() and AgentProfileCfg(name) as two separate
		// cfgMu critical sections, which could observe a torn read across a
		// concurrent profile change (self-heals next tick, but produces a
		// spurious "no profile configured" warning in the interim). Now a
		// single atomic accessor.
		startDepsAutoAnalyze(ctx, depsSvc, func() (string, bool) {
			name, p, ok := orch.ResolveDepsAnalyzerProfileCfg()
			return name, ok && config.ProfileEnabled(p)
		}, snap, depsanalysis.NewSidecarCache(depsanalysis.SidecarPath(filepath.Dir(workflowPath))), cfg,
			orch.DepsAnalysisModeCfg,
			func() []string {
				active, _, _ := orch.TrackerStatesCfg()
				return active
			})
		// CORE-045 — opt-in Prometheus endpoint, behind the bearer token.
		var metricsHandler http.Handler
		if cfg.Server.Metrics.Enabled {
			metricsHandler = newMetricsHandler(orch, ob, cfg.Tracker.Kind)
		}
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
			DepsAnalyzer:     depsSvc,
			// gaps_11 G-3 — merge_pr policy is read-only after startup, so it
			// is passed by value rather than via cfgMu-guarded accessors.
			MergeStrategy:       cfg.Agent.MergeStrategy,
			MergeBlockLabels:    cfg.Agent.MergeBlockLabels,
			AllowUncheckedMerge: cfg.Agent.AllowUncheckedMerge,
			// CORE-162 — DNS-rebinding Host guard inputs (unauthenticated
			// mode only); read-only after startup.
			BindHost:     cfg.Server.Host,
			AllowedHosts: cfg.Server.AllowedHosts,
			// CORE-043 — /api/v1/ready: event-loop atomics + the tracker
			// rate-limit gate + the reload loop's config status. No cfgMu,
			// no snapshot build.
			Readiness: func() server.ReadinessSignals {
				return readinessSignals(orch.Readiness(), generationStartedAt, cfg.Tracker.Kind, loadConfigInvalid())
			},
			Metrics: metricsHandler,
			// CORE-048 — web client error reports feed the RecentFailures
			// ring through the non-blocking RecordFailure (CORE-046).
			ReportClientError: clientErrorReporter(orch),
		})
		adapter.notify = srv.Notify
		bindDepsNotify(srv.Notify)
		if err := srv.Validate(); err != nil {
			return fmt.Errorf("server configuration error: %w", err)
		}
		orch.OnStateChange = func() {
			srv.Notify()
			heartbeat.Request()
		}
		srvDone = serveOnListener(ctx, srvListener, actualAddr, srv, srv.Shutdown)
	}

	// Forward web dashboard refresh signals to the orchestrator for an immediate re-poll.
	go func() {
		defer failFastOnPanic("refresh-forwarder")
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

	// outbox Task 3 — the flusher is the outbox's ONLY delivery path: it
	// calls the raw tracker (tr), never orch.writeSink(). Gated by the same
	// cfg.Tracker.Outbox check as the SetWriteSink/SetOutbox wiring above —
	// with the kill switch off nothing new is enqueued into ob (the
	// orchestrator kept its direct sink, and PostOperatorComment gates on
	// adapter.outboxEnabled rather than on ob being non-nil), so skipping the
	// goroutine keeps "outbox off" free of any extra background goroutine.
	// Entries left over from an earlier outbox-on run stay visible in the
	// snapshot and can be retried or dropped, but are not delivered until the
	// switch is turned back on.
	// flusherDone closes once the flusher goroutine and every absent-reconcile
	// child it spawned have returned. run() must join it before returning —
	// they write .itervox/outbox.json, and main()'s reload loop opens a second
	// handle on the same path.
	flusherDone := make(chan struct{})
	close(flusherDone)
	if cfg.Tracker.Outbox {
		flusherDone = make(chan struct{})
		go func() {
			defer close(flusherDone)
			defer failFastOnPanic("outbox-flusher-join")
			<-startOutboxFlusher(ctx, ob, tr, orch, cfg.Tracker.Kind)
		}()
	}

	// CORE-046: hand the previous generation's RecentFailures to this one,
	// and hand this one's on once its loop has exited (joinRun waits for it).
	// M6-close V3: the operator's failure acks ride with the ring they refer to.
	carriedFailures, carriedAcks := failureCarry.take()
	orch.SeedRecentFailures(carriedFailures)
	orch.SeedFailureAcks(carriedAcks)
	orchDone := make(chan error, 1)
	go func() { orchDone <- orch.Run(ctx) }()
	// CORE-057: relay main's drain request to the event loop and its
	// Drained() back to main.
	go func() {
		defer failFastOnPanic("drain-forwarder")
		dc.forwardDrain(ctx, orch)
	}()

	runErr := joinRun(orchDone, srvDone, srvListener, flusherDone, logBuf)
	final := orch.Snapshot()
	failureCarry.store(final.RecentFailures, final.FailureAcks)
	return runErr
}

// buildSnapFunc (the StateSnapshot wiring that used to live here) moved to
// snapshot_build.go — outbox Task 4 size-budget extraction.

// buildTUIConfig wires the terminal status-UI config and returns the cancel
// function (used as the 'x' key handler in statusui.Run). Extracted from run().
func buildTUIConfig(
	orch *orchestrator.Orchestrator,
	tr tracker.Tracker,
	cfg *config.Config,
	workflowPath string,
	quitApp func(),
) (statusui.Config, func(string) bool) {
	// Built once per run generation, so this is that generation's settings
	// generation (see run's settingsGen).
	settingsGen := currentSettingsGeneration(workflowPath)
	tuiCfg := statusui.Config{
		MaxAgents:     cfg.Agent.MaxConcurrentAgents,
		TodoStates:    cfg.Tracker.ActiveStates,
		BacklogStates: cfg.Tracker.BacklogStates,
		QuitApp:       quitApp,
	}
	if cfg.Server.Port != nil {
		if tok := os.Getenv("ITERVOX_API_TOKEN"); tok != "" {
			tuiCfg.DashboardURL = dashboardBaseURL(cfg.Server.Host, *cfg.Server.Port) + "?token=" + tok
		} else {
			tuiCfg.DashboardURL = dashboardBaseURL(cfg.Server.Host, *cfg.Server.Port)
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
			_ = saveProjectFilter(tpm, workflowPath, settingsGen, slugs, "tui") // logged inside
		}
	}
	tuiCfg.AdjustWorkers = func(delta int) {
		// V4: same serialized write-then-apply as the dashboard's
		// SetWorkers/BumpWorkers, so a TUI bump racing a dashboard save
		// cannot leave WORKFLOW.md and memory on different values.
		unlock, lockErr := beginSettingsSave(workflowPath, settingsGen)
		if lockErr != nil {
			slog.Warn("tui: worker change not saved; WORKFLOW.md is reloading — retry", "error", lockErr)
			return
		}
		defer unlock()
		next := max(1, min(orch.MaxWorkers()+delta, 50))
		if err := workflow.PatchIntField(workflowPath, "max_concurrent_agents", next); err != nil {
			slog.Warn("failed to persist max_concurrent_agents to WORKFLOW.md; worker count unchanged", "error", err)
			return
		}
		orch.SetMaxWorkers(next)
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
			tuiCfg.DispatchIssue = func(identifier string) error {
				dispCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				// cfg.Tracker.ActiveStates is cfgMu-guarded (runtime setter:
				// SetTrackerStatesCfg via PUT /settings/tracker/states) and this
				// closure runs at TUI keypress time, well after the HTTP server
				// starts serving — read via the getter at invocation time rather
				// than closing over a value captured at startup. BacklogStates
				// has no runtime setter, so the direct cfg read stays legal.
				active, _, _ := orch.TrackerStatesCfg()
				if len(active) == 0 {
					return fmt.Errorf("no tracker.active_states configured")
				}
				allStates := append(append([]string{}, cfg.Tracker.BacklogStates...), active...)
				issues, err := tr.FetchIssuesByStates(dispCtx, allStates)
				if err != nil {
					return err
				}
				for _, iss := range issues {
					if iss.Identifier == identifier {
						return tr.UpdateIssueState(dispCtx, iss.ID, active[0])
					}
				}
				return fmt.Errorf("issue %s not found", identifier)
			}
		}
	}
	tuiCfg.ResumeIssue = func(identifier string) bool {
		ok := orch.ResumeIssue(identifier) == nil
		if ok {
			orch.Refresh()
		}
		return ok
	}
	tuiCfg.TerminateIssue = func(identifier string) bool {
		ok := orch.TerminateIssue(identifier) == nil
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
		return orch.CancelIssue(identifier) == nil
	}
	return tuiCfg, tuiCancel
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
	permissionMode agent.PermissionMode,
) (agent.TurnResult, error) {
	resolver := r.resolve
	if resolver == nil {
		resolver = resolveAgentCommand
	}
	if workerHost == "" {
		command = resolveCommandLine(command, resolver)
	}
	return r.inner.RunTurn(ctx, log, onProgress, sessionID, prompt, workspacePath, command, workerHost, logDir, readTimeoutMs, turnTimeoutMs, permissionMode)
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

// resolveAgentCommand resolves a bare command name (e.g. "claude") to its full
// absolute path using the user's interactive login shell, which sources .zshrc
// and therefore picks up PATH additions from nvm, volta, homebrew, etc.
// If the command is already absolute, or resolution fails, the original value
// is returned unchanged.
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
	// settingsGen is the run generation this manager was built for; saves
	// from a superseded generation are refused (M1-close fence, CORE-160).
	settingsGen uint64
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
		if allowedState := strings.TrimSpace(os.Getenv("ITERVOX_MOVE_ISSUE_STATE")); allowedState != "" && strings.TrimSpace(*state) != allowedState {
			fmt.Fprintf(os.Stderr, "itervox action move-state: --state must be %q for this automation grant\n", allowedState)
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
	case "comment-pr":
		// P0-E Option A: structured-findings sibling of `comment`. Accepts
		// --summary plus optional --findings (path JSON file) and POSTs to
		// /agent-actions/{id}/comment_pr. The handler is handleAgentCommentPR
		// (internal/server/comment_pr.go).
		fs := flag.NewFlagSet("action comment-pr", flag.ExitOnError)
		summary := fs.String("summary", "", "review summary")
		findingsPath := fs.String("findings", "", "path to JSON file containing the findings array")
		_ = fs.Parse(args[1:])
		var findings []map[string]any
		if strings.TrimSpace(*findingsPath) != "" {
			raw, err := os.ReadFile(*findingsPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "itervox action comment-pr: cannot read --findings %s: %v\n", *findingsPath, err)
				fatalExit(2)
			}
			if err := json.Unmarshal(raw, &findings); err != nil {
				fmt.Fprintf(os.Stderr, "itervox action comment-pr: invalid JSON in --findings: %v\n", err)
				fatalExit(2)
			}
		}
		if strings.TrimSpace(*summary) == "" && len(findings) == 0 {
			fmt.Fprintln(os.Stderr, "itervox action comment-pr: --summary or --findings is required")
			fatalExit(2)
		}
		endpoint = "/api/v1/agent-actions/" + url.PathEscape(identifier) + "/comment_pr"
		payload := map[string]any{}
		if strings.TrimSpace(*summary) != "" {
			payload["summary"] = *summary
		}
		if len(findings) > 0 {
			payload["findings"] = findings
		}
		body = payload
	case "merge-pr":
		// P0-C: structured wrapper around the daemon's merge_pr endpoint.
		fs := flag.NewFlagSet("action merge-pr", flag.ExitOnError)
		pr := fs.Int("pr", 0, "GitHub pull request number to merge")
		strategy := fs.String("strategy", "", "merge strategy (squash|rebase|merge); empty = use daemon default")
		_ = fs.Parse(args[1:])
		if *pr <= 0 {
			fmt.Fprintln(os.Stderr, "itervox action merge-pr: --pr is required")
			fatalExit(2)
		}
		endpoint = "/api/v1/agent-actions/" + url.PathEscape(identifier) + "/merge_pr"
		body = map[string]any{"pr": *pr, "strategy": *strategy}
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
