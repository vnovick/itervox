// CORE-079 — URL-addressable issue selection (?issue=) and dashboard
// view/filters, exercised through the router's memory history (and, for the
// AuthGate token strip, the browser history).
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, render, waitFor } from '@testing-library/react';
import { QueryClientProvider } from '@tanstack/react-query';
import { BrowserRouter, createMemoryRouter, RouterProvider } from 'react-router';
import { authedFetch } from '../../auth/authedFetch';
import { makeTestQueryClient } from '../../test/queryClient';
import { resetAllStores } from '../../test/resetStores';
import { makeIssue } from '../../test/fixtures/issues';
import { useItervoxStore } from '../../store/itervoxStore';
import { useUIStore } from '../../store/uiStore';
import { useEffect, useState } from 'react';
import {
  isSyntacticIssueId,
  logsPath,
  mergeSearch,
  URL_FILTER_DEBOUNCE_MS,
  useAutomationsTabUrlState,
  useDashboardUrlState,
  useIssueUrlSync,
  useLogsUrlSelection,
} from '../useUrlState';

vi.mock('../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
const mockFetch = vi.mocked(authedFetch);

function urlOf(input: RequestInfo | URL): string {
  if (typeof input === 'string') return input;
  return input instanceof URL ? input.href : input.url;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/** issues: the /api/v1/issues answer; 'pending' never settles; 'error' is a 500. */
function routeIssues(issues: ReturnType<typeof makeIssue>[] | 'pending' | 'error') {
  let settle: ((r: Response) => void) | null = null;
  mockFetch.mockImplementation((input: RequestInfo | URL) => {
    const url = urlOf(input);
    if (url === '/api/v1/issues') {
      if (issues === 'pending') {
        return new Promise<Response>((resolve) => {
          settle = resolve;
        });
      }
      if (issues === 'error') return Promise.resolve(json({ error: { message: 'down' } }, 500));
      return Promise.resolve(json(issues));
    }
    const m = /^\/api\/v1\/issues\/([^/]+)$/.exec(url);
    if (m) {
      const id = decodeURIComponent(m[1]);
      const hit = Array.isArray(issues) ? issues.find((i) => i.identifier === id) : undefined;
      return Promise.resolve(hit ? json(hit) : json({ error: { message: 'not found' } }, 404));
    }
    return Promise.resolve(json({}));
  });
  return {
    resolve(list: ReturnType<typeof makeIssue>[]) {
      settle?.(json(list));
    },
  };
}

function Harness({ dashboard = true }: { dashboard?: boolean }) {
  useIssueUrlSync();
  return dashboard ? <DashboardHarness /> : null;
}
function DashboardHarness() {
  useDashboardUrlState();
  return null;
}

function renderMemory(initialEntries: string[], initialIndex?: number) {
  const router = createMemoryRouter(
    [
      { path: '/', element: <Harness /> },
      { path: '*', element: <Harness dashboard={false} /> },
    ],
    { initialEntries, initialIndex },
  );
  render(
    <QueryClientProvider client={makeTestQueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  const search = () => new URLSearchParams(router.state.location.search);
  return { router, search };
}

const selected = () => useItervoxStore.getState().selectedIdentifier;

beforeEach(() => {
  resetAllStores();
  mockFetch.mockReset();
});

describe('useUrlState', () => {
  it('deep link opens the issue slide (?issue= selects the issue)', async () => {
    routeIssues([makeIssue({ identifier: 'ENG-1' })]);
    renderMemory(['/?issue=ENG-1']);
    await waitFor(() => {
      expect(selected()).toBe('ENG-1');
    });
  });

  it('opening pushes ?issue=, Back closes the slide and Forward reopens it', async () => {
    routeIssues([makeIssue({ identifier: 'ENG-1' })]);
    const { router, search } = renderMemory(['/']);
    act(() => {
      useItervoxStore.getState().setSelectedIdentifier('ENG-1');
    });
    await waitFor(() => {
      expect(search().get('issue')).toBe('ENG-1');
    });
    expect(router.state.historyAction).toBe('PUSH');
    await act(async () => {
      await router.navigate(-1);
    });
    await waitFor(() => {
      expect(selected()).toBeNull();
    });
    expect(search().get('issue')).toBeNull();
    await act(async () => {
      await router.navigate(1);
    });
    await waitFor(() => {
      expect(selected()).toBe('ENG-1');
    });
  });

  it('closing the slide removes ?issue= without adding a history entry', async () => {
    routeIssues([makeIssue({ identifier: 'ENG-1' })]);
    const { router, search } = renderMemory(['/?issue=ENG-1']);
    await waitFor(() => {
      expect(selected()).toBe('ENG-1');
    });
    act(() => {
      useItervoxStore.getState().setSelectedIdentifier(null);
    });
    await waitFor(() => {
      expect(search().get('issue')).toBeNull();
    });
    expect(router.state.historyAction).toBe('REPLACE');
  });

  it('preserves unrelated params (openAutomation)', async () => {
    routeIssues([makeIssue({ identifier: 'ENG-1' })]);
    const { search } = renderMemory(['/?openAutomation=auto-1']);
    act(() => {
      useItervoxStore.getState().setSelectedIdentifier('ENG-1');
    });
    await waitFor(() => {
      expect(search().get('issue')).toBe('ENG-1');
    });
    expect(search().get('openAutomation')).toBe('auto-1');
    act(() => {
      useItervoxStore.getState().setSelectedIdentifier(null);
    });
    await waitFor(() => {
      expect(search().get('issue')).toBeNull();
    });
    expect(search().get('openAutomation')).toBe('auto-1');
  });

  it('never reintroduces a consumed ?token= (AuthGate strip then a slide open leaves no token in the URL)', async () => {
    routeIssues([makeIssue({ identifier: 'ENG-1' })]);
    window.history.replaceState(null, '', '/?token=secret&openAutomation=a1');
    render(
      <QueryClientProvider client={makeTestQueryClient()}>
        <BrowserRouter>
          <Harness dashboard={false} />
        </BrowserRouter>
      </QueryClientProvider>,
    );
    // AuthGate's one-shot strip runs outside the router (history.replaceState),
    // so the router's own location can still carry the token.
    window.history.replaceState(null, '', '/?openAutomation=a1');
    act(() => {
      useItervoxStore.getState().setSelectedIdentifier('ENG-1');
    });
    await waitFor(() => {
      expect(new URLSearchParams(window.location.search).get('issue')).toBe('ENG-1');
    });
    expect(window.location.search).not.toContain('token');
    expect(new URLSearchParams(window.location.search).get('openAutomation')).toBe('a1');
    window.history.replaceState(null, '', '/');
  });

  it('unknown issue id is ignored and removed', async () => {
    routeIssues([makeIssue({ identifier: 'ENG-1' })]);
    const { search } = renderMemory(['/?issue=ENG-404']);
    await waitFor(() => {
      expect(search().get('issue')).toBeNull();
    });
    expect(selected()).toBeNull();
  });

  it('unknown issue id is kept while the issues query is loading or errored and removed only after a successful load without it', async () => {
    const pending = routeIssues('pending');
    const { search } = renderMemory(['/?issue=ENG-404']);
    await waitFor(() => {
      expect(selected()).toBe('ENG-404');
    });
    // Still loading: kept.
    await new Promise((r) => setTimeout(r, 30));
    expect(search().get('issue')).toBe('ENG-404');
    pending.resolve([makeIssue({ identifier: 'ENG-1' })]);
    await waitFor(() => {
      expect(search().get('issue')).toBeNull();
    });
    expect(selected()).toBeNull();

    // One app at a time: both harnesses share the global selection store.
    cleanup();
    resetAllStores();
    routeIssues('error');
    const errored = renderMemory(['/?issue=ENG-405']);
    await waitFor(() => {
      expect(selected()).toBe('ENG-405');
    });
    await new Promise((r) => setTimeout(r, 30));
    expect(errored.search().get('issue')).toBe('ENG-405');
  });

  it('a syntactically invalid ?issue= is cleared immediately', async () => {
    routeIssues('pending');
    const { search } = renderMemory(['/?issue=%3Cscript%3E&openAutomation=a']);
    await waitFor(() => {
      expect(search().get('issue')).toBeNull();
    });
    expect(selected()).toBeNull();
    expect(search().get('openAutomation')).toBe('a');
  });

  it('dashboard view and filters hydrate from the URL', async () => {
    routeIssues([]);
    renderMemory(['/?view=list&q=auth&state=Todo']);
    await waitFor(() => {
      expect(useUIStore.getState().dashboardViewMode).toBe('list');
    });
    expect(useUIStore.getState().dashboardSearch).toBe('auth');
    expect(useUIStore.getState().dashboardStateFilter).toBe('Todo');
  });

  it('typing in filters replaces the URL (no history spam) and Back leaves the dashboard', async () => {
    routeIssues([]);
    const { router, search } = renderMemory(['/timeline', '/'], 1);
    for (const q of ['a', 'au', 'aut', 'auth']) {
      act(() => {
        useUIStore.getState().setDashboardSearch(q);
      });
    }
    act(() => {
      useUIStore.getState().setDashboardViewMode('list');
    });
    await waitFor(
      () => {
        expect(search().get('q')).toBe('auth');
      },
      { timeout: URL_FILTER_DEBOUNCE_MS * 4 },
    );
    expect(search().get('view')).toBe('list');
    expect(router.state.historyAction).toBe('REPLACE');
    await act(async () => {
      await router.navigate(-1);
    });
    expect(router.state.location.pathname).toBe('/timeline');
  });

  it('default view and filters are omitted from the URL', async () => {
    routeIssues([]);
    const { search } = renderMemory(['/?view=list']);
    await waitFor(() => {
      expect(useUIStore.getState().dashboardViewMode).toBe('list');
    });
    act(() => {
      useUIStore.getState().setDashboardViewMode('board');
    });
    await waitFor(
      () => {
        expect(search().get('view')).toBeNull();
      },
      { timeout: URL_FILTER_DEBOUNCE_MS * 4 },
    );
  });

  it('helpers: mergeSearch drops token and empty values; isSyntacticIssueId', () => {
    expect(mergeSearch('?token=t&a=1&issue=X', { issue: null, view: 'list' })).toBe(
      '?a=1&view=list',
    );
    expect(mergeSearch('', { issue: '' })).toBe('');
    expect(isSyntacticIssueId('ENG-12')).toBe(true);
    expect(isSyntacticIssueId('owner/repo#12')).toBe(true);
    expect(isSyntacticIssueId('<b>')).toBe(false);
    expect(isSyntacticIssueId('')).toBe(false);
    expect(isSyntacticIssueId('x'.repeat(200))).toBe(false);
  });

  // ── M5-close ────────────────────────────────────────────────────────────

  // BH-M5-1 — returning to the Dashboard through the sidebar (a URL with no
  // filter params) applied the defaults and wiped the operator's view.
  it('returning to the Dashboard with no params keeps the store and seeds the URL (replace)', async () => {
    routeIssues([]);
    useUIStore.setState({
      dashboardViewMode: 'list',
      dashboardSearch: 'auth',
      dashboardStateFilter: 'Todo',
    });
    const { router, search } = renderMemory(['/timeline']);
    await act(async () => {
      await router.navigate('/');
    });
    await waitFor(() => {
      expect(search().get('view')).toBe('list');
    });
    expect(search().get('q')).toBe('auth');
    expect(search().get('state')).toBe('Todo');
    expect(router.state.historyAction).toBe('REPLACE');
    expect(useUIStore.getState().dashboardViewMode).toBe('list');
    expect(useUIStore.getState().dashboardSearch).toBe('auth');
    expect(useUIStore.getState().dashboardStateFilter).toBe('Todo');
  });

  // BH-M5-2 — an in-app open pushed an entry and the close replaced it,
  // leaving [base, base]: Back after closing did nothing visible.
  it('closing an in-app opened slide pops its history entry (index returns to baseline)', async () => {
    routeIssues([makeIssue({ identifier: 'ENG-1' })]);
    window.history.replaceState(null, '', '/');
    render(
      <QueryClientProvider client={makeTestQueryClient()}>
        <BrowserRouter>
          <Harness dashboard={false} />
        </BrowserRouter>
      </QueryClientProvider>,
    );
    const idx = () => (window.history.state as { idx?: number } | null)?.idx ?? 0;
    const baseline = idx();
    act(() => {
      useItervoxStore.getState().setSelectedIdentifier('ENG-1');
    });
    await waitFor(() => {
      expect(new URLSearchParams(window.location.search).get('issue')).toBe('ENG-1');
    });
    expect(idx()).toBe(baseline + 1);
    // Switching issues while open replaces (still one entry).
    act(() => {
      useItervoxStore.getState().setSelectedIdentifier('ENG-1');
    });
    act(() => {
      useItervoxStore.getState().setSelectedIdentifier(null);
    });
    await waitFor(() => {
      expect(new URLSearchParams(window.location.search).get('issue')).toBeNull();
    });
    await waitFor(() => {
      expect(idx()).toBe(baseline);
    });
    window.history.replaceState(null, '', '/');
  });

  it('closing a deep-linked slide replaces (no Back into a closed slide, no dead entry)', async () => {
    routeIssues([makeIssue({ identifier: 'ENG-1' })]);
    const { router, search } = renderMemory(['/timeline', '/?issue=ENG-1'], 1);
    await waitFor(() => {
      expect(selected()).toBe('ENG-1');
    });
    act(() => {
      useItervoxStore.getState().setSelectedIdentifier(null);
    });
    await waitFor(() => {
      expect(search().get('issue')).toBeNull();
    });
    expect(router.state.historyAction).toBe('REPLACE');
    await act(async () => {
      await router.navigate(-1);
    });
    expect(router.state.location.pathname).toBe('/timeline');
  });

  // Verifier attack 3 — inbox "Open GONE-9" (a failed row whose issue is not
  // in the list) pushed ?issue=GONE-9, showed no dialog and never cleared.
  it('an unknown id opened from the UI is cleared like a URL one, without a dangling entry', async () => {
    routeIssues([makeIssue({ identifier: 'ENG-1' })]);
    const { router, search } = renderMemory(['/timeline', '/'], 1);
    act(() => {
      useItervoxStore.getState().setSelectedIdentifier('GONE-9');
    });
    await waitFor(() => {
      expect(selected()).toBeNull();
    });
    await waitFor(() => {
      expect(search().get('issue')).toBeNull();
    });
    expect(router.state.location.pathname).toBe('/');
    // The pushed entry was popped: one Back leaves the dashboard.
    await act(async () => {
      await router.navigate(-1);
    });
    expect(router.state.location.pathname).toBe('/timeline');
  });

  it('an issue missing from the list but fetched individually stays open', async () => {
    const issue = makeIssue({ identifier: 'DONE-1' });
    mockFetch.mockImplementation((input: RequestInfo | URL) => {
      const url = urlOf(input);
      if (url === '/api/v1/issues') return Promise.resolve(json([]));
      if (url === '/api/v1/issues/DONE-1') return Promise.resolve(json(issue));
      return Promise.resolve(json({}));
    });
    const { search } = renderMemory(['/']);
    act(() => {
      useItervoxStore.getState().setSelectedIdentifier('DONE-1');
    });
    await waitFor(() => {
      expect(search().get('issue')).toBe('DONE-1');
    });
    await new Promise((r) => setTimeout(r, 50));
    expect(selected()).toBe('DONE-1');
  });
});

// ─── CORE-085 — /logs/:identifier and the Automations ?tab= ──────────────────

interface LogsProps {
  union: string[];
  visible: string[];
  settled: boolean;
}
const logsHarness: {
  set: ((p: LogsProps) => void) | null;
  selected: string;
  select: ((id: string) => void) | null;
} = { set: null, selected: '', select: null };

function LogsHarness({ initial }: { initial: LogsProps }) {
  const [props, setProps] = useState(initial);
  const { selectedId, select } = useLogsUrlSelection(props);
  useEffect(() => {
    logsHarness.set = setProps;
    logsHarness.selected = selectedId;
    logsHarness.select = select;
  }, [selectedId, select]);
  return null;
}

const tabHarness: { tab: string; setTab: ((t: 'configure' | 'activity') => void) | null } = {
  tab: '',
  setTab: null,
};
function TabHarness() {
  const [tab, setTab] = useAutomationsTabUrlState();
  useEffect(() => {
    tabHarness.tab = tab;
    tabHarness.setTab = setTab;
  });
  return null;
}

function renderLogs(initialEntries: string[], initial: LogsProps) {
  const router = createMemoryRouter(
    [
      { path: '/logs/:identifier?', element: <LogsHarness initial={initial} /> },
      { path: '/automations', element: <TabHarness /> },
      { path: '*', element: null },
    ],
    { initialEntries },
  );
  render(<RouterProvider router={router} />);
  return router;
}

describe('useUrlState — logs selection and automations tab (CORE-085)', () => {
  it('logsPath encodes identifiers that contain / and #', () => {
    expect(logsPath('owner/repo#12')).toBe('/logs/owner%2Frepo%2312');
    expect(logsPath(null)).toBe('/logs');
  });

  it('a /logs/:identifier deep link selects that issue', async () => {
    const router = renderLogs(['/logs/ENG-2'], {
      union: ['ENG-1', 'ENG-2'],
      visible: ['ENG-1', 'ENG-2'],
      settled: true,
    });
    await waitFor(() => {
      expect(logsHarness.selected).toBe('ENG-2');
    });
    expect(useItervoxStore.getState().activeIssueId).toBe('ENG-2');
    expect(router.state.location.pathname).toBe('/logs/ENG-2');
  });

  it('an identifier absent from the first load is kept until loading settles, then kept when it appears', async () => {
    const router = renderLogs(['/logs/ENG-9'], { union: [], visible: [], settled: false });
    await waitFor(() => {
      expect(logsHarness.selected).toBe('ENG-9');
    });
    act(() => {
      logsHarness.set?.({ union: ['ENG-1'], visible: ['ENG-1'], settled: false });
    });
    expect(router.state.location.pathname).toBe('/logs/ENG-9');
    act(() => {
      logsHarness.set?.({ union: ['ENG-1', 'ENG-9'], visible: ['ENG-1', 'ENG-9'], settled: true });
    });
    expect(router.state.location.pathname).toBe('/logs/ENG-9');
    expect(logsHarness.selected).toBe('ENG-9');
  });

  it('an unknown identifier falls back to the first visible issue only after loading settles (replace)', async () => {
    const router = renderLogs(['/timeline', '/logs/NOPE-1'], {
      union: ['ENG-1', 'ENG-2'],
      visible: ['ENG-2'],
      settled: false,
    });
    await waitFor(() => {
      expect(logsHarness.selected).toBe('NOPE-1');
    });
    act(() => {
      logsHarness.set?.({ union: ['ENG-1', 'ENG-2'], visible: ['ENG-2'], settled: true });
    });
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/logs/ENG-2');
    });
    expect(router.state.historyAction).toBe('REPLACE');
  });

  it('bare /logs restores the stored active issue, else the first visible one (replace)', async () => {
    useItervoxStore.setState({ activeIssueId: 'ENG-2' });
    const router = renderLogs(['/logs'], {
      union: ['ENG-1', 'ENG-2'],
      visible: ['ENG-1', 'ENG-2'],
      settled: true,
    });
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/logs/ENG-2');
    });
    expect(router.state.historyAction).toBe('REPLACE');
    cleanup();
    useItervoxStore.setState({ activeIssueId: null });
    const router2 = renderLogs(['/logs'], {
      union: ['ENG-1', 'ENG-2'],
      visible: ['ENG-1', 'ENG-2'],
      settled: true,
    });
    await waitFor(() => {
      expect(router2.state.location.pathname).toBe('/logs/ENG-1');
    });
  });

  it('the legacy /logs?identifier= link lands on /logs/:identifier', async () => {
    const router = renderLogs(['/logs?identifier=ENG-2&keep=1'], {
      union: ['ENG-2'],
      visible: ['ENG-2'],
      settled: true,
    });
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/logs/ENG-2');
    });
    expect(router.state.location.search).toBe('?keep=1');
  });

  it('selecting an issue pushes and Back returns to the previous one', async () => {
    const router = renderLogs(['/logs/ENG-1'], {
      union: ['ENG-1', 'ENG-2'],
      visible: ['ENG-1', 'ENG-2'],
      settled: true,
    });
    await waitFor(() => {
      expect(logsHarness.selected).toBe('ENG-1');
    });
    act(() => {
      logsHarness.select?.('ENG-2');
    });
    await waitFor(() => {
      expect(router.state.location.pathname).toBe('/logs/ENG-2');
    });
    expect(router.state.historyAction).toBe('PUSH');
    await act(async () => {
      await router.navigate(-1);
    });
    expect(logsHarness.selected).toBe('ENG-1');
  });

  it('writing ?tab= preserves ?openAutomation= and unrelated params', async () => {
    const router = renderLogs(['/automations?openAutomation=auto-1&x=1'], {
      union: [],
      visible: [],
      settled: true,
    });
    await waitFor(() => {
      expect(tabHarness.tab).toBe('configure');
    });
    act(() => {
      tabHarness.setTab?.('activity');
    });
    await waitFor(() => {
      expect(new URLSearchParams(router.state.location.search).get('tab')).toBe('activity');
    });
    const params = new URLSearchParams(router.state.location.search);
    expect(params.get('openAutomation')).toBe('auto-1');
    expect(params.get('x')).toBe('1');
    expect(router.state.historyAction).toBe('PUSH');
    act(() => {
      tabHarness.setTab?.('configure');
    });
    await waitFor(() => {
      expect(new URLSearchParams(router.state.location.search).has('tab')).toBe(false);
    });
    expect(new URLSearchParams(router.state.location.search).get('openAutomation')).toBe('auto-1');
  });

  it('?tab= hydrates the tab and Back restores the previous tab', async () => {
    const router = renderLogs(['/automations?tab=activity'], {
      union: [],
      visible: [],
      settled: true,
    });
    await waitFor(() => {
      expect(tabHarness.tab).toBe('activity');
    });
    expect(useUIStore.getState().automationsTab).toBe('activity');
    act(() => {
      tabHarness.setTab?.('configure');
    });
    await waitFor(() => {
      expect(tabHarness.tab).toBe('configure');
    });
    await act(async () => {
      await router.navigate(-1);
    });
    await waitFor(() => {
      expect(tabHarness.tab).toBe('activity');
    });
  });
});
