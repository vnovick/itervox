import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import type * as ReactRouter from 'react-router';
import IssueDetailHeader from '../IssueDetailHeader';
import { makeAutomation, makeHistoryRow, makeRunningRow } from '../../../test/fixtures/snapshots';
import type { TrackerIssue } from '../../../types/schemas';

const navigateMock = vi.fn();

vi.mock('react-router', async (importOriginal) => {
  const actual = await importOriginal<typeof ReactRouter>();
  return {
    ...actual,
    useNavigate: () => navigateMock,
  };
});

const baseIssue: TrackerIssue = {
  identifier: 'ENG-1',
  title: 'Test issue',
  state: 'In Progress',
  description: '',
  url: '',
  orchestratorState: 'running',
  turnCount: 0,
  tokens: 0,
  elapsedMs: 0,
  lastMessage: '',
  error: '',
};

beforeEach(() => {
  navigateMock.mockReset();
});

describe('IssueDetailHeader automation badge (T-9)', () => {
  it('renders nothing when no run carries an automationId', () => {
    render(
      <MemoryRouter>
        <IssueDetailHeader
          issue={baseIssue}
          runningRows={[]}
          history={[]}
          profileDefs={undefined}
          defaultBackend="claude"
        />
      </MemoryRouter>,
    );
    expect(screen.queryByTestId('automation-source-badge')).not.toBeInTheDocument();
  });

  it('shows the badge when the live run has an automationId', () => {
    const live = makeRunningRow({
      identifier: 'ENG-1',
      automationId: 'pr-on-input',
    });
    render(
      <MemoryRouter>
        <IssueDetailHeader
          issue={baseIssue}
          runningRows={[live]}
          history={[]}
          profileDefs={undefined}
          defaultBackend="claude"
          automations={[makeAutomation({ id: 'pr-on-input' })]}
        />
      </MemoryRouter>,
    );
    const badge = screen.getByTestId('automation-source-badge');
    expect(badge.textContent).toContain('pr-on-input');
    expect(badge).not.toBeDisabled();
  });

  it('falls back to the latest history row when no live run', () => {
    const older = makeHistoryRow({
      identifier: 'ENG-1',
      automationId: 'cron-old',
      finishedAt: '2025-12-30T00:00:00Z',
    });
    const newer = makeHistoryRow({
      identifier: 'ENG-1',
      automationId: 'cron-new',
      finishedAt: '2026-04-01T00:00:00Z',
    });
    render(
      <MemoryRouter>
        <IssueDetailHeader
          issue={baseIssue}
          runningRows={[]}
          history={[older, newer]}
          profileDefs={undefined}
          defaultBackend="claude"
          automations={[makeAutomation({ id: 'cron-old' }), makeAutomation({ id: 'cron-new' })]}
        />
      </MemoryRouter>,
    );
    const badge = screen.getByTestId('automation-source-badge');
    expect(badge.textContent).toContain('cron-new');
  });

  it('navigates with ?openAutomation=<id> when clicked', () => {
    const live = makeRunningRow({
      identifier: 'ENG-1',
      automationId: 'pr-on-input',
    });
    render(
      <MemoryRouter>
        <IssueDetailHeader
          issue={baseIssue}
          runningRows={[live]}
          history={[]}
          profileDefs={undefined}
          defaultBackend="claude"
          automations={[makeAutomation({ id: 'pr-on-input' })]}
        />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByTestId('automation-source-badge'));
    expect(navigateMock).toHaveBeenCalledWith('/automations?openAutomation=pr-on-input');
  });

  it('greys out the badge with a "Rule deleted" tooltip when the rule no longer exists', () => {
    const live = makeRunningRow({
      identifier: 'ENG-1',
      automationId: 'deleted-rule',
    });
    render(
      <MemoryRouter>
        <IssueDetailHeader
          issue={baseIssue}
          runningRows={[live]}
          history={[]}
          profileDefs={undefined}
          defaultBackend="claude"
          automations={[makeAutomation({ id: 'still-here' })]}
        />
      </MemoryRouter>,
    );
    const badge = screen.getByTestId('automation-source-badge');
    expect(badge).toBeDisabled();
    expect(badge.getAttribute('title')).toBe('Rule deleted');
  });
});
