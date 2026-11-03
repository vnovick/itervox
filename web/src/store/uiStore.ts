import { create } from 'zustand';
import type { MemoryEntry, MemoryScope, MemoryType } from '../types/schemas';

type ViewMode = 'board' | 'list' | 'agents' | 'deps' | 'notifications';

export type AutomationsTab = 'configure' | 'activity';

/** Rules editor (#157): which layer tab, which view, and the preview inputs. */
export type RulesLayerTab = 'user' | 'project' | 'local';
export type RulesEditorView = 'form' | 'json';

/** Memory page (#202): the Entries list or the Lessons review queue. */
export type MemoryTab = 'entries' | 'lessons';

/** Lessons tab (#203): the review queue or the approved lessons not helping. */
export type LessonsView = 'queue' | 'approved' | 'not-helping';

export type MemoryUsageFilter = 'all' | 'used' | 'unused';

export interface MemoryFilters {
  search: string;
  type: MemoryType | 'all';
  scope: MemoryScope | 'all';
  profile: string;
  tag: string;
  usage: MemoryUsageFilter;
}

export const DEFAULT_MEMORY_FILTERS: MemoryFilters = {
  search: '',
  type: 'all',
  scope: 'all',
  profile: '',
  tag: '',
  usage: 'all',
};

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

  // #158 — schema-driven Settings section: which settings layer a save
  // writes, and the form or JSON view.
  settingsLayer: RulesLayerTab;
  settingsView: RulesEditorView;

  // #202 — Memory page: tab, filters, selected entry, and the entry the
  // last delete removed (its Undo puts it back).
  memoryTab: MemoryTab;
  memoryFilters: MemoryFilters;
  memorySelectedId: string | null;
  memoryUndo: MemoryEntry | null;
  // #203 — Lessons tab: review queue or "not helping".
  memoryLessonsView: LessonsView;
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
  setSettingsLayer: (layer: RulesLayerTab) => void;
  setSettingsView: (view: RulesEditorView) => void;
  setMemoryTab: (tab: MemoryTab) => void;
  setMemoryFilters: (patch: Partial<MemoryFilters>) => void;
  setMemorySelectedId: (id: string | null) => void;
  setMemoryUndo: (entry: MemoryEntry | null) => void;
  setMemoryLessonsView: (view: LessonsView) => void;
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
  settingsLayer: 'project',
  settingsView: 'form',
  memoryTab: 'entries',
  memoryFilters: DEFAULT_MEMORY_FILTERS,
  memorySelectedId: null,
  memoryUndo: null,
  memoryLessonsView: 'queue',

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
  setSettingsLayer: (settingsLayer) => {
    set({ settingsLayer });
  },
  setSettingsView: (settingsView) => {
    set({ settingsView });
  },
  setMemoryTab: (memoryTab) => {
    set({ memoryTab });
  },
  setMemoryFilters: (patch) => {
    set((s) => ({ memoryFilters: { ...s.memoryFilters, ...patch } }));
  },
  setMemorySelectedId: (memorySelectedId) => {
    set({ memorySelectedId });
  },
  setMemoryUndo: (memoryUndo) => {
    set({ memoryUndo });
  },
  setMemoryLessonsView: (memoryLessonsView) => {
    set({ memoryLessonsView });
  },
}));
