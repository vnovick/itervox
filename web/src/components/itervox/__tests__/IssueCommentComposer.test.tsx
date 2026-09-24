import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { IssueCommentComposer } from '../IssueCommentComposer';

vi.mock('../../../queries/issues', () => ({
  usePostIssueComment: vi.fn(),
}));

import * as issueQueries from '../../../queries/issues';

const mockUsePostIssueComment = vi.mocked(issueQueries.usePostIssueComment);

// Cast an incomplete mock object to any expected return type without using `any`.
function castMock<T>(_type: T | null, val: unknown): T;
function castMock(val: unknown): unknown;
function castMock(valOrType: unknown, val?: unknown): unknown {
  return val !== undefined ? val : valOrType;
}

describe('IssueCommentComposer', () => {
  it('button is disabled while the textarea is empty or whitespace', async () => {
    const mutate = vi.fn();
    mockUsePostIssueComment.mockReturnValue(castMock({ mutate, isPending: false }));
    render(<IssueCommentComposer identifier="ENG-10" />);

    const button = screen.getByRole('button', { name: /post comment/i });
    expect(button).toBeDisabled();

    const textbox = screen.getByRole('textbox', { name: /post a comment/i });
    await userEvent.type(textbox, '   ');
    expect(button).toBeDisabled();

    await userEvent.type(textbox, 'x');
    expect(button).not.toBeDisabled();
  });

  it('shows Posting… and disables the button while pending', () => {
    mockUsePostIssueComment.mockReturnValue(castMock({ mutate: vi.fn(), isPending: true }));
    render(<IssueCommentComposer identifier="ENG-10" />);

    const button = screen.getByRole('button', { name: /posting/i });
    expect(button).toBeDisabled();
  });

  it('clears the textarea after a successful post', async () => {
    const mutate = vi.fn(
      (_vars: { identifier: string; body: string }, options?: { onSuccess?: () => void }) => {
        options?.onSuccess?.();
      },
    );
    mockUsePostIssueComment.mockReturnValue(castMock({ mutate, isPending: false }));
    render(<IssueCommentComposer identifier="ENG-10" />);

    const textbox = screen.getByRole('textbox', { name: /post a comment/i });
    await userEvent.type(textbox, 'hi');
    await userEvent.click(screen.getByRole('button', { name: /post comment/i }));

    expect(mutate).toHaveBeenCalled();
    expect(screen.getByRole('textbox', { name: /post a comment/i })).toHaveValue('');
  });

  it('does not call mutate for whitespace-only input', async () => {
    const mutate = vi.fn();
    mockUsePostIssueComment.mockReturnValue(castMock({ mutate, isPending: false }));
    render(<IssueCommentComposer identifier="ENG-10" />);

    const textbox = screen.getByRole('textbox', { name: /post a comment/i });
    await userEvent.type(textbox, '   ');

    const button = screen.getByRole('button', { name: /post comment/i });
    expect(button).toBeDisabled();
    await userEvent.click(button);

    expect(mutate).not.toHaveBeenCalled();
  });
});
