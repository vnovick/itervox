// CORE-093 — the 'Rate limits & failover' Settings card: the per-issue
// switch cap (moved here from RetriesCard with its tests), agent backend
// breakers with Clear, the backend_fallback note and rate_limited rules.

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router';
import { RateLimitsFailoverCard } from '../RateLimitsFailoverCard';
import { useItervoxStore } from '../../../store/itervoxStore';
import { makeSnapshot } from '../../../test/fixtures/snapshots';
import { makeTestQueryClient } from '../../../test/queryClient';

function setup(overrides: Partial<Parameters<typeof RateLimitsFailoverCard>[0]> = {}) {
  const props: Parameters<typeof RateLimitsFailoverCard>[0] = {
    maxSwitchesPerIssuePerWindow: 2,
    switchWindowHours: 6,
    onSetMaxSwitchesPerIssuePerWindow: vi.fn().mockResolvedValue(true),
    onSetSwitchWindowHours: vi.fn().mockResolvedValue(true),
    ...overrides,
  };
  return {
    ...render(
      <QueryClientProvider client={makeTestQueryClient()}>
        <MemoryRouter>
          <RateLimitsFailoverCard {...props} />
        </MemoryRouter>
      </QueryClientProvider>,
    ),
    props,
  };
}

describe('RateLimitsFailoverCard', () => {
  beforeEach(() => {
    useItervoxStore.setState({ snapshot: makeSnapshot() });
  });

  it('renders the current switch-cap values', () => {
    setup({ maxSwitchesPerIssuePerWindow: 4, switchWindowHours: 12 });
    expect(screen.getByLabelText(/Rate-limit switch cap/i)).toHaveValue(4);
    // M5-close (axe label) — the hours input has its own accessible name.
    expect(screen.getByRole('spinbutton', { name: /switch window in hours/i })).toHaveValue(12);
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
    const windowInput = screen.getByRole('spinbutton', { name: /switch window in hours/i });
    fireEvent.change(windowInput, { target: { value: '0' } });
    fireEvent.blur(windowInput);
    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent(/positive integer/i);
    });
    expect(props.onSetSwitchWindowHours).not.toHaveBeenCalled();
  });

  it('lists agent backend breakers with a Clear action for an open one', () => {
    const until = new Date(Date.now() + 3_600_000).toISOString();
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        backendHealth: [
          { backend: 'claude', status: 'limited', limitedUntil: until, heldIssues: 2 },
          { backend: 'codex', status: 'healthy', limitedUntil: null, heldIssues: 0 },
        ],
      }),
    });
    setup();
    const list = screen.getByRole('list', { name: /agent backends/i });
    expect(list).toHaveTextContent(/claude/i);
    expect(list).toHaveTextContent(/codex/i);
    expect(screen.getByRole('button', { name: /clear claude/i })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /clear codex/i })).not.toBeInTheDocument();
  });

  it('says no backend has been limited when there are no breaker rows', () => {
    setup();
    expect(screen.getByText(/no agent backend has been rate limited/i)).toBeInTheDocument();
  });

  it('explains backend_fallback and links rate_limited automations', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        automations: [
          {
            id: 'swap',
            enabled: true,
            profile: 'default',
            trigger: { type: 'rate_limited' },
          } as never,
        ],
        autoSwitches: [{ identifier: 'ENG-3', source: 'backend_fallback', toBackend: 'codex' }],
      }),
    });
    setup();
    expect(screen.getByText('agent.backend_fallback')).toBeInTheDocument();
    expect(
      screen.getByText(/1 issue currently runs on an automatic override/i),
    ).toBeInTheDocument();
    const link = screen.getByRole('link', { name: /1 rate_limited automation/i });
    expect(link).toHaveAttribute('href', '/automations');
  });
});
