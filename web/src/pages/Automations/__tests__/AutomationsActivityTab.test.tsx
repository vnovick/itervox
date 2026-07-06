import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { MemoryRouter } from 'react-router';
import AutomationsActivityTab from '../AutomationsActivityTab';
import { useItervoxStore } from '../../../store/itervoxStore';
import {
  makeAutomation,
  makeHistoryRow,
  makeRunningRow,
  makeSnapshot,
} from '../../../test/fixtures/snapshots';
import { formatRFC3339, minutesAgo } from '../../../test/fixtures/time';
import { resetAllStores } from '../../../test/resetStores';

afterEach(() => {
  cleanup();
  resetAllStores();
});

function renderActivity() {
  return render(
    <MemoryRouter>
      <AutomationsActivityTab />
    </MemoryRouter>,
  );
}

describe('AutomationsActivityTab', () => {
  it('renders an empty state when no automations are configured', () => {
    useItervoxStore.setState({ snapshot: makeSnapshot({ automations: [] }) });

    renderActivity();

    expect(screen.getByText(/no automations configured yet/i)).toBeInTheDocument();
  });

  it('sorts automation cards by newest running or finished automation run', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        automations: [
          makeAutomation({ id: 'older-rule', trigger: { type: 'cron' } }),
          makeAutomation({ id: 'newer-rule', trigger: { type: 'input_required' } }),
        ],
        running: [
          makeRunningRow({
            identifier: 'ENG-RUN',
            automationId: 'newer-rule',
            startedAt: formatRFC3339(minutesAgo(1)),
          }),
        ],
        history: [
          makeHistoryRow({
            identifier: 'ENG-OLD',
            automationId: 'older-rule',
            finishedAt: formatRFC3339(minutesAgo(45)),
          }),
        ],
      }),
    });

    renderActivity();

    const activity = screen.getByTestId('automations-activity');
    const activityText = activity.textContent;
    expect(activityText.indexOf('newer-rule')).toBeLessThan(activityText.indexOf('older-rule'));
    expect(screen.getByText('ENG-RUN')).toBeInTheDocument();
    expect(screen.getByText('ENG-OLD')).toBeInTheDocument();
  });
});
