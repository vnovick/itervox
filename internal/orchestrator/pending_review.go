package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/vnovick/itervox/internal/domain"
	"github.com/vnovick/itervox/internal/tracker"
)

// M4-close BH-M4-2 — reviews refused by a drain survive the restart.
//
// A worker that succeeds while the daemon drains triggers its auto-review
// (or the next reviewer of a chain), and dispatchReviewerForIssue refuses it:
// nothing new is admitted while draining. The review chain itself is not
// persisted, so the refusal used to lose the review for good — and because
// the success path skips the workspace auto-clear whenever a reviewer "will
// run", the worktree leaked too.
//
// The refusal now records a PendingReview in State.PendingReviews, persisted
// to pending_reviews.json. On the next start the marker is loaded, and every
// tick (while admission is open and a slot is free) resumePendingReviews
// fetches the issue and dispatches the reviewer. A multi-reviewer chain
// restarts from its first reviewer (the in-memory verdicts of the interrupted
// chain are gone, so the quorum is judged on fresh evidence). The marker is
// cleared the moment a reviewer for that issue starts, whatever started it.

// PendingReview is one reviewer dispatch refused while draining.
type PendingReview struct {
	IssueID    string    `json:"issue_id"`
	Identifier string    `json:"identifier"`
	Profile    string    `json:"profile"`
	QueuedAt   time.Time `json:"queued_at"`
	// IssueState is the tracker state when the review was refused
	// (CORE-174). A replay drops the review when the issue has since moved to
	// a DIFFERENT terminal state; a terminal completion_state the review was
	// queued in is expected. Empty in files written before CORE-174.
	IssueState string `json:"issue_state,omitempty"`
}

// SetPendingReviewsFile sets the path for persisting reviews refused during a
// drain. Must be called before Run.
func (o *Orchestrator) SetPendingReviewsFile(path string) {
	o.pendingReviewsFile = path
}

// LoadPendingReviewIdentifiers reads a pending_reviews.json file and returns
// the identifiers it holds. cmd/itervox uses it before Run so the startup
// terminal-workspace cleanup keeps the worktrees those reviews need. A
// missing or unreadable file yields an empty set.
func LoadPendingReviewIdentifiers(path string) map[string]struct{} {
	out := map[string]struct{}{}
	disk, err := readPendingReviews(path)
	if err != nil {
		return out
	}
	for id := range disk {
		out[id] = struct{}{}
	}
	return out
}

func readPendingReviews(path string) (map[string]PendingReview, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var disk map[string]PendingReview
	if err := json.Unmarshal(data, &disk); err != nil {
		return nil, err
	}
	return disk, nil
}

// loadPendingReviewsFromDisk pre-populates state.PendingReviews at startup. A
// missing file is normal; a corrupt one is logged and ignored.
func (o *Orchestrator) loadPendingReviewsFromDisk(state State) State {
	path := o.pendingReviewsFile
	if path == "" {
		return state
	}
	disk, err := readPendingReviews(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("orchestrator: failed to load pending reviews file", "path", path, "error", err)
		}
		return state
	}
	if state.PendingReviews == nil {
		state.PendingReviews = make(map[string]PendingReview, len(disk))
	}
	for id, pr := range disk {
		if id == "" || pr.IssueID == "" || pr.Profile == "" {
			continue
		}
		pr.Identifier = id
		state.PendingReviews[id] = pr
	}
	if len(state.PendingReviews) > 0 {
		slog.Info("orchestrator: loaded reviews refused by the last drain", "path", path, "count", len(state.PendingReviews))
	}
	return state
}

// savePendingReviews persists a clone of the pending-review set. Event loop.
func (o *Orchestrator) savePendingReviews(state *State) {
	path := o.pendingReviewsFile
	if path == "" {
		return
	}
	data, err := json.Marshal(maps.Clone(state.PendingReviews))
	if err != nil {
		slog.Warn("orchestrator: failed to marshal pending reviews", "error", err)
		return
	}
	o.persistLedger(ledgerPendingReviews, path, data, 0o644)
}

// recordPendingReview remembers a reviewer dispatch refused while draining.
// Event loop only.
func (o *Orchestrator) recordPendingReview(state *State, issue domain.Issue, profile string, now time.Time) {
	if issue.ID == "" || issue.Identifier == "" || profile == "" {
		return
	}
	if state.PendingReviews == nil {
		state.PendingReviews = make(map[string]PendingReview)
	}
	state.PendingReviews[issue.Identifier] = PendingReview{
		IssueID: issue.ID, Identifier: issue.Identifier, Profile: profile, QueuedAt: now,
		IssueState: issue.State,
	}
	o.savePendingReviews(state)
}

// clearPendingReview drops the marker once a reviewer for identifier starts.
// Event loop only.
func (o *Orchestrator) clearPendingReview(state *State, identifier string) {
	if _, ok := state.PendingReviews[identifier]; !ok {
		return
	}
	delete(state.PendingReviews, identifier)
	o.savePendingReviews(state)
}

// resumePendingReviews dispatches reviews refused by an earlier drain, oldest
// first, while admission is open and slots are free. Event loop only.
func (o *Orchestrator) resumePendingReviews(ctx context.Context, state *State, now time.Time) {
	if len(state.PendingReviews) == 0 {
		return
	}
	o.syncDrainRequest(state)
	ids := slices.Collect(maps.Keys(state.PendingReviews))
	slices.SortFunc(ids, func(a, b string) int {
		return state.PendingReviews[a].QueuedAt.Compare(state.PendingReviews[b].QueuedAt)
	})
	for _, id := range ids {
		if AvailableSlots(*state) <= 0 { // 0 while draining
			return
		}
		pr := state.PendingReviews[id]
		if _, running := state.Running[pr.IssueID]; running {
			continue
		}
		if _, claimed := state.Claimed[pr.IssueID]; claimed {
			continue
		}
		issue, err := o.tracker.FetchIssueDetail(ctx, pr.IssueID)
		if errors.Is(err, tracker.ErrNotFound) { // CORE-174: deleted meanwhile
			slog.Warn("orchestrator: pending review dropped, issue no longer exists", "identifier", id)
			o.clearPendingReview(state, id)
			continue
		}
		if err != nil {
			slog.Warn("orchestrator: pending review: issue fetch failed, retrying next tick",
				"identifier", id, "error", err)
			continue
		}
		if issue == nil {
			slog.Warn("orchestrator: pending review dropped, issue no longer exists", "identifier", id)
			o.clearPendingReview(state, id)
			continue
		}
		if reason := o.pendingReviewObsolete(*state, pr, issue.State); reason != "" {
			slog.Info("orchestrator: pending review dropped", "identifier", id,
				"state", issue.State, "queued_in", pr.IssueState, "reason", reason)
			o.clearPendingReview(state, id)
			continue
		}
		profile := pr.Profile
		if chain := o.reviewerChainCfg(); len(chain) > 1 && slices.Contains(chain, profile) {
			ResetReviewChain(state, id)
			profile = chain[0]
		}
		slog.Info("orchestrator: dispatching review refused by the last drain",
			"identifier", id, "profile", profile, "queued_at", pr.QueuedAt)
		o.dispatchReviewerForIssue(ctx, state, *issue, profile, now)
		if _, started := state.Running[pr.IssueID]; !started && !state.Draining {
			// Profile removed, disabled or unresolvable: dispatchReviewerForIssue
			// logged why. Do not refetch the issue every tick forever.
			slog.Warn("orchestrator: pending review dropped, reviewer could not start", "identifier", id, "profile", profile)
			o.clearPendingReview(state, id)
		}
	}
}

// pendingReviewObsolete reports why a pending review must not be replayed
// (CORE-174), or "": the issue is now in a terminal state it was not in when
// the review was refused (moved to Done / Cancelled meanwhile). A terminal
// tracker.completion_state is how an auto-review normally starts, so a
// review queued in that state — or, for a pre-CORE-174 record with no
// state, an issue sitting in the completion state — still runs.
func (o *Orchestrator) pendingReviewObsolete(state State, pr PendingReview, current string) string {
	if !isTerminalState(current, state) {
		return ""
	}
	if pr.IssueState != "" {
		if strings.EqualFold(current, pr.IssueState) {
			return ""
		}
		return "issue moved to a terminal state after the review was queued"
	}
	if _, _, completion := o.TrackerStatesCfg(); completion != "" && strings.EqualFold(current, completion) {
		return ""
	}
	return "issue is in a terminal state"
}
