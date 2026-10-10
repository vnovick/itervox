package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vnovick/itervox/internal/agent"
	"github.com/vnovick/itervox/internal/agent/demoagent"
	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/orchestrator"
	"github.com/vnovick/itervox/internal/tracker"
	"github.com/vnovick/itervox/internal/tracker/local"
)

// `itervox demo` (#76): see Itervox working in about a minute with no
// tracker account, API keys or agent CLI. It writes a throwaway workflow in a
// scratch directory and runs the normal daemon against it with three demo
// hooks:
//
//   - the memory tracker with generated DEMO-n issues (one instance for the
//     whole session, so a WORKFLOW.md reload does not reset the board);
//   - demoagent.DemoRunner instead of the claude/codex runners, so no CLI is
//     needed (and none is validated);
//   - a controller that answers input requests and moves reviewed issues to
//     Done after a short delay, standing in for the human.
//
// Everything the daemon writes — logs, workspaces, .itervox runtime files —
// goes under the scratch directory, and the dashboard shows a "Demo mode"
// badge.

// demoSession is non-nil only while `itervox demo` runs.
var demoSession *demoRun

type demoRun struct {
	dir     string
	tracker tracker.Tracker
	runner  agent.Runner

	replyAfter  time.Duration
	mergeAfter  time.Duration
	orch        atomic.Pointer[orchestrator.Orchestrator]
	controlOnce sync.Once
}

// demoStateInReview is the demo board's review column (completion_state).
const demoStateInReview = "In Review"

// Seams tests replace.
var (
	demoOpenBrowser = openBrowser
	demoStep        = 900 * time.Millisecond
)

// prepareDemo parses `itervox demo` flags, writes the scratch workflow and
// installs demoSession. It returns the run-mode arguments main() continues
// with, or ok=false when nothing should run (help).
func prepareDemo(args []string, out io.Writer) (runArgs []string, ok bool, err error) {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(out)
	dir := fs.String("dir", "", "scratch directory for the demo (default: a new temporary directory)")
	noOpen := fs.Bool("no-open", false, "do not open the dashboard in a browser")
	trackerKind := fs.String("tracker", "memory", "demo issues: memory (in memory, gone on exit) or local (Markdown files in <dir>/.itervox/issues, kept)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if fs.NArg() > 0 {
		return nil, false, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if *trackerKind != "memory" && *trackerKind != "local" {
		return nil, false, fmt.Errorf("unknown --tracker %q (memory or local)", *trackerKind)
	}
	scratch := *dir
	if scratch == "" {
		if scratch, err = os.MkdirTemp("", "itervox-demo-"); err != nil {
			return nil, false, err
		}
	} else if err := os.MkdirAll(scratch, 0o755); err != nil {
		return nil, false, err
	}
	if scratch, err = filepath.Abs(scratch); err != nil {
		return nil, false, err
	}
	workflowPath := filepath.Join(scratch, "WORKFLOW.md")
	existing, readErr := os.ReadFile(workflowPath)
	switch {
	case readErr == nil && !strings.Contains(string(existing), demoWorkflowMarker):
		// --dir pointed at a real project: never touch its workflow.
		return nil, false, fmt.Errorf("%s already exists and is not a demo workflow; pick an empty --dir (or omit it)", workflowPath)
	case readErr == nil:
		// A previous demo's workflow: reuse it as is, so a second
		// `itervox demo --dir` on a running demo does not rewrite (and
		// reload) it before the pid lock refuses the second daemon.
	case os.IsNotExist(readErr):
		if err := os.WriteFile(workflowPath, []byte(demoWorkflow(scratch, *trackerKind)), 0o644); err != nil {
			return nil, false, err
		}
	default:
		return nil, false, readErr
	}
	// The demo's own settings win over a developer shell: these would move
	// the bind or turn on token auth or dry-run.
	for _, k := range []string{"ITERVOX_SERVER_PORT", "ITERVOX_SERVER_HOST", "PORT", "ITERVOX_API_TOKEN", "ITERVOX_DRY_RUN"} {
		_ = os.Unsetenv(k)
	}
	// Tools the daemon runs (gh for PR lookups, git) keep config, state and
	// caches under the XDG directories, by default in HOME (gh creates
	// ~/.local/state/gh on every call). Point them into the scratch
	// directory so the demo writes nothing outside it; this also keeps the
	// demo away from the operator's own gh login.
	xdg := filepath.Join(scratch, ".xdg")
	for k, sub := range map[string]string{
		"XDG_CONFIG_HOME": "config", "XDG_STATE_HOME": "state", "XDG_CACHE_HOME": "cache",
		"XDG_DATA_HOME": "data", "GH_CONFIG_DIR": "gh",
	} {
		_ = os.Setenv(k, filepath.Join(xdg, sub))
	}

	issues := tracker.GenerateDemoIssues(10)
	for i := range issues {
		issues[i].State = "Todo"
	}
	active, terminal := []string{"Todo", "In Progress"}, []string{"Done", "Cancelled"}
	var tr tracker.Tracker = tracker.NewMemoryTracker(issues, active, terminal)
	if *trackerKind == "local" {
		// #85: the same issues as files; a second demo in the same --dir
		// keeps the board it left.
		issuesDir := filepath.Join(scratch, ".itervox", "issues")
		if err := seedDemoIssueFiles(issuesDir, issues); err != nil {
			return nil, false, err
		}
		tr = local.New(local.Config{Dir: issuesDir, ActiveStates: active, TerminalStates: terminal})
		_, _ = fmt.Fprintf(out, "itervox demo: issues are files in %s — edit one and watch the board\n", issuesDir)
	}
	demoSession = &demoRun{
		dir:        scratch,
		tracker:    tr,
		runner:     demoagent.NewDemoRunner(demoStep),
		replyAfter: 6 * time.Second,
		mergeAfter: 5 * time.Second,
	}

	_, _ = fmt.Fprintf(out, "itervox demo: running with fake issues and a scripted agent — nothing leaves %s\n", scratch)
	_, _ = fmt.Fprintf(out, "itervox demo: press q (or Ctrl-C) to stop; delete the directory afterwards if you like\n")
	if !*noOpen {
		go demoOpenWhenReady(workflowPath, out)
	}
	return []string{"itervox", "-workflow", workflowPath, "-logs-dir", filepath.Join(scratch, "logs")}, true, nil
}

// demoWorkflowMarker marks a workflow written by `itervox demo`.
const demoWorkflowMarker = "# itervox demo workflow"

// seedDemoIssueFiles writes the demo issues as local tracker files, leaving
// any that already exist.
func seedDemoIssueFiles(dir string, issues []domain.Issue) error {
	for _, is := range issues {
		path := filepath.Join(dir, is.Identifier+".md")
		if _, err := os.Stat(path); err == nil {
			continue
		}
		spec := local.IssueSpec{Title: is.Title, State: is.State, Priority: is.Priority, Labels: is.Labels, Created: is.CreatedAt}
		if is.Description != nil {
			spec.Body = *is.Description
		}
		if err := local.WriteIssue(dir, is.Identifier, spec); err != nil {
			return err
		}
	}
	return nil
}

// demoWorkflow is the scratch WORKFLOW.md: memory (or local) tracker, a review column,
// fast polling and retries, an OS-assigned loopback port without auth, and
// workspaces inside the scratch directory.
func demoWorkflow(scratch, trackerKind string) string {
	return fmt.Sprintf(`---
`+demoWorkflowMarker+` — written by `+"`itervox demo`"+`; safe to delete with its directory.
itervox_schema_version: 2
tracker:
  kind: %s
  active_states: ["Todo", "In Progress"]
  working_state: "In Progress"
  completion_state: %q
  terminal_states: ["Done", "Cancelled"]
polling:
  interval_ms: 1000
agent:
  command: claude
  max_concurrent_agents: 3
  max_turns: 1
  max_retries: 2
  max_retry_backoff_ms: 3000
workspace:
  root: %q
server:
  host: 127.0.0.1
  port: 0
  allow_unauthenticated: true
---

Demo: work on {{ issue.identifier }} — {{ issue.title }}.
`, trackerKind, demoStateInReview, filepath.Join(scratch, "workspaces"))
}

// attach points the controller at orch. run() calls it for every daemon
// generation (a WORKFLOW.md reload builds a new orchestrator), so the
// controller always talks to the current one; it is started once and lives
// as long as the demo process.
func (d *demoRun) attach(orch *orchestrator.Orchestrator) {
	d.orch.Store(orch)
	d.controlOnce.Do(func() {
		go d.control(context.Background())
	})
}

// control answers input requests after replyAfter and moves issues that sat
// in review for mergeAfter to Done, standing in for the human operator.
func (d *demoRun) control(ctx context.Context) {
	defer orchestrator.RecoverGoroutine("demo-controller", "", nil)
	waiting := map[string]time.Time{}
	reviewing := map[string]time.Time{}
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		now := time.Now()
		orch := d.orch.Load()
		if orch == nil {
			continue
		}
		for identifier := range orch.Snapshot().InputRequiredIssues {
			since, seen := waiting[identifier]
			if !seen {
				waiting[identifier] = now
				continue
			}
			if now.Sub(since) >= d.replyAfter {
				if err := orch.ProvideInput(identifier, "Round once at the end, then show the rounding on the invoice. (demo reply)"); err == nil {
					delete(waiting, identifier)
				}
			}
		}
		inReview, err := d.tracker.FetchIssuesByStates(ctx, []string{demoStateInReview})
		if err != nil {
			continue
		}
		for _, issue := range inReview {
			since, seen := reviewing[issue.ID]
			if !seen {
				reviewing[issue.ID] = now
				_, _ = d.tracker.CreateComment(ctx, issue.ID, "🔗 Pull request created: "+demoagent.PRURL(issue.Identifier)+" (demo)")
				continue
			}
			if now.Sub(since) >= d.mergeAfter {
				_, _ = d.tracker.CreateComment(ctx, issue.ID, "Reviewed and merged. (demo)")
				if err := d.tracker.UpdateIssueState(ctx, issue.ID, "Done"); err == nil {
					delete(reviewing, issue.ID)
				}
			}
		}
	}
}

// demoOpenWhenReady waits for the daemon to publish its URL, then opens it.
func demoOpenWhenReady(workflowPath string, out io.Writer) {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(dashboardURLFilePath(workflowPath)); err == nil {
			if url := strings.TrimSpace(string(raw)); url != "" {
				if err := demoOpenBrowser(url); err != nil {
					_, _ = fmt.Fprintf(out, "itervox demo: open %s in your browser (%v)\n", url, err)
				}
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// openBrowser opens url with the platform's default handler.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap the opener so it does not linger as a zombie
	return nil
}
