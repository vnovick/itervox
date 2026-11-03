import type { IssueReview, ReviewVerdict } from '../../types/schemas';

type IssueReviewsProps = {
  review?: IssueReview;
  /** Shown when set: dispatches the configured reviewers again. */
  onRerun?: () => void;
  rerunPending?: boolean;
};

function formatReviewTime(at: string): string {
  const d = new Date(at);
  if (Number.isNaN(d.getTime())) return at;
  return new Intl.DateTimeFormat(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  }).format(d);
}

/** Keys for items that may repeat: the item's text plus its occurrence. */
function occurrenceKeys<T>(items: readonly T[], text: (item: T) => string): string[] {
  const seen = new Map<string, number>();
  return items.map((item) => {
    const base = text(item);
    const n = (seen.get(base) ?? 0) + 1;
    seen.set(base, n);
    return `${base}#${String(n)}`;
  });
}

function VerdictBadge({ verdict }: { verdict: string }) {
  const approved = verdict === 'approve';
  return (
    <span
      className={`inline-flex items-center rounded px-2 py-0.5 text-xs font-medium ${
        approved
          ? 'bg-theme-success-soft text-theme-success-text'
          : 'bg-theme-danger-soft text-theme-danger-text'
      }`}
    >
      {approved ? 'Approved' : 'Changes requested'}
    </span>
  );
}

function VerdictItem({ verdict }: { verdict: ReviewVerdict }) {
  const commentKeys = occurrenceKeys(
    verdict.comments ?? [],
    (c) => `${c.path}:${String(c.line ?? '')}:${c.body}`,
  );
  return (
    <li className="space-y-1.5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-theme-text text-sm font-medium">{verdict.profile}</span>
        <VerdictBadge verdict={verdict.verdict} />
        <span className="text-theme-muted text-xs">{formatReviewTime(verdict.recordedAt)}</span>
      </div>
      {verdict.reasons && verdict.reasons.length > 0 && (
        <ul className="text-theme-text-secondary list-disc space-y-0.5 pl-5 text-xs">
          {occurrenceKeys(verdict.reasons, (r) => r).map((key, i) => (
            <li key={key}>{verdict.reasons?.[i]}</li>
          ))}
        </ul>
      )}
      {verdict.comments && verdict.comments.length > 0 && (
        <ul className="space-y-1 text-xs">
          {verdict.comments.map((c, i) => (
            <li key={commentKeys[i]} className="text-theme-text-secondary">
              <code className="text-theme-text">
                {c.path}
                {c.line != null ? `:${String(c.line)}` : ''}
              </code>{' '}
              {c.body}
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}

/** #135 — each reviewer's verdict and the quorum result in issue detail. */
export function IssueReviews({ review, onRerun, rerunPending = false }: IssueReviewsProps) {
  if (!review || (review.verdicts.length === 0 && !review.inProgress)) return null;
  const { outcome } = review;

  return (
    <section
      aria-label="Review"
      className="border-theme-line bg-theme-bg-soft rounded-[var(--radius-sm)] border p-3"
    >
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <h4 className="text-theme-text text-xs font-medium tracking-wider uppercase">Review</h4>
        <div className="flex items-center gap-2">
          {outcome && (
            <span
              className={`text-xs font-medium ${
                outcome.blocked ? 'text-theme-danger-text' : 'text-theme-success-text'
              }`}
            >
              {outcome.blocked ? 'Blocked' : 'Passed'} — {outcome.approvals} approve /{' '}
              {outcome.blocks} block
              {outcome.quorum ? ` (${outcome.quorum})` : ''}
            </span>
          )}
          {review.inProgress && <span className="text-theme-muted text-xs">Reviewing…</span>}
          {onRerun && (
            <button
              type="button"
              onClick={onRerun}
              disabled={rerunPending || review.inProgress}
              className="text-theme-accent-text text-xs hover:underline disabled:opacity-50"
            >
              {rerunPending ? 'Starting…' : 'Re-run review'}
            </button>
          )}
        </div>
      </div>
      <ol className="space-y-3">
        {review.verdicts.map((v) => (
          <VerdictItem key={`${v.profile}:${v.recordedAt}`} verdict={v} />
        ))}
      </ol>
    </section>
  );
}
