import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import Settings from '../index';

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

vi.mock('../SkillsCard', () => ({
  SkillsCard: () => <div data-testid="skills-card" />,
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
  });

  it('passes the live autoReview flag through to WorkspaceCard', () => {
    render(<Settings />);

    expect(workspaceCardMock).toHaveBeenCalled();
    expect(workspaceCardMock.mock.calls[0][0]).toEqual(
      expect.objectContaining({
        autoReviewEnabled: true,
      }),
    );
  });

  it('renders the Dependencies section with the live mode', () => {
    render(<Settings />);

    expect(dependenciesCardMock).toHaveBeenCalled();
    expect(dependenciesCardMock.mock.calls[0][0]).toEqual(
      expect.objectContaining({
        mode: 'auto',
      }),
    );
  });

  // CORE-093
  it('renders Rate limits & failover section', () => {
    render(<Settings />);
    const section = screen.getByRole('region', { name: 'Rate limits & failover' });
    expect(section).not.toBeNull();
    expect(section).toContainElement(screen.getByTestId('rate-limits-card'));
    expect(rateLimitsCardMock.mock.calls[0][0]).toEqual(
      expect.objectContaining({ maxSwitchesPerIssuePerWindow: 2, switchWindowHours: 6 }),
    );
  });

  it('Capacity card renders adjacent to Retries', () => {
    const { container } = render(<Settings />);
    const retries = container.querySelector('#section-retries')?.closest('section');
    const next = retries?.nextElementSibling;
    expect(next?.querySelector('h2')).toHaveTextContent('Capacity');
    expect(next).toContainElement(screen.getByTestId('capacity-card'));
  });

  it('page description names what Settings holds (no stale "profiles")', () => {
    render(<Settings />);
    expect(pageMetaMock.mock.calls[0][0]).toEqual(
      expect.objectContaining({ description: expect.not.stringContaining('profiles') }),
    );
  });
});
