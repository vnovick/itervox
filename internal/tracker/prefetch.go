package tracker

import (
	"context"
	"log/slog"

	"github.com/vnovick/itervox/internal/domain"
)

// PrefetchDetails fetches full issue detail — including comments — for ids in
// one request when tr implements DetailBatcher, returning a map keyed by issue
// ID. A nil return is safe to index: callers read `prefetched[id]` and fall
// back to FetchIssueDetail on a nil result.
//
// It exists for issue #42. The tracker-reply check, the pending-input resume,
// and the input-required replay each cost one request per entry per tick —
// together ~56% of the traffic measured in that incident. All three read
// Comments, which FetchIssueStatesByIDs deliberately omits, so none of them had
// a batch path at all until DetailBatcher existed.
//
// Three properties every caller depends on:
//
//  1. A miss is not an error. Trackers without DetailBatcher (GitHub, whose
//     REST API cannot batch — see the note on its FetchIssueDetail) return nil
//     and every caller takes its existing per-issue path unchanged.
//
//  2. A batch failure is not fatal. On error this returns nil rather than
//     propagating, so a failed batch degrades to per-issue instead of skipping
//     the tick. Losing the optimisation is recoverable; losing a reply check is
//     not — an answered question would never be noticed.
//
//  3. Absence is not deletion. An ID missing from the result means only "not in
//     this response". Callers MUST NOT treat a missing entry as a gone issue;
//     they fall through to an authoritative single fetch, exactly as the
//     dependency-audit refresh confirms before retiring a row.
func PrefetchDetails(ctx context.Context, tr Tracker, ids []string) map[string]*domain.Issue {
	if len(ids) < 2 {
		// One id is not a batch: the batched query costs the same single
		// request as FetchIssueDetail, so there is nothing to win and a
		// second code path would be exercised for no benefit.
		return nil
	}
	batcher, ok := tr.(DetailBatcher)
	if !ok {
		return nil
	}
	issues, err := batcher.FetchIssueDetailsByIDs(ctx, ids)
	if err != nil {
		slog.Warn("tracker: batched detail prefetch failed, falling back to per-issue",
			"count", len(ids), "error", err)
		return nil
	}
	out := make(map[string]*domain.Issue, len(issues))
	for i := range issues {
		issue := issues[i]
		out[issue.ID] = &issue
	}
	return out
}
