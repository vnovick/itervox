import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import AppHeader from '../AppHeader';
import { useItervoxStore } from '../../store/itervoxStore';

function renderWithRouter(ui: React.ReactElement) {
  return render(<MemoryRouter>{ui}</MemoryRouter>);
}

vi.mock('zustand/react/shallow', () => ({
  useShallow: (fn: unknown) => fn,
}));

vi.mock('../../store/itervoxStore', () => ({
  useItervoxStore: vi.fn(),
}));

const mockUseItervoxStore = vi.mocked(useItervoxStore);

function setupStore(snapshotOverride: Record<string, unknown> = {}) {
  const state = {
    sseConnected: true,
    snapshot: {
      running: [],
      paused: [],
      retrying: [],
      inputRequired: [],
      maxConcurrentAgents: 3,
      ...snapshotOverride,
    },
  };
  mockUseItervoxStore.mockImplementation((selector: (s: typeof state) => unknown) =>
    selector(state),
  );
}

describe('AppHeader', () => {
  it('shows pending resume instead of idle when a reply is queued', () => {
    setupStore({
      inputRequired: [
        {
          identifier: 'ENG-1',
          sessionId: 'session-1',
          state: 'pending_input_resume',
          context: 'Reply received, waiting to resume.',
          queuedAt: '2026-04-15T00:00:00Z',
        },
      ],
    });

    renderWithRouter(<AppHeader />);

    // CORE-076 — the state chip uses the shared STATUS_META label.
    expect(screen.getByTestId('header-orchestrator-state')).toHaveTextContent('Resuming');
    expect(screen.getByText('1 resuming')).toBeInTheDocument();
  });

  it('shows input required instead of idle when human input is still needed', () => {
    setupStore({
      inputRequired: [
        {
          identifier: 'ENG-2',
          sessionId: 'session-2',
          state: 'input_required',
          context: 'Need approval',
          queuedAt: '2026-04-15T00:00:00Z',
        },
      ],
    });

    renderWithRouter(<AppHeader />);

    expect(screen.getByTestId('header-orchestrator-state')).toHaveTextContent('Needs input');
    expect(screen.getByText('1 needs input')).toBeInTheDocument();
  });

  it('renders the config-invalid banner when snapshot.configInvalid is set (T-26)', () => {
    setupStore({
      configInvalid: {
        path: 'WORKFLOW.md',
        error: 'invalid cron expression: bad token',
        retryAttempt: 3,
        retryAt: '2026-04-28T15:00:00Z',
      },
    });

    renderWithRouter(<AppHeader />);

    const banner = screen.getByTestId('config-invalid-banner');
    expect(banner).toBeInTheDocument();
    expect(banner).toHaveTextContent('WORKFLOW.md is invalid:');
    expect(banner).toHaveTextContent('invalid cron expression');
    expect(banner).toHaveTextContent('retry attempt 3');
  });

  it('hides the config-invalid banner when snapshot.configInvalid is null', () => {
    setupStore({
      configInvalid: null,
    });

    renderWithRouter(<AppHeader />);

    expect(screen.queryByTestId('config-invalid-banner')).toBeNull();
  });

  it('prioritizes input required over retrying in the state badge', () => {
    setupStore({
      retrying: [{ identifier: 'ENG-3', attempt: 1, dueAt: '2026-04-15T00:00:00Z' }],
      inputRequired: [
        {
          identifier: 'ENG-2',
          sessionId: 'session-2',
          state: 'input_required',
          context: 'Need approval',
          queuedAt: '2026-04-15T00:00:00Z',
        },
      ],
    });

    renderWithRouter(<AppHeader />);

    expect(screen.getByTestId('header-orchestrator-state')).toHaveTextContent('Needs input');
    expect(screen.getByText('1 needs input')).toBeInTheDocument();
    expect(screen.getByText('running/max')).toBeInTheDocument();
    expect(screen.getByText(/1 retrying/i)).toBeInTheDocument();
  });
  // CORE-086 — every count pill is a link to the dashboard section behind it.
  // The status word comes from STATUS_META (M5 CORE-076): "needs input", not
  // "need input" as the spec's regex had it.
  it.each([
    ['running', { running: [{ identifier: 'ENG-1' }] }, /running/, '/#running-sessions'],
    ['paused', { paused: ['ENG-2'] }, /paused/, '/#running-sessions'],
    [
      'needs input',
      {
        inputRequired: [
          { identifier: 'ENG-3', sessionId: 's', state: 'input_required', queuedAt: '' },
        ],
      },
      /needs input/,
      '/#attention-inbox',
    ],
    ['retrying', { retrying: [{ identifier: 'ENG-4' }] }, /retrying/, '/#retry-queue'],
  ])(
    'running, paused, need-input and retrying pills each link to their filtered dashboard view — %s',
    (_label, snapshot, name, href) => {
      setupStore(snapshot);
      renderWithRouter(<AppHeader />);
      const link = screen.getByRole('link', { name });
      expect(link).toHaveAttribute('href', href);
      expect(link).toHaveAccessibleName(/\d/);
    },
  );

  it('pill links keep the dashboard filters when already on the dashboard', () => {
    setupStore({ retrying: [{ identifier: 'ENG-4' }] });
    render(
      <MemoryRouter initialEntries={['/?view=list&q=eng']}>
        <AppHeader />
      </MemoryRouter>,
    );
    expect(screen.getByRole('link', { name: /retrying/ })).toHaveAttribute(
      'href',
      '/?view=list&q=eng#retry-queue',
    );
  });
});
