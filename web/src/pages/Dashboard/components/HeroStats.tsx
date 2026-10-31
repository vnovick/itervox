import { useNavigate } from 'react-router';
import { useItervoxStore } from '../../../store/itervoxStore';
import { useUIStore } from '../../../store/uiStore';
import { EMPTY_HISTORY } from '../../../utils/constants';
import { automationsFiredToday } from './dashboardMetrics';
import { STATUS_META, toneTextClass, type StatusKey } from '../../../lib/statusModel';
import { useStatusSummary } from '../../../hooks/useStatusSummary';
import { fmtTokens } from '../../../utils/format';
import type { Totals } from '../../../types/schemas';

// v0.2.0 audit P3-5 — the backward-compat re-export was bridge code from the
// dashboardMetrics extraction; all production callers now import from
// `./dashboardMetrics` directly. The test was the last user of the re-export
// and was migrated alongside this change.

function StatTile({
  label,
  value,
  sub,
  valueClass,
  onClick,
  testId,
  statusKey,
  statusCount,
}: {
  label: string;
  value: string;
  sub: string;
  /** A text-colour class for the value (an alerting tone). */
  valueClass?: string;
  onClick?: () => void;
  testId?: string;
  statusKey?: StatusKey;
  statusCount?: number;
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
      data-status-key={statusKey}
      data-status-count={statusCount}
      className="stat-tile"
    >
      <span className="stat-tile-label">{label}</span>
      <span className={`stat-tile-value ${valueClass ?? ''}`}>{value}</span>
      <span className="stat-tile-sub">{sub}</span>
    </div>
  );
}

export function HeroStats() {
  // CORE-076 — counts, labels and tones from the shared status model.
  const summary = useStatusSummary();
  const history = useItervoxStore((s) => s.snapshot?.history ?? EMPTY_HISTORY);
  const totals = useItervoxStore((s) => s.snapshot?.totals);
  const { running, maxAgents: max } = summary;
  const navigate = useNavigate();
  const setTimelineAutomationOnly = useUIStore((s) => s.setTimelineAutomationOnly);
  const automationsToday = automationsFiredToday(history);
  const tile = (key: StatusKey, value: number, sub: string) => (
    <StatTile
      label={STATUS_META[key].label}
      value={String(value)}
      sub={sub}
      statusKey={key}
      statusCount={value}
      valueClass={value > 0 ? toneTextClass(STATUS_META[key].tone) : undefined}
    />
  );
  return (
    <div
      // CORE-091 / CORE-175 note — seven tiles keep one row on desktop (M5);
      // with the eighth (cost) tile, two rows of four until 2xl.
      className={`grid w-full grid-cols-2 gap-2.5 sm:grid-cols-4 ${
        totals ? 'lg:grid-cols-4 xl:grid-cols-8' : 'lg:grid-cols-7'
      }`}
      data-testid="hero-stats"
    >
      {tile('running', running, 'agents')}
      {tile('paused', summary.paused, 'issues')}
      {tile('retrying', summary.retrying, 'queued')}
      {tile('input_required', summary.needsInput, 'waiting on you')}
      {tile('pending_input_resume', summary.resuming, 'reply received')}
      <StatTile
        testId="hero-stat-automations-today"
        label="Automations"
        value={String(automationsToday)}
        sub="fired today"
        onClick={() => {
          setTimelineAutomationOnly(true);
          void navigate('/timeline');
        }}
      />
      <StatTile
        label="Capacity"
        value={max > 0 ? `${String(running)}/${String(max)}` : '—'}
        sub={max > 0 ? `${String(Math.round((running / max) * 100))}% used` : 'No cap'}
        valueClass={max > 0 && running / max >= 0.9 ? 'text-theme-danger-text' : undefined}
      />
      {totals && <CostTile totals={totals} />}
    </div>
  );
}

/**
 * CORE-091 — daemon-session totals. The dollar figure is Claude's own
 * client-side estimate (total_cost_usd), so it is always labelled
 * "estimated"; Codex reports no cost, hence "Claude runs only" whenever a
 * Codex run contributed tokens, and '—' (never $0) when nothing reported one.
 */
function CostTile({ totals }: { totals: Totals }) {
  const tokens = totals.inputTokens + totals.outputTokens;
  const codexRuns = totals.costCoverage?.codexRuns ?? 0;
  const cost = totals.costUsdEstimated;
  return (
    <StatTile
      testId="hero-stat-cost"
      label="Est. cost"
      value={cost === null ? '—' : `$${cost.toFixed(2)}`}
      sub={`${fmtTokens(tokens)} tokens · ${codexRuns > 0 ? 'estimated, Claude runs only' : 'estimated'}`}
    />
  );
}
