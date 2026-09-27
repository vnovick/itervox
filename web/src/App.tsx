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
  SettingsIcon,
  TimelineIcon,
} from './components/layout/NavIcons';
import { ThemeToggle } from './components/ui/ThemeToggle/ThemeToggle';
import AppHeader from './layout/AppHeader';
import { useDialogLayer } from './components/ui/dialog/useDialogLayer';
import { useMultiTabWarning } from './hooks/useMultiTabWarning';
import { useAttentionCount } from './hooks/useOperatorQueue';
import { useIssueUrlSync } from './hooks/useUrlState';

import { PageLoader } from './components/common/PageLoader';
import { CommandPaletteHost } from './components/itervox/CommandPaletteHost';
import { AttentionNotifier } from './components/itervox/AttentionNotifier';
const Dashboard = lazy(() => import('./pages/Dashboard'));
const Logs = lazy(() => import('./pages/Logs'));
const Timeline = lazy(() => import('./pages/Timeline'));
const Agents = lazy(() => import('./pages/Agents'));
const Automations = lazy(() => import('./pages/Automations'));
const Settings = lazy(() => import('./pages/Settings'));
const NotFound = lazy(() => import('./pages/OtherPage/NotFound'));

/** Tailwind's `md` breakpoint (the drawer is `md:hidden`). */
const MD_BREAKPOINT_QUERY = '(min-width: 768px)';

const NAV_ITEMS = [
  { to: '/', icon: <DashboardIcon />, label: 'Dashboard' },
  { to: '/timeline', icon: <TimelineIcon />, label: 'Timeline' },
  { to: '/logs', icon: <LogsIcon />, label: 'Logs' },
  { to: '/agents', icon: <AgentsIcon />, label: 'Agents' },
  { to: '/automations', icon: <AutomationsIcon />, label: 'Automations' },
  { to: '/settings', icon: <SettingsIcon />, label: 'Settings' },
] as const;

function SidebarContent() {
  // CORE-078 — same count as the attention inbox and the page title.
  const attention = useAttentionCount();
  return (
    <>
      {/* Nav links — brand moved to AppHeader so the sidebar is icon-only and
          aligns with the dashboard content row in the main column. */}
      <nav className="flex flex-1 flex-col gap-1">
        {NAV_ITEMS.map((item) => (
          <NavLink
            key={item.to}
            to={item.to}
            icon={item.icon}
            label={item.label}
            badge={item.to === '/' ? attention : 0}
          />
        ))}
      </nav>

      {/* Theme toggle pinned to bottom */}
      <ThemeToggle />
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
    <div className="flex min-h-screen">
      {/* Desktop sidebar — hidden on mobile */}
      <aside className="bg-theme-bg-soft border-theme-line fixed top-0 bottom-0 left-0 z-40 hidden w-16 flex-col items-center gap-2 border-r py-4 md:flex">
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
          className="absolute inset-0"
          style={{ background: 'rgba(0,0,0,0.5)' }}
          aria-label="Close navigation"
          role="button"
          tabIndex={0}
          onClick={closeMobileNav}
          onKeyDown={(e) => {
            if (e.key === 'Enter' || e.key === ' ') closeMobileNav();
          }}
        />
        <aside
          className={`bg-theme-bg-soft border-theme-line absolute top-0 bottom-0 left-0 flex w-16 flex-col items-center gap-2 border-r py-4 transition-transform duration-200 ${
            mobileNavOpen ? 'translate-x-0' : '-translate-x-full'
          }`}
        >
          <SidebarContent />
        </aside>
      </div>

      <main className="flex min-w-0 flex-1 flex-col md:ml-16">
        <AppHeader
          onMenuClick={() => {
            setMobileNavOpen(true);
          }}
        />
        <div className="flex-1 p-3 md:p-6">
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
  return (
    <Router>
      <AppWithSSE />
    </Router>
  );
}
