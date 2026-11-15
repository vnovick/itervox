// Command itervox-evals runs the profile evals against a real agent CLI
// (#189). It is a developer tool, not part of the released binary:
//
//	itervox-evals recorded [flags]                 judge the committed recordings (no agent, no tokens)
//	itervox-evals live    [flags]                  run the live suite; transcripts go to -out
//	itervox-evals record  [flags]                  re-record: write recording.candidate.jsonl next to each fixture
//	itervox-evals promote [-force] -all | <profile>/<scenario>...
//	                                               replace recording.jsonl with a reviewed candidate
//
// live and record spend tokens and refuse to start unless
// ITERVOX_EVALS_LIVE=1. Recorded mode (`make evals-fast`) stays the free gate
// in `make verify`. recorded, live and record also save the judged report as
// JSON under -results-dir (default .itervox/evals, #201) for the dashboard's
// Repo Analysis page. See docs/evals.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/vnovick/itervox/internal/evals"
)

const (
	defaultFixtures = "internal/evals/fixtures"
	defaultWorkflow = "internal/evals/project/WORKFLOW.md"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run is main without the process exit, so tests can drive it. Exit codes:
// 0 every scenario passed, 1 a scenario failed, 2 usage or setup error.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "recorded":
		return runRecorded(args[1:], stdout, stderr)
	case "live", "record":
		return runLive(ctx, args[0] == "record", args[1:], stdout, stderr)
	case "promote":
		return runPromote(args[1:], stdout, stderr)
	case "-h", "-help", "--help", "help":
		usage(stdout)
		return 0
	}
	_, _ = fmt.Fprintf(stderr, "itervox-evals: unknown command %q\n", args[0])
	usage(stderr)
	return 2
}

func usage(w io.Writer) {
	_, _ = fmt.Fprint(w, `usage:
  itervox-evals recorded [flags]  judge the committed recordings; saves the report under -results-dir
  itervox-evals live    [flags]   run the live suite (needs ITERVOX_EVALS_LIVE=1); transcripts go to -out
  itervox-evals record  [flags]   same, writing recording.candidate.jsonl next to each fixture
  itervox-evals promote [-fixtures dir] [-force] -all | <profile>/<scenario>...

Run "itervox-evals live -h" for the live flags.
`)
}

func runLive(ctx context.Context, candidate bool, args []string, stdout, stderr io.Writer) int {
	name := "live"
	if candidate {
		name = "record"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := evals.LiveOptions{Candidate: candidate, Log: stderr}
	fs.StringVar(&opts.FixturesRoot, "fixtures", defaultFixtures, "fixture root (<profile>/<scenario>/)")
	fs.StringVar(&opts.Workflow, "workflow", defaultWorkflow, "WORKFLOW.md whose profiles, rules and prompt template the run uses")
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
	resultsDir := resultsDirFlag(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if os.Getenv(evals.LiveOptInEnv) != "1" {
		_, _ = fmt.Fprintln(stderr, "itervox-evals:", evals.ErrLiveNotEnabled)
		return 2
	}
	if !candidate && opts.OutDir == "" {
		dir, err := os.MkdirTemp("", "itervox-evals-live-*")
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "itervox-evals:", err)
			return 2
		}
		opts.OutDir = dir
	}
	started := time.Now()
	res, err := evals.RunLive(ctx, opts)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "itervox-evals:", err)
		return 2
	}
	_, _ = fmt.Fprint(stdout, res.Report.Format())
	mode := evals.ModeLive
	if candidate {
		mode = evals.ModeRecord
	}
	saveResults(*resultsDir, evals.NewResultFile(mode, res.Report, res.Runs, opts.Workflow, started, time.Now()), stdout, stderr)
	spent := 0.0
	for _, r := range res.Runs {
		spent += r.CostUSD
	}
	_, _ = fmt.Fprintf(stdout, "[evals] spent $%.4f of the $%.2f budget\n", spent, opts.Budget.MaxCostUSD)
	if candidate {
		_, _ = fmt.Fprintln(stdout, "[evals] candidates written next to each fixture as "+evals.CandidateName+"; review them, then run `make evals-promote SCENARIO=<profile>/<scenario>` (or SCENARIO=all)")
	} else {
		_, _ = fmt.Fprintln(stdout, "[evals] transcripts in", opts.OutDir)
	}
	return res.Report.ExitCode()
}

func runPromote(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("promote", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fixtures := fs.String("fixtures", defaultFixtures, "fixture root (<profile>/<scenario>/)")
	all := fs.Bool("all", false, "promote every scenario that has a candidate")
	force := fs.Bool("force", false, "promote even when the candidate fails its judges")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	wanted := fs.Args()
	if *all == (len(wanted) > 0) {
		_, _ = fmt.Fprintln(stderr, "itervox-evals promote: pass -all or one or more <profile>/<scenario>")
		return 2
	}
	scenarios, err := evals.LoadScenarios(*fixtures, "")
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "itervox-evals promote:", err)
		return 2
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
			_, _ = fmt.Fprintln(stderr, "itervox-evals promote: no "+evals.CandidateName+" found under", *fixtures)
			return 2
		}
	}
	code := 0
	var promoted []evals.Scenario
	for _, key := range wanted {
		s, ok := byKey[filepath.ToSlash(strings.Trim(key, "/"))]
		if !ok {
			_, _ = fmt.Fprintf(stderr, "itervox-evals promote: unknown scenario %q\n", key)
			code = 2
			continue
		}
		if _, err := evals.PromoteCandidate(s, *force); err != nil {
			_, _ = fmt.Fprintln(stderr, "itervox-evals promote:", err)
			if errors.Is(err, evals.ErrNoCandidate) {
				code = 2
			} else if code == 0 {
				code = 1
			}
			continue
		}
		_, _ = fmt.Fprintf(stdout, "promoted %s -> %s\n", key, evals.RecordingPath(s))
		promoted = append(promoted, s)
	}
	if len(promoted) > 0 {
		// Replay what was just promoted exactly as `make evals-fast` will,
		// so a forced promotion of a failing transcript is visible here.
		report := evals.EvaluateAll(promoted)
		_, _ = fmt.Fprint(stdout, report.Format())
		if report.ExitCode() != 0 && code == 0 {
			code = 1
		}
	}
	return code
}

// resultsDirFlag registers -results-dir: where the judged report is saved
// as JSON (#201); "" saves nothing.
func resultsDirFlag(fs *flag.FlagSet) *string {
	return fs.String("results-dir", evals.ResultsDir, "directory the judged report is saved to as <ISO8601>.json for the dashboard's Repo Analysis page (\"\" = do not save)")
}

// saveResults persists a judged report. A failure to save is reported but
// does not change the exit code: the verdicts were already printed.
func saveResults(dir string, f evals.ResultFile, stdout, stderr io.Writer) {
	if dir == "" {
		return
	}
	path, err := evals.WriteResultFile(dir, f)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "itervox-evals: save report:", err)
		return
	}
	_, _ = fmt.Fprintln(stdout, "[evals] report saved to", path)
}

// runRecorded judges the committed recordings — what `make evals-fast`
// checks in `make verify` — and saves the report (#201). No agent runs.
func runRecorded(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("recorded", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fixtures := fs.String("fixtures", defaultFixtures, "fixture root (<profile>/<scenario>/)")
	profile := fs.String("profile", "", "only this profile's scenarios")
	resultsDir := resultsDirFlag(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	started := time.Now()
	scenarios, err := evals.LoadScenarios(*fixtures, *profile)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "itervox-evals recorded:", err)
		return 2
	}
	if len(scenarios) == 0 {
		_, _ = fmt.Fprintln(stderr, "itervox-evals recorded: no scenarios under", *fixtures)
		return 2
	}
	report := evals.EvaluateAll(scenarios)
	_, _ = fmt.Fprint(stdout, report.Format())
	saveResults(*resultsDir, evals.NewResultFile(evals.ModeRecorded, report, nil, "", started, time.Now()), stdout, stderr)
	return report.ExitCode()
}
