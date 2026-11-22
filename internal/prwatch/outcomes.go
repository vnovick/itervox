package prwatch

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
)

// PR outcomes (cost and outcome visibility): whether a PR an itervox run
// opened was merged or closed unmerged. The orchestrator keeps the ledger;
// the watcher only observes, by poll (one conditional GET per tracked PR per
// pass, answered 304 while the PR is unchanged) and by the pull_request "closed" /
// "reopened" webhook deliveries it already receives.

// Outcome states, as tracked and as reported to Config.Outcome.
const (
	OutcomeOpen   = "open"
	OutcomeMerged = "merged"
	OutcomeClosed = "closed" // closed without merging
)

// maxOutcomePollsPerPass bounds the PR reads one PollOutcomes pass makes.
const maxOutcomePollsPerPass = 100

// kindOutcome is the internal Event kind the webhook queue carries an
// outcome under; Handler.Run hands it to Config.Outcome, never to Emit.
const kindOutcome = "pr_outcome"

// Tracked is a PR whose outcome can still change: open, or closed without
// merging (it can be reopened). Merged PRs are not tracked here.
type Tracked struct {
	Managed
	State string // OutcomeOpen | OutcomeClosed
}

// Outcome is one observed change of a tracked PR's state.
type Outcome struct {
	PRURL      string
	Identifier string
	State      string // Outcome*
	At         time.Time
	Source     string // SourcePoll | SourceWebhook
}

// PollOutcomes reads each tracked PR and reports the ones whose state
// differs from the tracked one. It keeps no memory of what it reported: a PR
// is reported again on the next pass while the ledger has not taken the
// change, so a report the orchestrator dropped is not lost.
func (w *Watcher) PollOutcomes(ctx context.Context) {
	if w.cfg.TrackedOutcomes == nil || w.cfg.Outcome == nil || w.cfg.Client == nil {
		return
	}
	tracked := w.cfg.TrackedOutcomes()
	if len(tracked) > maxOutcomePollsPerPass {
		slog.Debug("pr outcomes: more tracked PRs than one pass reads; the rest wait for the next pass",
			"tracked", len(tracked), "per_pass", maxOutcomePollsPerPass)
		tracked = tracked[:maxOutcomePollsPerPass]
	}
	for _, t := range tracked {
		if ctx.Err() != nil {
			return
		}
		ref, ok := ParsePRURL(t.URL)
		if !ok {
			continue // not a GitHub PR URL: no outcome from here
		}
		pr, err := w.cfg.Client.PR(ctx, ref)
		if err != nil {
			if !errors.Is(err, ErrNotFound) && ctx.Err() == nil {
				slog.Debug("pr outcomes: poll failed", "pr_url", t.URL, "error", err)
			}
			continue
		}
		o := outcomeOf(t.Managed, pr.State, pr.Merged, pr.MergedAt, pr.ClosedAt, SourcePoll)
		if o.State != t.State {
			w.cfg.Outcome(ctx, o)
		}
	}
}

// outcomeOf maps a PR's state to an Outcome.
func outcomeOf(m Managed, state string, merged bool, mergedAt, closedAt *time.Time, source string) Outcome {
	o := Outcome{PRURL: m.URL, Identifier: m.Identifier, State: OutcomeOpen, Source: source}
	switch {
	case merged:
		o.State = OutcomeMerged
		if mergedAt != nil {
			o.At = *mergedAt
		}
	case strings.ToLower(state) == "closed":
		o.State = OutcomeClosed
		if closedAt != nil {
			o.At = *closedAt
		}
	}
	return o
}

// trackedFor returns the tracked PR for a URL.
func (w *Watcher) trackedFor(prURL string) (Tracked, bool) {
	if w.cfg.TrackedOutcomes == nil || w.cfg.Outcome == nil || prURL == "" {
		return Tracked{}, false
	}
	for _, t := range w.cfg.TrackedOutcomes() {
		if strings.EqualFold(t.URL, prURL) {
			return t, true
		}
	}
	return Tracked{}, false
}

// webhookOutcome builds the outcome event of a pull_request "closed" or
// "reopened" delivery for a tracked PR whose state it changes.
func (w *Watcher) webhookOutcome(action string, pr whPR) (Event, bool) {
	if action != "closed" && action != "reopened" {
		return Event{}, false
	}
	t, ok := w.trackedFor(pr.HTMLURL)
	if !ok {
		return Event{}, false
	}
	state := "open"
	if action == "closed" {
		state = "closed"
	}
	o := outcomeOf(t.Managed, state, pr.Merged, pr.MergedAt, pr.ClosedAt, SourceWebhook)
	if o.State == t.State {
		return Event{}, false
	}
	return Event{Kind: kindOutcome, Identifier: o.Identifier, PRURL: o.PRURL, Outcome: o.State, OutcomeAt: o.At, Source: SourceWebhook}, true
}

// deliverOutcome hands a queued outcome event to Config.Outcome.
func (w *Watcher) deliverOutcome(ctx context.Context, ev Event) {
	if w.cfg.Outcome == nil {
		return
	}
	w.cfg.Outcome(ctx, Outcome{PRURL: ev.PRURL, Identifier: ev.Identifier, State: ev.Outcome, At: ev.OutcomeAt, Source: ev.Source})
}
