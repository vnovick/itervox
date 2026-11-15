// Package cli is the command line of the profile evals (#189, #201): the
// recorded, live, record and promote commands. Two front ends share it:
// `itervox evals …` (shipped in every release; the project is the directory
// holding WORKFLOW.md, and the built-in scenarios are embedded in the
// binary) and the developer tool cmd/itervox-evals that the Makefile's
// evals targets run against the repository's fixtures.
//
// live and record spend tokens and refuse to start unless
// ITERVOX_EVALS_LIVE=1. recorded, live and record save the judged report as
// JSON under -results-dir (default .itervox/evals) for the dashboard's
// Repo Analysis page. See docs/evals.md.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/evals"
)

// ProjectFixturesDir is where a project keeps its own scenarios, relative
// to the directory holding WORKFLOW.md. `itervox evals` uses it when
// -fixtures is not given and it exists.
const ProjectFixturesDir = ".itervox/evals/fixtures"

// Defaults configures a front end.
type Defaults struct {
	// Prog names the command in usage and error lines ("itervox evals").
	Prog string
	// Fixtures is the -fixtures default. "" resolves at run time: the
	// project's ProjectFixturesDir when it exists, else the built-in
	// scenarios embedded in the binary (read-only: record and promote then
	// refuse and ask for -fixtures).
	Fixtures string
	// Workflow is the -workflow default (the WORKFLOW.md live and record
	// run against).
	Workflow string
	// Project makes the directory holding -workflow the project: every
	// command takes -workflow, the results default to <project>/.itervox/evals
	// and the project fixtures are found there.
	Project bool
	// PromoteHint is printed after record; "" prints the
	// `<prog> promote -fixtures DIR` form.
	PromoteHint string
}

// Exit codes: 0 every scenario passed, 1 a scenario failed, 2 usage or
// setup error.
const (
	exitFailed = 1
	exitError  = 2
)

// Run dispatches an evals command and returns the process exit code. The
// caller owns the process (signals, the exit itself).
func Run(ctx context.Context, d Defaults, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(d, stderr)
		return exitError
	}
	switch args[0] {
	case "recorded":
		return runRecorded(d, args[1:], stdout, stderr)
	case "live", "record":
		return runLive(ctx, d, args[0] == "record", args[1:], stdout, stderr)
	case "promote":
		return runPromote(d, args[1:], stdout, stderr)
	case "-h", "-help", "--help", "help":
		usage(d, stdout)
		return 0
	}
	_, _ = fmt.Fprintf(stderr, "%s: unknown command %q\n", d.Prog, args[0])
	usage(d, stderr)
	return exitError
}

func usage(d Defaults, w io.Writer) {
	text := `usage:
  {prog} recorded [flags]  judge the recordings (no agent, no tokens); saves the report under -results-dir
  {prog} live    [flags]   run the live suite (needs ITERVOX_EVALS_LIVE=1); transcripts go to -out
  {prog} record  [flags]   same, writing recording.candidate.jsonl next to each fixture
  {prog} promote [-fixtures dir] [-force] -all | <profile>/<scenario>...
`
	if d.Fixtures == "" {
		text += `
Scenarios come from -fixtures DIR, else ` + ProjectFixturesDir + ` next to
WORKFLOW.md when it exists, else the built-in scenarios in this binary.
record and promote write files next to the fixtures, so they need -fixtures
or ` + ProjectFixturesDir + `.
`
	}
	text += `
Run "{prog} live -h" for the live flags.
`
	_, _ = fmt.Fprint(w, strings.ReplaceAll(text, "{prog}", d.Prog))
}

// commonFlags are the flags every command shares.
type commonFlags struct {
	fs         *flag.FlagSet
	fixtures   *string
	workflow   *string // nil unless the command takes -workflow
	resultsDir *string // nil for promote
}

func newFlags(d Defaults, name string, withWorkflow, withResults bool, stderr io.Writer) *commonFlags {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	c := &commonFlags{fs: fs}
	fixturesHelp := "fixture root (<profile>/<scenario>/)"
	if d.Fixtures == "" {
		fixturesHelp += " (default: " + ProjectFixturesDir + " next to WORKFLOW.md if present, else the built-in scenarios)"
	}
	c.fixtures = fs.String("fixtures", d.Fixtures, fixturesHelp)
	if withWorkflow || d.Project {
		help := "WORKFLOW.md whose profiles, rules and prompt template the run uses"
		if d.Project {
			help += "; its directory is the project (results and project fixtures)"
		}
		c.workflow = fs.String("workflow", d.Workflow, help)
	}
	if withResults {
		help := "directory the judged report is saved to as <ISO8601>.json for the dashboard's Repo Analysis page (\"\" = do not save)"
		if d.Project {
			help += "; relative to the WORKFLOW.md directory unless set"
		}
		c.resultsDir = fs.String("results-dir", evals.ResultsDir, help)
	}
	return c
}

func (c *commonFlags) isSet(name string) bool {
	set := false
	c.fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

// projectDir is the directory holding -workflow ("." without one).
func (c *commonFlags) projectDir() string {
	if c.workflow == nil {
		return "."
	}
	return filepath.Dir(*c.workflow)
}

// results is the effective -results-dir: in a project front end the
// default is anchored at the WORKFLOW.md directory.
func (c *commonFlags) results(d Defaults) string {
	dir := *c.resultsDir
	if d.Project && !c.isSet("results-dir") {
		dir = filepath.Join(c.projectDir(), filepath.FromSlash(evals.ResultsDir))
	}
	return dir
}

// errReadOnlyFixtures: record and promote write next to the fixtures, which
// the built-in scenarios cannot take.
var errReadOnlyFixtures = errors.New("the built-in scenarios are read-only: pass -fixtures DIR (a copy you can write) or create " + ProjectFixturesDir + " next to WORKFLOW.md")

// resolveFixtures picks the fixture root: -fixtures (or a front end's fixed
// default), else the project's ProjectFixturesDir, else the built-in
// scenarios materialized into a temporary directory. writable refuses the
// built-in set. cleanup is never nil.
func (c *commonFlags) resolveFixtures(d Defaults, writable bool, stderr io.Writer) (dir string, cleanup func(), err error) {
	noop := func() {}
	if d.Fixtures != "" || c.isSet("fixtures") {
		return *c.fixtures, noop, nil
	}
	project := filepath.Join(c.projectDir(), filepath.FromSlash(ProjectFixturesDir))
	if info, err := os.Stat(project); err == nil && info.IsDir() {
		_, _ = fmt.Fprintln(stderr, "[evals] fixtures:", project)
		return project, noop, nil
	}
	if writable {
		return "", noop, errReadOnlyFixtures
	}
	dir, cleanup, err = evals.MaterializeBuiltinFixtures()
	if err != nil {
		return "", noop, err
	}
	_, _ = fmt.Fprintln(stderr, "[evals] fixtures: built-in scenarios")
	return dir, cleanup, nil
}

func runLive(ctx context.Context, d Defaults, candidate bool, args []string, stdout, stderr io.Writer) int {
	name := "live"
	if candidate {
		name = "record"
	}
	c := newFlags(d, name, true, true, stderr)
	fs := c.fs
	opts := evals.LiveOptions{Candidate: candidate, Log: stderr}
	fs.StringVar(&opts.Profile, "profile", "", "only this profile's scenarios")
	fs.StringVar(&opts.Scenario, "scenario", "", "only the scenario with this name")
	fs.StringVar(&opts.Command, "command", "", "agent command to run instead of the profile's (e.g. an absolute CLI path)")
	fs.StringVar(&opts.Backend, "backend", "", "backend to run: claude or codex (default: the profile's)")
	fs.IntVar(&opts.Budget.MaxTurns, "max-turns", evals.DefaultMaxTurns, "per-scenario agent turn cap (claude --max-turns)")
	fs.IntVar(&opts.Budget.MaxTokens, "max-tokens", evals.DefaultMaxTokens, "per-scenario token cap (input+cached+output)")
	fs.Float64Var(&opts.Budget.MaxCostUSD, "max-cost-usd", evals.DefaultMaxCostUSD, "suite cost cap in USD; later scenarios are not started once it is spent")
	fs.DurationVar(&opts.Budget.Timeout, "timeout", evals.DefaultTimeout, "per-scenario wall-clock limit")
	if !candidate {
		fs.StringVar(&opts.OutDir, "out", "", "directory for the run's transcripts (default: a new temp dir)")
	}
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if os.Getenv(evals.LiveOptInEnv) != "1" {
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", d.Prog, evals.ErrLiveNotEnabled)
		return exitError
	}
	opts.Workflow = *c.workflow
	fixtures, cleanup, err := c.resolveFixtures(d, candidate, stderr)
	defer cleanup()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s %s: %v\n", d.Prog, name, err)
		return exitError
	}
	opts.FixturesRoot = fixtures
	if !candidate && opts.OutDir == "" {
		dir, err := os.MkdirTemp("", "itervox-evals-live-*")
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "%s: %v\n", d.Prog, err)
			return exitError
		}
		opts.OutDir = dir
	}
	started := time.Now()
	res, err := evals.RunLive(ctx, opts)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: %v\n", d.Prog, err)
		return exitError
	}
	_, _ = fmt.Fprint(stdout, res.Report.Format())
	mode := evals.ModeLive
	if candidate {
		mode = evals.ModeRecord
	}
	saveResults(d, c.results(d), evals.NewResultFile(mode, res.Report, res.Runs, opts.Workflow, started, time.Now()), stdout, stderr)
	spent := 0.0
	for _, r := range res.Runs {
		spent += r.CostUSD
	}
	_, _ = fmt.Fprintf(stdout, "[evals] spent $%.4f of the $%.2f budget\n", spent, opts.Budget.MaxCostUSD)
	if candidate {
		hint := d.PromoteHint
		if hint == "" {
			hint = d.Prog + " promote -fixtures " + fixtures + " <profile>/<scenario>` (or -all)"
		}
		_, _ = fmt.Fprintln(stdout, "[evals] candidates written next to each fixture as "+evals.CandidateName+"; review them, then run `"+hint)
	} else {
		_, _ = fmt.Fprintln(stdout, "[evals] transcripts in", opts.OutDir)
	}
	return res.Report.ExitCode()
}

func runPromote(d Defaults, args []string, stdout, stderr io.Writer) int {
	c := newFlags(d, "promote", false, false, stderr)
	all := c.fs.Bool("all", false, "promote every scenario that has a candidate")
	force := c.fs.Bool("force", false, "promote even when the candidate fails its judges")
	if err := c.fs.Parse(args); err != nil {
		return exitError
	}
	prefix := d.Prog + " promote:"
	wanted := c.fs.Args()
	if *all == (len(wanted) > 0) {
		_, _ = fmt.Fprintln(stderr, prefix, "pass -all or one or more <profile>/<scenario>")
		return exitError
	}
	fixtures, cleanup, err := c.resolveFixtures(d, true, stderr)
	defer cleanup()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, prefix, err)
		return exitError
	}
	scenarios, err := evals.LoadScenarios(fixtures, "")
	if err != nil {
		_, _ = fmt.Fprintln(stderr, prefix, err)
		return exitError
	}
	byKey := map[string]evals.Scenario{}
	for _, s := range scenarios {
		byKey[s.Profile+"/"+s.Name] = s
	}
	if *all {
		for _, s := range scenarios {
			if _, err := os.Stat(evals.CandidatePath(s)); err == nil {
				wanted = append(wanted, s.Profile+"/"+s.Name)
			}
		}
		if len(wanted) == 0 {
			_, _ = fmt.Fprintln(stderr, prefix, "no "+evals.CandidateName+" found under", fixtures)
			return exitError
		}
	}
	code := 0
	var promoted []evals.Scenario
	for _, key := range wanted {
		s, ok := byKey[filepath.ToSlash(strings.Trim(key, "/"))]
		if !ok {
			_, _ = fmt.Fprintf(stderr, "%s unknown scenario %q\n", prefix, key)
			code = exitError
			continue
		}
		if _, err := evals.PromoteCandidate(s, *force); err != nil {
			_, _ = fmt.Fprintln(stderr, prefix, err)
			if errors.Is(err, evals.ErrNoCandidate) {
				code = exitError
			} else if code == 0 {
				code = exitFailed
			}
			continue
		}
		_, _ = fmt.Fprintf(stdout, "promoted %s -> %s\n", key, evals.RecordingPath(s))
		promoted = append(promoted, s)
	}
	if len(promoted) > 0 {
		// Replay what was just promoted exactly as recorded mode will, so a
		// forced promotion of a failing transcript is visible here.
		report := evals.EvaluateAll(promoted)
		_, _ = fmt.Fprint(stdout, report.Format())
		if report.ExitCode() != 0 && code == 0 {
			code = exitFailed
		}
	}
	return code
}

// saveResults persists a judged report. A failure to save is reported but
// does not change the exit code: the verdicts were already printed.
func saveResults(d Defaults, dir string, f evals.ResultFile, stdout, stderr io.Writer) {
	if dir == "" {
		return
	}
	path, err := evals.WriteResultFile(dir, f)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%s: save report: %v\n", d.Prog, err)
		return
	}
	_, _ = fmt.Fprintln(stdout, "[evals] report saved to", path)
}

// runRecorded judges the recordings — what `make evals-fast` checks in
// `make verify` for the repository's fixtures — and saves the report
// (#201). No agent runs.
func runRecorded(d Defaults, args []string, stdout, stderr io.Writer) int {
	c := newFlags(d, "recorded", false, true, stderr)
	profile := c.fs.String("profile", "", "only this profile's scenarios")
	if err := c.fs.Parse(args); err != nil {
		return exitError
	}
	prefix := d.Prog + " recorded:"
	started := time.Now()
	fixtures, cleanup, err := c.resolveFixtures(d, false, stderr)
	defer cleanup()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, prefix, err)
		return exitError
	}
	scenarios, err := evals.LoadScenarios(fixtures, *profile)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, prefix, err)
		return exitError
	}
	if len(scenarios) == 0 {
		_, _ = fmt.Fprintln(stderr, prefix, "no scenarios under", fixtures)
		return exitError
	}
	report := evals.EvaluateAll(scenarios)
	_, _ = fmt.Fprint(stdout, report.Format())
	saveResults(d, c.results(d), evals.NewResultFile(evals.ModeRecorded, report, nil, "", started, time.Now()), stdout, stderr)
	return report.ExitCode()
}
