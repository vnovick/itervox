import { Link } from 'react-router';
import { useMemo } from 'react';
import { Card } from '../../components/ui/Card/Card';
import { Sparkline } from '../../components/itervox/Sparkline';
import type { AutomationDef, HistoryRow, RunningRow } from '../../types/schemas';

const MAX_RUN_ROWS = 10;
const SPARKLINE_DAYS = 7;
const MS_PER_DAY = 24 * 60 * 60 * 1000;

interface AutomationActivityCardProps {
  automation: AutomationDef;
  running: RunningRow[];
  history: HistoryRow[];
}

interface RunRowEntry {
  identifier: string;
  status: string;
  elapsedMs: number;
  timestamp: string;
  isLive: boolean;
  sessionId?: string;
}

interface AggregateStats {
  totalRuns: number;
  successRate: number; // 0..1
  meanElapsedMs: number;
  lastFireMs: number;
  succeededRuns: number;
}

/**
 * Renders one card per configured automation. The card surfaces:
 *   • title + trigger chip + enabled badge
 *   • 7-day daily-bucket sparkline of fire counts
 *   • the last 10 runs (newest first), with status + elapsed + a Logs link
 *   • aggregate stats over the last 30 days: success rate, mean elapsed, last fire
 *
 * All inputs are derived from the SSE snapshot — no extra fetches needed.
 */
export function AutomationActivityCard({
  automation,
  running,
  history,
}: AutomationActivityCardProps) {
  const data = useMemo(
    () => buildCardData(automation.id, running, history),
    [automation.id, running, history],
  );

  return (
    <div data-testid={`automation-activity-${automation.id}`}>
      <Card variant="elevated" className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <h2 className="text-theme-text text-base font-semibold tracking-tight">
            {automation.id}
          </h2>
          <TriggerChip type={automation.trigger.type} />
          <EnabledChip enabled={automation.enabled} />
          <span className="text-theme-muted ml-auto text-xs">profile: {automation.profile}</span>
        </div>

        <Sparkline
          values={data.sparkValues}
          height={32}
          color="var(--accent-color, #4f46e5)"
          ariaLabel={`Last ${String(SPARKLINE_DAYS)} days of fires for ${automation.id}`}
        />

        <div className="grid grid-cols-3 gap-2 text-xs">
          <Stat label="Success rate" value={formatSuccessRate(data.stats)} />
          <Stat label="Avg elapsed" value={formatElapsed(data.stats.meanElapsedMs)} />
          <Stat label="Last fire" value={formatLastFire(data.stats.lastFireMs)} />
        </div>

        {data.runs.length === 0 ? (
          <p className="text-theme-muted text-xs italic">Never fired yet.</p>
        ) : (
          <ul className="space-y-1" data-testid={`automation-runs-${automation.id}`}>
            {data.runs.map((run) => (
              <li
                key={
                  // codex-B6: include the live/done discriminator EVEN when
                  // sessionId is non-null, because live and history sources
                  // can carry the same sessionId for the same run during the
                  // brief window between live exit and history index update.
                  `${run.sessionId ?? `${run.identifier}-${run.timestamp}`}-${run.isLive ? 'live' : 'done'}`
                }
                className="flex items-center gap-2 text-xs"
              >
                <span className="text-theme-text font-mono">{run.identifier}</span>
                <StatusDot status={run.status} live={run.isLive} />
                <span className="text-theme-muted">{run.status}</span>
                <span className="text-theme-muted">{formatElapsed(run.elapsedMs)}</span>
                <span className="text-theme-muted ml-auto">{formatTimestamp(run.timestamp)}</span>
                <Link
                  to={`/logs?identifier=${encodeURIComponent(run.identifier)}`}
                  className="text-theme-accent text-xs hover:underline"
                  data-testid="automation-run-logs-link"
                >
                  View
                </Link>
              </li>
            ))}
          </ul>
        )}
      </Card>
    </div>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="bg-theme-bg-soft border-theme-line rounded-md border px-2 py-1.5">
      <p className="text-theme-muted text-[10px] tracking-wide uppercase">{label}</p>
      <p className="text-theme-text text-sm font-semibold">{value}</p>
    </div>
  );
}

function TriggerChip({ type }: { type: string }) {
  return (
    <span className="bg-theme-bg-soft text-theme-muted border-theme-line rounded-full border px-2 py-0.5 text-[10px] font-semibold uppercase">
      {type}
    </span>
  );
}

function EnabledChip({ enabled }: { enabled: boolean }) {
  return (
    <span
      className={`rounded-full px-2 py-0.5 text-[10px] font-semibold uppercase ${
        enabled ? 'bg-emerald-500/15 text-emerald-300' : 'bg-amber-500/15 text-amber-300'
      }`}
    >
      {enabled ? 'enabled' : 'disabled'}
    </span>
  );
}

function StatusDot({ status, live }: { status: string; live: boolean }) {
  const color = live
    ? 'bg-sky-400 animate-pulse'
    : status === 'succeeded'
      ? 'bg-emerald-400'
      : status === 'failed'
        ? 'bg-rose-500'
        : status === 'cancelled'
          ? 'bg-zinc-400'
          : 'bg-amber-400';
  return <span aria-hidden className={`inline-block h-1.5 w-1.5 rounded-full ${color}`} />;
}

function buildCardData(
  automationId: string,
  running: RunningRow[],
  history: HistoryRow[],
): { runs: RunRowEntry[]; stats: AggregateStats; sparkValues: number[] } {
  const liveRuns: RunRowEntry[] = running
    .filter((r) => r.automationId === automationId)
    .map((r) => ({
      identifier: r.identifier,
      status: 'running',
      elapsedMs: r.elapsedMs,
      timestamp: r.startedAt,
      isLive: true,
      sessionId: r.sessionId,
    }));

  const completed = history.filter((r) => r.automationId === automationId);
  const completedRuns: RunRowEntry[] = completed.map((r) => ({
    identifier: r.identifier,
    status: r.status,
    elapsedMs: r.elapsedMs,
    timestamp: r.finishedAt,
    isLive: false,
    sessionId: r.sessionId,
  }));

  const allRuns = [...liveRuns, ...completedRuns]
    .sort((a, b) => Date.parse(b.timestamp) - Date.parse(a.timestamp))
    .slice(0, MAX_RUN_ROWS);

  const succeeded = completed.filter((r) => r.status === 'succeeded');
  const totalRuns = completed.length;
  const successRate = totalRuns === 0 ? 0 : succeeded.length / totalRuns;
  const meanElapsedMs =
    succeeded.length === 0
      ? 0
      : Math.round(succeeded.reduce((acc, r) => acc + r.elapsedMs, 0) / succeeded.length);
  const lastFireMs = completed.reduce<number>((acc, r) => {
    const t = Date.parse(r.finishedAt);
    return Number.isNaN(t) ? acc : Math.max(acc, t);
  }, 0);

  return {
    runs: allRuns,
    stats: { totalRuns, successRate, meanElapsedMs, lastFireMs, succeededRuns: succeeded.length },
    sparkValues: bucketByDay(completed, SPARKLINE_DAYS),
  };
}

function bucketByDay(rows: HistoryRow[], days: number, now: number = Date.now()): number[] {
  // Buckets are aligned to UTC midnight. The newest bucket is on the right
  // so the sparkline reads "past → present" as the user scans left to right.
  const start = startOfDayUtc(now) - (days - 1) * MS_PER_DAY;
  const buckets = new Array<number>(days).fill(0);
  for (const row of rows) {
    const t = Date.parse(row.finishedAt);
    if (Number.isNaN(t)) continue;
    const dayIdx = Math.floor((t - start) / MS_PER_DAY);
    if (dayIdx >= 0 && dayIdx < days) {
      buckets[dayIdx] += 1;
    }
  }
  return buckets;
}

function startOfDayUtc(ms: number): number {
  const d = new Date(ms);
  return Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate());
}

function formatSuccessRate(stats: AggregateStats): string {
  if (stats.totalRuns === 0) return '—';
  return `${String(Math.round(stats.successRate * 100))}%`;
}

function formatElapsed(ms: number): string {
  if (ms <= 0) return '—';
  if (ms < 1000) return `${String(ms)}ms`;
  if (ms < 60_000) return `${String(Math.round(ms / 1000))}s`;
  return `${String(Math.round(ms / 60_000))}m`;
}

function formatLastFire(ms: number): string {
  if (ms === 0) return 'Never';
  const diff = Date.now() - ms;
  if (diff < 0) return 'just now';
  if (diff < 60_000) return `${String(Math.round(diff / 1000))}s ago`;
  if (diff < 3_600_000) return `${String(Math.round(diff / 60_000))}m ago`;
  if (diff < MS_PER_DAY) return `${String(Math.round(diff / 3_600_000))}h ago`;
  return `${String(Math.round(diff / MS_PER_DAY))}d ago`;
}

function formatTimestamp(iso: string): string {
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return '';
  const d = new Date(t);
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
}
