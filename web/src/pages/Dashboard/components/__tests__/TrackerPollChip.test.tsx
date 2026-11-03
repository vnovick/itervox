import { render as rtlRender, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import type { ReactElement } from 'react';
import { beforeEach, describe, expect, it } from 'vitest';
import { useItervoxStore } from '../../../../store/itervoxStore';
import { makeSnapshot } from '../../../../test/fixtures/snapshots';
import type { TrackerPollRow } from '../../../../types/schemas';
import { LiveOpsStrip } from '../LiveOpsStrip';
import { TrackerPollChip, trackerPollChipModel } from '../TrackerPollChip';

const render = (ui: ReactElement) => rtlRender(ui, { wrapper: MemoryRouter });

const NOW = Date.parse('2026-10-20T10:00:00Z');

function row(overrides: Partial<TrackerPollRow> = {}): TrackerPollRow {
  return {
    inFlight: false,
    lastDurationMs: 800,
    slowAfterMs: 45_000,
    slow: false,
    degraded: false,
    ...overrides,
  };
}

describe('trackerPollChipModel (#187)', () => {
  it('hides while the poll is healthy and on daemons without the field', () => {
    expect(trackerPollChipModel(undefined, NOW)).toBeNull();
    expect(trackerPollChipModel(row(), NOW)).toBeNull();
    expect(
      trackerPollChipModel(
        row({ inFlight: true, startedAt: new Date(NOW - 10_000).toISOString() }),
        NOW,
      ),
    ).toBeNull();
  });

  it('warns while a poll runs past the slow threshold, on the dashboard clock', () => {
    const model = trackerPollChipModel(
      row({ inFlight: true, startedAt: new Date(NOW - 50_000).toISOString(), runningForMs: 1 }),
      NOW,
    );
    expect(model?.label).toBe('Tracker poll running 50s');
    expect(model?.danger).toBe(false);
  });

  it('warns when the last poll was slow', () => {
    const model = trackerPollChipModel(row({ slow: true, lastDurationMs: 52_000 }), NOW);
    expect(model?.label).toBe('Tracker poll slow · last 52s');
  });

  it('is danger while the tracker breaker has the poll degraded', () => {
    const model = trackerPollChipModel(
      row({ degraded: true, consecutiveTransportFailures: 3 }),
      NOW,
    );
    expect(model?.label).toBe('Tracker degraded');
    expect(model?.danger).toBe(true);
    expect(model?.title).toContain('3 polls in a row');
  });
});

describe('TrackerPollChip', () => {
  it('renders nothing when healthy', () => {
    render(<TrackerPollChip row={row()} now={NOW} />);
    expect(screen.queryByTestId('tracker-poll-chip')).toBeNull();
  });

  it('renders the degraded tile', () => {
    render(<TrackerPollChip row={row({ degraded: true })} now={NOW} />);
    expect(screen.getByTestId('tracker-poll-chip')).toHaveTextContent('Tracker degraded');
  });
});

describe('LiveOpsStrip tracker poll chip', () => {
  beforeEach(() => {
    useItervoxStore.setState({ snapshot: null });
  });

  it('shows the chip from the snapshot slice', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({ trackerPoll: row({ degraded: true }) }),
    });
    render(<LiveOpsStrip />);
    expect(screen.getByTestId('tracker-poll-chip')).toHaveTextContent('Tracker degraded');
  });
});
