import { create } from 'zustand';

type ViewMode = 'board' | 'list' | 'agents' | 'deps' | 'notifications';

export type AutomationsTab = 'configure' | 'activity';

/** Rules editor (#157): which layer tab, which view, and the preview inputs. */
export type RulesLayerTab = 'user' | 'project' | 'local';
export type RulesEditorView = 'form' | 'json';

interface UIState {
  // Dashboard preferences (ZUSTAND-3) — persist across navigation
  dashboardViewMode: ViewMode;
  dashboardSearch: string;
  dashboardStateFilter: string;

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

  // CORE-095 — command palette (Mod+K or the header button).
  commandPaletteOpen: boolean;

  // #157 — Rules editor, kept across navigation.
  rulesLayer: RulesLayerTab;
  rulesView: RulesEditorView;
  rulesPreviewProfile: string;
  rulesPreviewIssue: string;
}

interface UIActions {
  setDashboardViewMode: (mode: ViewMode) => void;
  setDashboardSearch: (search: string) => void;
  setDashboardStateFilter: (filter: string) => void;
  setExpandedRunningId: (id: string | null) => void;
  setExpandedPausedId: (id: string | null) => void;
  setAutomationsTab: (tab: AutomationsTab) => void;
  setLogsAutomationOnly: (value: boolean) => void;
  setLogsIssueSearch: (search: string) => void;
  setTimelineAutomationOnly: (value: boolean) => void;
  setCommandPaletteOpen: (open: boolean) => void;
  setRulesLayer: (layer: RulesLayerTab) => void;
  setRulesView: (view: RulesEditorView) => void;
  setRulesPreviewProfile: (profile: string) => void;
  setRulesPreviewIssue: (issue: string) => void;
}

export const useUIStore = create<UIState & UIActions>((set) => ({
  dashboardViewMode: 'board',
  dashboardSearch: '',
  dashboardStateFilter: 'all',
  expandedRunningId: null,
  expandedPausedId: null,
  automationsTab: 'configure',
  logsAutomationOnly: false,
  logsIssueSearch: '',
  timelineAutomationOnly: false,
  commandPaletteOpen: false,
  rulesLayer: 'project',
  rulesView: 'form',
  rulesPreviewProfile: '',
  rulesPreviewIssue: '',

  setDashboardViewMode: (dashboardViewMode) => {
    set({ dashboardViewMode });
  },
  setDashboardSearch: (dashboardSearch) => {
    set({ dashboardSearch });
  },
  setDashboardStateFilter: (dashboardStateFilter) => {
    set({ dashboardStateFilter });
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
  setCommandPaletteOpen: (commandPaletteOpen) => {
    set({ commandPaletteOpen });
  },
  setRulesLayer: (rulesLayer) => {
    set({ rulesLayer });
  },
  setRulesView: (rulesView) => {
    set({ rulesView });
  },
  setRulesPreviewProfile: (rulesPreviewProfile) => {
    set({ rulesPreviewProfile });
  },
  setRulesPreviewIssue: (rulesPreviewIssue) => {
    set({ rulesPreviewIssue });
  },
}));
