// Reset every Zustand store + browser auth storage between tests. The render
// harness calls this automatically; tests can also call it directly when they
// want a clean slate without rendering anything.

import { useAuthStore } from '../auth/authStore';
import { useTokenStore } from '../auth/tokenStore';
import { useItervoxStore } from '../store/itervoxStore';
import { useToastStore } from '../store/toastStore';
import { useUIStore } from '../store/uiStore';

export function resetAllStores(): void {
  // Cancel any pending toast auto-dismiss timers before clearing the map so
  // setTimeout callbacks can't fire against a wiped store.
  for (const timer of useToastStore.getState()._timers.values()) {
    clearTimeout(timer);
  }

  useAuthStore.setState({ status: 'unknown', rejectedReason: null });
  useTokenStore.setState({ token: null });
  useItervoxStore.setState({
    snapshot: null,
    logs: [],
    sseConnected: false,
    selectedIdentifier: null,
    activeIssueId: null,
    tokenSamples: [],
  });
  useToastStore.setState({ toasts: [], _timers: new Map() });
  useUIStore.setState({
    dashboardViewMode: 'board',
    dashboardSearch: '',
    dashboardStateFilter: 'all',
    dashboardSearchVisible: false,
    expandedRunningId: null,
    expandedPausedId: null,
    logsIssueSearch: '',
  });

  // Clear browser storage so token-store cross-tab state doesn't leak.
  try {
    sessionStorage.removeItem('itervox.apiToken');
    localStorage.removeItem('itervox.apiToken.persistent');
  } catch {
    // jsdom occasionally throws here on SecurityError; safe to ignore.
  }
}
