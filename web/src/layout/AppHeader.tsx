import { useShallow } from 'zustand/react/shallow';
import { useItervoxStore } from '../store/itervoxStore';
import { MobileMenuButton } from '../components/ui/MobileMenuButton';
import { fmtMs } from '../utils/format';
import { useStatusSummary } from '../hooks/useStatusSummary';
import { useConnectionState } from '../hooks/useConnectionState';
import { SchemaDriftBanner } from '../components/itervox/SchemaDriftBanner';
import { DemoModeBadge } from '../components/itervox/DemoModeBadge';
import { HubProjectSwitcher } from '../components/itervox/HubProjectSwitcher';
import { useUIStore } from '../store/uiStore';
import { NewTaskButton } from '../components/itervox/newTask/NewTaskButton';
import { useEmbedded } from '../hooks/useEmbeddedShell';
import { EmbeddedTitle } from './EmbeddedTitle';
import { StatusPills } from './StatusPills';

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
  const { running, maxAgents, capacityPct: pct } = useStatusSummary();
  // Embedded in the desktop app: `project · ISSUE` in place of the project
  // name (the window's project tabs name the project too).
  const embedded = useEmbedded();

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
      {/* One row from md up: the status pills fold into "+N" rather than
          wrap (phones may still take two rows). */}
      <header className="glass-panel glass-bar flex min-w-0 flex-wrap items-center gap-2 px-3 py-2.5 text-sm sm:gap-3 md:flex-nowrap md:px-4">
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
          <span className={embedded ? 'sr-only' : undefined}>{liveLabel}</span>
        </span>

        {embedded && <EmbeddedTitle />}

        {/* Project name — disambiguates multiple daemons running for different repos. */}
        {projectName && !embedded && (
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

        {/* Orchestrator state and counts, one row ("+N" for the rest). */}
        <StatusPills />

        <span aria-hidden="true" className="flex-1" />

        {/* Capacity bar — hidden on mobile */}
        {maxAgents > 0 && (
          <span className="hidden shrink-0 items-center gap-2.5 xl:flex">
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
          className="glass-chip text-theme-text hover:bg-theme-panel-strong inline-flex h-9 shrink-0 items-center gap-1.5 px-3 text-[13px] font-medium transition-colors sm:px-3.5"
          aria-keyshortcuts="Control+K Meta+K"
          onClick={() => {
            useUIStore.getState().setCommandPaletteOpen(true);
          }}
        >
          {/* Phones: a search glyph keeps the top bar to two rows; the
              label stays the accessible name. */}
          <svg
            aria-hidden="true"
            viewBox="0 0 24 24"
            className="h-4 w-4 sm:hidden"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
          >
            <circle cx="11" cy="11" r="7" />
            <path d="m20 20-3.5-3.5" />
          </svg>
          <span className="sr-only sm:not-sr-only">Commands</span>
          <kbd className="text-theme-muted hidden font-sans sm:inline">Ctrl/⌘ K</kbd>
        </button>

        {/* #251 — New task, from every page (operator scope). */}
        <NewTaskButton />
      </header>
    </div>
  );
};

export default AppHeader;
