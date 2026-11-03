import { useState } from 'react';
import { Button } from '../../ui/button';
import { useReviewDraftStore, type LineTarget } from '../../../store/reviewDraftStore';
import { describeTarget } from './lineSelection';

/**
 * #238 / review and continue — the comment box under a selected line or
 * range: the comment joins the issue's draft review, which goes to the
 * agent (or the PR) as a whole from the review panel.
 */
export function LineCommentBox({
  identifier,
  target,
  quote,
  onDone,
}: {
  identifier: string;
  target: LineTarget;
  quote: string;
  onDone: () => void;
}) {
  const [body, setBody] = useState('');
  const add = useReviewDraftStore((s) => s.add);
  const text = body.trim();
  const submit = () => {
    if (!text) return;
    add(identifier, { ...target, body: text, quote });
    onDone();
  };

  return (
    <section
      className="glass-card space-y-2 p-3"
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
        onKeyDown={(e) => {
          if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            submit();
          }
        }}
        rows={3}
        placeholder="What should change here?"
        className="border-theme-line bg-theme-bg-elevated text-theme-text placeholder:text-theme-muted focus:ring-theme-accent w-full rounded-lg border px-2 py-1.5 text-sm focus:ring-1 focus:outline-none"
      />
      <div className="flex flex-wrap items-center justify-end gap-2">
        <span className="text-theme-muted mr-auto text-[11px]">Ctrl/⌘+Enter adds it</span>
        <Button size="xs" variant="ghost" onClick={onDone}>
          Cancel
        </Button>
        <Button size="xs" variant="primary" disabled={!text} onClick={submit}>
          Add to review
        </Button>
      </div>
    </section>
  );
}
