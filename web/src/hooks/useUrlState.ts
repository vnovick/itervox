import { useCallback, useEffect, useRef } from 'react';
import { useLocation, useNavigate, useParams, type Location } from 'react-router';
import { useShallow } from 'zustand/react/shallow';
import { useItervoxStore } from '../store/itervoxStore';
import { useUIStore, type AutomationsTab } from '../store/uiStore';
import { useIssue, useIssues } from '../queries/issues';
import { useToastStore } from '../store/toastStore';

// CORE-079 — URL state, single writer. Everything that puts app state into
// the URL goes through here (AuthGate's one-shot ?token= strip is the only
// other writer). Two hooks:
//
// useIssueUrlSync (mounted once, next to the global IssueDetailSlide):
//   ?issue=<id> <-> itervoxStore.selectedIdentifier. Opening the slide
//   PUSHES an entry (Back closes it, Forward reopens it); switching issues or
//   closing REPLACES. A deep link opens the slide. A syntactically invalid id
//   is cleared at once; an id that is merely unknown is cleared only after
//   the issues query has loaded successfully without it AND the single-issue
//   fetch has failed — never while loading, never on a query error.
//
// useDashboardUrlState (mounted by the Dashboard page):
//   ?view= / ?q= / ?state= <-> uiStore dashboard view mode, search and state
//   filter. Writes are debounced and always REPLACE, so typing never spams
//   history. Defaults are omitted from the URL.
//
// useLogsUrlSelection (mounted by the Logs page, CORE-085):
//   /logs/:identifier <-> the Logs selection (and itervoxStore.activeIssueId,
//   which Timeline shares). Choosing an issue in the sidebar PUSHES; every
//   automatic choice (bare /logs, the legacy ?identifier= link, the fallback
//   for an unknown id) REPLACES. An id in the URL wins over the auto-select
//   and is only abandoned once loading has settled without it.
//
// useAutomationsTabUrlState (mounted by the Automations page, CORE-085):
//   ?tab= <-> uiStore.automationsTab. A tab click PUSHES (Back returns to the
//   previous tab); arriving without ?tab= seeds it from the store (REPLACE).
//   'configure' is the default and is omitted.
//
// Every write merges into the router's current search (never rebuilds it),
// so unrelated params such as ?openAutomation= survive, and it always drops
// `token`, so a consumed ?token= can never be re-added even when the router
// read its location before AuthGate stripped it.

export const URL_FILTER_DEBOUNCE_MS = 250;

// Tracker identifiers: "ENG-12", "#12", "owner/repo#12", "local:7" …
const ISSUE_ID_RE = /^[A-Za-z0-9#][A-Za-z0-9_.\-#/:]{0,127}$/;
const NEVER_KEEP = ['token'] as const;

export function isSyntacticIssueId(value: string): boolean {
  return ISSUE_ID_RE.test(value);
}

/** Merge `patch` into `search`; null/'' deletes a key; `token` is always dropped. */
export function mergeSearch(search: string, patch: Record<string, string | null>): string {
  const params = new URLSearchParams(search);
  for (const key of NEVER_KEEP) params.delete(key);
  for (const [key, value] of Object.entries(patch)) {
    if (value === null || value === '') params.delete(key);
    else params.set(key, value);
  }
  const out = params.toString();
  return out ? `?${out}` : '';
}

/** History-state marker for an entry this hook pushed to open the slide. */
const ISSUE_PUSH_MARK = 'itervoxIssuePush';

function pushedByUs(loc: Location): boolean {
  const state: unknown = loc.state;
  return typeof state === 'object' && state !== null && ISSUE_PUSH_MARK in state;
}

function useWrite() {
  const navigate = useNavigate();
  return (
    loc: Location,
    patch: Record<string, string | null>,
    replace: boolean,
    state: unknown = replace ? loc.state : null,
  ) => {
    const search = mergeSearch(loc.search, patch);
    if (search === loc.search) return;
    // A replace keeps the entry's state (so an entry we pushed stays marked).
    void navigate({ pathname: loc.pathname, search, hash: loc.hash }, { replace, state });
  };
}

export function useIssueUrlSync(): void {
  const location = useLocation();
  const navigate = useNavigate();
  const write = useWrite();
  const selected = useItervoxStore((s) => s.selectedIdentifier);
  const setSelected = useItervoxStore((s) => s.setSelectedIdentifier);
  const urlIssue = new URLSearchParams(location.search).get('issue');

  const issues = useIssues();
  // Same key as IssueDetailSlide's fetch, so this adds no request.
  const single = useIssue(selected ?? '');

  // URL -> store (initial load, Back/Forward, deep links).
  useEffect(() => {
    if (urlIssue !== null && !isSyntacticIssueId(urlIssue)) {
      write(location, { issue: null }, true);
      return;
    }
    if (urlIssue !== useItervoxStore.getState().selectedIdentifier) {
      setSelected(urlIssue);
    }
    // Keyed on the URL value only: store changes are handled below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [urlIssue]);

  // store -> URL (the slide opened, switched or closed in the app).
  //  - open (none -> id): PUSH an entry marked as ours, so Back closes it;
  //  - switch (id -> id): REPLACE, keeping the mark;
  //  - close: if the current entry is one we pushed, go Back to pop it
  //    (BH-M5-2: a replace left a dead duplicate of the base entry);
  //    otherwise (a deep link, a reload) REPLACE.
  useEffect(() => {
    const current = useItervoxStore.getState().selectedIdentifier;
    if (current === urlIssue) return;
    if (current === null) {
      if (pushedByUs(location)) void navigate(-1);
      else write(location, { issue: null }, true);
      return;
    }
    if (urlIssue === null) write(location, { issue: current }, false, { [ISSUE_PUSH_MARK]: true });
    else write(location, { issue: current }, true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selected]);

  // Unknown id — from a deep link or from the UI (the inbox can open a failed
  // row whose issue has left the tracker): clear it once the issue list has
  // loaded successfully without it AND the single-issue fetch has failed.
  // Never while either is loading or when only the list errored.
  const listSettledWithout =
    issues.isSuccess && !issues.data.some((i) => i.identifier === selected);
  const singleFailed = single.isError;
  useEffect(() => {
    if (!selected || !(listSettledWithout && singleFailed)) return;
    useToastStore.getState().addToast(`${selected} was not found in the tracker.`, 'info');
    setSelected(null);
  }, [selected, listSettledWithout, singleFailed, setSelected]);
}

type ViewMode = ReturnType<typeof useUIStore.getState>['dashboardViewMode'];
const VIEW_MODES: readonly ViewMode[] = ['board', 'list', 'agents', 'deps', 'notifications'];
const DEFAULTS = { view: 'board', q: '', state: 'all' } as const;

interface DashboardUrlFilters {
  view: ViewMode;
  q: string;
  state: string;
}

function filtersFromSearch(search: string): DashboardUrlFilters {
  const params = new URLSearchParams(search);
  const view = params.get('view') ?? DEFAULTS.view;
  return {
    view: (VIEW_MODES as readonly string[]).includes(view) ? (view as ViewMode) : DEFAULTS.view,
    q: params.get('q') ?? DEFAULTS.q,
    state: params.get('state') ?? DEFAULTS.state,
  };
}

function hasFilterParams(search: string): boolean {
  const params = new URLSearchParams(search);
  return params.has('view') || params.has('q') || params.has('state');
}

function filtersKey(f: DashboardUrlFilters): string {
  return `${f.view}\u0000${f.q}\u0000${f.state}`;
}

export function useDashboardUrlState(): void {
  const location = useLocation();
  const write = useWrite();
  const { view, q, state } = useUIStore(
    useShallow((s) => ({
      view: s.dashboardViewMode,
      q: s.dashboardSearch,
      state: s.dashboardStateFilter,
    })),
  );
  // Latest location, read when a debounced write fires (an issue push may
  // have happened since the write was scheduled).
  const locationRef = useRef(location);
  // The URL filters last applied or written; URL -> store runs only when the
  // URL's filters change (initial load, Back/Forward), never because some
  // other param changed while the operator is mid-typing.
  const appliedRef = useRef<string | null>(null);

  useEffect(() => {
    locationRef.current = location;
  }, [location]);

  // URL -> store.
  const mountedRef = useRef(false);
  useEffect(() => {
    const firstRun = !mountedRef.current;
    mountedRef.current = true;
    // BH-M5-1 — arriving with no filter params (the sidebar link, a bare
    // "/") must not reset the operator's view to the defaults: keep the
    // store and let the store -> URL effect below seed the URL (replace).
    if (firstRun && !hasFilterParams(location.search)) {
      const ui = useUIStore.getState();
      appliedRef.current = filtersKey({
        view: ui.dashboardViewMode,
        q: ui.dashboardSearch,
        state: ui.dashboardStateFilter,
      });
      return;
    }
    const fromUrl = filtersFromSearch(location.search);
    const key = filtersKey(fromUrl);
    if (key === appliedRef.current) return;
    appliedRef.current = key;
    const ui = useUIStore.getState();
    if (ui.dashboardViewMode !== fromUrl.view) ui.setDashboardViewMode(fromUrl.view);
    if (ui.dashboardSearch !== fromUrl.q) ui.setDashboardSearch(fromUrl.q);
    if (ui.dashboardStateFilter !== fromUrl.state) ui.setDashboardStateFilter(fromUrl.state);
  }, [location.search]);

  // store -> URL, debounced, replace only.
  useEffect(() => {
    const timer = setTimeout(() => {
      const loc = locationRef.current;
      const next: DashboardUrlFilters = { view, q, state };
      if (filtersKey(filtersFromSearch(loc.search)) === filtersKey(next)) return;
      appliedRef.current = filtersKey(next);
      write(
        loc,
        {
          view: view === DEFAULTS.view ? null : view,
          q: q === DEFAULTS.q ? null : q,
          state: state === DEFAULTS.state ? null : state,
        },
        true,
      );
    }, URL_FILTER_DEBOUNCE_MS);
    return () => {
      clearTimeout(timer);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [view, q, state]);
}

// ─── CORE-085 — /logs/:identifier ────────────────────────────────────────────

/** Path of the Logs page for `identifier` (encoded: ids may contain / and #). */
export function logsPath(identifier?: string | null): string {
  return identifier ? `/logs/${encodeURIComponent(identifier)}` : '/logs';
}

export interface LogsUrlSelectionInput {
  /** Every identifier the sidebar can show, before the search filter. */
  union: readonly string[];
  /** The sidebar rows after the search filter, in display order. */
  visible: readonly string[];
  /** Issues, snapshot and log identifiers have all loaded (or failed). */
  settled: boolean;
}

export function useLogsUrlSelection({ union, visible, settled }: LogsUrlSelectionInput): {
  selectedId: string;
  select: (identifier: string) => void;
} {
  const location = useLocation();
  const navigate = useNavigate();
  const { identifier: urlId = null } = useParams();
  const legacyId = new URLSearchParams(location.search).get('identifier');
  const storeId = useItervoxStore((s) => s.activeIssueId);
  const setStoreId = useItervoxStore((s) => s.setActiveIssueId);

  const go = useCallback(
    (identifier: string | null, replace: boolean) => {
      void navigate(
        {
          pathname: logsPath(identifier),
          search: mergeSearch(location.search, { identifier: null }),
          hash: location.hash,
        },
        { replace },
      );
    },
    [navigate, location.search, location.hash],
  );

  const inUnion = (id: string) => union.includes(id);
  const firstVisible = visible.length > 0 ? visible[0] : null;
  const unionKey = union.join('\u0000');

  useEffect(() => {
    // Legacy deep link (AutomationActivityCard used /logs?identifier=).
    if (urlId === null && legacyId) {
      go(isSyntacticIssueId(legacyId) ? legacyId : null, true);
      return;
    }
    if (urlId !== null) {
      if (!isSyntacticIssueId(urlId)) {
        go(null, true);
        return;
      }
      if (urlId !== storeId) setStoreId(urlId);
      // Wait for loading to settle before deciding the id is unknown.
      if (inUnion(urlId) || !settled) return;
      go(firstVisible, true);
      return;
    }
    // Bare /logs: the last active issue (shared with Timeline), else the
    // first sidebar row.
    if (storeId && (inUnion(storeId) || !settled)) {
      go(storeId, true);
      return;
    }
    if (firstVisible) go(firstVisible, true);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- unionKey stands for union
  }, [urlId, legacyId, settled, unionKey, firstVisible, storeId, go, setStoreId]);

  const select = useCallback(
    (identifier: string) => {
      if (identifier === urlId) return;
      go(identifier, false);
    },
    [go, urlId],
  );

  return { selectedId: urlId ?? '', select };
}

// ─── CORE-085 — Automations ?tab= ────────────────────────────────────────────

const AUTOMATIONS_TABS: readonly AutomationsTab[] = ['configure', 'activity'];
const DEFAULT_AUTOMATIONS_TAB: AutomationsTab = 'configure';

function tabFromSearch(search: string): AutomationsTab | null {
  const raw = new URLSearchParams(search).get('tab');
  return raw !== null && (AUTOMATIONS_TABS as readonly string[]).includes(raw)
    ? (raw as AutomationsTab)
    : null;
}

export function useAutomationsTabUrlState(): [AutomationsTab, (tab: AutomationsTab) => void] {
  const location = useLocation();
  const write = useWrite();
  const storeTab = useUIStore((s) => s.automationsTab);
  const setStoreTab = useUIStore((s) => s.setAutomationsTab);
  const urlTab = tabFromSearch(location.search);

  useEffect(() => {
    if (urlTab !== null) {
      // URL -> store (deep link, reload, Back/Forward).
      if (urlTab !== useUIStore.getState().automationsTab) setStoreTab(urlTab);
      return;
    }
    // No (valid) ?tab=: seed it from the remembered tab so a reload keeps it.
    const remembered = useUIStore.getState().automationsTab;
    write(location, { tab: remembered === DEFAULT_AUTOMATIONS_TAB ? null : remembered }, true);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- keyed on the URL tab only
  }, [urlTab]);

  const setTab = useCallback(
    (tab: AutomationsTab) => {
      setStoreTab(tab);
      write(location, { tab: tab === DEFAULT_AUTOMATIONS_TAB ? null : tab }, false);
    },
    [location, setStoreTab, write],
  );

  return [urlTab ?? storeTab, setTab];
}
