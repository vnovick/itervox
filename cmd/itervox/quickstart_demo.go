package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/tracker/local"
)

// The demo issue `itervox quickstart` writes for a local tracker: a small,
// harmless task (one README section, nothing else) in an active state, so
// the first `itervox run --issue DEMO-1` has something real to do. With
// Linear or GitHub nothing is created on the service: quickstart prints how
// to hand it an issue instead.

const (
	demoIssueIdentifier = "DEMO-1"
	demoIssueTitle      = "Add a README section on how to run the tests"
	demoIssueState      = "Todo"
)

// demoIssueBody is the demo task. testCmd, when known, is the command the
// plan found; the agent checks it rather than trusting it.
func demoIssueBody(testCmd string) string {
	hint := "Find out how this project runs its tests (a Makefile, package.json scripts, a CI workflow or the language's usual command)."
	if testCmd != "" {
		hint = "Itervox's quickstart found `" + testCmd + "`; check that this is how the project runs its tests before you write it down."
	}
	return `A demo task from ` + "`itervox quickstart`" + `: small and safe, to see a whole run.

Add a short "Running the tests" section to README.md (create README.md if
there is none) with the command that runs this project's tests, in one or
two sentences and a code block.

` + hint + `

Change nothing else: no code, no configuration, no other file.`
}

// writeDemoIssue writes DEMO-1 unless the file is already there, and
// reports whether it did.
func writeDemoIssue(issuesDir, testCmd string, out io.Writer) bool {
	if _, err := os.Stat(filepath.Join(issuesDir, demoIssueIdentifier+".md")); err == nil {
		_, _ = fmt.Fprintf(out, "itervox quickstart: %s already exists; left it unchanged\n", demoIssueIdentifier)
		return false
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := local.WriteIssue(issuesDir, demoIssueIdentifier, local.IssueSpec{
		Title:   demoIssueTitle,
		State:   demoIssueState,
		Labels:  []string{"demo"},
		Body:    demoIssueBody(testCmd),
		Created: &now,
	}); err != nil {
		_, _ = fmt.Fprintf(out, "itervox quickstart: demo issue not written: %v\n", err)
		return false
	}
	_, _ = fmt.Fprintf(out, "itervox quickstart: wrote the demo issue %s (in %s)\n",
		filepath.Join(issuesDir, demoIssueIdentifier+".md"), demoIssueState)
	return true
}

// firstTestCommand is the run command of the proposed "test" check.
func firstTestCommand(checks []proposedCheck) string {
	for _, c := range checks {
		if c.Name == "test" {
			return c.Run
		}
	}
	return ""
}

// quickstartWorkflowFlag is the --workflow argument the printed commands
// need: none when the workflow is ./WORKFLOW.md of the current directory.
func quickstartWorkflowFlag(workflowPath string) string {
	if cwd, err := os.Getwd(); err == nil && pathEquals(filepath.Join(cwd, "WORKFLOW.md"), workflowPath) {
		return ""
	}
	return " --workflow " + shellQuoteArg(workflowPath)
}

// shellQuoteArg quotes a path for a copy-pasted command when it needs it.
func shellQuoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// printDemoNextSteps prints the exact command that runs the demo issue and
// how to open the dashboard afterwards.
func printDemoNextSteps(out io.Writer, workflowPath string) {
	wf := quickstartWorkflowFlag(workflowPath)
	daemonWF := strings.Replace(wf, "--workflow", "-workflow", 1)
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format, a...) }
	p("\nReady for a first run. The demo issue %s asks the agent for one README section and nothing else.\n", demoIssueIdentifier)
	p("Run it now, once, in this terminal (a git worktree under .itervox/run/worktrees/, so your checkout is not touched):\n\n")
	p("  itervox run --issue %s --worktree%s\n\n", demoIssueIdentifier, wf)
	p("Then start the board and open the dashboard:\n\n")
	p("  itervox quickstart%s   # starts the daemon in the background (or run `itervox%s` in the foreground)\n", wf, daemonWF)
	p("  itervox open%s         # opens the dashboard, logged in\n\n", wf)
	p("New issues are Markdown files in %s; set state: Todo when an agent should pick one up.\n",
		filepath.Join(filepath.Dir(workflowPath), ".itervox", "issues")+string(filepath.Separator))
}

// printTrackerNextSteps prints how to hand a Linear or GitHub issue to the
// agent; quickstart creates nothing on the service. started says whether
// the daemon is running (it picks up the issue itself) or not (`itervox
// run` runs it once).
func printTrackerNextSteps(out io.Writer, workflowPath string, started bool) {
	cfg, err := config.Load(workflowPath)
	if err != nil || (cfg.Tracker.Kind != "github" && cfg.Tracker.Kind != "linear") {
		return
	}
	active := "todo"
	if len(cfg.Tracker.ActiveStates) > 0 {
		active = cfg.Tracker.ActiveStates[0]
	}
	wf := quickstartWorkflowFlag(workflowPath)
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format, a...) }
	example := "ENG-123"
	if cfg.Tracker.Kind == "github" {
		example = "42"
		p("\nHand an issue to the agent: give a small issue the %q label (an active state), for example:\n\n", active)
		p("  gh issue edit %s --add-label %s\n\n", example, shellQuoteArg(active))
	} else {
		p("\nHand an issue to the agent: move a small issue the workflow reads to %q in Linear.\n\n", active)
	}
	if started {
		p("The daemon picks it up on its next poll; watch it on the dashboard (`itervox open%s`).\n", wf)
		return
	}
	p("Run it once, in this terminal:\n\n  itervox run --issue %s --worktree%s\n", example, wf)
}
