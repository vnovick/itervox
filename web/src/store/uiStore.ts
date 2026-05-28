import { create } from 'zustand';

type ViewMode = 'board' | 'list' | 'agents' | 'deps' | 'notifications';

export type AutomationsTab = 'configure' | 'activity';

interface UIState {
  // Dashboard preferences (ZUSTAND-3) — persist across navigation
  dashboardViewMode: ViewMode;
  dashboardSearch: string;
  dashboardStateFilter: string;
  dashboardSearchVisible: boolean;

  // Accordion expansion (ZUSTAND-6) — persist across re-renders
  expandedRunningId: string | null;
  expandedPausedId: string | null;

  // /automations sub-tab — Configure (editor) vs Activity (per-rule run history)
  automationsTab: AutomationsTab;

  // Logs page chip — restrict to AUTOMATION FIRED entries.
  logsAutomationOnly: boolean;
  logsIssueSearch: string;

  // Timeline page chip — restrict to runs with an automationId.
  timelineAutomationOnly: boolean;
}

interface UIActions {
  setDashboardViewMode: (mode: ViewMode) => void;
  setDashboardSearch: (search: string) => void;
  setDashboardStateFilter: (filter: string) => void;
  setDashboardSearchVisible: (visible: boolean) => void;
  setExpandedRunningId: (id: string | null) => void;
  setExpandedPausedId: (id: string | null) => void;
  setAutomationsTab: (tab: AutomationsTab) => void;
  setLogsAutomationOnly: (value: boolean) => void;
  setLogsIssueSearch: (search: string) => void;
  setTimelineAutomationOnly: (value: boolean) => void;
}

export const useUIStore = create<UIState & UIActions>((set) => ({
  dashboardViewMode: 'board',
  dashboardSearch: '',
  dashboardStateFilter: 'all',
  dashboardSearchVisible: false,
  expandedRunningId: null,
  expandedPausedId: null,
  automationsTab: 'configure',
  logsAutomationOnly: false,
  logsIssueSearch: '',
  timelineAutomationOnly: false,

  setDashboardViewMode: (dashboardViewMode) => {
    set({ dashboardViewMode });
  },
  setDashboardSearch: (dashboardSearch) => {
    set({ dashboardSearch });
  },
  setDashboardStateFilter: (dashboardStateFilter) => {
    set({ dashboardStateFilter });
  },
  setDashboardSearchVisible: (dashboardSearchVisible) => {
    set({ dashboardSearchVisible });
  },
  setExpandedRunningId: (expandedRunningId) => {
    set({ expandedRunningId });
  },
  setExpandedPausedId: (expandedPausedId) => {
    set({ expandedPausedId });
  },
  setAutomationsTab: (automationsTab) => {
    set({ automationsTab });
  },
  setLogsAutomationOnly: (logsAutomationOnly) => {
    set({ logsAutomationOnly });
  },
  setLogsIssueSearch: (logsIssueSearch) => {
    set({ logsIssueSearch });
  },
  setTimelineAutomationOnly: (timelineAutomationOnly) => {
    set({ timelineAutomationOnly });
  },
}));
