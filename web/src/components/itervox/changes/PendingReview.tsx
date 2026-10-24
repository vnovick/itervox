import { useState } from 'react';
import { Button } from '../../ui/button';
import { usePendingComments, useReviewDraftStore } from '../../../store/reviewDraftStore';
import { useSubmitPrReview, type PrReviewEvent } from '../../../queries/review';
import type { PrReviewResult } from '../../../types/schemas';
import { describeTarget } from './lineSelection';

const EVENTS: readonly { id: PrReviewEvent; label: string }[] = [
  { id: 'comment', label: 'Comment' },
  { id: 'request_changes', label: 'Request changes' },
  { id: 'approve', label: 'Approve' },
];

function PolicyNote({ result }: { result: PrReviewResult }) {
  const { blockLabels, quorum } = result.mergePolicy;
  return (
    <p role="status" className="text-theme-muted text-xs" data-testid="review-policy">
      Review submitted with {result.comments} line {result.comments === 1 ? 'comment' : 'comments'}.
      Itervox does not merge from here; merging still goes through the merge policy
      {blockLabels.length > 0 ? ` (blocked by label: ${blockLabels.join(', ')})` : ''}.
      {quorum
        ? ` Agent review quorum: ${quorum.blocked ? 'blocked' : 'passed'} (${String(quorum.approvals)} approve, ${String(quorum.blocks)} block${quorum.quorum ? `, ${quorum.quorum}` : ''}), unchanged by this review.`
        : ''}
    </p>
  );
}

/**
 * #238 — the pending line comments of the issue's PR review and the review
 * itself: Comment, Request changes or Approve, submitted as ONE GitHub
 * review carrying every pending comment. It never merges.
 */
export function PendingReview({ identifier, prUrl }: { identifier: string; prUrl: string }) {
  const pending = usePendingComments(identifier);
  const remove = useReviewDraftStore((s) => s.remove);
  const clear = useReviewDraftStore((s) => s.clear);
  const [body, setBody] = useState('');
  const [result, setResult] = useState<PrReviewResult | null>(null);
  const submit = useSubmitPrReview(identifier);

  const run = (event: PrReviewEvent) => {
    submit.mutate(
      {
        event,
        body: body.trim(),
        comments: pending.map((c) => ({
          path: c.path,
          line: c.line,
          side: c.side,
          startLine: c.startLine,
          startSide: c.startSide,
          body: c.body,
        })),
      },
      {
        onSuccess: (res) => {
          clear(identifier);
          setBody('');
          setResult(res);
        },
      },
    );
  };
  const needsText = !body.trim() && pending.length === 0;

  return (
    <section className="border-theme-line space-y-2 border-t pt-3" data-testid="pending-review">
      <div className="flex items-center justify-between gap-2">
        <h4 className="text-xs font-medium tracking-wider uppercase">Review the PR</h4>
        <a
          href={prUrl}
          target="_blank"
          rel="noopener noreferrer"
          className="text-theme-accent-text text-xs hover:underline"
        >
          Open on GitHub →
        </a>
      </div>
      {pending.length > 0 && (
        <ul className="space-y-1 text-xs" aria-label="Pending comments">
          {pending.map((c) => (
            <li key={c.id} className="flex items-start gap-2">
              <span className="min-w-0 flex-1">
                <code className="font-mono">{c.path}</code> · {describeTarget(c)} —{' '}
                <span className="text-theme-text">{c.body}</span>
              </span>
              <button
                type="button"
                className="text-theme-muted hover:text-theme-danger-text"
                aria-label={`Remove the comment on ${c.path} ${describeTarget(c)}`}
                onClick={() => {
                  remove(identifier, c.id);
                }}
              >
                ✕
              </button>
            </li>
          ))}
        </ul>
      )}
      <textarea
        aria-label="Review summary"
        value={body}
        onChange={(e) => {
          setBody(e.target.value);
        }}
        rows={2}
        placeholder="Summary (optional for an approval)"
        className="border-theme-line bg-theme-bg-elevated text-theme-text placeholder:text-theme-muted w-full rounded-lg border px-2 py-1.5 text-sm focus:ring-1 focus:ring-orange-400 focus:outline-none"
      />
      <div className="flex flex-wrap justify-end gap-2">
        {EVENTS.map((e) => (
          <Button
            key={e.id}
            size="xs"
            variant={e.id === 'approve' ? 'primary' : 'secondary'}
            disabled={submit.isPending || (e.id !== 'approve' && needsText)}
            onClick={() => {
              run(e.id);
            }}
          >
            {e.label}
            {pending.length > 0 && e.id === 'comment' ? ` (${String(pending.length)})` : ''}
          </Button>
        ))}
      </div>
      {result && <PolicyNote result={result} />}
    </section>
  );
}
