import { Link } from 'react-router';
import { useShallow } from 'zustand/react/shallow';
import { useItervoxStore } from '../store/itervoxStore';
import { MobileMenuButton } from '../components/ui/MobileMenuButton';
import { ItervoxLogo } from '../components/brand/ItervoxLogo';
import { formatOrchestratorState } from '../utils/format';
import { inputRequiredRowState } from '../utils/inputRequired';
import { useConnectionState } from '../hooks/useConnectionState';

const AppHeader: React.FC<{ onMenuClick?: () => void }> = ({ onMenuClick }) => {
  // v0.2.0 audit P2-10 — connection-state timing comes from the shared
  // useConnectionState hook so this header and the Dashboard overlay agree
  // on "is the API offline?" instead of running two competing timers.
  const { sseConnected, hasSnapshot, timedOut } = useConnectionState();
  const {
    running,
    paused,
    retrying,
    awaitingInput,
    pendingInputResumes,
    maxAgents,
    projectName,
    configInvalid,
  } = useItervoxStore(
    useShallow((s) => ({
      running: s.snapshot?.running.length ?? 0,
      paused: s.snapshot?.paused.length ?? 0,
      retrying: s.snapshot?.retrying.length ?? 0,
      pendingInputResumes: (s.snapshot?.inputRequired ?? []).filter(
        (entry) => inputRequiredRowState(entry) === 'pending_input_resume',
      ).length,
      awaitingInput: (s.snapshot?.inputRequired ?? []).filter(
        (entry) => inputRequiredRowState(entry) === 'input_required',
      ).length,
      maxAgents: s.snapshot?.maxConcurrentAgents ?? 0,
      projectName: s.snapshot?.projectName ?? '',
      configInvalid: s.snapshot?.configInvalid ?? null,
    })),
  );
  const orchestratorState =
    running > 0
      ? 'running'
      : pendingInputResumes > 0
        ? 'pending_input_resume'
        : awaitingInput > 0
          ? 'input_required'
          : retrying > 0
            ? 'retrying'
            : paused > 0
              ? 'paused'
              : 'idle';
  const pct = maxAgents > 0 ? Math.round((running / maxAgents) * 100) : 0;

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
          className="bg-theme-warning-soft text-theme-warning border-theme-warning sticky top-0 z-40 border-b px-4 py-2 text-sm"
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

        {/* Orchestrator state */}
        <span className="bg-theme-bg-elevated text-theme-text-secondary shrink-0 rounded px-2 py-0.5 font-mono text-xs">
          {formatOrchestratorState(orchestratorState)}
        </span>

        {/* Running count */}
        {running > 0 && (
          <span className="text-theme-success flex items-center gap-1.5">
            <strong>{running}</strong>
            <span className="text-theme-text-secondary">running</span>
          </span>
        )}

        {paused > 0 && (
          <span className="bg-theme-danger-soft text-theme-danger rounded-full px-2 py-0.5 text-xs">
            {paused} paused
          </span>
        )}

        {awaitingInput > 0 && (
          <span className="rounded-full bg-orange-500/15 px-2 py-0.5 text-xs text-orange-400">
            {awaitingInput} need input
          </span>
        )}

        {pendingInputResumes > 0 && (
          <Link
            to="/#pending-resume"
            className="rounded-full bg-orange-500/15 px-2 py-0.5 text-xs text-orange-400 transition-colors hover:bg-orange-500/25"
            title="Jump to the Resuming panel"
          >
            {pendingInputResumes} resuming
          </Link>
        )}

        {retrying > 0 && (
          <span className="bg-theme-warning-soft text-theme-warning rounded-full px-2 py-0.5 text-xs">
            ↻ {retrying} retrying
          </span>
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
      </header>
    </>
  );
};

export default AppHeader;
