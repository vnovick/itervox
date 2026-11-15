import { useState } from 'react';
import { Button } from '../../ui/button';
import { useReviewDraftStore, type LineTarget } from '../../../store/reviewDraftStore';
import { useSendLineComment, type CommentDestination } from '../../../queries/review';
import { describeTarget } from './lineSelection';

/**
 * #238 — the comment box under a selected line or range: write a comment,
 * choose where it goes — the running agent (a steering message quoting the
 * file and lines) or the PR (posted now, or added to the pending review) —
 * and send it. A destination that is not available is shown disabled with
 * the reason.
 */
export function LineCommentBox({
  identifier,
  target,
  quote,
  canAgent,
  canPR,
  onDone,
}: {
  identifier: string;
  target: LineTarget;
  quote: string;
  /** A run is live: the agent can take a steering message. */
  canAgent: boolean;
  /** The diff shows the PR's files: GitHub can anchor the comment. */
  canPR: boolean;
  onDone: () => void;
}) {
  const [body, setBody] = useState('');
  const [dest, setDest] = useState<CommentDestination>(canAgent || !canPR ? 'agent' : 'pr');
  const send = useSendLineComment(identifier);
  const addPending = useReviewDraftStore((s) => s.add);
  const text = body.trim();
  const destOK = dest === 'agent' ? canAgent : canPR;

  const options: { id: CommentDestination; label: string; enabled: boolean; why: string }[] = [
    { id: 'agent', label: 'Send to the agent', enabled: canAgent, why: 'no run is live' },
    {
      id: 'pr',
      label: 'Post on the PR',
      enabled: canPR,
      why: 'switch the diff to the PR files',
    },
  ];

  return (
    <section
      className="border-theme-line bg-theme-bg-soft space-y-2 rounded-[var(--radius-sm)] border p-2"
      data-testid="line-comment-box"
      aria-label={`Comment on ${target.path}, ${describeTarget(target)}`}
    >
      <p className="text-theme-muted text-xs">
        <code className="font-mono">{target.path}</code> · {describeTarget(target)}
      </p>
      <textarea
        aria-label="Comment"
        value={body}
        onChange={(e) => {
          setBody(e.target.value);
        }}
        rows={3}
        placeholder="What should change here?"
        className="border-theme-line bg-theme-bg-elevated text-theme-text placeholder:text-theme-muted w-full rounded-lg border px-2 py-1.5 text-sm focus:ring-1 focus:ring-orange-400 focus:outline-none"
      />
      <fieldset className="flex flex-wrap gap-3 text-xs">
        <legend className="sr-only">Destination</legend>
        {options.map((o) => (
          <label key={o.id} className={o.enabled ? '' : 'text-theme-muted'}>
            <input
              type="radio"
              name={`dest-${target.path}`}
              value={o.id}
              checked={dest === o.id}
              disabled={!o.enabled}
              onChange={() => {
                setDest(o.id);
              }}
              className="mr-1"
            />
            {o.label}
            {!o.enabled && <span> ({o.why})</span>}
          </label>
        ))}
      </fieldset>
      <div className="flex flex-wrap justify-end gap-2">
        <Button size="xs" variant="ghost" onClick={onDone}>
          Cancel
        </Button>
        {dest === 'pr' && (
          <Button
            size="xs"
            disabled={!text || !destOK}
            onClick={() => {
              addPending(identifier, { ...target, body: text });
              onDone();
            }}
          >
            Add to review
          </Button>
        )}
        <Button
          size="xs"
          variant="primary"
          disabled={!text || !destOK || send.isPending}
          onClick={() => {
            send.mutate(
              { destination: dest, target, body: text, quote },
              {
                onSuccess: onDone,
              },
            );
          }}
        >
          {send.isPending ? 'Sending…' : dest === 'agent' ? 'Send to agent' : 'Post now'}
        </Button>
      </div>
    </section>
  );
}
