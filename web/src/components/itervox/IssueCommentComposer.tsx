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
    // One line that grows with its text (multi-line comments still work),
    // with the Post comment chip inline.
    <div data-testid="issue-comment-composer" className="flex items-end gap-2">
      <textarea
        aria-label="Post a comment"
        value={text}
        onChange={(e) => {
          setText(e.target.value);
        }}
        placeholder="Post a comment on this issue…"
        rows={1}
        className={`${FIELD_CLASS} field-sizing-content max-h-48 min-h-10 resize-none rounded-[14px] py-2`}
      />
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
        className={plainButtonClass('secondary', 'lg', { pill: true, extra: 'shrink-0' })}
      >
        {post.isPending ? 'Posting…' : 'Post comment'}
      </button>
    </div>
  );
}
