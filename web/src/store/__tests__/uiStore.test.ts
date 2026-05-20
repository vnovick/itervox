import { describe, it, expect, beforeEach } from 'vitest';
import { useUIStore } from '../uiStore';

describe('uiStore', () => {
  beforeEach(() => {
    // Reset store between tests
    useUIStore.setState({
      dashboardViewMode: 'board',
      dashboardSearch: '',
      dashboardStateFilter: 'all',
      dashboardSearchVisible: false,
      expandedRunningId: null,
      expandedPausedId: null,
      logsIssueSearch: '',
    });
  });

  it('has correct initial state', () => {
    const state = useUIStore.getState();
    expect(state.dashboardViewMode).toBe('board');
    expect(state.dashboardSearch).toBe('');
    expect(state.dashboardStateFilter).toBe('all');
    expect(state.dashboardSearchVisible).toBe(false);
    expect(state.expandedRunningId).toBeNull();
    expect(state.expandedPausedId).toBeNull();
    expect(state.logsIssueSearch).toBe('');
  });

  it('sets dashboard view mode', () => {
    useUIStore.getState().setDashboardViewMode('list');
    expect(useUIStore.getState().dashboardViewMode).toBe('list');
  });

  it('sets dashboard search', () => {
    useUIStore.getState().setDashboardSearch('ENG-');
    expect(useUIStore.getState().dashboardSearch).toBe('ENG-');
  });

  it('sets dashboard state filter', () => {
    useUIStore.getState().setDashboardStateFilter('In Progress');
    expect(useUIStore.getState().dashboardStateFilter).toBe('In Progress');
  });

  it('toggles search visibility', () => {
    useUIStore.getState().setDashboardSearchVisible(true);
    expect(useUIStore.getState().dashboardSearchVisible).toBe(true);
  });

  it('sets logs issue search', () => {
    useUIStore.getState().setLogsIssueSearch('TIENG-');
    expect(useUIStore.getState().logsIssueSearch).toBe('TIENG-');
  });

  it('sets expanded running ID', () => {
    useUIStore.getState().setExpandedRunningId('ENG-42');
    expect(useUIStore.getState().expandedRunningId).toBe('ENG-42');
  });

  it('clears expanded running ID', () => {
    useUIStore.getState().setExpandedRunningId('ENG-42');
    useUIStore.getState().setExpandedRunningId(null);
    expect(useUIStore.getState().expandedRunningId).toBeNull();
  });

  it('sets expanded paused ID', () => {
    useUIStore.getState().setExpandedPausedId('ENG-99');
    expect(useUIStore.getState().expandedPausedId).toBe('ENG-99');
  });
});
