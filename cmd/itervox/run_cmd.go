package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/logging"
	"github.com/vnovick/itervox/internal/tracker"
)

// `itervox run --issue <id>` (#252): one issue, then exit — the entry point
// for CI. It loads the same configuration as the daemon and runs the
// daemon's own orchestrator in a single-issue mode: the named issue is the
// only candidate, at most one agent runs, and the process exits once that
// run and its reviewers are done. There is no HTTP listener, hub, heartbeat,
// dashboard, automation or tracker polling beyond that one issue.
//
// Exit codes are the contract CI scripts and the GitHub Action read.
const (
	runExitOK         = 0 // PR opened, or nothing to do
	runExitFailed     = 2
	runExitNeedsInput = 3 // a question or a guardrail approval; see the summary
	runExitBudget     = 4 // agent.budget reached (#136)
	runExitConfig     = 5 // configuration or usage error
)

const runUsage = `Usage: itervox run --issue <id> [flags]

Run one issue to completion and exit (for CI): the agent's turns, the
reviewer quorum, then push the branch, open the PR, comment on the issue
and move it to tracker.completion_state. No server, hub or dashboard.

Flags:
  --issue ID         the issue to run (required; "#12" or "12" on GitHub)
  --workflow PATH    path to WORKFLOW.md (default: WORKFLOW.md)
  --worktree         work in a git worktree under .itervox/run/worktrees/
                     instead of switching the checkout to the issue branch
  --answer TEXT      continue a run that asked a question (exit 3) with this
                     answer; @FILE reads the answer from a file
  --approve CMD      allow this guardrail approval command for this run only
                     (the command an earlier run named when it exited 3)
  --timeout DUR      cap the whole run (e.g. 45m); the agent turn timeout is
                     kept below it (default: no cap)
  --json             print one JSON object instead of the Markdown summary
  --log-dir DIR      write redacted logs (itervox.log, issues/) here
  --verbose          DEBUG-level logs

Exit codes: 0 PR opened or nothing to do, 2 failed, 3 needs input,
4 budget exceeded, 5 configuration error. The Markdown summary is also
appended to $GITHUB_STEP_SUMMARY when it is set.`

// runOptions are the parsed flags of `itervox run`.
type runOptions struct {
	issue    string
	workflow string
	answer   string
	approve  string
	logDir   string
	worktree bool
	asJSON   bool
	verbose  bool
	timeout  time.Duration
}

// issueRunDeps are the seams of `itervox run`: production builds the tracker and
// the agent runners from the config and opens PRs with gh; tests substitute
// a memory tracker, fake runners and a fake PR opener.
type issueRunDeps struct {
	tracker func(cfg *config.Config) (tracker.Tracker, error)
	runner  func(cfg *config.Config) agent.Runner
	// validateAgent checks the agent CLIs exist (the daemon's startup check).
	validateAgent bool
	// prCreate opens a pull request for head against base and returns its
	// URL.
	prCreate func(ctx context.Context, dir, head, base, title, body string) (string, error)
	// guardrailExe is the binary guardrail shims run ("" = this binary).
	guardrailExe string
}

func defaultIssueRunDeps() issueRunDeps {
	return issueRunDeps{
		tracker: buildTracker,
		runner: func(*config.Config) agent.Runner {
			return commandResolverRunner{inner: agent.NewMultiRunner(agent.NewClaudeRunner(),
				map[string]agent.Runner{"codex": agent.NewCodexRunner()})}
		},
		validateAgent: true,
		prCreate:      ghCreatePR,
	}
}

// newIssueRunDeps is replaced by tests.
var newIssueRunDeps = defaultIssueRunDeps

// runIssueCLI is `itervox run`: it exits with the run's exit code.
func runIssueCLI(args []string) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := runIssueCommand(ctx, args, os.Stdout, os.Stderr)
	stop()
	fatalExit(code)
}

// runIssueCommand runs `itervox run` and returns its exit code. The summary
// goes to stdout; logs go to stderr (and --log-dir), always redacted.
func runIssueCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	stderr = &lockedWriter{w: stderr} // workers and the event loop log at once
	// CI hands the run its credentials in the environment and uploads its
	// logs: redact their exact values whatever their shape.
	for _, k := range append([]string{"GH_TOKEN"}, secretEnvKeys...) {
		logging.RegisterSecret(os.Getenv(k))
	}
	opts, err := parseRunFlags(args, stderr)
	if err != nil {
		return runExitConfig
	}
	logFile, restore, err := installRunLogging(opts, stderr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "itervox run: %v\n", err)
		return runExitConfig
	}
	defer restore()
	if logFile != nil {
		defer func() { _ = logFile.Close() }()
	}
	res := executeRun(ctx, opts, newIssueRunDeps())
	writeRunOutput(res, opts, stdout, stderr)
	return res.ExitCode
}

// parseRunFlags parses and checks the flags; errors are printed with the
// usage.
func parseRunFlags(args []string, stderr io.Writer) (runOptions, error) {
	var opts runOptions
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.issue, "issue", "", "")
	fs.StringVar(&opts.workflow, "workflow", "WORKFLOW.md", "")
	fs.StringVar(&opts.answer, "answer", "", "")
	fs.StringVar(&opts.approve, "approve", "", "")
	fs.StringVar(&opts.logDir, "log-dir", "", "")
	fs.BoolVar(&opts.worktree, "worktree", false, "")
	fs.BoolVar(&opts.asJSON, "json", false, "")
	fs.BoolVar(&opts.verbose, "verbose", false, "")
	fs.DurationVar(&opts.timeout, "timeout", 0, "")
	fail := func(err error) (runOptions, error) {
		_, _ = fmt.Fprintf(stderr, "itervox run: %v\n\n%s\n", err, runUsage)
		return opts, err
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = fmt.Fprintln(stderr, runUsage)
		}
		return fail(err)
	}
	if fs.NArg() > 0 {
		return fail(fmt.Errorf("unexpected argument %q", fs.Arg(0)))
	}
	opts.issue = strings.TrimSpace(opts.issue)
	if opts.issue == "" {
		return fail(errors.New("--issue is required"))
	}
	if opts.timeout < 0 {
		return fail(errors.New("--timeout must not be negative"))
	}
	if strings.HasPrefix(opts.answer, "@") {
		data, err := os.ReadFile(strings.TrimPrefix(opts.answer, "@"))
		if err != nil {
			return fail(fmt.Errorf("--answer: %w", err))
		}
		opts.answer = string(data)
	}
	opts.answer = strings.TrimSpace(opts.answer)
	opts.approve = strings.Join(strings.Fields(opts.approve), " ")
	return opts, nil
}

// installRunLogging makes the slog default a redacting handler writing to
// stderr and, with --log-dir, to <log-dir>/itervox.log. restore puts the
// previous default back.
func installRunLogging(opts runOptions, stderr io.Writer) (*os.File, func(), error) {
	level := slog.LevelInfo
	if opts.verbose {
		level = slog.LevelDebug
	}
	format := resolveLogFormat("", os.Getenv("ITERVOX_LOG_FORMAT"))
	handlers := []slog.Handler{newStderrLogHandler(stderr, level, format)}
	var file *os.File
	if opts.logDir != "" {
		if err := os.MkdirAll(opts.logDir, 0o755); err != nil {
			return nil, nil, fmt.Errorf("create --log-dir: %w", err)
		}
		f, err := os.OpenFile(filepath.Join(opts.logDir, "itervox.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, nil, fmt.Errorf("open log file: %w", err)
		}
		file = f
		handlers = append(handlers, newFileLogHandler(f, level, format))
	}
	prev := slog.Default()
	slog.SetDefault(slog.New(logging.NewRedactingHandler(logging.NewFanoutHandler(handlers...))))
	return file, func() { slog.SetDefault(prev) }, nil
}
