import { useState } from 'react';
import { usePostIssueComment } from '../../queries/issues';
import { FIELD_CLASS } from '../ui/Input/fieldClass';
import { plainButtonClass } from '../ui/button/buttonRecipe';

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
        className={`${FIELD_CLASS} rounded-[14px] py-2.5`}
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
          className={plainButtonClass('primary', 'lg', { pill: true })}
        >
          {post.isPending ? 'Posting…' : 'Post comment'}
        </button>
      </div>
    </div>
  );
}
