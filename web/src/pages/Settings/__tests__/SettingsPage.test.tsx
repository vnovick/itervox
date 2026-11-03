import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import Settings from '../index';
import { useItervoxStore } from '../../../store/itervoxStore';
import { makeSnapshot } from '../../../test/fixtures/snapshots';

vi.mock('../AccessCard', () => ({
  AccessCard: () => <div data-testid="access-card" />,
}));

const workspaceCardMock = vi.fn(() => <div data-testid="workspace-card" />);
const dependenciesCardMock = vi.fn(() => <div data-testid="dependencies-card" />);

const pageMetaMock = vi.hoisted(() => vi.fn<(props: unknown) => null>(() => null));
vi.mock('../../../components/common/PageMeta', () => ({
  default: (props: unknown) => pageMetaMock(props),
}));

vi.mock('../TrackerStatesCard', () => ({
  TrackerStatesCard: () => <div data-testid="tracker-states-card" />,
}));

vi.mock('../ProjectFilterCard', () => ({
  ProjectFilterCard: () => <div data-testid="project-filter-card" />,
}));

vi.mock('../SSHHostsCard', () => ({
  SSHHostsCard: () => <div data-testid="ssh-hosts-card" />,
}));

vi.mock('../GeneralCard', () => ({
  GeneralCard: () => <div data-testid="general-card" />,
}));

vi.mock('../WorkspaceCard', () => ({
  WorkspaceCard: (props: unknown) => workspaceCardMock(props),
}));

vi.mock('../DependenciesCard', () => ({
  DependenciesCard: (props: unknown) => dependenciesCardMock(props),
}));

vi.mock('../schema/SchemaSettingsCard', () => ({
  SchemaSettingsCard: () => <div data-testid="schema-settings-card" />,
}));

vi.mock('../CapacityCard', () => ({
  CapacityCard: () => <div data-testid="capacity-card">capacity</div>,
}));

vi.mock('../RateLimitsFailoverCard', () => ({
  RateLimitsFailoverCard: (props: unknown) => rateLimitsCardMock(props),
}));

vi.mock('../../../components/ui/button/ConfirmButton', () => ({
  ConfirmButton: () => <button type="button">confirm</button>,
}));

vi.mock('../../../queries/issues', () => ({
  useClearAllLogs: () => ({ isPending: false, mutate: vi.fn() }),
  useClearAllWorkspaces: () => ({ isPending: false, mutate: vi.fn() }),
}));

vi.mock('../useSettingsPageData', () => ({
  useSettingsPageData: () => ({
    activeStates: ['Todo'],
    terminalStates: ['Done'],
    completionState: 'Done',
    autoClearWorkspace: false,
    depsAnalysisMode: 'auto',
    autoReview: true,
    inlineInput: false,
    trackerKind: 'linear',
    activeProjectFilter: [],
    filterSupported: false,
    maxRetries: 5,
    failedState: '',
    maxSwitchesPerIssuePerWindow: 2,
    switchWindowHours: 6,
    trackerStateOptions: ['Todo', 'In Progress', 'Done', 'Backlog'],
    updateTrackerStates: vi.fn().mockResolvedValue(true),
    setAutoClearWorkspace: vi.fn().mockResolvedValue(true),
    setDepsAnalysisMode: vi.fn().mockResolvedValue(true),
    setProjectFilter: vi.fn().mockResolvedValue(true),
    setInlineInput: vi.fn().mockResolvedValue(true),
    setMaxRetries: vi.fn().mockResolvedValue(true),
    setFailedState: vi.fn().mockResolvedValue(true),
    setMaxSwitchesPerIssuePerWindow: vi.fn().mockResolvedValue(true),
    setSwitchWindowHours: vi.fn().mockResolvedValue(true),
  }),
}));

const rateLimitsCardMock = vi.hoisted(() =>
  vi.fn<(props: unknown) => React.JSX.Element>(() => (
    <div data-testid="rate-limits-card">rate limits</div>
  )),
);

describe('Settings page', () => {
  beforeEach(() => {
    workspaceCardMock.mockClear();
    dependenciesCardMock.mockClear();
    useItervoxStore.setState({ snapshot: makeSnapshot({ callerScope: 'admin' }) });
  });

  it('passes the live autoReview flag through to WorkspaceCard', () => {
    render(<Settings />, { wrapper: MemoryRouter });

    expect(workspaceCardMock).toHaveBeenCalled();
    expect(workspaceCardMock.mock.calls[0][0]).toEqual(
      expect.objectContaining({
        autoReviewEnabled: true,
      }),
    );
  });

  it('renders the Dependencies section with the live mode', () => {
    render(<Settings />, { wrapper: MemoryRouter });

    expect(dependenciesCardMock).toHaveBeenCalled();
    expect(dependenciesCardMock.mock.calls[0][0]).toEqual(
      expect.objectContaining({
        mode: 'auto',
      }),
    );
  });

  // CORE-093
  it('renders Rate limits & failover section', () => {
    render(<Settings />, { wrapper: MemoryRouter });
    const section = screen.getByRole('region', { name: 'Rate limits & failover' });
    expect(section).not.toBeNull();
    expect(section).toContainElement(screen.getByTestId('rate-limits-card'));
    expect(rateLimitsCardMock.mock.calls[0][0]).toEqual(
      expect.objectContaining({ maxSwitchesPerIssuePerWindow: 2, switchWindowHours: 6 }),
    );
  });

  it('Capacity card renders adjacent to Retries', () => {
    const { container } = render(<Settings />, { wrapper: MemoryRouter });
    const retries = container.querySelector('#section-retries')?.closest('section');
    const next = retries?.nextElementSibling;
    expect(next?.querySelector('h2')).toHaveTextContent('Capacity');
    expect(next).toContainElement(screen.getByTestId('capacity-card'));
  });

  it('renders the schema-driven All settings section (#158)', () => {
    const { container } = render(<Settings />, { wrapper: MemoryRouter });
    const section = container.querySelector('#section-all-settings')?.closest('section');
    expect(section).toContainElement(screen.getByTestId('schema-settings-card'));
  });

  it('page description names what Settings holds (no stale "profiles")', () => {
    render(<Settings />, { wrapper: MemoryRouter });
    expect(pageMetaMock.mock.calls[0][0]).toEqual(
      expect.objectContaining({ description: expect.not.stringContaining('profiles') }),
    );
  });

  it('links the Skills Inventory to Repo Analysis instead of rendering it (#200)', () => {
    render(<Settings />, { wrapper: MemoryRouter });
    expect(screen.queryByText('Skills Inventory')).not.toBeInTheDocument();
    expect(screen.queryByText(/loading skills inventory/i)).not.toBeInTheDocument();
    expect(screen.getByText(/skills inventory moved to/i)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Repo Analysis' })).toHaveAttribute(
      'href',
      '/repo#capabilities',
    );
  });

  // #214 — scope-aware hiding.
  it('shows Access and every settings card to an admin', () => {
    render(<Settings />, { wrapper: MemoryRouter });
    expect(screen.getByRole('region', { name: 'Access' })).toContainElement(
      screen.getByTestId('access-card'),
    );
    expect(screen.getByTestId('general-card')).toBeInTheDocument();
    expect(screen.queryByTestId('settings-scope-notice')).not.toBeInTheDocument();
    expect(screen.getByText('Reset all workspaces')).toBeInTheDocument();
  });

  it('hides settings, Access and workspace reset from an operator but keeps Clear all logs', () => {
    useItervoxStore.setState({ snapshot: makeSnapshot({ callerScope: 'operator' }) });
    render(<Settings />, { wrapper: MemoryRouter });
    expect(screen.getByTestId('settings-scope-notice')).toHaveTextContent('operator scope');
    expect(screen.queryByTestId('access-card')).not.toBeInTheDocument();
    expect(screen.queryByTestId('general-card')).not.toBeInTheDocument();
    expect(screen.queryByTestId('schema-settings-card')).not.toBeInTheDocument();
    expect(screen.getByText('Clear all logs')).toBeInTheDocument();
    expect(screen.queryByText('Reset all workspaces')).not.toBeInTheDocument();
  });

  it('shows a viewer no controls at all', () => {
    useItervoxStore.setState({ snapshot: makeSnapshot({ callerScope: 'viewer' }) });
    render(<Settings />, { wrapper: MemoryRouter });
    expect(screen.getByTestId('settings-scope-notice')).toHaveTextContent('viewer scope');
    expect(screen.queryByTestId('access-card')).not.toBeInTheDocument();
    expect(screen.queryByText('Clear all logs')).not.toBeInTheDocument();
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });
});
