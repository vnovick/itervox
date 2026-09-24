import { useState } from 'react';
import { usePostIssueComment } from '../../queries/issues';

interface IssueCommentComposerProps {
  identifier: string;
}

/**
 * IssueCommentComposer posts a plain operator comment on the issue. It is not
 * the input-required reply box — that lives in InputRequiredPanel and resumes
 * the agent directly. A comment posted here behaves like one typed in the
 * tracker: it is delivered through the outbox and may trigger automations.
 */
export function IssueCommentComposer({ identifier }: IssueCommentComposerProps) {
  const [text, setText] = useState('');
  const post = usePostIssueComment();
  const trimmed = text.trim();

  return (
    <div data-testid="issue-comment-composer" className="space-y-2">
      <textarea
        aria-label="Post a comment"
        value={text}
        onChange={(e) => {
          setText(e.target.value);
        }}
        placeholder="Post a comment on this issue…"
        rows={3}
        className="border-theme-line bg-theme-bg-elevated text-theme-text placeholder:text-theme-muted focus:ring-theme-accent w-full rounded-lg border px-3 py-2 text-sm focus:ring-1 focus:outline-none"
      />
      <div className="flex justify-end">
        <button
          type="button"
          disabled={post.isPending || trimmed === ''}
          onClick={() => {
            post.mutate(
              { identifier, body: trimmed },
              {
                onSuccess: () => {
                  setText('');
                },
              },
            );
          }}
          className="bg-theme-accent rounded-lg px-4 py-2 text-sm font-medium text-white hover:opacity-90 disabled:opacity-50"
        >
          {post.isPending ? 'Posting…' : 'Post comment'}
        </button>
      </div>
    </div>
  );
}
