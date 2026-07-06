import { BrowserRouter as Router, Routes, Route, Outlet } from 'react-router';
import { lazy, Suspense, useCallback, useEffect, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useItervoxSSE } from './hooks/useItervoxSSE';
import { useLogStream } from './hooks/useLogStream';
import { useItervoxStore } from './store/itervoxStore';
import { ISSUES_KEY } from './queries/issues';
import { logIdentifiersKey } from './queries/logs';
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
import { useFocusTrap } from './hooks/useFocusTrap';
import { useMultiTabWarning } from './hooks/useMultiTabWarning';
import { inputRequiredFingerprintValue } from './utils/inputRequired';

const Dashboard = lazy(() => import('./pages/Dashboard'));
const Logs = lazy(() => import('./pages/Logs'));
const Timeline = lazy(() => import('./pages/Timeline'));
const Agents = lazy(() => import('./pages/Agents'));
const Automations = lazy(() => import('./pages/Automations'));
const Settings = lazy(() => import('./pages/Settings'));
const NotFound = lazy(() => import('./pages/OtherPage/NotFound'));

function PageLoader() {
  return (
    <div className="flex h-64 items-center justify-center">
      <div className="h-6 w-6 animate-spin rounded-full border-2 border-current border-t-transparent" />
    </div>
  );
}

const NAV_ITEMS = [
  { to: '/', icon: <DashboardIcon />, label: 'Dashboard' },
  { to: '/timeline', icon: <TimelineIcon />, label: 'Timeline' },
  { to: '/logs', icon: <LogsIcon />, label: 'Logs' },
  { to: '/agents', icon: <AgentsIcon />, label: 'Agents' },
  { to: '/automations', icon: <AutomationsIcon />, label: 'Automations' },
  { to: '/settings', icon: <SettingsIcon />, label: 'Settings' },
] as const;

function SidebarContent() {
  return (
    <>
      {/* Nav links — brand moved to AppHeader so the sidebar is icon-only and
          aligns with the dashboard content row in the main column. */}
      <nav className="flex flex-1 flex-col gap-1">
        {NAV_ITEMS.map((item) => (
          <NavLink key={item.to} to={item.to} icon={item.icon} label={item.label} />
        ))}
      </nav>

      {/* Theme toggle pinned to bottom */}
      <ThemeToggle />
    </>
  );
}

function AppShell() {
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const drawerRef = useRef<HTMLDivElement>(null);

  useFocusTrap(drawerRef, mobileNavOpen);

  const closeMobileNav = useCallback(() => {
    setMobileNavOpen(false);
  }, []);

  useEffect(() => {
    if (!mobileNavOpen) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        closeMobileNav();
      }
    };
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [mobileNavOpen, closeMobileNav]);

  return (
    <div className="flex min-h-screen">
      {/* Desktop sidebar — hidden on mobile */}
      <aside className="bg-theme-bg-soft border-theme-line fixed top-0 bottom-0 left-0 z-40 hidden w-16 flex-col items-center gap-2 border-r py-4 md:flex">
        <SidebarContent />
      </aside>

      {/* Mobile nav drawer — slides from left */}
      <div
        ref={drawerRef}
        role="dialog"
        aria-modal="true"
        aria-label="Navigation"
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

export function buildSnapshotInvalidationFingerprint(
  snapshot: ReturnType<typeof useItervoxStore.getState>['snapshot'],
): string | null {
  if (!snapshot) return null;
  const sortStrings = (values: readonly string[]) => [...values].sort((a, b) => a.localeCompare(b));
  return JSON.stringify({
    running: sortStrings(snapshot.running.map((row) => row.identifier)),
    retrying: sortStrings(snapshot.retrying.map((row) => row.identifier)),
    paused: sortStrings(snapshot.paused),
    pausedWithPR: snapshot.pausedWithPR ?? {},
    inputRequired: sortStrings(
      (snapshot.inputRequired ?? []).map((entry) => inputRequiredFingerprintValue(entry)),
    ),
  });
}

/**
 * Invalidates the issues cache whenever the orchestrator's activity fingerprint
 * changes (sessions start, stop, pause, enter input-required, or pick up PR metadata).
 * This bridges the real-time SSE snapshot to the issues list so the kanban
 * board and issue detail refresh immediately instead of waiting for a stale query.
 */
function useSnapshotInvalidation() {
  const queryClient = useQueryClient();
  const fingerprint = useItervoxStore((s) => buildSnapshotInvalidationFingerprint(s.snapshot));
  const prevRef = useRef<string | null>(null);

  useEffect(() => {
    if (fingerprint === null) return; // no snapshot yet
    if (prevRef.current !== null && prevRef.current !== fingerprint) {
      void queryClient.invalidateQueries({ queryKey: ISSUES_KEY });
      void queryClient.invalidateQueries({ queryKey: ['issue'] });
      void queryClient.invalidateQueries({ queryKey: logIdentifiersKey() });
    }
    prevRef.current = fingerprint;
  }, [fingerprint, queryClient]);
}

function AppWithSSE() {
  useItervoxSSE();
  useLogStream();
  useSnapshotInvalidation();
  useMultiTabWarning();

  const refreshSnapshot = useItervoxStore((s) => s.refreshSnapshot);
  useEffect(() => {
    void refreshSnapshot();
  }, [refreshSnapshot]);

  return (
    <>
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
            path="/logs"
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
        </Route>
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
      </Routes>
      <IssueDetailSlide />
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
