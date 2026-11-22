// Cost and outcome visibility — the issue detail's run/PR outcome line.
import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { IssueOutcomeLine } from '../IssueOutcomeLine';

const base = {
  runs: 3,
  succeeded: 2,
  retries: 1,
  turns: 12,
  elapsedMs: 250_000,
  inputTokens: 12_000,
  outputTokens: 3_000,
  lastStatus: 'succeeded',
};

describe('IssueOutcomeLine', () => {
  it('renders nothing without recorded runs', () => {
    const { container } = render(<IssueOutcomeLine />);
    expect(container).toBeEmptyDOMElement();
    const { container: zero } = render(<IssueOutcomeLine outcome={{ ...base, runs: 0 }} />);
    expect(zero).toBeEmptyDOMElement();
  });

  it('sums the runs and links the merged PR', () => {
    render(
      <IssueOutcomeLine
        outcome={{
          ...base,
          cacheReadTokens: 90_000,
          prUrl: 'https://github.com/acme/app/pull/7',
          prOutcome: 'merged',
        }}
      />,
    );
    const line = screen.getByLabelText('Run outcome');
    expect(line).toHaveTextContent('3 runs (2 succeeded)');
    expect(line).toHaveTextContent('1 retry');
    expect(line).toHaveTextContent('12 turns');
    expect(line).toHaveTextContent('cache read');
    expect(line).toHaveTextContent('last: succeeded');
    const pr = screen.getByRole('link', { name: 'PR merged' });
    expect(pr).toHaveAttribute('href', 'https://github.com/acme/app/pull/7');
  });

  it('names a reverted PR and pluralises retries; no PR, no link', () => {
    const { rerender } = render(
      <IssueOutcomeLine
        outcome={{ ...base, retries: 2, prUrl: 'https://x/pull/1', prOutcome: 'reverted' }}
      />,
    );
    expect(screen.getByLabelText('Run outcome')).toHaveTextContent('2 retries');
    expect(screen.getByRole('link', { name: 'PR reverted' })).toBeInTheDocument();
    rerender(<IssueOutcomeLine outcome={{ ...base, retries: 0, lastStatus: 'input_required' }} />);
    expect(screen.queryByRole('link')).toBeNull();
    expect(screen.getByLabelText('Run outcome')).toHaveTextContent('last: input required');
    expect(screen.getByLabelText('Run outcome')).not.toHaveTextContent('retr');
  });
});
