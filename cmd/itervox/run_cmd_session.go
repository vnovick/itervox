package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/logbuffer"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/workspace"
)

// runSettlePoll is how often the run re-checks whether it is done when no
// state change woke it.
const runSettlePoll = 250 * time.Millisecond

// runReplyHint is the footer of the question comment a run posts when it
// exits 3: how to answer from the issue (the documented workflows run
// `itervox run --answer` / `--approve` on these comments).
const runReplyHint = "_Reply with a comment `/itervox <your answer>` to continue this run, " +
	"or `/itervox approve <command>` to allow a command it asked for._"

// runSession is one `itervox run`: its config, tracker, checkout and issue.
type runSession struct {
	opts    runOptions
	deps    issueRunDeps
	cfg     *config.Config
	tr      tracker.Tracker
	issue   domain.Issue
	dir     string // the checkout's top-level directory
	ws      *workspace.CheckoutProvider
	base    string // the branch PRs target
	started time.Time

	mu       sync.Mutex
	refused  []string // approval commands the run's shims refused
	prPushed bool
}

// executeRun runs the issue and returns its summary, exit code included.
func executeRun(ctx context.Context, opts runOptions, deps issueRunDeps) runSummary {
	s := &runSession{opts: opts, deps: deps, started: time.Now()}
	sum := s.newSummary()
	cfg, err := config.Load(opts.workflow)
	if err == nil {
		err = config.ValidateDispatch(cfg)
	}
	if err != nil {
		return s.finish(sum, "config_error", runExitConfig, "configuration error: "+err.Error())
	}
	s.cfg = cfg
	workflowAbs, err := filepath.Abs(opts.workflow)
	if err != nil {
		return s.finish(sum, "config_error", runExitConfig, err.Error())
	}
	if s.dir, err = workspace.CheckoutRoot(ctx, filepath.Dir(workflowAbs)); err != nil {
		return s.finish(sum, "config_error", runExitConfig, err.Error())
	}
	if err := workspace.ExcludeRunDir(ctx, s.dir); err != nil {
		slog.Warn("run: could not exclude .itervox/run from git", "error", err)
	}
	if s.tr, err = deps.tracker(cfg); err != nil {
		return s.finish(sum, "config_error", runExitConfig, "tracker: "+err.Error())
	}
	issue, err := fetchRunIssue(ctx, s.tr, opts.issue)
	if err != nil {
		return s.finish(sum, "failed", runExitFailed, err.Error())
	}
	s.issue = *issue
	sum = s.newSummary()
	release, err := lockRunIssue(s.dir, issue.Identifier) // "12" and "#12" share one lock
	if err != nil {
		return s.finish(sum, "failed", runExitFailed, err.Error())
	}
	defer release()
	if !slices.ContainsFunc(cfg.Tracker.ActiveStates, func(st string) bool { return strings.EqualFold(st, issue.State) }) {
		return s.finish(sum, "nothing_to_do", runExitOK,
			fmt.Sprintf("%s is %q, not in tracker.active_states: nothing to do", issue.Identifier, issue.State))
	}
	runner := deps.runner(cfg)
	if deps.validateAgent {
		if err := validateBackend(configuredBackend(cfg.Agent.Command, cfg.Agent.Backend), "", map[string]struct{}{}, cfg); err != nil {
			return s.finish(sum, "config_error", runExitConfig, err.Error())
		}
	}
	s.ws = &workspace.CheckoutProvider{Dir: s.dir, Worktree: opts.worktree}
	s.base = runBaseBranch(ctx, cfg, s.dir, workspace.ResolveWorktreeBranch(issue.BranchName, issue.Identifier))
	applyRunConfig(cfg, opts.timeout)
	return s.run(ctx, runner)
}

// applyRunConfig narrows the loaded config to one run: one agent at a time,
// and an agent turn timeout below --timeout.
func applyRunConfig(cfg *config.Config, timeout time.Duration) {
	cfg.Agent.MaxConcurrentAgents = 1
	if timeout <= 0 {
		return
	}
	capMs := max(int((timeout * 9 / 10).Milliseconds()), 1000)
	if cfg.Agent.TurnTimeoutMs <= 0 || cfg.Agent.TurnTimeoutMs > capMs {
		cfg.Agent.TurnTimeoutMs = capMs
	}
}

// fetchRunIssue reads the issue by identifier; on GitHub a bare number is
// tried as "#<number>" too.
func fetchRunIssue(ctx context.Context, tr tracker.Tracker, id string) (*domain.Issue, error) {
	issue, err := tr.FetchIssueByIdentifier(ctx, id)
	if (err != nil || issue == nil) && strings.Trim(id, "0123456789") == "" {
		if alt, altErr := tr.FetchIssueByIdentifier(ctx, "#"+id); altErr == nil && alt != nil {
			return alt, nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("issue %s: %w", id, err)
	}
	if issue == nil {
		return nil, fmt.Errorf("issue %s not found", id)
	}
	return issue, nil
}

// singleIssueTracker offers the orchestrator exactly one candidate: the
// run's issue, while it is in an active state, until it was dispatched once
// and for as long as that run is running (so the poll never reports the
// running issue as out of scope). Every other read and write goes to the
// real tracker.
type singleIssueTracker struct {
	tracker.Tracker
	issueID    string
	active     []string
	dispatched atomic.Bool
	fetches    atomic.Int64
	// running reports whether the issue has a run now (from the
	// orchestrator's snapshot); set before the orchestrator runs.
	running func() bool
}

// FetchCandidateIssues is the run's issue, read fresh, or nothing.
func (t *singleIssueTracker) FetchCandidateIssues(ctx context.Context) ([]domain.Issue, error) {
	defer t.fetches.Add(1)
	if t.dispatched.Load() && (t.running == nil || !t.running()) {
		return nil, nil
	}
	issue, err := t.FetchIssueDetail(ctx, t.issueID)
	if err != nil {
		return nil, err
	}
	if issue == nil || !slices.ContainsFunc(t.active, func(s string) bool { return strings.EqualFold(s, issue.State) }) {
		return nil, nil
	}
	return []domain.Issue{*issue}, nil
}

// runSettled reports whether the issue's run is over: it asked for input,
// it is paused (budget, cancel) or otherwise not running after having been
// dispatched, or it was never dispatched after a full poll (ineligible).
// Reviewers are dispatched synchronously from the implementer's exit, so a
// review in progress is always a running entry.
func runSettled(snap orchestrator.State, issue domain.Issue, fetches int64) bool {
	if snap.InputRequiredIssues[issue.Identifier] != nil {
		return true
	}
	if _, running := snap.Running[issue.ID]; running {
		return false
	}
	if snap.PauseReasons[issue.Identifier] == orchestrator.PauseReasonBudgetExceeded {
		return true
	}
	if _, started := snap.LatestRun[issue.ID]; started {
		return true
	}
	_, claimPending := snap.ClaimChecks[issue.ID]
	return fetches >= 2 && !claimPending
}

// noteRefused records approval commands a run's shims refused
// (OneShot.OnApprovalRefused).
func (s *runSession) noteRefused(_ string, commands []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range commands {
		if !slices.Contains(s.refused, c) {
			s.refused = append(s.refused, c)
		}
	}
}

func (s *runSession) refusedCommands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.refused)
}

// stageIssueLogs points the orchestrator's per-issue logs at a staging
// directory; copyRedactedIssueLogs later copies them, redacted, into
// --log-dir/issues. Returns "" without --log-dir.
func stageIssueLogs(buf *logbuffer.Buffer, logDir string) string {
	if logDir == "" {
		return ""
	}
	staging, err := os.MkdirTemp("", "itervox-run-logs-*")
	if err != nil {
		slog.Warn("run: per-issue logs not kept", "error", err)
		return ""
	}
	buf.SetLogDir(staging)
	return staging
}
