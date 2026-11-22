package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vnovick/itervox/internal/referee"
)

// The plan `itervox quickstart` shows before it writes a new workflow: what
// it found (tracker, agent CLIs, local model servers, CI, deploy tools and
// the project's test and lint commands) and what it will write. Detection
// only reads files, looks names up on PATH and probes the loopback model
// server ports (quickstart_detect_models.go). It never runs an agent CLI,
// and it reports a credential by its presence only, never its value.

// quickstartAgentCLIs are the agent CLIs the plan looks for on PATH, by
// name only (exec.LookPath): none of them is run.
var quickstartAgentCLIs = []string{"claude", "codex", "opencode", "cursor-agent"}

// quickstartRefereeGuideURL is the referee guide the plan points CI at.
const quickstartRefereeGuideURL = "https://itervox.dev/guides/referee/"

// foundCLI is an agent CLI and where PATH has it ("" when it does not).
type foundCLI struct {
	Name, Path string
}

// proposedCheck is a verify check the plan suggests from the repository's
// own test and lint commands.
type proposedCheck struct {
	Name, Run, From string
}

// quickstartFindings is everything the plan shows.
type quickstartFindings struct {
	quickstartDetection
	AgentCLIs []foundCLI
	// ModelServers are the servers that answered; ModelServerNote says why
	// none were probed (--no-local-model, --model-server) when not.
	ModelServers    []detectedModelServer
	ModelServerNote string
	CI              []string // e.g. "GitHub Actions (.github/workflows)"
	Tools           []string // referee adapters the repository uses
	Checks          []proposedCheck
	Demo            bool // a demo issue will be written
}

// detectQuickstartFindings gathers the plan's findings around det.
func detectQuickstartFindings(dir string, det quickstartDetection, opts quickstartOptions) quickstartFindings {
	f := quickstartFindings{quickstartDetection: det}
	for _, name := range quickstartAgentCLIs {
		path, _ := quickstartLookPath(name)
		f.AgentCLIs = append(f.AgentCLIs, foundCLI{Name: name, Path: path})
	}
	switch {
	case opts.NoLocalModel:
		f.ModelServerNote = "not checked (--no-local-model)"
	case opts.ModelServer != "":
		f.ModelServerNote = opts.ModelServer + " (--model-server)"
	default:
		f.ModelServers = detectModelServers(quickstartModelServerCandidates)
	}
	f.CI = detectCI(dir)
	f.Tools = referee.DetectTools(dir)
	f.Checks = detectProjectChecks(dir)
	f.Demo = det.Tracker == "local" && !opts.NoDemo
	return f
}

// detectCI lists the CI systems the repository configures.
func detectCI(dir string) []string {
	exists := func(rel string) bool {
		_, err := os.Stat(filepath.Join(dir, rel))
		return err == nil
	}
	var out []string
	if entries, err := os.ReadDir(filepath.Join(dir, ".github", "workflows")); err == nil {
		for _, e := range entries {
			if ext := filepath.Ext(e.Name()); !e.IsDir() && (ext == ".yml" || ext == ".yaml") {
				out = append(out, "GitHub Actions (.github/workflows)")
				break
			}
		}
	}
	if exists(".gitlab-ci.yml") {
		out = append(out, "GitLab CI (.gitlab-ci.yml)")
	}
	if exists(".circleci") {
		out = append(out, "CircleCI (.circleci)")
	}
	switch {
	case exists(".buildkite"):
		out = append(out, "Buildkite (.buildkite)")
	case exists("buildkite.yml"), exists("buildkite.yaml"):
		out = append(out, "Buildkite (buildkite.yml)")
	}
	return out
}

// makeTargetRe matches a Makefile rule line for a target (not a `:=`
// assignment).
var makeTargetRe = regexp.MustCompile(`(?m)^([A-Za-z][A-Za-z0-9_-]*)[ \t]*:([^=]|$)`)

// detectProjectChecks proposes verify checks from the repository's own
// commands: Makefile targets first (the project's own entry points), then
// package.json scripts, then go.mod's defaults. A check name is proposed
// once, from the first source that has it.
func detectProjectChecks(dir string) []proposedCheck {
	var out []proposedCheck
	seen := map[string]bool{}
	add := func(name, run, from string) {
		if !seen[name] {
			seen[name] = true
			out = append(out, proposedCheck{Name: name, Run: run, From: from})
		}
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "Makefile")); err == nil {
		targets := map[string]bool{}
		for _, m := range makeTargetRe.FindAllStringSubmatch(string(raw), -1) {
			targets[m[1]] = true
		}
		for _, t := range []string{"test", "lint", "check", "typecheck"} {
			if targets[t] {
				add(t, "make "+t, "Makefile")
			}
		}
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if json.Unmarshal(raw, &pkg) == nil {
			pm := nodePackageManager(dir)
			for _, s := range []string{"test", "lint", "typecheck", "check"} {
				if _, ok := pkg.Scripts[s]; ok {
					add(s, pm+" run "+s, "package.json scripts")
				}
			}
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
		add("test", "go test ./...", "go.mod")
		add("vet", "go vet ./...", "go.mod")
	}
	return out
}

// nodePackageManager picks the package manager from the lockfile.
func nodePackageManager(dir string) string {
	for _, lf := range []struct{ file, pm string }{
		{"pnpm-lock.yaml", "pnpm"}, {"yarn.lock", "yarn"}, {"bun.lockb", "bun"}, {"bun.lock", "bun"},
	} {
		if _, err := os.Stat(filepath.Join(dir, lf.file)); err == nil {
			return lf.pm
		}
	}
	return "npm"
}

// renderQuickstartPlan writes the plan: what was found, what will be
// written, and the suggestions that are not written.
func renderQuickstartPlan(out io.Writer, f quickstartFindings, workflowPath string) {
	p := func(format string, a ...any) { _, _ = fmt.Fprintf(out, format, a...) }
	p("\nHere's what I found:\n")
	p("  tracker        %s — %s\n", f.Tracker, f.TrackerReason)
	for _, c := range f.AgentCLIs {
		switch {
		case c.Path == "":
			p("  agent CLI      %s: not found\n", c.Name)
		case c.Name == f.Runner:
			p("  agent CLI      %s: %s (the default agent)\n", c.Name, c.Path)
		default:
			p("  agent CLI      %s: %s\n", c.Name, c.Path)
		}
	}
	switch {
	case f.ModelServerNote != "":
		p("  model server   %s\n", f.ModelServerNote)
	case len(f.ModelServers) == 0:
		p("  model server   none answering on the usual local ports\n")
	default:
		for _, d := range f.ModelServers {
			p("  model server   %s (asked about after the checks)\n", d.describe())
		}
	}
	if len(f.CI) == 0 {
		p("  CI             none found\n")
	}
	for _, ci := range f.CI {
		p("  CI             %s — `itervox referee` can hold agent changes to CI for a human: %s\n", ci, quickstartRefereeGuideURL)
	}
	if len(f.Tools) > 0 {
		p("  deploy tools   %s — `itervox referee envcheck` checks the deploy-target variables they read: %s#referee-envcheck\n",
			strings.Join(f.Tools, ", "), quickstartRefereeGuideURL)
	}
	if len(f.Checks) == 0 {
		p("  test/lint      none found (Makefile, package.json scripts, go.mod)\n")
	}
	for _, c := range f.Checks {
		p("  test/lint      %s: %s (%s)\n", c.Name, c.Run, c.From)
	}

	itervoxDir := filepath.Join(filepath.Dir(workflowPath), ".itervox")
	p("\nHere's what I'll write:\n")
	p("  %s — schema 2, tracker %s, agent %s, the prompt and the profiles\n", workflowPath, f.Tracker, f.Runner)
	p("  %s — tracker, agent, workspace and server settings\n", filepath.Join(itervoxDir, "settings.jsonc"))
	p("  %s — the implementer, reviewer and other profiles' SOUL.md and INSTRUCTIONS.md\n", filepath.Join(itervoxDir, "agents")+string(filepath.Separator))
	p("  %s — placeholders for credentials (gitignored)\n", filepath.Join(itervoxDir, ".env"))
	if f.Tracker == "local" {
		p("  %s — an example issue (Backlog)\n", filepath.Join(itervoxDir, "issues", localSeedIdentifier+".md"))
	}
	if f.Demo {
		p("  %s — a demo task (Todo): %q\n", filepath.Join(itervoxDir, "issues", demoIssueIdentifier+".md"), demoIssueTitle)
	}
	if len(f.Checks) > 0 {
		p("\nSuggested verify checks (not written: after your first run, add them to a profile in %s and the daemon runs them itself after each run):\n", workflowPath)
		p("      verify:\n")
		for _, c := range f.Checks {
			p("        - { name: %s, run: %q }\n", c.Name, c.Run)
		}
	}
	p("\n")
}
