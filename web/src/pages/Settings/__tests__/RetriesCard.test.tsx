// Gap §4.4 — RetriesCard vitest. Covers max_retries (G) and failed_state (G);
// the switch-cap controls moved to RateLimitsFailoverCard (CORE-093). The card
// commits on blur — typing alone shouldn't fire the setter, but blur with
// a changed value should.

import { describe, it, expect, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { RetriesCard } from '../RetriesCard';

function setup(overrides: Partial<Parameters<typeof RetriesCard>[0]> = {}) {
  const props: Parameters<typeof RetriesCard>[0] = {
    maxRetries: 5,
    failedState: '',
    trackerStateOptions: ['Backlog', 'In Progress', 'Done'],
    completionState: 'Done',
    onSetMaxRetries: vi.fn().mockResolvedValue(true),
    onSetFailedState: vi.fn().mockResolvedValue(true),
    ...overrides,
  };
  return { ...render(<RetriesCard {...props} />), props };
}

describe('RetriesCard', () => {
  it('renders the current max_retries value', () => {
    setup({ maxRetries: 7 });
    expect(screen.getByLabelText(/Max retries per issue/i)).toHaveValue(7);
  });

  // CORE-093 — the switch cap lives in the 'Rate limits & failover' section.
  it('no longer renders the switch cap', () => {
    setup();
    expect(screen.queryByLabelText(/Rate-limit switch cap/i)).not.toBeInTheDocument();
  });

  it('commits a changed max_retries on blur and reverts a non-integer', async () => {
    const { props } = setup({ maxRetries: 5 });
    const input = screen.getByLabelText(/Max retries per issue/i);
    fireEvent.change(input, { target: { value: '8' } });
    fireEvent.blur(input);
    await waitFor(() => {
      expect(props.onSetMaxRetries).toHaveBeenCalledWith(8);
    });
    fireEvent.change(input, { target: { value: 'many' } });
    fireEvent.blur(input);
    expect(await screen.findByText(/non-negative integer/i)).toBeInTheDocument();
    expect(input).toHaveValue(5);
  });

  it('surfaces a failed max_retries save and a failed failed_state save', async () => {
    const { props } = setup({
      maxRetries: 5,
      onSetMaxRetries: vi.fn().mockResolvedValue(false),
      onSetFailedState: vi.fn().mockResolvedValue(false),
    });
    const input = screen.getByLabelText(/Max retries per issue/i);
    fireEvent.change(input, { target: { value: '9' } });
    fireEvent.blur(input);
    await waitFor(() => {
      expect(screen.getAllByText(/Failed to save/i)).toHaveLength(1);
    });
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'Backlog' } });
    await waitFor(() => {
      expect(props.onSetFailedState).toHaveBeenCalledWith('Backlog');
    });
    await waitFor(() => {
      expect(screen.getAllByText(/Failed to save/i)).toHaveLength(2);
    });
  });
});
