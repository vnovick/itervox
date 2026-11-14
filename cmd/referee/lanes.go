package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/vnovick/itervox/internal/guardrails"
)

const lanesUsage = `usage: referee lanes --base <ref> [--head HEAD] [--policy .referee.yml] [--json] [--dir .]

Classifies every path the diff from the merge base of <base> and <head> to
<head> changes (git diff --name-only --no-renames) into a lane:

  human           CI workflows, CI and test-runner config, ownership rules:
                  a human must approve the change
  second_opinion  a reviewer other than the writer must approve it
  auto            everything else

The policy is read from the BASE ref (git show <base>:.referee.yml), never
from the working tree or the head, so the branch under review cannot change
the rules it is judged by. Without a policy file on the base ref the
default human lane applies. The policy file itself is always human lane.
--base defaults to origin/$GITHUB_BASE_REF in a pull_request workflow.

Exit codes: 0 auto, 10 second_opinion, 20 human, 2 usage or error.`

// lanesReport is the stable --json shape (field names and types do not
// change within a "version").
type lanesReport struct {
	Version       int      `json:"version"`
	Lane          string   `json:"lane"`
	ExitCode      int      `json:"exit_code"`
	Base          string   `json:"base"`
	BaseCommit    string   `json:"base_commit"`
	MergeBase     string   `json:"merge_base"`
	Head          string   `json:"head"`
	HeadCommit    string   `json:"head_commit"`
	Policy        string   `json:"policy"`
	Human         []string `json:"human"`
	SecondOpinion []string `json:"second_opinion"`
	Auto          []string `json:"auto"`
}

func lanesCmd(ctx context.Context, args []string, env envFunc, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("referee lanes", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprintln(stderr, lanesUsage) }
	base := fs.String("base", "", "base ref the pull request merges into (its policy is the one used)")
	head := fs.String("head", "HEAD", "head ref under review")
	policyPath := fs.String("policy", defaultPolicyPath, "policy file path, read from the base ref")
	asJSON := fs.Bool("json", false, "print a JSON report")
	dir := fs.String("dir", ".", "repository directory")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if fs.NArg() > 0 {
		return usageError(stderr, lanesUsage, "unexpected argument %q", fs.Arg(0))
	}
	baseRef, err := resolveBaseFlag(*base, env)
	if err != nil {
		return usageError(stderr, lanesUsage, "%v", err)
	}
	rep, err := classifyLanes(ctx, *dir, baseRef, *head, *policyPath)
	if err != nil {
		return failf(stderr, "lanes", "%v", err)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return failf(stderr, "lanes", "%v", err)
		}
	} else {
		printLanes(stdout, rep)
	}
	if summary := env("GITHUB_STEP_SUMMARY"); summary != "" {
		if err := appendFile(summary, lanesMarkdown(rep)); err != nil {
			return failf(stderr, "lanes", "write the step summary: %v", err)
		}
	}
	return rep.ExitCode
}

// classifyLanes builds the report for base...head.
func classifyLanes(ctx context.Context, dir, base, head, policyPath string) (lanesReport, error) {
	r, err := resolveRange(ctx, dir, base, head)
	if err != nil {
		return lanesReport{}, err
	}
	pol, err := loadPolicy(ctx, dir, r.Base, policyPath)
	if err != nil {
		return lanesReport{}, err
	}
	paths, err := changedPaths(ctx, dir, r)
	if err != nil {
		return lanesReport{}, err
	}
	res := pol.Lanes.Classify(paths)
	rep := lanesReport{
		Version: 1, Lane: res.Lane, ExitCode: laneExitCode(res.Lane),
		Base: base, BaseCommit: r.Base, MergeBase: r.MergeBase, Head: head, HeadCommit: r.Head,
		Policy: pol.Source,
		Human:  nonNil(res.Human), SecondOpinion: nonNil(res.SecondOpinion), Auto: []string{},
	}
	for _, p := range paths {
		if pol.Lanes.Lane(p) == guardrails.LaneAuto {
			rep.Auto = append(rep.Auto, p)
		}
	}
	slices.Sort(rep.Auto)
	rep.Auto = slices.Compact(rep.Auto)
	return rep, nil
}

func laneExitCode(lane string) int {
	switch lane {
	case guardrails.LaneAuto:
		return exitOK
	case guardrails.LaneSecondOpinion:
		return exitSecondOpinion
	}
	return exitHuman // human, or anything unknown: fail closed
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func printLanes(w io.Writer, rep lanesReport) {
	_, _ = fmt.Fprintf(w, "lane: %s\n", rep.Lane)
	_, _ = fmt.Fprintf(w, "diff: %s...%s (merge base %s)\n", rep.Base, rep.Head, rep.MergeBase[:min(len(rep.MergeBase), 12)])
	_, _ = fmt.Fprintf(w, "policy: %s\n", rep.Policy)
	for _, l := range []struct {
		name  string
		paths []string
	}{{"human", rep.Human}, {"second_opinion", rep.SecondOpinion}, {"auto", rep.Auto}} {
		_, _ = fmt.Fprintf(w, "%s (%d):\n", l.name, len(l.paths))
		for _, p := range l.paths {
			_, _ = fmt.Fprintf(w, "  %s\n", displayPath(p))
		}
	}
}

// lanesMarkdown is the GitHub step summary.
func lanesMarkdown(rep lanesReport) string {
	var b strings.Builder
	verdict := map[string]string{
		guardrails.LaneHuman:         "**human**: a maintainer must approve this pipeline change",
		guardrails.LaneSecondOpinion: "**second_opinion**: a reviewer other than the writer must approve",
		guardrails.LaneAuto:          "**auto**: no pipeline file changed",
	}[rep.Lane]
	fmt.Fprintf(&b, "### referee lanes\n\nLane: %s\n\nPolicy: `%s` (read from the base ref)\n\n", verdict, mdCode(rep.Policy))
	for _, l := range []struct {
		name  string
		paths []string
	}{{"human", rep.Human}, {"second_opinion", rep.SecondOpinion}, {"auto", rep.Auto}} {
		fmt.Fprintf(&b, "<details%s><summary>%s (%d)</summary>\n\n", map[bool]string{true: " open", false: ""}[l.name != "auto" && len(l.paths) > 0], l.name, len(l.paths))
		for _, p := range l.paths {
			fmt.Fprintf(&b, "- `%s`\n", mdCode(p))
		}
		b.WriteString("\n</details>\n\n")
	}
	return b.String()
}

// displayPath makes a path safe to print on one terminal line.
func displayPath(p string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, p)
}

// mdCode makes a string safe inside a Markdown code span.
func mdCode(s string) string { return strings.ReplaceAll(displayPath(s), "`", "'") }

// appendFile appends text to path (the step summary file).
func appendFile(path, text string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, text); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
