import { fireEvent, render, screen, within } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { FailuresPanel } from '../FailuresPanel';
import type { FailureRow } from '../../../../types/schemas';

// CORE-046 — dashboard Failures panel over snapshot.recentFailures.

function failure(overrides: Partial<FailureRow> = {}): FailureRow {
  return {
    kind: 'worker_failed',
    identifier: 'ENG-1',
    message: 'worker run failed on attempt 1 (agent error)',
    occurredAt: '2026-09-26T10:00:00Z',
    recordedAt: '2026-09-26T10:00:01Z',
    count: 1,
    ...overrides,
  };
}

describe('FailuresPanel', () => {
  it('renders nothing when there are no failures', () => {
    const { container } = render(<FailuresPanel failures={[]} onSelectIssue={vi.fn()} />);
    expect(container.firstChild).toBeNull();
  });

  it('is a labelled region with a list sorted by occurredAt, newest first', () => {
    render(
      <FailuresPanel
        failures={[
          failure({
            message: 'middle',
            occurredAt: '2026-09-26T10:00:00Z',
            recordedAt: '2026-09-26T10:00:01Z',
          }),
          failure({
            message: 'newest',
            occurredAt: '2026-09-26T11:00:00Z',
            recordedAt: '2026-09-26T11:00:01Z',
          }),
          // Recorded last (newest ring entry) but occurred first.
          failure({
            message: 'oldest',
            occurredAt: '2026-09-26T09:00:00Z',
            recordedAt: '2026-09-26T12:00:00Z',
          }),
        ]}
        onSelectIssue={vi.fn()}
      />,
    );
    const region = screen.getByRole('region', { name: /recent failures/i });
    const items = within(region).getAllByRole('listitem');
    expect(items.map((li) => li.textContent)).toEqual([
      expect.stringContaining('newest'),
      expect.stringContaining('middle'),
      expect.stringContaining('oldest'),
    ]);
    const time = items[0].querySelector('time');
    expect(time?.getAttribute('dateTime')).toBe('2026-09-26T11:00:00Z');
  });

  it('shows the repeat count and a readable kind label', () => {
    render(
      <FailuresPanel
        failures={[failure({ kind: 'tracker_poll', identifier: undefined, count: 4 })]}
        onSelectIssue={vi.fn()}
      />,
    );
    expect(screen.getByText('Tracker poll')).toBeTruthy();
    expect(screen.getByLabelText('repeated 4 times')).toBeTruthy();
  });

  it('falls back to the raw kind for a kind this bundle does not know', () => {
    render(
      <FailuresPanel failures={[failure({ kind: 'quantum_flux' })]} onSelectIssue={vi.fn()} />,
    );
    expect(screen.getByText('quantum_flux')).toBeTruthy();
  });

  it('opens the issue when its identifier is clicked', () => {
    const onSelectIssue = vi.fn();
    render(
      <FailuresPanel failures={[failure({ identifier: 'ENG-7' })]} onSelectIssue={onSelectIssue} />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Open ENG-7' }));
    expect(onSelectIssue).toHaveBeenCalledWith('ENG-7');
  });
});
