import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { AutomationActivityCard } from '../AutomationActivityCard';
import { makeAutomation, makeHistoryRow, makeRunningRow } from '../../../test/fixtures/snapshots';
import { formatRFC3339, minutesAgo, secondsAgo } from '../../../test/fixtures/time';

const renderCard = (
  ...args: Parameters<typeof AutomationActivityCard>
): ReturnType<typeof render> =>
  render(
    <MemoryRouter>
      <AutomationActivityCard {...args[0]} />
    </MemoryRouter>,
  );

describe('AutomationActivityCard (T-2)', () => {
  it('renders the automation id, trigger chip, and enabled chip', () => {
    const automation = makeAutomation({
      id: 'pr-on-input',
      enabled: true,
      trigger: { type: 'input_required' },
    });
    renderCard({ automation, running: [], history: [] });
    expect(screen.getByText('pr-on-input')).toBeInTheDocument();
    expect(screen.getByText(/input_required/i)).toBeInTheDocument();
    expect(screen.getByText(/enabled/i)).toBeInTheDocument();
  });

  it('shows "Never fired yet" when there is no history', () => {
    const automation = makeAutomation({ id: 'never-fired' });
    renderCard({ automation, running: [], history: [] });
    expect(screen.getByText(/never fired yet/i)).toBeInTheDocument();
  });

  it('lists last 10 runs newest-first and ignores rows from other automations', () => {
    const automation = makeAutomation({
      id: 'cron-nightly',
      trigger: { type: 'cron', cron: '0 3 * * *' },
    });
    // Distinct sessionIds per row — the fixture default would otherwise make
    // these distinct runs look like duplicates to the G-13 dedup pass.
    const history = [
      makeHistoryRow({
        identifier: 'ENG-1',
        sessionId: 'sess-eng-1',
        automationId: 'cron-nightly',
        finishedAt: formatRFC3339(minutesAgo(60)),
      }),
      makeHistoryRow({
        identifier: 'ENG-2',
        sessionId: 'sess-eng-2',
        automationId: 'cron-nightly',
        finishedAt: formatRFC3339(minutesAgo(5)),
      }),
      makeHistoryRow({
        identifier: 'ENG-OTHER',
        sessionId: 'sess-eng-other',
        automationId: 'a-different-rule',
        finishedAt: formatRFC3339(secondsAgo(30)),
      }),
    ];
    renderCard({ automation, running: [], history });
    const rows = screen.getByTestId('automation-runs-cron-nightly').querySelectorAll('li');
    expect(rows.length).toBe(2);
    // Newest first: ENG-2 (5m ago) before ENG-1 (60m ago).
    expect(rows[0].textContent).toContain('ENG-2');
    expect(rows[1].textContent).toContain('ENG-1');
    // Other rule's run must not leak in.
    expect(screen.queryByText('ENG-OTHER')).toBeNull();
  });

  it('caps the run list at 10 entries', () => {
    const automation = makeAutomation({ id: 'busy' });
    const history = Array.from({ length: 15 }, (_, i) =>
      makeHistoryRow({
        identifier: `ENG-${String(i)}`,
        // Distinct sessionIds — see comment in the newest-first test above.
        sessionId: `sess-busy-${String(i)}`,
        automationId: 'busy',
        finishedAt: formatRFC3339(minutesAgo(i + 1)),
      }),
    );
    renderCard({ automation, running: [], history });
    const rows = screen.getByTestId('automation-runs-busy').querySelectorAll('li');
    expect(rows.length).toBe(10);
  });

  it('computes success rate over completed history (whole percentages)', () => {
    const automation = makeAutomation({ id: 'mixed' });
    const history = [
      makeHistoryRow({ identifier: 'a', automationId: 'mixed', status: 'succeeded' }),
      makeHistoryRow({ identifier: 'b', automationId: 'mixed', status: 'succeeded' }),
      makeHistoryRow({ identifier: 'c', automationId: 'mixed', status: 'succeeded' }),
      makeHistoryRow({ identifier: 'd', automationId: 'mixed', status: 'failed' }),
    ];
    renderCard({ automation, running: [], history });
    expect(screen.getByText('75%')).toBeInTheDocument();
  });

  it('shows live runs alongside completed ones', () => {
    const automation = makeAutomation({ id: 'live' });
    const running = [
      makeRunningRow({
        identifier: 'ENG-LIVE',
        automationId: 'live',
        startedAt: formatRFC3339(secondsAgo(15)),
      }),
    ];
    renderCard({ automation, running, history: [] });
    expect(screen.getByText('ENG-LIVE')).toBeInTheDocument();
    expect(screen.getByText(/running/i)).toBeInTheDocument();
  });

  it('renders a Logs deep-link per run row', () => {
    const automation = makeAutomation({ id: 'r' });
    const history = [makeHistoryRow({ identifier: 'TIPRD-25', automationId: 'r' })];
    renderCard({ automation, running: [], history });
    const link = screen.getByTestId('automation-run-logs-link');
    expect(link.getAttribute('href')).toBe('/logs?identifier=TIPRD-25');
  });
});
