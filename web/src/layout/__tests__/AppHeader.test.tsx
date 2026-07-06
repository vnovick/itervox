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

    expect(screen.getByText('reply received')).toBeInTheDocument();
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

    expect(screen.getByText('input required')).toBeInTheDocument();
    expect(screen.getByText('1 need input')).toBeInTheDocument();
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

    expect(screen.getByText('input required')).toBeInTheDocument();
    expect(screen.getByText('1 need input')).toBeInTheDocument();
    expect(screen.getByText('running/max')).toBeInTheDocument();
    expect(screen.getByText(/1 retrying/i)).toBeInTheDocument();
  });
});
