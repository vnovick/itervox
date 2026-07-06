import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import Automations from '../index';
import { useUIStore } from '../../../store/uiStore';

vi.mock('../../../components/common/PageMeta', () => ({
  default: () => null,
}));

// AutomationsCard pulls in form modal + live mutations; stub it for the tab
// test so we only exercise the routing logic on this page.
vi.mock('../../Settings/AutomationsCard', () => ({
  AutomationsCard: () => <div data-testid="configure-tab-body" />,
}));

vi.mock('../AutomationsActivityTab', () => ({
  default: () => <div data-testid="activity-tab-body" />,
}));

vi.mock('../../Settings/useSettingsPageData', () => ({
  useSettingsPageData: () => ({
    automations: [],
    automationProfileOptions: [],
    trackerStateOptions: [],
    automationLabelOptions: [],
    setAutomations: vi.fn(),
    setAutomationsTyped: vi.fn(),
  }),
}));

describe('Automations tabs (T-1)', () => {
  beforeEach(() => {
    useUIStore.setState({ automationsTab: 'configure' });
  });

  it('renders the Configure tab by default', () => {
    render(
      <MemoryRouter>
        <Automations />
      </MemoryRouter>,
    );
    expect(screen.getByTestId('configure-tab-body')).toBeInTheDocument();
    expect(screen.getByTestId('configure-tab-body')).toBeVisible();
    expect(screen.getByTestId('activity-tab-body')).not.toBeVisible();
    const configureBtn = screen.getByRole('tab', { name: /configure/i });
    expect(configureBtn).toHaveAttribute('aria-selected', 'true');
  });

  it('switches to the Activity tab on click and persists the choice in uiStore', () => {
    render(
      <MemoryRouter>
        <Automations />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole('tab', { name: /activity/i }));
    expect(screen.getByTestId('activity-tab-body')).toBeVisible();
    expect(screen.getByTestId('configure-tab-body')).not.toBeVisible();
    expect(useUIStore.getState().automationsTab).toBe('activity');
  });

  it('reads the persisted tab from uiStore on mount', () => {
    useUIStore.setState({ automationsTab: 'activity' });
    render(
      <MemoryRouter>
        <Automations />
      </MemoryRouter>,
    );
    expect(screen.getByTestId('activity-tab-body')).toBeVisible();
    expect(screen.getByTestId('configure-tab-body')).not.toBeVisible();
    const activityBtn = screen.getByRole('tab', { name: /activity/i });
    expect(activityBtn).toHaveAttribute('aria-selected', 'true');
  });

  it('exposes both tabs as keyboard-reachable roles with proper ARIA wiring', () => {
    render(
      <MemoryRouter>
        <Automations />
      </MemoryRouter>,
    );
    const tablist = screen.getByRole('tablist', { name: /automations sections/i });
    expect(tablist).toBeInTheDocument();
    const tabs = screen.getAllByRole('tab');
    expect(tabs).toHaveLength(2);
    const activeIds = tabs.map((t) => t.getAttribute('aria-selected'));
    expect(activeIds).toContain('true');
    for (const tab of tabs) {
      const panelId = tab.getAttribute('aria-controls');
      expect(panelId).toBeTruthy();
      const panel = document.getElementById(panelId ?? '');
      expect(panel).toHaveAttribute('role', 'tabpanel');
      expect(panel).toHaveAttribute('aria-labelledby', tab.id);
    }
  });

  it('supports arrow, Home, and End keyboard navigation between tabs', () => {
    render(
      <MemoryRouter>
        <Automations />
      </MemoryRouter>,
    );

    const configure = screen.getByRole('tab', { name: /configure/i });
    const activity = screen.getByRole('tab', { name: /activity/i });

    configure.focus();
    fireEvent.keyDown(configure, { key: 'ArrowRight' });
    expect(activity).toHaveFocus();
    expect(useUIStore.getState().automationsTab).toBe('activity');

    fireEvent.keyDown(activity, { key: 'ArrowLeft' });
    expect(configure).toHaveFocus();
    expect(useUIStore.getState().automationsTab).toBe('configure');

    fireEvent.keyDown(configure, { key: 'End' });
    expect(activity).toHaveFocus();
    expect(useUIStore.getState().automationsTab).toBe('activity');

    fireEvent.keyDown(activity, { key: 'Home' });
    expect(configure).toHaveFocus();
    expect(useUIStore.getState().automationsTab).toBe('configure');
  });
});
