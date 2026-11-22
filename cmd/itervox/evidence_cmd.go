package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/server"
)

// evidence_cmd.go — `itervox evidence <issue> [--json]`: the issue's
// evidence report (GET /api/v1/issues/{identifier}/evidence) as text, or
// the response itself with --json. A client of the daemon's API like the
// other terminal commands: it reads no stdin and changes no terminal mode.
// The daemon has already redacted the report for the caller's token.

// evidenceCommand prints identifier's evidence report.
func evidenceCommand(ctx context.Context, c *daemonClient, identifier string, asJSON bool, out io.Writer) error {
	var rep server.EvidenceReport
	var raw json.RawMessage
	if err := c.request(ctx, http.MethodGet, "/issues/"+url.PathEscape(identifier)+"/evidence", nil, &raw); err != nil {
		return err
	}
	if asJSON {
		_, err := fmt.Fprintln(out, strings.TrimSpace(string(raw)))
		return err
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		return fmt.Errorf("the daemon's evidence report could not be read: %w", err)
	}
	_, err := io.WriteString(out, evidenceText(rep))
	return err
}

// evidenceText renders the report: what ran, what was checked, who
// approved, the PRs.
func evidenceText(rep server.EvidenceReport) string {
	var b strings.Builder
	line := func(format string, args ...any) { _, _ = fmt.Fprintf(&b, format+"\n", args...) }
	title := rep.Identifier
	if rep.Title != "" {
		title += " — " + rep.Title
	}
	line("Evidence for %s (as %s)", title, rep.Scope)

	line("\nRuns (%d)", len(rep.Runs))
	if len(rep.Runs) == 0 {
		line("  none recorded")
	}
	for _, r := range rep.Runs {
		line("  %s %s  %s  %s", evShortTime(r.StartedAt), r.Kind, evOr(r.Profile, "(default)"), r.Status)
		agent := evJoin(r.Backend, r.ProfileModel, r.WorkerHost)
		line("    run %s  %s  %d turns  %s", evOr(r.RunID, "-"), evOr(agent, "-"), r.TurnCount, evDuration(r.ElapsedMs))
		if r.RetryAttempt > 0 {
			line("    retry attempt %d", r.RetryAttempt)
		}
		if r.PromptDigest != "" {
			line("    prompt %s", evShort(r.PromptDigest, 12))
		}
		if r.RulesHash != "" || len(r.RuleIDs) > 0 {
			line("    rules %s [%s]", evShort(r.RulesHash, 12), strings.Join(r.RuleIDs, ", "))
		}
		if len(r.MemoryIDs) > 0 {
			line("    memory [%s]", strings.Join(r.MemoryIDs, ", "))
		}
		if r.Playbook != "" {
			line("    playbook %s", r.Playbook)
		}
		if r.ReviewVerdict != "" {
			line("    verdict %s", r.ReviewVerdict)
		}
		if n := len(r.Steers); n > 0 || r.SteersDropped > 0 {
			line("    steering: %d delivered (%d older dropped)", n, r.SteersDropped)
			for _, s := range r.Steers {
				if s.Text != "" {
					line("      %s %s: %s", evShortTime(s.At), s.Delivery, s.Text)
				}
			}
		}
		if r.Checks != nil {
			evGateLine(line, "evidence gate", r.Checks.Evidence)
			evGateLine(line, "signed checks", r.Checks.Signed)
		}
		if r.PRURL != "" {
			line("    PR %s", r.PRURL)
		}
	}
	if rep.Retry != nil {
		line("  retry %d due %s", rep.Retry.Attempt, evShortTime(rep.Retry.DueAt))
	}

	line("\nChecks")
	checks := 0
	for _, sr := range rep.SignedChecks {
		checks++
		state := "verified"
		if !sr.Verified {
			state = "NOT verified"
		}
		if !sr.Sealed {
			state += ", no seal"
		}
		line("  signed %s (%s): %s", sr.AttestRunID, evOr(sr.Profile, "-"), state)
		for _, c := range sr.Checks {
			result := "ok"
			switch {
			case c.TimedOut:
				result = "timed out"
			case !c.Passed:
				result = fmt.Sprintf("failed (exit %d)", c.ExitCode)
			}
			line("    %s: %s, %s", c.Name, result, evDuration(c.DurationMs))
		}
	}
	for _, v := range rep.Reviews.Verdicts {
		checks++
		line("  review %s: %s on %s (%d reasons, %d line comments)", v.Profile, v.Verdict, evOr(v.Backend, "-"), v.Reasons, v.Comments)
	}
	if o := rep.Reviews.Outcome; o != nil {
		decision := "passed"
		if o.Blocked {
			decision = "blocked"
		}
		line("  review quorum %s: %s (%d approve, %d block)", evOr(o.Quorum, "-"), decision, o.Approvals, o.Blocks)
	}
	for _, g := range rep.GuardrailBlocks {
		checks++
		line("  guardrail refused %s at %s", g.Kind, evShortTime(g.At))
	}
	if l := rep.PushLane; l != nil {
		checks++
		trunc := ""
		if l.Truncated {
			trunc = fmt.Sprintf(", %d kept", l.PathsKept)
		}
		line("  push lane %s: %d files%s (writers: %s)", l.Lane, l.PathsTotal, trunc, evOr(strings.Join(l.WriterBackends, ", "), "-"))
	}
	if checks == 0 && rep.Reviews.Outcome == nil {
		line("  none recorded")
	}

	line("\nApprovals (%d)", len(rep.Approvals))
	if len(rep.Approvals) == 0 {
		line("  none recorded")
	}
	for _, a := range rep.Approvals {
		what := a.Kind
		if a.Push {
			what += " push"
		}
		who := evJoin(a.Via, a.ActorName, a.Actor)
		line("  %s %s %s  %s", evShortTime(a.At), what, a.Decision, who)
	}

	line("\nPull requests (%d)", len(rep.PullRequests))
	if len(rep.PullRequests) == 0 {
		line("  none recorded")
	}
	for _, pr := range rep.PullRequests {
		line("  %s  %s", pr.URL, pr.State)
	}
	if l := rep.Limits; l.FeedSince != nil {
		line("\nApprovals, refusals and merges since %s (the feed keeps %d h).", evShortTime(*l.FeedSince), l.FeedMaxAgeHours)
	}
	return b.String()
}

func evGateLine(line func(string, ...any), name string, g *server.EvidenceGate) {
	if g == nil {
		return
	}
	result := "passed"
	if !g.Passed {
		result = "FAILED"
		if len(g.Failed) > 0 {
			result += ": " + strings.Join(g.Failed, ", ")
		}
	}
	line("    %s [%s]: %s", name, strings.Join(g.Required, ", "), result)
}

func evShortTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04Z")
}

func evDuration(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).Round(time.Second).String()
}

func evShort(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func evOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// evJoin joins the non-empty parts with " · ".
func evJoin(parts ...string) string {
	kept := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, " · ")
}
