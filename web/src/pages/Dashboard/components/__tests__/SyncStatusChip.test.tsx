import { render as rtlRender, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import type { ReactElement } from 'react';
import { beforeEach, describe, expect, it } from 'vitest';
import { useItervoxStore } from '../../../../store/itervoxStore';
import { makeSnapshot } from '../../../../test/fixtures/snapshots';
import type { SyncStatusRow } from '../../../../types/schemas';
import { LiveOpsStrip } from '../LiveOpsStrip';
import { SyncStatusChip, compactDuration, syncStatusChipModel } from '../SyncStatusChip';

const render = (ui: ReactElement) => rtlRender(ui, { wrapper: MemoryRouter });

const NOW = Date.parse('2026-10-14T10:00:00Z');

function row(overrides: Partial<SyncStatusRow> = {}): SyncStatusRow {
  return {
    mode: 'store',
    lastPullAt: new Date(NOW - 42_000).toISOString(),
    lagMs: 42_000,
    lagWarnAfterMs: 300_000,
    stale: false,
    syncIntervalMs: 60_000,
    pendingWrites: 0,
    pendingUrgent: 0,
    pendingBatched: 0,
    pendingRetrying: 0,
    pendingDegraded: 0,
    pendingRateLimited: 0,
    pushIntervalMs: 10_000,
    ...overrides,
  };
}

describe('compactDuration', () => {
  it('renders seconds, minutes, hours and days', () => {
    expect(compactDuration(42_000)).toBe('42s');
    expect(compactDuration(12 * 60_000)).toBe('12m');
    expect(compactDuration(3 * 3_600_000)).toBe('3h');
    expect(compactDuration(72 * 3_600_000)).toBe('3d');
    expect(compactDuration(-5)).toBe('0s');
  });
});

describe('syncStatusChipModel (#117)', () => {
  it('hides on daemons without syncStatus', () => {
    expect(syncStatusChipModel(undefined, NOW)).toBeNull();
  });

  it('shows the lag since the last pull, the pull time and pending writes', () => {
    const model = syncStatusChipModel(
      row({
        pendingWrites: 3,
        pendingUrgent: 1,
        pendingBatched: 2,
        nextPushAt: new Date(NOW + 8_000).toISOString(),
      }),
      NOW,
    );
    expect(model?.label).toBe('Synced 42s ago · 3 to push');
    expect(model?.warning).toBe(false);
    expect(model?.danger).toBe(false);
    expect(model?.title).toContain('Last pull from the tracker: ');
    expect(model?.title).toContain('Lag 42s (warns after 5m)');
    expect(model?.title).toContain('3 tracker writes pending (1 urgent, 2 batched) · next push ');
  });

  it('recomputes the lag on the dashboard clock and warns past the threshold', () => {
    // The snapshot said 42s, but no push came for 12 minutes.
    const later = NOW + 12 * 60_000;
    const model = syncStatusChipModel(row(), later);
    expect(model?.label).toMatch(/^Sync lag 12m · last pull /);
    expect(model?.stale).toBe(true);
    expect(model?.warning).toBe(true);
    expect(model?.danger).toBe(false);
  });

  it('is danger when stale and pulls are failing, with the classified error in the title', () => {
    const model = syncStatusChipModel(
      row({
        lastPullAt: new Date(NOW - 20 * 60_000).toISOString(),
        consecutiveFailures: 4,
        lastError: {
          at: new Date(NOW).toISOString(),
          kind: 'network',
          message: 'tracker unreachable (network error)',
        },
      }),
      NOW,
    );
    expect(model?.danger).toBe(true);
    expect(model?.title).toContain(
      'Last sync error (network, 4 in a row): tracker unreachable (network error)',
    );
  });

  it('reports a store that has not pulled yet', () => {
    const model = syncStatusChipModel(
      row({
        lastPullAt: undefined,
        lagMs: undefined,
        stale: true,
        lastError: {
          at: '2026-10-14T10:00:00Z',
          kind: 'auth',
          message: 'linear: api status 401 (check the tracker API key)',
        },
      }),
      NOW,
    );
    expect(model?.label).toBe('Sync: no pull yet');
    expect(model?.danger).toBe(true);
  });

  it('describes the local issue sync', () => {
    const model = syncStatusChipModel(row({ mode: 'local_sync', syncIntervalMs: 60_000 }), NOW);
    expect(model?.title).toContain('Local issue files synced with the remote tracker every 1m');
  });

  it('describes direct polling', () => {
    const model = syncStatusChipModel(row({ mode: 'direct', syncIntervalMs: 30_000 }), NOW);
    expect(model?.title).toContain('Direct polling every 30s');
  });
});

describe('SyncStatusChip in the LiveOps strip (#117)', () => {
  beforeEach(() => {
    useItervoxStore.setState({ snapshot: null });
  });

  it('renders pull lag and pending writes from the snapshot', () => {
    const pulledAt = new Date(Date.now() - 30_000).toISOString();
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        syncStatus: row({ lastPullAt: pulledAt, pendingWrites: 2, pendingBatched: 2 }),
      }),
    });
    render(<LiveOpsStrip />);
    const chip = screen.getByTestId('sync-status-chip');
    expect(chip).toHaveTextContent(/^Synced \d+s ago · 2 to push$/);
    expect(chip.getAttribute('title')).toContain(
      `Last pull from the tracker: ${new Date(pulledAt).toLocaleString()}`,
    );
    expect(chip.getAttribute('title')).toContain('2 tracker writes pending (2 batched)');
  });

  it('turns warning-toned when the lag is past the threshold', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        syncStatus: row({ lastPullAt: new Date(Date.now() - 15 * 60_000).toISOString() }),
      }),
    });
    render(<LiveOpsStrip />);
    const chip = screen.getByTestId('sync-status-chip');
    expect(chip).toHaveTextContent(/^Sync lag 15m · last pull /);
    expect(chip.className).toContain('bg-theme-warning-soft');
  });

  it('is absent when the daemon sends no syncStatus', () => {
    useItervoxStore.setState({ snapshot: makeSnapshot() });
    render(<LiveOpsStrip />);
    expect(screen.queryByTestId('sync-status-chip')).not.toBeInTheDocument();
  });

  it('renders standalone with the given clock', () => {
    render(<SyncStatusChip row={row()} now={NOW} />);
    expect(screen.getByTestId('sync-status-chip')).toHaveTextContent('Synced 42s ago');
  });
});
