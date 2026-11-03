import { Link, type To } from 'react-router';
import { useShallow } from 'zustand/react/shallow';
import { useItervoxStore } from '../store/itervoxStore';
import { MobileMenuButton } from '../components/ui/MobileMenuButton';
import { ItervoxLogo } from '../components/brand/ItervoxLogo';
import { fmtMs } from '../utils/format';
import { STATUS_META, type StatusKey } from '../lib/statusModel';
import { useStatusSummary } from '../hooks/useStatusSummary';
import { useConnectionState } from '../hooks/useConnectionState';
import { SchemaDriftBanner } from '../components/itervox/SchemaDriftBanner';
import { DemoModeBadge } from '../components/itervox/DemoModeBadge';
import { useDashboardHref } from '../hooks/useDashboardHref';
import { useUIStore } from '../store/uiStore';

// CORE-076 — pill colour per status-model tone (one palette for all pills).
const PILL_TONE_CLASS: Record<string, string> = {
  success: 'bg-theme-success-soft text-theme-success-text',
  warning: 'bg-theme-warning-soft text-theme-warning-text',
  danger: 'bg-theme-danger-soft text-theme-danger-text',
  info: 'bg-theme-accent-soft text-theme-accent-strong',
  neutral: 'bg-theme-bg-elevated text-theme-text-secondary',
};

// CORE-086 — every count pill links to the dashboard section behind it.
function StatusPill({
  statusKey,
  count,
  to,
  prefix = '',
  title,
}: {
  statusKey: StatusKey;
  count: number;
  to: To;
  prefix?: string;
  title?: string;
}) {
  const meta = STATUS_META[statusKey];
  return (
    <Link
      to={to}
      data-status-key={statusKey}
      data-status-count={count}
      title={title}
      className={`rounded-full px-2 py-0.5 text-xs transition-colors hover:opacity-80 ${PILL_TONE_CLASS[meta.tone] ?? ''}`}
    >
      {prefix}
      {count} {meta.noun}
    </Link>
  );
}

const AppHeader: React.FC<{ onMenuClick?: () => void }> = ({ onMenuClick }) => {
  // v0.2.0 audit P2-10 — connection-state timing comes from the shared
  // useConnectionState hook so this header and the Dashboard overlay agree
  // on "is the API offline?" instead of running two competing timers.
  const { sseConnected, hasSnapshot, timedOut, isStale, dataAgeMs } = useConnectionState();
  // CORE-076 — every count, label and tone comes from the shared status
  // model (lib/statusModel), the same derivation LiveOpsStrip, HeroStats and
  // the operator queue use.
  const { projectName, configInvalid, demoMode } = useItervoxStore(
    useShallow((s) => ({
      projectName: s.snapshot?.projectName ?? '',
      configInvalid: s.snapshot?.configInvalid ?? null,
      demoMode: s.snapshot?.demoMode ?? false,
    })),
  );
  const {
    running,
    paused,
    retrying,
    needsInput: awaitingInput,
    resuming: pendingInputResumes,
    maxAgents,
    capacityPct: pct,
    headline,
  } = useStatusSummary();
  const dashboardHref = useDashboardHref();

  const liveLabel = sseConnected
    ? 'Live'
    : hasSnapshot
      ? 'Reconnecting\u2026'
      : timedOut
        ? 'Disconnected'
        : 'Connecting\u2026';

  return (
    <>
      {/* Config-invalid banner — surfaces an in-flight WORKFLOW.md reload
          failure so the operator knows their last edit didn't take and the
          daemon is running on the previously-valid config (T-26). */}
      {configInvalid && (
        <div
          role="alert"
          className="bg-theme-warning-soft text-theme-warning-text border-theme-warning sticky top-0 z-40 border-b px-4 py-2 text-sm"
          data-testid="config-invalid-banner"
        >
          <strong className="font-semibold">WORKFLOW.md is invalid:</strong>{' '}
          <span className="font-mono text-xs">{configInvalid.error}</span>
          <span className="text-theme-text-secondary ml-2 text-xs">
            (retry attempt {configInvalid.retryAttempt}
            {configInvalid.retryAt ? ` at ${configInvalid.retryAt}` : ''}; daemon running on the
            last valid config)
          </span>
        </div>
      )}
      {/* Schema-drift banner — CORE-047. */}
      <SchemaDriftBanner />
      {/* Stale-data banner — CORE-023. A snapshot can go stale in place (the
          daemon stopped responding but the last-fetched state stays on
          screen with only a small "Reconnecting…" label to notice). This is
          the prominent, sticky version: it only shows once BOTH the SSE
          channel (keepalives included) and the polling fallback have gone
          quiet past the threshold — see useConnectionState. */}
      {isStale && (
        <div
          role="alert"
          className="bg-theme-warning-soft text-theme-warning-text border-theme-warning sticky top-0 z-40 border-b px-4 py-2 text-sm"
          data-testid="stale-data-banner"
        >
          <strong className="font-semibold">Data may be out of date:</strong>{' '}
          <span>
            last update {dataAgeMs !== null ? `${fmtMs(dataAgeMs)} ago` : 'unknown'} — the dashboard
            cannot currently reach the itervox daemon.
          </span>
        </div>
      )}
      <header className="bg-theme-bg-soft border-theme-line sticky top-0 z-30 flex min-w-0 flex-wrap items-center gap-2 border-b px-4 py-3 text-sm sm:gap-3">
        {/* Mobile menu button */}
        {onMenuClick && <MobileMenuButton onClick={onMenuClick} />}

        {/* Brand — sits adjacent to the live pulse so the connection status reads as
          itervox status, not unattributed UI chrome. Replaces the previous bordered
          logo box at the top of the sidebar. */}
        <ItervoxLogo className="h-5 w-auto" aria-label="Itervox" />

        {/* Live pulse */}
        <span className="flex shrink-0 items-center gap-2">
          <span className="relative flex h-2.5 w-2.5">
            {running > 0 && (
              <span className="bg-theme-success absolute inline-flex h-full w-full animate-ping rounded-full opacity-75" />
            )}
            <span
              className={`relative inline-flex h-2.5 w-2.5 rounded-full ${sseConnected ? 'bg-theme-success' : 'bg-theme-danger'}`}
            />
          </span>
          <span className="text-theme-text-secondary">{liveLabel}</span>
        </span>

        {/* Project name — disambiguates multiple daemons running for different repos. */}
        {projectName && (
          <span
            className="text-theme-text max-w-[120px] min-w-0 truncate font-mono text-xs font-semibold sm:max-w-[200px]"
            title={`Project: ${projectName}`}
          >
            {projectName}
          </span>
        )}

        {demoMode && <DemoModeBadge />}

        {/* Orchestrator state */}
        <span
          data-testid="header-orchestrator-state"
          className="bg-theme-bg-elevated text-theme-text-secondary shrink-0 rounded px-2 py-0.5 font-mono text-xs"
        >
          {STATUS_META[headline].label}
        </span>

        {/* Running count */}
        {running > 0 && (
          <Link
            to={dashboardHref('running-sessions')}
            data-status-key="running"
            data-status-count={running}
            title="Jump to the running sessions"
            className="text-theme-success-text flex items-center gap-1.5 rounded underline-offset-2 hover:underline"
          >
            <strong>{running}</strong>
            <span className="text-theme-text-secondary">{STATUS_META.running.noun}</span>
          </Link>
        )}

        {paused > 0 && (
          <StatusPill
            statusKey="paused"
            count={paused}
            to={dashboardHref('running-sessions')}
            title="Jump to the paused sessions"
          />
        )}

        {awaitingInput > 0 && (
          <StatusPill
            statusKey="input_required"
            count={awaitingInput}
            to={dashboardHref('attention-inbox')}
            title="Jump to the attention inbox"
          />
        )}

        {pendingInputResumes > 0 && (
          <StatusPill
            statusKey="pending_input_resume"
            count={pendingInputResumes}
            to={dashboardHref('pending-resume')}
            title="Jump to the Resuming panel"
          />
        )}

        {retrying > 0 && (
          <StatusPill
            statusKey="retrying"
            count={retrying}
            prefix="↻ "
            to={dashboardHref('retry-queue')}
            title="Jump to the retry queue"
          />
        )}

        {/* Capacity bar — hidden on mobile */}
        {maxAgents > 0 && (
          <span className="ml-2 hidden items-center gap-2 md:flex">
            <span className="text-theme-muted text-xs">running/max</span>
            <span className="bg-theme-bg-elevated h-1.5 w-20 overflow-hidden rounded-full">
              <span
                className="block h-full rounded-full transition-all"
                style={{
                  width: `${String(pct)}%`,
                  background:
                    pct >= 90 ? 'var(--danger)' : pct >= 60 ? 'var(--warning)' : 'var(--success)',
                }}
              />
            </span>
            <span className="text-theme-text-secondary font-mono text-xs">
              {running}/{maxAgents}
            </span>
          </span>
        )}

        {/* CORE-095 — the command palette's visible entry point (also Mod+K). */}
        {/* A plain button (the Button primitive's ghost/xs look): the header is
            in the main entry and the primitives pull in tailwind-merge (M6-close). */}
        <button
          type="button"
          className="text-theme-text-secondary hover:bg-theme-panel-strong hover:text-theme-text focus-visible:ring-theme-accent ml-auto inline-flex h-6 items-center gap-1.5 rounded-[var(--radius-sm)] px-2 text-[11px] font-medium transition-colors focus-visible:ring-2 focus-visible:outline-none"
          aria-keyshortcuts="Control+K Meta+K"
          onClick={() => {
            useUIStore.getState().setCommandPaletteOpen(true);
          }}
        >
          Commands <kbd className="text-theme-muted font-mono">Ctrl/⌘ K</kbd>
        </button>
      </header>
    </>
  );
};

export default AppHeader;
