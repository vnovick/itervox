import MarkdownPanel from './MarkdownPanel';
import type { CommentRow } from '../../types/schemas';

interface IssueReviewThreadProps {
  comments: CommentRow[];
  /**
   * Optional override for which comments are considered agent-authored. When
   * not provided, defaults to the canonical Itervox bot author name. Tests
   * and host adapters that surface a different agent author can plug in here.
   */
  isAgentAuthor?: (author?: string | null) => boolean;
}

const DEFAULT_AGENT_NAMES = new Set(['itervox', 'itervox-bot', 'reviewer-bot']);

function defaultIsAgentAuthor(author?: string | null): boolean {
  if (!author) return false;
  return DEFAULT_AGENT_NAMES.has(author.trim().toLowerCase());
}

/**
 * IssueReviewThread (T-7) renders the AGENT-AUTHORED comments inline below the
 * issue body so operators can spot reviewer activity at a glance without
 * scrolling through every human reply. Human comments are intentionally
 * excluded — they live in the broader Comments section already rendered by
 * IssueDetailSlide.
 *
 * The component is intentionally minimal: when there are no agent-authored
 * comments it renders nothing rather than an empty-state heading, so issues
 * with no review activity don't accrue clutter on the detail slide.
 */
export function IssueReviewThread({
  comments,
  isAgentAuthor = defaultIsAgentAuthor,
}: IssueReviewThreadProps) {
  const agentComments = comments.filter((c) => isAgentAuthor(c.author));
  if (agentComments.length === 0) return null;

  const sorted = [...agentComments].sort((a, b) => {
    const at = a.createdAt ? new Date(a.createdAt).getTime() : 0;
    const bt = b.createdAt ? new Date(b.createdAt).getTime() : 0;
    return at - bt;
  });

  return (
    <div data-testid="issue-review-thread" className="space-y-3">
      <h4 className="text-theme-text text-xs font-semibold tracking-wider uppercase">
        Agent reviews ({sorted.length})
      </h4>
      <ul className="space-y-3">
        {sorted.map((comment, idx) => {
          const isAgent = isAgentAuthor(comment.author);
          const key = `${comment.author}-${comment.createdAt ?? String(idx)}`;
          return (
            <li
              key={key}
              data-testid={isAgent ? 'review-comment-agent' : 'review-comment-human'}
              className={`rounded-lg border p-3 ${
                isAgent
                  ? 'border-emerald-500/40 bg-emerald-500/5'
                  : 'border-theme-line bg-theme-bg-soft'
              }`}
            >
              <div className="flex items-center gap-2 text-xs">
                {isAgent ? (
                  <span aria-label="Agent" title="Posted by an agent">
                    🤖
                  </span>
                ) : (
                  <span
                    className="bg-theme-bg-elevated text-theme-text inline-flex h-5 w-5 items-center justify-center rounded-full text-[10px] font-semibold"
                    aria-hidden
                  >
                    {(comment.author || '?').charAt(0).toUpperCase()}
                  </span>
                )}
                <span className="text-theme-text font-medium">{comment.author || 'Unknown'}</span>
                {comment.createdAt && (
                  <span className="text-theme-muted ml-auto">
                    {new Date(comment.createdAt).toLocaleString([], {
                      month: 'short',
                      day: 'numeric',
                      hour: '2-digit',
                      minute: '2-digit',
                    })}
                  </span>
                )}
              </div>
              <div className="mt-2 text-sm">
                <MarkdownPanel>{comment.body}</MarkdownPanel>
              </div>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

export default IssueReviewThread;
