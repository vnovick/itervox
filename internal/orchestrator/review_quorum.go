package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/config"
	"github.com/vnovick/itervox/internal/domain"
)

// Review verdict values a reviewer may emit.
const (
	ReviewVerdictApprove = "approve"
	ReviewVerdictBlock   = "block"
)

// ReviewVerdictFileName is the file a reviewer writes its verdict to, inside
// the per-issue review directory. A file rather than an HTTP action because
// the existing agent-action surface is token-scoped and would need a new
// endpoint, whereas the handoff mechanism already establishes "agent writes a
// file at a documented path, orchestrator reads it after the run" as the way
// a worker returns structured output.
const ReviewVerdictFileName = "verdict.json"

// ReviewVerdict is one reviewer's judgement on one issue.
type ReviewVerdict struct {
	// Profile is the reviewer profile that produced this verdict. It is the
	// dedup key: a profile that somehow runs twice replaces its own earlier
	// verdict rather than getting two votes.
	Profile string `json:"profile"`
	// Verdict is ReviewVerdictApprove or ReviewVerdictBlock. Any
	// unrecognized value is treated as a block by NormalizeReviewVerdict —
	// see that function for why.
	Verdict string `json:"verdict"`
	// Reasons is the reviewer's rationale, surfaced to the operator. Not
	// interpreted by the quorum.
	Reasons []string `json:"reasons,omitempty"`
	// RecordedAt is when the orchestrator ingested the verdict.
	RecordedAt time.Time `json:"recordedAt"`
}

// NormalizeReviewVerdict maps a raw verdict string to one of the two accepted
// values, defaulting to block.
//
// Fail-closed is the only safe default here: a reviewer that crashed, wrote
// malformed JSON, or emitted a value we do not understand has NOT approved
// anything. Treating an unparseable verdict as approval would turn every
// reviewer bug into a silent auto-approve, which is the precise failure mode
// a review gate exists to prevent.
func NormalizeReviewVerdict(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), ReviewVerdictApprove) {
		return ReviewVerdictApprove
	}
	return ReviewVerdictBlock
}

// ReviewOutcome is the combined result of every reviewer that ran.
type ReviewOutcome struct {
	// Blocked is the gate decision.
	Blocked bool
	// Approvals / Blocks are the vote counts behind that decision.
	Approvals int
	Blocks    int
	// Quorum is the rule that produced it, echoed for operator-visible
	// explanation ("blocked 1/3 under any_block").
	Quorum string
}

// EvaluateReviewQuorum combines verdicts under the given quorum rule. It is a
// pure function.
//
// An empty verdict set is NOT blocked: no reviewer ran, so there is nothing to
// gate on. That is distinct from "reviewers ran and all approved", and callers
// that need to tell those apart should check len(verdicts) themselves — the
// caller in this codebase only dispatches the gate after at least one reviewer
// has completed.
func EvaluateReviewQuorum(verdicts []ReviewVerdict, quorum string) ReviewOutcome {
	out := ReviewOutcome{Quorum: quorum}
	for _, v := range verdicts {
		if NormalizeReviewVerdict(v.Verdict) == ReviewVerdictBlock {
			out.Blocks++
			continue
		}
		out.Approvals++
	}
	if len(verdicts) == 0 {
		return out
	}

	switch quorum {
	case config.ReviewQuorumUnanimous:
		// Every reviewer must block. One dissenting approval lets it through.
		out.Blocked = out.Blocks == len(verdicts)
	case config.ReviewQuorumMajority:
		out.Blocked = out.Blocks*2 > len(verdicts)
	default:
		// config.ReviewQuorumAnyBlock, and any unrecognized value. Config
		// validation normalizes unknown values at load time; defaulting to
		// the strictest rule here means a State assembled outside the loader
		// still fails closed.
		out.Blocked = out.Blocks > 0
	}
	return out
}

// ReviewerProfileChain returns the ordered reviewer profiles for a config,
// falling back to the single ReviewerProfile when the list form is unset.
// Blank entries are dropped so a stray "- " in YAML cannot dispatch a
// nameless reviewer.
func ReviewerProfileChain(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	source := cfg.Agent.ReviewerProfiles
	if len(source) == 0 {
		if cfg.Agent.ReviewerProfile == "" {
			return nil
		}
		source = []string{cfg.Agent.ReviewerProfile}
	}
	out := make([]string, 0, len(source))
	for _, p := range source {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	// Reviewer fan-out is DISABLED for the v0.2.1 release: the chain is
	// truncated to its first entry, so a multi-entry reviewer_profiles
	// behaves exactly like the long-standing single-reviewer path.
	//
	// Measured with the truncation removed, on a config setting
	// reviewer_profiles: [sec, corr] plus tracker.completion_state and a
	// workspace provider: chain=[sec corr] but only 2 RunTurn calls — the
	// worker and reviewer "sec". Reviewer "corr" never ran, and the log shows
	// "dispatching reviewer profile=sec" immediately followed by "workspace
	// auto-cleared". Two distinct defects produce that:
	//
	//  1. The chain never advances. advanceReviewChainForIssue is reached
	//     only from the TerminalSucceeded branch guarded by
	//     `liveEntry != nil && liveEntry.Kind == "reviewer"`. Setting
	//     completion_state makes the worker move the issue terminal, so
	//     ReconcileTrackerStates deletes the run from state.Running before
	//     the reviewer's own exit event arrives — liveEntry is nil by then,
	//     the guard never fires, and the quorum never closes.
	//  2. The workspace is auto-cleared while a reviewer is still live: the
	//     clear decision keys off runEligibleForAutoReview, which answers
	//     "would a FRESH review start?", not "is a review in progress?".
	//
	// Both are lifecycle problems between the review chain, terminal-state
	// reconciliation, and workspace clearing — not local bugs — so the chain
	// machinery is left intact and gated here rather than redesigned under
	// release pressure. Deleting this truncation re-enables the whole path.
	//
	// A reviewer_profiles-only config no longer fails to boot: the loader
	// promotes the first entry into reviewer_profile and warns
	// (config.normalizeReviewerProfiles), so such a config runs that one
	// reviewer instead of hard-failing startup.
	if len(out) > 1 {
		out = out[:1]
	}
	return out
}

// ReviewDir is the per-issue directory reviewers write verdicts into. It is
// namespaced by profile so sequential reviewers cannot overwrite each other.
func ReviewDir(workdir, identifier, profile string) string {
	return filepath.Join(workdir, ".itervox", "review", identifier, profile)
}

// ReadReviewVerdict loads and normalizes the verdict a reviewer wrote for one
// issue/profile pair.
//
// A missing file is NOT an error the caller should ignore — it means the
// reviewer finished without recording a judgement. The caller converts that
// into a blocking verdict (see recordReviewVerdict), consistent with the
// fail-closed rule in NormalizeReviewVerdict.
func ReadReviewVerdict(workdir, identifier, profile string, now time.Time) (ReviewVerdict, error) {
	path := filepath.Join(ReviewDir(workdir, identifier, profile), ReviewVerdictFileName)
	raw, err := os.ReadFile(path) //nolint:gosec // path is composed from daemon-controlled workdir + tracker identifier
	if err != nil {
		return ReviewVerdict{}, fmt.Errorf("orchestrator: read review verdict: %w", err)
	}
	var parsed ReviewVerdict
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return ReviewVerdict{}, fmt.Errorf("orchestrator: parse review verdict: %w", err)
	}
	parsed.Profile = profile
	parsed.Verdict = NormalizeReviewVerdict(parsed.Verdict)
	parsed.RecordedAt = now
	return parsed, nil
}

// AdvanceReviewChain records the verdict of the reviewer that just finished
// and reports what should happen next. It mutates only the maps on state, and
// is called from the event-loop goroutine.
//
// verdictErr carries the outcome of reading the reviewer's verdict file. A
// non-nil error becomes a BLOCKING verdict rather than being skipped: a
// reviewer that ran but recorded nothing has not approved anything, and
// silently dropping it would let a crashed reviewer shrink the quorum
// denominator until the gate passes by attrition.
//
// Returns the next reviewer profile to dispatch, or "" when the chain is
// complete — in which case the quorum outcome has been written to
// state.ReviewOutcomes.
func AdvanceReviewChain(
	state *State,
	identifier string,
	finishedProfile string,
	verdict ReviewVerdict,
	verdictErr error,
	chain []string,
	quorum string,
	now time.Time,
) (nextProfile string, done bool) {
	if verdictErr != nil {
		verdict = ReviewVerdict{
			Profile: finishedProfile,
			Verdict: ReviewVerdictBlock,
			Reasons: []string{"no parseable verdict recorded: " + verdictErr.Error()},
		}
	}
	verdict.Profile = finishedProfile
	verdict.Verdict = NormalizeReviewVerdict(verdict.Verdict)
	verdict.RecordedAt = now

	state.ReviewVerdicts[identifier] = UpsertReviewVerdict(state.ReviewVerdicts[identifier], verdict)

	next := state.ReviewChainIndex[identifier] + 1
	if next < len(chain) {
		state.ReviewChainIndex[identifier] = next
		return chain[next], false
	}

	state.ReviewOutcomes[identifier] = EvaluateReviewQuorum(state.ReviewVerdicts[identifier], quorum)
	delete(state.ReviewChainIndex, identifier)
	return "", true
}

// advanceReviewChainForIssue is the event-loop side of the reviewer chain: it
// reads the finished reviewer's verdict off the issue workspace, records it,
// and either dispatches the next reviewer or logs the closed quorum.
//
// No-ops when the chain has one entry or fewer, so single-reviewer setups —
// the existing behavior for every current config — are completely untouched
// by this path.
func (o *Orchestrator) advanceReviewChainForIssue(
	ctx context.Context,
	state State,
	issue domain.Issue,
	finishedProfile string,
	now time.Time,
) State {
	chain := ReviewerProfileChain(o.cfg)
	if len(chain) <= 1 {
		return state
	}
	if _, inFlight := state.ReviewChainIndex[issue.Identifier]; !inFlight {
		return state
	}

	var (
		verdict    ReviewVerdict
		verdictErr = errNoWorkspaceForReview
	)
	if o.workspace != nil {
		if wsPath := o.workspace.ResolvePath(issue.Identifier); wsPath != "" {
			verdict, verdictErr = ReadReviewVerdict(wsPath, issue.Identifier, finishedProfile, now)
		}
	}

	next, done := AdvanceReviewChain(
		&state, issue.Identifier, finishedProfile, verdict, verdictErr,
		chain, o.cfg.Agent.ReviewQuorum, now,
	)
	if !done {
		slog.Info("orchestrator: review chain advancing",
			"identifier", issue.Identifier, "finished", finishedProfile, "next", next)
		o.dispatchReviewerForIssue(ctx, &state, issue, next, now)
		return state
	}

	outcome := state.ReviewOutcomes[issue.Identifier]
	slog.Info("orchestrator: review quorum closed",
		"identifier", issue.Identifier,
		"blocked", outcome.Blocked,
		"approvals", outcome.Approvals,
		"blocks", outcome.Blocks,
		"quorum", outcome.Quorum)
	if o.logBuf != nil {
		o.logBuf.Add(issue.Identifier, makeBufLine("INFO", fmt.Sprintf(
			"review: quorum %s — %d approve / %d block → %s",
			outcome.Quorum, outcome.Approvals, outcome.Blocks,
			map[bool]string{true: "BLOCKED", false: "passed"}[outcome.Blocked])))
	}
	return state
}

// errNoWorkspaceForReview is the verdict-read error used when the issue has no
// resolvable workspace. It fails closed like any other unreadable verdict.
var errNoWorkspaceForReview = errors.New("orchestrator: no workspace path for review verdict")

// ResetReviewChain clears any prior review state for an issue so a re-review
// starts from an empty verdict set instead of inheriting stale votes from a
// previous run.
func ResetReviewChain(state *State, identifier string) {
	delete(state.ReviewVerdicts, identifier)
	delete(state.ReviewOutcomes, identifier)
	state.ReviewChainIndex[identifier] = 0
}

// UpsertReviewVerdict appends v to the issue's verdict list, replacing any
// existing verdict from the same profile so a re-run cannot double-vote.
func UpsertReviewVerdict(existing []ReviewVerdict, v ReviewVerdict) []ReviewVerdict {
	for i := range existing {
		if existing[i].Profile == v.Profile {
			existing[i] = v
			return existing
		}
	}
	return append(existing, v)
}
