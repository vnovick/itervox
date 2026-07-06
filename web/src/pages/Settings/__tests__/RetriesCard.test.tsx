// Gap §4.4 — RetriesCard vitest. Covers max_retries (G), failed_state (G),
// and the switch-cap controls (E §6.1 in plan / iteration 2). The card
// commits on blur — typing alone shouldn't fire the setter, but blur with
// a changed value should.

import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { RetriesCard } from '../RetriesCard';

function setup(overrides: Partial<Parameters<typeof RetriesCard>[0]> = {}) {
  const props: Parameters<typeof RetriesCard>[0] = {
    maxRetries: 5,
    failedState: '',
    trackerStateOptions: ['Backlog', 'In Progress', 'Done'],
    completionState: 'Done',
    maxSwitchesPerIssuePerWindow: 2,
    switchWindowHours: 6,
    onSetMaxRetries: vi.fn().mockResolvedValue(true),
    onSetFailedState: vi.fn().mockResolvedValue(true),
    onSetMaxSwitchesPerIssuePerWindow: vi.fn().mockResolvedValue(true),
    onSetSwitchWindowHours: vi.fn().mockResolvedValue(true),
    ...overrides,
  };
  return { ...render(<RetriesCard {...props} />), props };
}

describe('RetriesCard', () => {
  it('renders the current max_retries and switch-cap values', () => {
    setup({ maxRetries: 7, maxSwitchesPerIssuePerWindow: 4, switchWindowHours: 12 });
    expect(screen.getByLabelText(/Max retries per issue/i)).toHaveValue(7);
    expect(screen.getByLabelText(/Rate-limit switch cap/i)).toHaveValue(4);
    // The hours input is the second number input in the switch-cap row.
    const inputs = screen.getAllByRole('spinbutton');
    expect(inputs[2]).toHaveValue(12);
  });

  it('commits a changed switch cap on blur', async () => {
    const { props } = setup();
    const capInput = screen.getByLabelText(/Rate-limit switch cap/i);
    fireEvent.change(capInput, { target: { value: '5' } });
    fireEvent.blur(capInput);
    await waitFor(() => {
      expect(props.onSetMaxSwitchesPerIssuePerWindow).toHaveBeenCalledWith(5);
    });
  });

  it('does NOT call setter when the value is unchanged', async () => {
    const { props } = setup({ maxSwitchesPerIssuePerWindow: 2 });
    const capInput = screen.getByLabelText(/Rate-limit switch cap/i);
    fireEvent.change(capInput, { target: { value: '2' } });
    fireEvent.blur(capInput);
    // Give React a tick to settle.
    await new Promise((r) => setTimeout(r, 10));
    expect(props.onSetMaxSwitchesPerIssuePerWindow).not.toHaveBeenCalled();
  });

  it('rejects non-integer cap input and reverts the draft to the prop', async () => {
    const { props } = setup({ maxSwitchesPerIssuePerWindow: 3 });
    const capInput = screen.getByLabelText(/Rate-limit switch cap/i);
    fireEvent.change(capInput, { target: { value: 'soon' } });
    fireEvent.blur(capInput);
    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent(/non-negative integer/i);
    });
    expect(props.onSetMaxSwitchesPerIssuePerWindow).not.toHaveBeenCalled();
    // The draft must revert so the operator sees the live value, not their
    // bad input.
    expect(capInput.value).toBe('3');
  });

  it('rejects zero or negative window-hours and surfaces a positive-integer error', async () => {
    const { props } = setup({ switchWindowHours: 6 });
    const inputs = screen.getAllByRole('spinbutton');
    const windowInput = inputs[2] as HTMLInputElement;
    fireEvent.change(windowInput, { target: { value: '0' } });
    fireEvent.blur(windowInput);
    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent(/positive integer/i);
    });
    expect(props.onSetSwitchWindowHours).not.toHaveBeenCalled();
  });
});
