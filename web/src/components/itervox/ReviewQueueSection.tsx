import { useMemo, useState } from 'react';
import { useItervoxStore } from '../../store/itervoxStore';
import { useShallow } from 'zustand/react/shallow';
import { useIssues, useTriggerAIReview } from '../../queries/issues';
import { EMPTY_RUNNING, EMPTY_HISTORY } from '../../utils/constants';
import { classifyReviewSource } from '../../lib/operatorQueue';
import { QueueSearchInput } from './QueueSearchInput';
import { ReviewQueueRows } from './ReviewQueueRows';

/**
 * ReviewQueueSection shows a dashboard section with:
 * - Issues awaiting review (in completionState, no active reviewer)
 * - Issues currently being reviewed (running with kind="reviewer")
 * - Recent review completions from history
 *
 * Only rendered when reviewerProfile is configured.
 */
export function ReviewQueueSection() {
  // v0.2.0 audit P3-4 — drop `currentAppSessionId` and the `void` no-op
  // statements left over from an earlier refactor. `classifyReviewSource`
  // reads the session id from the snapshot it already receives, so the
  // destructured field was dead weight.
  const { reviewerProfile, completionState, running, history } = useItervoxStore(
    useShallow((s) => ({
      reviewerProfile: s.snapshot?.reviewerProfile ?? '',
      completionState: s.snapshot?.completionState ?? '',
      running: s.snapshot?.running ?? EMPTY_RUNNING,
      history: s.snapshot?.history ?? EMPTY_HISTORY,
    })),
  );

  const { data: issues = [] } = useIssues();
  const triggerReview = useTriggerAIReview();
  const [expanded, setExpanded] = useState(false);
  const [search, setSearch] = useState('');

  // Issues in completionState awaiting review
  const awaitingReview = useMemo(() => {
    if (!completionState) return [];
    const reviewingIdentifiers = new Set(
      running.filter((r) => r.kind === 'reviewer').map((r) => r.identifier),
    );
    return issues.filter(
      (i) =>
        i.state.toLowerCase() === completionState.toLowerCase() &&
        !reviewingIdentifiers.has(i.identifier),
    );
  }, [issues, completionState, running]);

  // Currently being reviewed
  const reviewing = useMemo(() => running.filter((r) => r.kind === 'reviewer'), [running]);

  // Recent review completions (last 5)
  const recentReviews = useMemo(
    () => history.filter((h) => h.kind === 'reviewer').slice(0, 5),
    [history],
  );

  // Per-identifier review-source classification, delegated to the shared
  // helper in lib/operatorQueue so this surface and NotificationsView use
  // identical logic. Gap §10.1.
  const snapshot = useItervoxStore((s) => s.snapshot);
  const reviewSourceByIdentifier = useMemo(() => {
    const map: Record<string, 'session' | 'tracker'> = {};
    if (!snapshot) return map;
    for (const issue of awaitingReview) {
      map[issue.identifier] = classifyReviewSource(snapshot, issue.identifier);
    }
    return map;
  }, [awaitingReview, snapshot]);
  const totalItems = awaitingReview.length + reviewing.length + recentReviews.length;
  const q = search.trim().toLowerCase();
  const filteredAwaiting = useMemo(
    () =>
      q === ''
        ? awaitingReview
        : awaitingReview.filter((issue) =>
            [
              issue.identifier,
              issue.title,
              issue.state,
              reviewSourceByIdentifier[issue.identifier] ?? 'tracker',
            ]
              .join(' ')
              .toLowerCase()
              .includes(q),
          ),
    [awaitingReview, q, reviewSourceByIdentifier],
  );
  const filteredReviewing = useMemo(
    () =>
      q === ''
        ? reviewing
        : reviewing.filter((row) =>
            [row.identifier, row.state, row.kind, `turn ${String(row.turnCount)}`]
              .join(' ')
              .toLowerCase()
              .includes(q),
          ),
    [reviewing, q],
  );
  const filteredRecentReviews = useMemo(
    () =>
      q === ''
        ? recentReviews
        : recentReviews.filter((row) =>
            [row.identifier, row.status, row.kind].join(' ').toLowerCase().includes(q),
          ),
    [recentReviews, q],
  );
  const visibleItems =
    filteredAwaiting.length + filteredReviewing.length + filteredRecentReviews.length;

  // Don't render if no reviewer profile
  if (!reviewerProfile) return null;

  return (
    <div className="border-theme-line bg-theme-bg-elevated shadow-theme-sm overflow-hidden rounded-[var(--radius-lg)] border">
      {/* Header */}
      <div className="border-theme-line flex items-center justify-between gap-3 border-b px-4 py-3">
        <div className="flex items-center gap-2">
          <h2 className="text-theme-text text-sm font-semibold tracking-tight">Review Queue</h2>
          <span className="bg-theme-bg-soft text-theme-text-secondary rounded-full px-1.5 py-0.5 text-[10px] font-bold">
            {q ? `${String(visibleItems)}/${String(totalItems)}` : totalItems}
          </span>
        </div>
        <div className="flex items-center gap-2">
          <span className="bg-theme-accent-soft text-theme-accent-strong rounded-full px-2 py-0.5 text-[10px] font-medium">
            {reviewerProfile}
          </span>
          <button
            type="button"
            aria-label={expanded ? 'Collapse review queue' : 'Expand review queue'}
            onClick={() => {
              setExpanded((prev) => !prev);
            }}
            className="border-theme-line text-theme-text-secondary hover:text-theme-text min-h-8 rounded-[var(--radius-sm)] border px-2.5 py-1 text-[11px] font-medium"
          >
            {expanded ? 'Collapse' : 'Expand'}
          </button>
        </div>
      </div>

      {!expanded ? null : totalItems === 0 ? (
        <div className="text-theme-muted px-4 py-8 text-center text-sm">
          No issues in review queue
        </div>
      ) : (
        <>
          <div className="border-theme-line border-b px-4 py-3">
            <QueueSearchInput
              value={search}
              onChange={setSearch}
              label="Search review queue"
              placeholder="Search review issue, title, source, or status..."
            />
          </div>
          {visibleItems === 0 ? (
            <div className="text-theme-muted px-4 py-8 text-center text-sm">
              No matching review queue items
            </div>
          ) : (
            <div
              data-testid="review-queue-scroll"
              className="divide-theme-line max-h-[260px] divide-y overflow-y-auto"
            >
              <ReviewQueueRows
                awaitingReview={filteredAwaiting}
                reviewing={filteredReviewing}
                recentReviews={filteredRecentReviews}
                reviewSourceByIdentifier={reviewSourceByIdentifier}
                reviewPending={triggerReview.isPending}
                onTriggerReview={(identifier) => {
                  triggerReview.mutate(identifier);
                }}
              />
            </div>
          )}
        </>
      )}
    </div>
  );
}
