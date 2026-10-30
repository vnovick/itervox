package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/guardrails"
	"github.com/vnovick/itervox/internal/logging"
)

// OneShot is what `itervox run --issue` (#252) changes about a run: the
// orchestrator, its event loop and its workers are the daemon's, and
// cmd/itervox feeds them exactly one issue. Everything here is nil or empty
// in the daemon, which then behaves exactly as before.
//
// Set once with SetOneShot before Run; read-only afterwards except Resume,
// which the event loop consumes on the issue's first dispatch.
type OneShot struct {
	// OpenPR pushes a successful implementer run's branch and opens its pull
	// request when the run found none open for the branch (the daemon leaves
	// opening the PR to the agent). It returns the PR's URL, or "" when
	// there was nothing to open (no commits beyond the base). Called on the
	// worker goroutine, before the PR link is commented on the issue.
	OpenPR func(ctx context.Context, wsPath, branch string, issue domain.Issue) (string, error)
	// Resume, when set, continues the issue's previous run: its first
	// dispatch starts a fresh agent session told the question the previous
	// run asked (InputContext) and the operator's answer (UserMessage).
	Resume *ResumeContext
	// ReplyHint replaces the footer of the question comment an
	// input-required exit posts, telling a human how to answer in CI.
	ReplyHint string
	// PreApprovedCommands are approval commands (guardrails.approval_commands)
	// the operator approved for this run; they run without asking. Every
	// other approval command is refused (there is no approval channel), and
	// the run then ends input-required naming it.
	PreApprovedCommands []string
	// OnApprovalRefused is told the commands a run's shims refused for want
	// of an approval, when the run ends input-required because of them.
	// Called on the worker goroutine.
	OnApprovalRefused func(identifier string, commands []string)
}

// SetOneShot puts the orchestrator in `itervox run` mode. Must be called
// before Run.
func (o *Orchestrator) SetOneShot(s OneShot) {
	o.oneShot = &s
}

// takeOneShotResume returns the run-mode resume context for the issue's
// first implementer dispatch (nil for a reviewer run) and forgets it. Event
// loop only.
func (o *Orchestrator) takeOneShotResume(reviewerRun bool) *ResumeContext {
	if o.oneShot == nil || o.oneShot.Resume == nil || reviewerRun {
		return nil
	}
	r := *o.oneShot.Resume
	o.oneShot.Resume = nil
	return &r
}

// oneShotAnswers reports whether this run brings the answer to the issue's
// open question (`itervox run --answer` / `--approve`), so the question
// still unanswered on the tracker does not hold the dispatch. Event loop.
func (o *Orchestrator) oneShotAnswers() bool {
	return o.oneShot != nil && o.oneShot.Resume != nil
}

// openOneShotPR runs OneShot.OpenPR for a successful implementer run that
// found no open PR, and returns the PR's URL ("" when there is none or no
// hook). A failure is logged: the run still succeeds, without a PR link.
// Worker goroutine.
func (o *Orchestrator) openOneShotPR(ctx context.Context, wsPath, branch string, issue domain.Issue) string {
	if o.oneShot == nil || o.oneShot.OpenPR == nil {
		return ""
	}
	prCtx, cancel := context.WithTimeout(ctx, postRunTimeout)
	defer cancel()
	url, err := o.oneShot.OpenPR(prCtx, wsPath, branch, issue)
	if err != nil {
		slog.Warn("worker: opening the pull request failed",
			"issue_identifier", issue.Identifier, "branch", branch, "error", logging.RedactString(err.Error()))
		if o.logBuf != nil {
			o.logBuf.Add(issue.Identifier, makeBufLine("WARN",
				"worker: opening the pull request failed: "+logging.RedactString(err.Error())))
		}
		return ""
	}
	return url
}

// oneShotRefusedApprovals reports the approval commands this run's shims
// refused (run mode only: the daemon has an approval channel) and tells
// OnApprovalRefused. Worker goroutine.
func (o *Orchestrator) oneShotRefusedApprovals(identifier string, guard *guardrails.Runtime) []string {
	if o.oneShot == nil || guard == nil {
		return nil
	}
	cmds := guard.RefusedApprovals()
	if len(cmds) > 0 && o.oneShot.OnApprovalRefused != nil {
		o.oneShot.OnApprovalRefused(identifier, cmds)
	}
	return cmds
}

// oneShotApprovalContext is the question an input-required exit asks when
// the run's approval commands were refused.
func oneShotApprovalContext(commands []string) string {
	var b strings.Builder
	b.WriteString("Approval needed. The agent tried to run ")
	for i, c := range commands {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("`" + c + "`")
	}
	b.WriteString(", which guardrails.approval_commands only allows with a human approval. " +
		"Nothing ran; this run stopped instead of waiting.")
	return b.String()
}

// inputRequiredCommentBody is the body of an input-required question
// comment: the daemon's, with the run-mode reply hint as its footer when
// one is set.
func (o *Orchestrator) inputRequiredCommentBody(entry *InputRequiredEntry) string {
	if o.oneShot == nil || o.oneShot.ReplyHint == "" || entry == nil {
		return buildInputRequiredComment(entry, o.InlineInputCfg())
	}
	return fmt.Sprintf("%s\n\n%s\n\n---\n%s", itervoxCommentPrefix, boundInputRequiredContext(entry.Context), o.oneShot.ReplyHint)
}

// HandoffPathFor is the handoff file path (relative to the workspace root)
// for a run that started at t under role: the name every handoff in
// .itervox/handoff/ gets, so it sorts with the agents' own.
func HandoffPathFor(t time.Time, role string) string {
	return handoffPathFor(handoffRunTimestamp(t), role)
}
