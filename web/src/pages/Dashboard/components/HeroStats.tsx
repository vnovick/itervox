import { useShallow } from 'zustand/react/shallow';
import { useNavigate } from 'react-router';
import { useItervoxStore } from '../../../store/itervoxStore';
import { useUIStore } from '../../../store/uiStore';
import { EMPTY_HISTORY } from '../../../utils/constants';
import { automationsFiredToday } from './dashboardMetrics';

// v0.2.0 audit P3-5 — the backward-compat re-export was bridge code from the
// dashboardMetrics extraction; all production callers now import from
// `./dashboardMetrics` directly. The test was the last user of the re-export
// and was migrated alongside this change.

function StatTile({
  label,
  value,
  sub,
  valueColor,
  onClick,
  testId,
}: {
  label: string;
  value: string;
  sub: string;
  valueColor?: string;
  onClick?: () => void;
  testId?: string;
}) {
  const isInteractive = !!onClick;
  return (
    <div
      role={isInteractive ? 'button' : undefined}
      tabIndex={isInteractive ? 0 : undefined}
      onClick={onClick}
      onKeyDown={
        isInteractive
          ? (e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                onClick();
              }
            }
          : undefined
      }
      data-testid={testId}
      className={`flex flex-col items-center rounded-[var(--radius-md)] bg-white/[0.04] px-4 py-3 ${
        isInteractive ? 'cursor-pointer hover:bg-white/[0.08]' : ''
      }`}
    >
      <span className="text-theme-muted text-[10px] font-semibold tracking-[0.06em] uppercase">
        {label}
      </span>
      <span
        className="mt-1 text-lg font-bold tabular-nums"
        style={{ color: valueColor ?? 'var(--text)' }}
      >
        {value}
      </span>
      <span className="text-theme-text-secondary text-[10px]">{sub}</span>
    </div>
  );
}

export function HeroStats() {
  const { running, paused, retrying, inputRequired, max, history } = useItervoxStore(
    useShallow((s) => ({
      running: s.snapshot?.running.length ?? 0,
      paused: s.snapshot?.paused.length ?? 0,
      retrying: s.snapshot?.retrying.length ?? 0,
      inputRequired: (s.snapshot?.inputRequired ?? []).length,
      max: s.snapshot?.maxConcurrentAgents ?? 0,
      history: s.snapshot?.history ?? EMPTY_HISTORY,
    })),
  );
  const navigate = useNavigate();
  const setTimelineAutomationOnly = useUIStore((s) => s.setTimelineAutomationOnly);
  const automationsToday = automationsFiredToday(history);
  return (
    <div
      className="grid w-full flex-shrink-0 grid-cols-2 gap-2 sm:grid-cols-3 lg:w-auto lg:grid-cols-6"
      data-testid="hero-stats"
    >
      <StatTile
        label="Running"
        value={String(running)}
        sub="agents"
        valueColor={running > 0 ? 'var(--success)' : undefined}
      />
      <StatTile
        label="Paused"
        value={String(paused)}
        sub="issues"
        valueColor={paused > 0 ? 'var(--warning)' : undefined}
      />
      <StatTile
        label="Retrying"
        value={String(retrying)}
        sub="queued"
        valueColor={retrying > 0 ? 'var(--danger)' : undefined}
      />
      <StatTile
        label="Input Required"
        value={String(inputRequired)}
        sub="blocked"
        valueColor={inputRequired > 0 ? 'var(--warning)' : undefined}
      />
      <StatTile
        testId="hero-stat-automations-today"
        label="Automations"
        value={String(automationsToday)}
        sub="fired today"
        valueColor={automationsToday > 0 ? 'var(--success)' : undefined}
        onClick={() => {
          setTimelineAutomationOnly(true);
          void navigate('/timeline');
        }}
      />
      <StatTile
        label="Capacity"
        value={max > 0 ? `${String(running)}/${String(max)}` : '—'}
        sub={max > 0 ? `${String(Math.round((running / max) * 100))}% used` : 'No cap'}
        valueColor={
          max > 0 && running / max >= 0.9
            ? 'var(--danger)'
            : max > 0 && running > 0
              ? 'var(--success)'
              : undefined
        }
      />
    </div>
  );
}
