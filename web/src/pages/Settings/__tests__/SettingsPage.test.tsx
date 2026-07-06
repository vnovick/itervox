import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render } from '@testing-library/react';
import Settings from '../index';

const workspaceCardMock = vi.fn(() => <div data-testid="workspace-card" />);

vi.mock('../../../components/common/PageMeta', () => ({
  default: () => null,
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
    setProjectFilter: vi.fn().mockResolvedValue(true),
    setInlineInput: vi.fn().mockResolvedValue(true),
    setMaxRetries: vi.fn().mockResolvedValue(true),
    setFailedState: vi.fn().mockResolvedValue(true),
    setMaxSwitchesPerIssuePerWindow: vi.fn().mockResolvedValue(true),
    setSwitchWindowHours: vi.fn().mockResolvedValue(true),
  }),
}));

describe('Settings page', () => {
  beforeEach(() => {
    workspaceCardMock.mockClear();
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
});
