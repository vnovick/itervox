import { Link, type To } from 'react-router';
import { useShallow } from 'zustand/react/shallow';
import { useItervoxStore } from '../store/itervoxStore';
import { MobileMenuButton } from '../components/ui/MobileMenuButton';
import { fmtMs } from '../utils/format';
import { STATUS_META, type StatusKey } from '../lib/statusModel';
import { useStatusSummary } from '../hooks/useStatusSummary';
import { useConnectionState } from '../hooks/useConnectionState';
import { SchemaDriftBanner } from '../components/itervox/SchemaDriftBanner';
import { DemoModeBadge } from '../components/itervox/DemoModeBadge';
import { HubProjectSwitcher } from '../components/itervox/HubProjectSwitcher';
import { useDashboardHref } from '../hooks/useDashboardHref';
import { useUIStore } from '../store/uiStore';

// CORE-076 — pill colour per status-model tone (one palette for all pills).
// The `.pill-*` classes live in index.css (tokens.contrast.test.ts measures
// them on the top bar's glass).
const PILL_TONE_CLASS: Record<string, string> = {
  success: 'pill-ok',
  warning: 'pill-warn',
  danger: 'pill-danger',
  info: 'pill-accent',
  neutral: 'pill-neutral',
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
      className={`pill hover:opacity-80 ${PILL_TONE_CLASS[meta.tone] ?? ''}`}
    >
      {prefix}
      {count} {meta.noun}
    </Link>
  );
}

// #245 — the config-invalid banner names the file whose edit did not load:
// WORKFLOW.md or one of the settings files (".itervox/settings.jsonc").
function configFileLabel(path: string | undefined): string {
  if (!path) return 'WORKFLOW.md';
  const parts = path.split(/[\\/]/).filter(Boolean);
  const base = parts[parts.length - 1] ?? path;
  const parent = parts[parts.length - 2];
  return parent === '.itervox' ? `.itervox/${base}` : base;
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
    // The banners and the top bar stick together above the page content.
    <div className="sticky top-0 z-30 flex flex-col gap-2 pt-3 md:pt-4">
      {/* Config-invalid banner — surfaces an edit of WORKFLOW.md or a
          settings file that did not load, so the operator knows it didn't
          take and the daemon is running on the previously-valid config
          (T-26, #245). retryAttempt is 0 when the running generation kept
          its config and waits for the next edit. */}
      {configInvalid && (
        <div role="alert" className="banner-warn" data-testid="config-invalid-banner">
          <strong className="font-semibold">
            {configFileLabel(configInvalid.path)} is invalid:
          </strong>{' '}
          <span className="font-mono text-xs">{configInvalid.error}</span>
          <span className="text-theme-text-secondary ml-2 text-xs">
            {configInvalid.retryAttempt > 0
              ? `(retry attempt ${String(configInvalid.retryAttempt)}${
                  configInvalid.retryAt ? ` at ${configInvalid.retryAt}` : ''
                }; daemon running on the last valid config)`
              : '(daemon running on the last valid config; fix the file to apply it)'}
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
        <div role="alert" className="banner-warn" data-testid="stale-data-banner">
          <strong className="font-semibold">Data may be out of date:</strong>{' '}
          <span>
            last update {dataAgeMs !== null ? `${fmtMs(dataAgeMs)} ago` : 'unknown'} — the dashboard
            cannot currently reach the itervox daemon.
          </span>
        </div>
      )}
      <header className="glass-panel flex min-w-0 flex-wrap items-center gap-2 px-3 py-2.5 text-sm sm:gap-3 md:px-4">
        {/* Mobile menu button */}
        {onMenuClick && <MobileMenuButton onClick={onMenuClick} />}

        {/* Live pulse — the connection status of this itervox daemon. */}
        <span className="text-theme-text flex shrink-0 items-center gap-1.5 font-semibold">
          <span className="relative flex h-2 w-2">
            {running > 0 && (
              <span className="bg-theme-success absolute inline-flex h-full w-full animate-ping rounded-full opacity-75" />
            )}
            <span className={`status-dot relative ${sseConnected ? 'dot-ok' : 'dot-danger'}`} />
          </span>
          <span>{liveLabel}</span>
        </span>

        {/* Project name — disambiguates multiple daemons running for different repos. */}
        {projectName && (
          <span
            className="text-theme-text max-w-[120px] min-w-0 truncate font-mono text-[13px] font-medium sm:max-w-[200px]"
            title={`Project: ${projectName}`}
          >
            {projectName}
          </span>
        )}

        {/* #228 — through the hub: switch project. */}
        <HubProjectSwitcher />

        {demoMode && <DemoModeBadge />}

        {/* Orchestrator state */}
        <span
          data-testid="header-orchestrator-state"
          className={`pill shrink-0 font-semibold ${PILL_TONE_CLASS[STATUS_META[headline].tone] ?? 'pill-neutral'}`}
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
            className="text-theme-text flex items-center gap-1 rounded underline-offset-2 hover:underline"
          >
            <strong>{running}</strong>
            <span>{STATUS_META.running.noun}</span>
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

        <span aria-hidden="true" className="flex-1" />

        {/* Capacity bar — hidden on mobile */}
        {maxAgents > 0 && (
          <span className="hidden items-center gap-2.5 md:flex">
            <span className="sr-only">running/max</span>
            <span className="subtle-fill h-1.5 w-20 overflow-hidden rounded-full">
              <span
                className="block h-full rounded-full transition-all"
                style={{
                  width: `${String(pct)}%`,
                  background:
                    pct >= 90 ? 'var(--danger)' : pct >= 75 ? 'var(--warning)' : 'var(--accent)',
                }}
              />
            </span>
            <span className="text-theme-text font-semibold tabular-nums">
              {running}/{maxAgents}
            </span>
          </span>
        )}

        {/* CORE-095 — the command palette's visible entry point (also Mod+K). */}
        {/* A plain button with the glass-chip class: the header is in the main
            entry and the primitives pull in tailwind-merge (M6-close). */}
        <button
          type="button"
          className="glass-chip text-theme-text hover:bg-theme-panel-strong inline-flex h-9 items-center gap-1.5 px-3.5 text-[13px] font-medium transition-colors"
          aria-keyshortcuts="Control+K Meta+K"
          onClick={() => {
            useUIStore.getState().setCommandPaletteOpen(true);
          }}
        >
          Commands <kbd className="text-theme-muted font-sans">Ctrl/⌘ K</kbd>
        </button>
      </header>
    </div>
  );
};

export default AppHeader;
