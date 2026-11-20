package referee

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
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
  test_path_ignored     a test file excluded (testPathIgnorePatterns, exclude)

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
	rep.Findings = append(rep.Findings, deletedTests(status)...)
	patch, err := git(ctx, dir, "-c", "core.quotePath=false", "diff", "-U0", "--no-renames", "--no-color", "--no-ext-diff", "--no-textconv",
		"--src-prefix=a/", "--dst-prefix=b/", r.MergeBase, r.Head, "--")
	if err != nil {
		return auditReport{}, err
	}
	found, err := auditPatch(patch)
	if err != nil {
		return auditReport{}, err
	}
	rep.Findings = append(rep.Findings, found...)
	if len(rep.Findings) > 0 {
		rep.ExitCode = exitHuman
	}
	return rep, nil
}

// deletedTests reads `git diff --name-status -z --no-renames`: a test file
// that is deleted (a rename is a deletion plus an addition) is a finding.
func deletedTests(status []byte) []finding {
	var out []finding
	fields := bytes.Split(status, []byte{0})
	for i := 0; i+1 < len(fields); i += 2 {
		st, p := string(fields[i]), string(fields[i+1])
		if st == "D" && isTestFile(p) {
			out = append(out, finding{Rule: "test_deleted", Path: p, Message: "test file deleted or renamed"})
		}
	}
	return out
}

// hunk is one -U0 hunk's removed and added lines.
type hunk struct {
	firstNew       int
	removed, added []string
}

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// auditPatch applies the line rules and the threshold rule to a -U0 patch.
func auditPatch(patch []byte) ([]finding, error) {
	var out []finding
	var file string
	var cur *hunk
	flush := func() {
		if cur != nil && file != "" {
			for _, rule := range hunkRules {
				out = append(out, rule(file, *cur)...)
			}
		}
		cur = nil
	}
	sc := bufio.NewScanner(bytes.NewReader(patch))
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			file = ""
		case cur == nil && strings.HasPrefix(line, "+++ "):
			file = patchPath(strings.TrimPrefix(line, "+++ "))
		case strings.HasPrefix(line, "@@ "):
			flush()
			m := hunkHeader.FindStringSubmatch(line)
			if m == nil {
				return nil, fmt.Errorf("unexpected hunk header %q", line)
			}
			n, _ := strconv.Atoi(m[1])
			cur = &hunk{firstNew: n}
		case cur != nil && strings.HasPrefix(line, "-"):
			cur.removed = append(cur.removed, line[1:])
		case cur != nil && strings.HasPrefix(line, "+"):
			text := line[1:]
			lineNo := cur.firstNew + len(cur.added)
			cur.added = append(cur.added, text)
			if file == "" {
				continue
			}
			for _, r := range lineRules {
				if r.Files(file) && r.match(text) {
					out = append(out, finding{Rule: r.ID, Path: file, Line: lineNo, Message: r.Message, Text: text})
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read diff: %w", err)
	}
	flush()
	return out, nil
}

// patchPath is the repository path of a "+++ b/path" line ("" for
// /dev/null: a deleted file has no added lines).
func patchPath(s string) string {
	s = strings.TrimSuffix(s, "\t")
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			s = u
		}
	}
	if s == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(s, "b/")
}

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
