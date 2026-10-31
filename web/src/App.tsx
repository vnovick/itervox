import { BrowserRouter as Router, Routes, Route, Outlet } from 'react-router';
import { lazy, Suspense, useCallback, useEffect, useRef, useState } from 'react';
import { useItervoxSSE } from './hooks/useItervoxSSE';
import {
  buildSnapshotInvalidationFingerprint,
  useSnapshotInvalidation,
} from './hooks/useSnapshotInvalidation';
import { useItervoxStore } from './store/itervoxStore';
import IssueDetailSlide from './components/itervox/IssueDetailSlide';
import Toast from './components/common/Toast';
import { PageErrorBoundary } from './components/common/PageErrorBoundary';
import { NavLink } from './components/layout/NavLink';
import {
  AgentsIcon,
  AutomationsIcon,
  DashboardIcon,
  LogsIcon,
  MemoryIcon,
  RepoIcon,
  RulesIcon,
  SessionsIcon,
  SettingsIcon,
  TimelineIcon,
} from './components/layout/NavIcons';
import { ThemeToggle } from './components/ui/ThemeToggle/ThemeToggle';
import AppHeader from './layout/AppHeader';
import { useDialogLayer } from './components/ui/dialog/useDialogLayer';
import { useMultiTabWarning } from './hooks/useMultiTabWarning';
import { useAttentionCount } from './hooks/useOperatorQueue';
import { useIssueUrlSync } from './hooks/useUrlState';
import { inputRequiredRowState } from './utils/inputRequired';

import { PageLoader } from './components/common/PageLoader';
import { CommandPaletteHost } from './components/itervox/CommandPaletteHost';
import { AttentionNotifier } from './components/itervox/AttentionNotifier';
import { getApiContext, routerBasename } from './auth/apiBase';
const Dashboard = lazy(() => import('./pages/Dashboard'));
const Logs = lazy(() => import('./pages/Logs'));
const Sessions = lazy(() => import('./pages/Sessions'));
const Timeline = lazy(() => import('./pages/Timeline'));
const Agents = lazy(() => import('./pages/Agents'));
const Automations = lazy(() => import('./pages/Automations'));
const Rules = lazy(() => import('./pages/Rules'));
const RepoAnalysis = lazy(() => import('./pages/Repo'));
const Memory = lazy(() => import('./pages/Memory'));
const Settings = lazy(() => import('./pages/Settings'));
const NotFound = lazy(() => import('./pages/OtherPage/NotFound'));
const HubPage = lazy(() => import('./pages/Hub'));
// #231 — the phone layout: the hub's /hub/m and one issue at
// /hub/p/{project}/m/{issue}.
const HubMobilePage = lazy(() => import('./pages/HubMobile'));
const MobileIssuePage = lazy(() => import('./pages/MobileIssue'));

/** Tailwind's `md` breakpoint (the drawer is `md:hidden`). */
const MD_BREAKPOINT_QUERY = '(min-width: 768px)';

const NAV_ITEMS = [
  { to: '/', icon: <DashboardIcon />, label: 'Dashboard' },
  { to: '/timeline', icon: <TimelineIcon />, label: 'Timeline' },
  { to: '/sessions', icon: <SessionsIcon />, label: 'Sessions' },
  { to: '/logs', icon: <LogsIcon />, label: 'Logs' },
  { to: '/agents', icon: <AgentsIcon />, label: 'Agents' },
  { to: '/automations', icon: <AutomationsIcon />, label: 'Automations' },
  { to: '/rules', icon: <RulesIcon />, label: 'Rules' },
  { to: '/repo', icon: <RepoIcon />, label: 'Repo Analysis' },
  { to: '/memory', icon: <MemoryIcon />, label: 'Memory' },
  { to: '/settings', icon: <SettingsIcon />, label: 'Settings' },
] as const;

function SidebarContent() {
  // CORE-078 — same count as the attention inbox and the page title.
  const attention = useAttentionCount();
  // #178 — sessions waiting for input badge the Sessions tab.
  const waitingSessions = useItervoxStore(
    (s) =>
      s.snapshot?.inputRequired?.filter((r) => inputRequiredRowState(r) === 'input_required')
        .length ?? 0,
  );
  // #203 — proposed lessons waiting for review badge the Memory item.
  const proposedLessons = useItervoxStore((s) => s.snapshot?.proposedLessons ?? 0);
  const badgeFor = (to: string): number =>
    to === '/'
      ? attention
      : to === '/sessions'
        ? waitingSessions
        : to === '/memory'
          ? proposedLessons
          : 0;
  return (
    <>
      <div className="flex items-center gap-2.5 px-2.5 pt-1 pb-[18px]">
        <span aria-hidden="true" className="brand-tile" />
        <span className="text-theme-text text-[17px] font-bold tracking-[-0.01em]">Itervox</span>
      </div>
      <nav className="flex flex-col gap-0.5">
        {NAV_ITEMS.map((item) => (
          <NavLink
            key={item.to}
            to={item.to}
            icon={item.icon}
            label={item.label}
            badge={badgeFor(item.to)}
            badgeLabel={item.to === '/memory' ? 'lessons to review' : undefined}
          />
        ))}
      </nav>

      {/* Theme toggle pinned to the bottom */}
      <div className="mt-auto pt-6">
        <ThemeToggle />
      </div>
    </>
  );
}

// CORE-024 — exported (not just used internally) so a test can exercise the
// mobile nav drawer's `inert` behavior without needing to also mount the SSE
// hooks, routes and query client that the default App export wires up.
export function AppShell() {
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const drawerRef = useRef<HTMLDivElement>(null);

  const closeMobileNav = useCallback(() => {
    setMobileNavOpen(false);
  }, []);

  // M5-close BH-M5-6 — the drawer is md:hidden, so widening the window past
  // md hid it while it stayed open (focus trap and scroll state included).
  // Close it when the viewport crosses the breakpoint.
  useEffect(() => {
    if (!mobileNavOpen || typeof window.matchMedia !== 'function') return;
    const mq = window.matchMedia(MD_BREAKPOINT_QUERY);
    const onChange = (e: { matches: boolean }) => {
      if (e.matches) setMobileNavOpen(false);
    };
    mq.addEventListener('change', onChange);
    return () => {
      mq.removeEventListener('change', onChange);
    };
  }, [mobileNavOpen]);

  // CORE-067: the drawer joins the shared overlay stack — focus trap with
  // restore, and Escape closes it only when it is the top-most dialog. No
  // scroll lock: the drawer never locked the page and the content under it
  // is not scrollable on the widths where it shows.
  useDialogLayer({
    isOpen: mobileNavOpen,
    onClose: closeMobileNav,
    containerRef: drawerRef,
    lockScroll: false,
  });

  return (
    <div className="min-h-screen md:pl-[232px]">
      {/* Desktop sidebar: a labelled glass column, inset from the window. */}
      <aside className="glass-shell fixed top-4 bottom-4 left-4 z-40 hidden w-[200px] flex-col overflow-y-auto px-2.5 py-4 md:flex">
        <SidebarContent />
      </aside>

      {/* Mobile nav drawer — slides from left. CORE-024: while closed it
          previously stayed in the tab order and the a11y tree (hidden only
          via opacity-0/pointer-events-none), so `inert` now removes it from
          both whenever it's not open — but only then, so the dialog focus trap above
          keeps working while the drawer IS open. */}
      <div
        ref={drawerRef}
        role="dialog"
        aria-modal="true"
        aria-label="Navigation"
        inert={!mobileNavOpen}
        className={`fixed inset-0 z-50 transition-opacity duration-200 md:hidden ${
          mobileNavOpen ? 'pointer-events-auto opacity-100' : 'pointer-events-none opacity-0'
        }`}
      >
        <div
          className="glass-scrim absolute inset-0"
          aria-label="Close navigation"
          role="button"
          tabIndex={0}
          onClick={closeMobileNav}
          onKeyDown={(e) => {
            if (e.key === 'Enter' || e.key === ' ') closeMobileNav();
          }}
        />
        <aside
          className={`glass-sheet absolute top-2.5 bottom-2.5 left-2.5 flex w-[250px] max-w-[calc(100vw-40px)] flex-col overflow-y-auto px-2.5 py-5 transition-transform duration-200 ${
            mobileNavOpen ? 'translate-x-0' : '-translate-x-[110%]'
          }`}
        >
          <SidebarContent />
        </aside>
      </div>

      <main className="flex min-w-0 flex-col px-3 pb-6 md:pr-4 md:pl-0">
        <AppHeader
          onMenuClick={() => {
            setMobileNavOpen(true);
          }}
        />
        <div className="min-w-0 flex-1 pt-4">
          <Outlet />
        </div>
      </main>
    </div>
  );
}

// CORE-075 — moved to hooks/useSnapshotInvalidation (memoized on snapshot
// identity); re-exported for existing importers.
export { buildSnapshotInvalidationFingerprint };

// CORE-087 — the route table, exported so a test can render it without the
// SSE hooks. The catch-all 404 is inside AppShell so it keeps the sidebar and
// header navigation.
export function AppRouteTable() {
  return (
    <Routes>
      <Route
        path="/m/:identifier"
        element={
          <Suspense fallback={<PageLoader />}>
            <PageErrorBoundary>
              <MobileIssuePage />
            </PageErrorBoundary>
          </Suspense>
        }
      />
      <Route element={<AppShell />}>
        <Route
          index
          element={
            <Suspense fallback={<PageLoader />}>
              <PageErrorBoundary>
                <Dashboard />
              </PageErrorBoundary>
            </Suspense>
          }
        />
        <Route
          path="/timeline"
          element={
            <Suspense fallback={<PageLoader />}>
              <PageErrorBoundary>
                <Timeline />
              </PageErrorBoundary>
            </Suspense>
          }
        />
        <Route
          path="/logs/:identifier?"
          element={
            <Suspense fallback={<PageLoader />}>
              <PageErrorBoundary>
                <Logs />
              </PageErrorBoundary>
            </Suspense>
          }
        />
        <Route
          path="/sessions"
          element={
            <Suspense fallback={<PageLoader />}>
              <PageErrorBoundary>
                <Sessions />
              </PageErrorBoundary>
            </Suspense>
          }
        />
        <Route
          path="/settings"
          element={
            <Suspense fallback={<PageLoader />}>
              <PageErrorBoundary>
                <Settings />
              </PageErrorBoundary>
            </Suspense>
          }
        />
        <Route
          path="/agents"
          element={
            <Suspense fallback={<PageLoader />}>
              <PageErrorBoundary>
                <Agents />
              </PageErrorBoundary>
            </Suspense>
          }
        />
        <Route
          path="/automations"
          element={
            <Suspense fallback={<PageLoader />}>
              <PageErrorBoundary>
                <Automations />
              </PageErrorBoundary>
            </Suspense>
          }
        />
        <Route
          path="/rules"
          element={
            <Suspense fallback={<PageLoader />}>
              <PageErrorBoundary>
                <Rules />
              </PageErrorBoundary>
            </Suspense>
          }
        />
        <Route
          path="/memory"
          element={
            <Suspense fallback={<PageLoader />}>
              <PageErrorBoundary>
                <Memory />
              </PageErrorBoundary>
            </Suspense>
          }
        />
        <Route
          path="/repo"
          element={
            <Suspense fallback={<PageLoader />}>
              <PageErrorBoundary>
                <RepoAnalysis />
              </PageErrorBoundary>
            </Suspense>
          }
        />
        <Route
          path="*"
          element={
            <Suspense fallback={<PageLoader />}>
              <PageErrorBoundary>
                <NotFound />
              </PageErrorBoundary>
            </Suspense>
          }
        />
      </Route>
    </Routes>
  );
}

function AppWithSSE() {
  useItervoxSSE();
  // CORE-075: the global log stream is opened by NarrativeFeed, not here.
  useSnapshotInvalidation();
  useMultiTabWarning();
  // CORE-079 — ?issue= <-> the global IssueDetailSlide selection.
  useIssueUrlSync();

  const refreshSnapshot = useItervoxStore((s) => s.refreshSnapshot);
  useEffect(() => {
    void refreshSnapshot();
  }, [refreshSnapshot]);

  return (
    <>
      <AppRouteTable />
      <IssueDetailSlide />
      <CommandPaletteHost />
      <AttentionNotifier />
      <Toast />
    </>
  );
}

export default function App() {
  // #228 — /hub/ is the hub's project switcher; /hub/p/{project}/ is that
  // project's unchanged dashboard over the hub's proxied API.
  // #231 — /hub/m is the phone layout (the installed web app's start page).
  const ctx = getApiContext();
  if (ctx.mode === 'hub') {
    return (
      <Suspense fallback={<PageLoader />}>
        {ctx.view === 'mobile' ? <HubMobilePage /> : <HubPage />}
        <Toast />
      </Suspense>
    );
  }
  return (
    <Router basename={routerBasename() || undefined}>
      <AppWithSSE />
    </Router>
  );
}
