import { useMemo, useState } from 'react';
import { Button } from '../../ui/button';
import { Textarea } from '../../ui/Textarea';
import {
  commentOrder,
  useDraftNote,
  usePendingComments,
  useReviewDraftStore,
} from '../../../store/reviewDraftStore';
import { useItervoxStore } from '../../../store/itervoxStore';
import {
  useSendReviewToAgent,
  useSubmitPrReview,
  type PrReviewEvent,
} from '../../../queries/review';
import type { PrReviewResult } from '../../../types/schemas';
import { describeTarget } from './lineSelection';

const EMPTY_STATES: readonly string[] = [];

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

function plural(n: number): string {
  return `${String(n)} ${n === 1 ? 'comment' : 'comments'}`;
}

/**
 * Review and continue — the issue's draft review: its line and range
 * comments (jump to one, remove one), an overall note, and one "Send to
 * agent" for all of it. A live run is steered; otherwise a new run starts
 * on the issue's branch, after moving the issue to the first active state
 * ("Move to …", on by default, hidden when the run is live or the issue is
 * already active). With a PR, "Also post on the PR" (on by default) posts
 * the comments as one PR review too. The PR's own review (Comment /
 * Request changes / Approve, #238) uses the same draft.
 */
export function ReviewDraft({
  identifier,
  live,
  issueState,
  prUrl,
  prReview,
  files,
  onJump,
}: {
  identifier: string;
  live: boolean;
  /** The issue's tracker state, when the caller knows it. */
  issueState?: string;
  prUrl?: string;
  /** The diff shows the PR's files: the PR review can anchor comments. */
  prReview: boolean;
  /** The diff's files in list order (orders the comments). */
  files: readonly string[];
  onJump: (commentId: string, path: string) => void;
}) {
  const comments = usePendingComments(identifier);
  const note = useDraftNote(identifier);
  const setNote = useReviewDraftStore((s) => s.setNote);
  const remove = useReviewDraftStore((s) => s.remove);
  const clear = useReviewDraftStore((s) => s.clear);
  const activeStates = useItervoxStore((s) => s.snapshot?.activeStates ?? EMPTY_STATES);
  const queued = useItervoxStore((s) =>
    s.snapshot?.queuedReviews?.find((q) => q.identifier === identifier),
  );
  const [moveToActive, setMoveToActive] = useState(true);
  const [alsoPR, setAlsoPR] = useState(true);
  const [result, setResult] = useState<PrReviewResult | null>(null);
  const send = useSendReviewToAgent(identifier);
  const submit = useSubmitPrReview(identifier);

  const ordered = useMemo(
    () => [...comments].sort((a, b) => commentOrder(files, a, b)),
    [comments, files],
  );
  const firstActive = activeStates[0];
  const isActive =
    issueState !== undefined &&
    activeStates.some((s) => s.toLowerCase() === issueState.toLowerCase());
  const showMove = !live && !isActive && Boolean(firstActive);
  const empty = comments.length === 0 && !note.trim();
  const busy = send.isPending || submit.isPending;

  const sendToAgent = () => {
    send.mutate(
      {
        comments,
        note: note.trim(),
        moveToActive: showMove ? moveToActive : true,
        alsoPostOnPR: Boolean(prUrl) && alsoPR,
      },
      {
        onSuccess: () => {
          clear(identifier);
        },
      },
    );
  };

  const reviewPR = (event: PrReviewEvent) => {
    submit.mutate(
      {
        event,
        body: note.trim(),
        comments: comments.map((c) => ({
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
          setResult(res);
        },
      },
    );
  };

  return (
    <section
      className="glass-card space-y-3 p-3"
      data-testid="review-draft"
      aria-label="Your review"
    >
      <div className="flex flex-wrap items-center gap-2">
        <h4 className="text-theme-text text-sm font-semibold">Your review</h4>
        <span className="pill pill-neutral px-2 py-0 text-[11px]" data-testid="review-draft-count">
          {plural(comments.length)}
        </span>
      </div>
      {queued && (
        <p role="status" className="banner-warn px-3 py-2 text-xs" data-testid="review-queued">
          A review ({plural(queued.comments)}) is queued for the next run of {identifier}; it starts
          when a slot and the issue&apos;s gates admit it.
        </p>
      )}
      {ordered.length > 0 && (
        <ul className="space-y-1 text-xs" aria-label="Draft comments">
          {ordered.map((c) => (
            <li key={c.id} className="flex items-start gap-2">
              <button
                type="button"
                className="hover:text-theme-accent-text min-w-0 flex-1 text-left"
                onClick={() => {
                  onJump(c.id, c.path);
                }}
                aria-label={`Show the comment on ${c.path} ${describeTarget(c)}`}
              >
                <code className="font-mono">{c.path}</code> · {describeTarget(c)} —{' '}
                <span className="text-theme-text">{c.body}</span>
              </button>
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
      <Textarea
        aria-label="Overall note"
        value={note}
        onChange={(e) => {
          setNote(identifier, e.target.value);
        }}
        rows={2}
        placeholder="Overall note (optional)"
        className="text-sm"
      />
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2" data-testid="review-send-bar">
        {showMove && (
          <label className="text-theme-text flex items-center gap-1.5 text-xs">
            <input
              type="checkbox"
              className="accent-theme-accent"
              checked={moveToActive}
              onChange={(e) => {
                setMoveToActive(e.target.checked);
              }}
            />
            Move to {firstActive}
          </label>
        )}
        {prUrl && (
          <label className="text-theme-text flex items-center gap-1.5 text-xs">
            <input
              type="checkbox"
              className="accent-theme-accent"
              checked={alsoPR}
              onChange={(e) => {
                setAlsoPR(e.target.checked);
              }}
            />
            Also post on the PR
          </label>
        )}
        <Button
          size="sm"
          variant="primary"
          className="ml-auto"
          disabled={empty || busy}
          onClick={sendToAgent}
        >
          {send.isPending ? 'Sending…' : 'Send to agent'}
        </Button>
      </div>
      <p className="text-theme-muted text-[11px]">
        {live
          ? 'The running agent gets the whole review as one message.'
          : showMove && moveToActive
            ? `No run is live: ${identifier} moves to ${firstActive} and a new run continues on its branch.`
            : `No run is live: a new run continues on ${identifier}'s branch without changing its tracker state.`}
      </p>
      {prReview && prUrl && (
        <div className="border-theme-line space-y-2 border-t pt-3" data-testid="pending-review">
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
          <p className="text-theme-muted text-[11px]">
            Submits the draft as one GitHub review; the overall note is its summary.
          </p>
          <div className="flex flex-wrap justify-end gap-2">
            {EVENTS.map((e) => (
              <Button
                key={e.id}
                size="xs"
                variant={e.id === 'approve' ? 'secondary' : 'ghost'}
                disabled={busy || (e.id !== 'approve' && empty)}
                onClick={() => {
                  reviewPR(e.id);
                }}
              >
                {e.label}
                {comments.length > 0 && e.id === 'comment' ? ` (${String(comments.length)})` : ''}
              </Button>
            ))}
          </div>
          {result && <PolicyNote result={result} />}
        </div>
      )}
    </section>
  );
}
