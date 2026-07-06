import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { IssueReviewThread } from '../IssueReviewThread';
import type { CommentRow } from '../../../types/schemas';

// MarkdownPanel lazily loads react-markdown which never resolves inside the
// test runner's synchronous render. Stub it to render the raw body so we can
// assert on the comment text directly.
vi.mock('../MarkdownPanel', () => ({
  default: ({ children }: { children: string }) => <span>{children}</span>,
}));

const human = (overrides: Partial<CommentRow>): CommentRow => ({
  author: 'alice',
  body: 'hello',
  createdAt: '2026-04-30T10:00:00Z',
  ...overrides,
});

const agent = (overrides: Partial<CommentRow>): CommentRow => ({
  author: 'itervox',
  body: 'agent body',
  createdAt: '2026-04-30T10:00:00Z',
  ...overrides,
});

describe('IssueReviewThread (T-7)', () => {
  it('renders nothing when there are no agent-authored comments', () => {
    const { container } = render(
      <IssueReviewThread
        comments={[
          human({ author: 'alice', body: 'no agents' }),
          human({ author: 'bob', body: 'still no agents' }),
        ]}
      />,
    );
    expect(container.firstChild).toBeNull();
  });

  it('filters to agent comments only, marking each with 🤖', () => {
    render(
      <IssueReviewThread
        comments={[
          human({ author: 'alice', body: 'human reply' }),
          agent({ body: 'PR opened: #42' }),
          agent({ body: 'tests pass', createdAt: '2026-04-30T10:30:00Z' }),
        ]}
      />,
    );
    // Two agent comments are rendered; the human comment is excluded.
    const agents = screen.getAllByTestId('review-comment-agent');
    expect(agents).toHaveLength(2);
    expect(screen.queryByTestId('review-comment-human')).toBeNull();
    expect(screen.getByText(/PR opened: #42/i)).toBeInTheDocument();
    expect(screen.queryByText(/human reply/i)).toBeNull();
    for (const node of agents) {
      expect(node.textContent).toContain('🤖');
    }
  });

  it('sorts agent comments oldest-first regardless of input order', () => {
    render(
      <IssueReviewThread
        comments={[
          agent({ body: 'late', createdAt: '2026-04-30T11:00:00Z' }),
          agent({ body: 'early', createdAt: '2026-04-30T09:00:00Z' }),
        ]}
      />,
    );
    const items = screen.getAllByRole('listitem');
    expect(items).toHaveLength(2);
    expect(items[0].textContent).toContain('early');
    expect(items[1].textContent).toContain('late');
  });

  it('treats unknown authors as human (filters them out)', () => {
    const { container } = render(
      <IssueReviewThread comments={[human({ author: undefined, body: 'mystery' })]} />,
    );
    expect(container.firstChild).toBeNull();
  });

  it('accepts a custom isAgentAuthor predicate so adapters can plug in', () => {
    render(
      <IssueReviewThread
        comments={[
          human({ author: 'alice', body: 'human reply' }),
          human({ author: 'review-bot-v2', body: 'ack' }),
        ]}
        isAgentAuthor={(a) => a === 'review-bot-v2'}
      />,
    );
    const agentNode = screen.getByTestId('review-comment-agent');
    expect(agentNode.textContent).toContain('🤖');
    expect(agentNode.textContent).toContain('ack');
    expect(screen.queryByText(/human reply/i)).toBeNull();
  });

  it('shows the agent-only count in the heading', () => {
    render(
      <IssueReviewThread
        comments={[
          human({ author: 'a', body: 'one' }),
          human({ author: 'b', body: 'two' }),
          agent({ body: 'three' }),
        ]}
      />,
    );
    expect(screen.getByRole('heading', { name: /Agent reviews \(1\)/i })).toBeInTheDocument();
  });
});
