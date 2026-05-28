import type { HistoryRow, RunningRow, TrackerIssue } from '../../types/schemas';
import { fmtMs } from '../../utils/format';
import { ReviewSourcePill } from './ReviewSourcePill';

type ReviewSource = 'session' | 'tracker';

interface ReviewQueueRowsProps {
  awaitingReview: readonly TrackerIssue[];
  reviewing: readonly RunningRow[];
  recentReviews: readonly HistoryRow[];
  reviewSourceByIdentifier: Record<string, ReviewSource>;
  reviewPending: boolean;
  onTriggerReview: (identifier: string) => void;
}

export function ReviewQueueRows({
  awaitingReview,
  reviewing,
  recentReviews,
  reviewSourceByIdentifier,
  reviewPending,
  onTriggerReview,
}: ReviewQueueRowsProps) {
  return (
    <>
      {awaitingReview.map((issue) => (
        <div
          key={issue.identifier}
          data-testid="review-queue-row"
          className="hover:bg-theme-bg-soft flex min-h-[52px] items-center gap-3 px-4 py-2.5 transition-colors"
        >
          <span className="text-xs text-amber-400">⏳</span>
          <span className="text-theme-accent font-mono text-xs font-semibold">
            {issue.identifier}
          </span>
          <ReviewSourcePill source={reviewSourceByIdentifier[issue.identifier] ?? 'tracker'} />
          <span className="text-theme-text-secondary flex-1 truncate text-xs">{issue.title}</span>
          <button
            onClick={() => {
              onTriggerReview(issue.identifier);
            }}
            disabled={reviewPending}
            className="border-theme-line text-theme-accent flex-shrink-0 rounded-[var(--radius-sm)] border px-2.5 py-1 text-[10px] font-medium transition-colors hover:opacity-80"
          >
            {reviewPending ? '…' : '▶ Review'}
          </button>
        </div>
      ))}

      {reviewing.map((row) => (
        <div
          key={row.identifier}
          data-testid="review-queue-row"
          className="bg-theme-success-soft/30 flex min-h-[52px] items-center gap-3 px-4 py-2.5"
        >
          <span className="text-theme-success text-xs">🔍</span>
          <span className="text-theme-accent font-mono text-xs font-semibold">
            {row.identifier}
          </span>
          <span className="text-theme-text-secondary flex-1 text-xs">
            Reviewing…
            {row.turnCount > 0 && ` (turn ${String(row.turnCount)})`}
          </span>
          <span className="text-theme-muted font-mono text-[10px]">{fmtMs(row.elapsedMs)}</span>
        </div>
      ))}

      {recentReviews.map((row) => (
        <div
          key={`${row.identifier}-${String(row.sessionId)}`}
          data-testid="review-queue-row"
          className="flex min-h-[52px] items-center gap-3 px-4 py-2.5 opacity-70"
        >
          <span className="text-xs">{row.status === 'succeeded' ? '✓' : '✗'}</span>
          <span className="text-theme-text-secondary font-mono text-xs font-semibold">
            {row.identifier}
          </span>
          <span className="text-theme-muted flex-1 text-xs">Review {row.status}</span>
          <span className="text-theme-muted font-mono text-[10px]">{fmtMs(row.elapsedMs)}</span>
        </div>
      ))}
    </>
  );
}
