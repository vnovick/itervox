package referee

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/vnovick/itervox/internal/guardrails"
)

const auditUsage = `usage: referee audit --base <ref> [--head HEAD] [--json] [--dir .]

Lists changes in the diff from the merge base of <base> and <head> to <head>
that weaken what CI checks:

  test_deleted          a test file (incl. a Detox *.e2e.ts) deleted or renamed
  go_skip               t.Skip / t.SkipNow / t.Skipf added in a _test.go file
  js_skip               it.skip / test.skip / describe.skip / xit( added
  js_only               it.only / test.only / describe.only added
  py_skip               @pytest.mark.skip(if) / @unittest.skip added
  coverage_lowered      a coverage threshold lowered in a common config file
  ci_continue_on_error  continue-on-error: true (or GitLab allow_failure: true)
  ci_or_true            "|| true" added in a CI workflow
  git_no_verify         git commit/push --no-verify, or git commit -n, in a script

TypeScript, lint and JS test runners:

  ts_ignore             // @ts-ignore added in a JS/TS file
  ts_expect_error       // @ts-expect-error added outside a test file
  ts_nocheck            // @ts-nocheck added
  ts_strict_off         strict, noImplicitAny, strictNullChecks, noEmitOnError
                        ... set to false (noCheck to true) in tsconfig*.json
  eslint_disable        eslint-disable-line / eslint-disable-next-line added
  eslint_disable_file   /* eslint-disable */ added
  eslint_rule_off       a rule set to "off" or 0 in an ESLint config
  eslint_max_warnings   eslint --max-warnings raised, made -1 or dropped
  flow_suppress         $FlowFixMe / $FlowIgnore / @noflow added
  check_or_true         "|| true" after a lint, type or test command in a script
  script_stubbed        a package.json test/lint/typecheck script set to true,
                        exit 0 or echo
  test_pass_with_no_tests  --passWithNoTests / passWithNoTests: true added
  snapshot_update       jest/vitest -u, --updateSnapshot, --ci=false in CI
  test_path_ignored     a test file excluded (testPathIgnorePatterns, test
                        exclude) or testMatch narrowed; configs read whole

Only added lines are read, and comment lines are skipped (except by the
rules whose construct is a comment). --base defaults to
origin/$GITHUB_BASE_REF in a pull_request workflow.

Exit codes: 0 no findings, 20 findings, 2 usage or error.`

type auditReport struct {
	Version    int       `json:"version"`
	ExitCode   int       `json:"exit_code"`
	Base       string    `json:"base"`
	MergeBase  string    `json:"merge_base"`
	Head       string    `json:"head"`
	HeadCommit string    `json:"head_commit"`
	Findings   []finding `json:"findings"`
}

func auditCmd(ctx context.Context, args []string, env envFunc, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("referee audit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprintln(stderr, auditUsage) }
	base := fs.String("base", "", "base ref the change merges into")
	head := fs.String("head", "HEAD", "head ref under review")
	asJSON := fs.Bool("json", false, "print a JSON report")
	dir := fs.String("dir", ".", "repository directory")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if fs.NArg() > 0 {
		return usageError(stderr, auditUsage, "unexpected argument %q", fs.Arg(0))
	}
	baseRef, err := resolveBaseFlag(*base, env)
	if err != nil {
		return usageError(stderr, auditUsage, "%v", err)
	}
	rep, err := audit(ctx, *dir, baseRef, *head)
	if err != nil {
		return failf(stderr, "audit", "%v", err)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return failf(stderr, "audit", "%v", err)
		}
	} else {
		printAudit(stdout, rep)
	}
	if summary := env("GITHUB_STEP_SUMMARY"); summary != "" {
		if err := appendFile(summary, auditMarkdown(rep)); err != nil {
			return failf(stderr, "audit", "write the step summary: %v", err)
		}
	}
	return rep.ExitCode
}

func audit(ctx context.Context, dir, base, head string) (auditReport, error) {
	r, err := resolveRange(ctx, dir, base, head)
	if err != nil {
		return auditReport{}, err
	}
	rep := auditReport{Version: 1, Base: base, MergeBase: r.MergeBase, Head: head, HeadCommit: r.Head, Findings: []finding{}}
	status, err := git(ctx, dir, "diff", "--name-status", "-z", "--no-renames", "--no-ext-diff", r.MergeBase, r.Head, "--")
	if err != nil {
		return auditReport{}, err
	}
	patch, err := git(ctx, dir, "-c", "core.quotePath=false", "diff", "-U"+strconv.Itoa(guardrails.AuditContext), "--no-renames", "--no-color", "--no-ext-diff", "--no-textconv",
		"--src-prefix=a/", "--dst-prefix=b/", r.MergeBase, r.Head, "--")
	if err != nil {
		return auditReport{}, err
	}
	found, err := guardrails.AuditDiff(ctx, dir, r.MergeBase, r.Head, status, patch)
	if err != nil {
		return auditReport{}, err
	}
	rep.Findings = append(rep.Findings, found...)
	if len(rep.Findings) > 0 {
		rep.ExitCode = exitHuman
	}
	return rep, nil
}

// The rules and the patch reader live in internal/guardrails (the daemon
// shares them); this name keeps the referee's code and tests readable.
type finding = guardrails.AuditFinding

func printAudit(w io.Writer, rep auditReport) {
	if len(rep.Findings) == 0 {
		_, _ = fmt.Fprintf(w, "audit: no findings (%s...%s)\n", rep.Base, rep.Head)
		return
	}
	_, _ = fmt.Fprintf(w, "audit: %d finding(s) that weaken checks (%s...%s)\n", len(rep.Findings), rep.Base, rep.Head)
	for _, f := range rep.Findings {
		loc := displayPath(f.Path)
		if f.Line > 0 {
			loc += ":" + strconv.Itoa(f.Line)
		}
		_, _ = fmt.Fprintf(w, "  %s  [%s] %s\n", loc, f.Rule, f.Message)
		if f.Text != "" {
			_, _ = fmt.Fprintf(w, "      %s\n", clip(displayPath(strings.TrimSpace(f.Text)), 200))
		}
	}
}

func auditMarkdown(rep auditReport) string {
	var b strings.Builder
	b.WriteString("### referee audit\n\n")
	if len(rep.Findings) == 0 {
		b.WriteString("No changes that weaken checks.\n\n")
		return b.String()
	}
	fmt.Fprintf(&b, "%d change(s) that weaken checks:\n\n", len(rep.Findings))
	for _, f := range rep.Findings {
		loc := mdCode(f.Path)
		if f.Line > 0 {
			loc += ":" + strconv.Itoa(f.Line)
		}
		fmt.Fprintf(&b, "- `%s` **%s**: %s\n", loc, f.Rule, f.Message)
	}
	b.WriteString("\n")
	return b.String()
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
