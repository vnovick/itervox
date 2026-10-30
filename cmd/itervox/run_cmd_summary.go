package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/logging"
)

// runSummary is what `itervox run` reports: the Markdown summary on stdout
// (and $GITHUB_STEP_SUMMARY), or this object with --json. Every free text
// in it is redacted before it is printed.
type runSummary struct {
	Issue    string `json:"issue"`
	Title    string `json:"title,omitempty"`
	URL      string `json:"url,omitempty"`
	Status   string `json:"status"` // pr_opened|succeeded|nothing_to_do|needs_input|budget_exceeded|failed|config_error
	ExitCode int    `json:"exit_code"`
	Message  string `json:"message,omitempty"`
	Branch   string `json:"branch,omitempty"`
	PRURL    string `json:"pr_url,omitempty"`
	Turns    int    `json:"turns"`
	// InputTokens and OutputTokens cover every run of the issue in this
	// invocation, reviewers included.
	InputTokens  int      `json:"input_tokens"`
	OutputTokens int      `json:"output_tokens"`
	CostUSD      *float64 `json:"cost_usd,omitempty"`
	// Reviewers are the reviewer verdicts and ReviewOutcome the closed
	// quorum ("passed" or "blocked").
	Reviewers     []runReviewer `json:"reviewers,omitempty"`
	ReviewOutcome string        `json:"review_outcome,omitempty"`
	// Question is what the run asks on exit 3; ApprovalCommands the
	// guardrail approval commands it was refused, when that is why.
	Question         string   `json:"question,omitempty"`
	ApprovalCommands []string `json:"approval_commands,omitempty"`
	Warnings         []string `json:"warnings,omitempty"`
	DurationMs       int64    `json:"duration_ms"`
	// Summary is the Markdown summary (in the JSON output only).
	Summary string `json:"summary,omitempty"`

	// asked is set when this invocation's run asked the question (not when
	// the question was already open on the tracker): only then is it
	// committed as a handoff.
	asked bool
}

// runReviewer is one reviewer's verdict.
type runReviewer struct {
	Profile string   `json:"profile"`
	Verdict string   `json:"verdict"`
	Reasons []string `json:"reasons,omitempty"`
}

// newSummary starts a summary for the session's issue.
func (s *runSession) newSummary() runSummary {
	sum := runSummary{Issue: s.opts.issue}
	if s.issue.Identifier != "" {
		sum.Issue, sum.Title = s.issue.Identifier, s.issue.Title
		if s.issue.URL != nil {
			sum.URL = *s.issue.URL
		}
	}
	return sum
}

// finish sets the outcome on sum.
func (s *runSession) finish(sum runSummary, status string, code int, message string) runSummary {
	sum.Status, sum.ExitCode, sum.Message = status, code, message
	sum.DurationMs = time.Since(s.started).Milliseconds()
	return sum
}

// redacted is sum with every free text redacted.
func (sum runSummary) redacted() runSummary {
	r := logging.RedactString
	sum.Title, sum.Message, sum.Question = r(sum.Title), r(sum.Message), r(sum.Question)
	for i := range sum.Warnings {
		sum.Warnings[i] = r(sum.Warnings[i])
	}
	for i := range sum.Reviewers {
		for j := range sum.Reviewers[i].Reasons {
			sum.Reviewers[i].Reasons[j] = r(sum.Reviewers[i].Reasons[j])
		}
	}
	return sum
}

// writeRunOutput prints the summary (Markdown, or JSON with --json) and
// appends the Markdown to $GITHUB_STEP_SUMMARY when it is set.
func writeRunOutput(sum runSummary, opts runOptions, stdout, stderr io.Writer) {
	sum = sum.redacted()
	md := renderRunMarkdown(sum)
	if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
		if err := appendStepSummary(path, md+"\n"); err != nil {
			_, _ = fmt.Fprintf(stderr, "itervox run: writing $GITHUB_STEP_SUMMARY: %v\n", err)
		}
	}
	if opts.asJSON {
		sum.Summary = md
		enc := json.NewEncoder(stdout)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(sum)
		return
	}
	_, _ = fmt.Fprintln(stdout, md)
}

func appendStepSummary(path, text string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// runStatusLabels are the headline of each status.
var runStatusLabels = map[string]string{
	"pr_opened":       "✅ Pull request opened",
	"succeeded":       "✅ Succeeded",
	"nothing_to_do":   "➖ Nothing to do",
	"needs_input":     "❓ Needs input",
	"budget_exceeded": "💸 Budget exceeded",
	"failed":          "❌ Failed",
	"config_error":    "⚙️ Configuration error",
}

// renderRunMarkdown is the Markdown summary.
func renderRunMarkdown(sum runSummary) string {
	var b strings.Builder
	issue := sum.Issue
	if sum.URL != "" {
		issue = "[" + sum.Issue + "](" + sum.URL + ")"
	}
	fmt.Fprintf(&b, "## itervox run: %s\n\n", issue)
	if sum.Title != "" {
		fmt.Fprintf(&b, "**%s**\n\n", sum.Title)
	}
	fmt.Fprintf(&b, "**%s** (exit %d)", runStatusLabels[sum.Status], sum.ExitCode)
	if sum.Message != "" {
		fmt.Fprintf(&b, " — %s", sum.Message)
	}
	b.WriteString("\n\n| | |\n|---|---|\n")
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "| %s | %s |\n", k, strings.ReplaceAll(v, "|", "\\|"))
		}
	}
	if sum.Branch != "" {
		row("Branch", "`"+sum.Branch+"`")
	}
	row("Pull request", sum.PRURL)
	row("Turns", fmt.Sprint(sum.Turns))
	row("Tokens", fmt.Sprintf("%d in / %d out", sum.InputTokens, sum.OutputTokens))
	if sum.CostUSD != nil {
		row("Cost", fmt.Sprintf("$%.4f", *sum.CostUSD))
	} else {
		row("Cost", "not reported")
	}
	if sum.ReviewOutcome != "" {
		row("Review", sum.ReviewOutcome)
	}
	row("Duration", (time.Duration(sum.DurationMs) * time.Millisecond).Round(time.Second).String())
	if len(sum.Reviewers) > 0 {
		b.WriteString("\n### Reviewer verdicts\n\n")
		for _, r := range sum.Reviewers {
			fmt.Fprintf(&b, "- **%s**: %s", r.Profile, r.Verdict)
			if len(r.Reasons) > 0 {
				fmt.Fprintf(&b, " — %s", strings.Join(r.Reasons, "; "))
			}
			b.WriteString("\n")
		}
	}
	if sum.ExitCode == runExitNeedsInput {
		b.WriteString("\n### Question\n\n")
		for _, line := range strings.Split(strings.TrimSpace(sum.Question), "\n") {
			b.WriteString("> " + line + "\n")
		}
		b.WriteString("\nAnswer with an issue comment `/itervox <answer>`, or run `itervox run --issue " + sum.Issue + " --answer \"…\"`.\n")
		for _, c := range sum.ApprovalCommands {
			b.WriteString("Allow `" + c + "` for one run with `/itervox approve " + c + "`, or `itervox run --issue " + sum.Issue + " --approve \"" + c + "\"`.\n")
		}
	}
	if len(sum.Warnings) > 0 {
		b.WriteString("\n### Warnings\n\n")
		for _, w := range sum.Warnings {
			b.WriteString("- " + w + "\n")
		}
	}
	return b.String()
}
