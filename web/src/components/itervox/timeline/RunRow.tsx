import { memo } from 'react';
import { activateOnKey } from '../../../utils/activateOnKey';
import { fmtMs } from '../../../utils/format';
import type { NormalisedSession, SubagentSegment } from './types';
import { clamp01 } from './types';
import { SubagentBar } from './SubagentBar';
import { formatAxisTime } from './timeFormatting';
import {
  BAR_HEIGHT,
  SUB_BAR_HEIGHT,
  ROW_MIN_HEIGHT,
  ROW_GRID,
  ROW_META,
  ROW_TRACK,
  barFillClass,
  statusBadgeTone,
  statusBadgeLabel,
} from './styles';

function fmtTokens(n: number): string {
  if (!n || n <= 0) return '—';
  if (n < 1000) return String(n);
  if (n < 10000) return `${(n / 1000).toFixed(1)}k`;
  return `${String(Math.round(n / 1000))}k`;
}

interface RunRowProps {
  session: NormalisedSession;
  subagents: SubagentSegment[];
  viewStart: number;
  viewEnd: number;
  expanded: boolean;
  selectedSubagentIdx: number | null;
  runNumber: number;
  onToggleExpand: () => void;
  onSelectSubagent: (idx: number | null) => void;
}

export const RunRow = memo(function RunRow({
  session,
  subagents,
  viewStart,
  viewEnd,
  expanded,
  selectedSubagentIdx,
  runNumber,
  onToggleExpand,
  onSelectSubagent,
}: RunRowProps) {
  const span = viewEnd - viewStart;
  const start = new Date(session.startedAt).getTime();
  const end = session.finishedAt
    ? new Date(session.finishedAt).getTime()
    : start + Math.max(session.elapsedMs, 1000);

  const barLeft = clamp01((start - viewStart) / span);
  const barRight = clamp01((end - viewStart) / span);
  const barWidth = Math.max(barRight - barLeft, 0.02);

  const isLive = session.status === 'live';
  const fill = barFillClass(session.status);

  const timeLabel = formatAxisTime(new Date(session.startedAt).getTime());
  const mainSelected = selectedSubagentIdx === null;

  return (
    <>
      {/* Run row */}
      {/* CORE-088 — was mouse-only (no keyboard path to a run's logs). */}
      <div
        role="button"
        tabIndex={0}
        aria-expanded={expanded}
        aria-label={`Run #${String(runNumber)} at ${timeLabel}, ${fmtMs(session.elapsedMs)}`}
        data-testid="timeline-run-row"
        className={`${ROW_GRID} hover:bg-theme-subtle focus-visible:ring-theme-accent cursor-pointer rounded-[var(--radius-sm)] py-2 transition-colors focus-visible:ring-2 focus-visible:outline-none`}
        style={{ minHeight: ROW_MIN_HEIGHT }}
        onClick={onToggleExpand}
        onKeyDown={activateOnKey(onToggleExpand)}
      >
        {/* Expand chevron */}
        <span
          aria-hidden="true"
          className={`text-center text-xs ${
            subagents.length > 0 ? 'text-theme-text-secondary' : 'text-transparent'
          }`}
        >
          {expanded ? '▼' : '▶'}
        </span>

        {/* Run number + time */}
        <span
          className="min-w-0 truncate font-mono text-[11px] leading-tight"
          title={session.startedAt}
        >
          <span className="text-theme-text font-sans text-xs font-semibold">#{runNumber}</span>
          <span className="text-theme-muted"> · {timeLabel}</span>
        </span>

        {/* Bar track */}
        <div
          data-testid="timeline-run-track"
          className={`${ROW_TRACK} glass-card relative overflow-hidden rounded-[var(--radius-sm)]`}
          style={{ height: BAR_HEIGHT + 12 }}
        >
          {/* Progress bar */}
          <div
            data-testid="timeline-run-bar"
            className={`absolute top-1.5 bottom-1.5 rounded-lg ${fill}`}
            style={{
              left: `${String(barLeft * 100)}%`,
              width: `${String(barWidth * 100)}%`,
            }}
            title={`${session.identifier} — ${fmtMs(session.elapsedMs)}`}
          />
          {/* Subagent tick marks */}
          {subagents.map((sa, si) => (
            <div
              key={si}
              className="bg-theme-hairline absolute top-1.5 bottom-1.5 z-10 w-0.5"
              style={{ left: `${String((barLeft + sa.startFrac * barWidth) * 100)}%` }}
            />
          ))}
        </div>

        <span className={ROW_META}>
          {/* Elapsed time */}
          <span className="text-theme-text-secondary w-[60px] text-right font-mono text-[11px]">
            {fmtMs(session.elapsedMs)}
          </span>

          {/* Tokens consumed */}
          <span
            className="text-theme-muted w-[44px] text-right font-mono text-[11px]"
            title={`${String(session.tokens)} tokens`}
          >
            {fmtTokens(session.tokens)}
          </span>

          {/* Status badge (non-live only); a fixed slot keeps every track equal. */}
          <span className="flex justify-end @xl:w-[76px]">
            {!isLive && (
              <span className={`tag ${statusBadgeTone(session.status)}`}>
                {statusBadgeLabel(session.status)}
              </span>
            )}
          </span>
        </span>
      </div>

      {/* Expanded subagent rows: the same grid and time axis as the run row. */}
      {expanded && (
        <div className="space-y-0.5 pb-1">
          {/* Main run bar (when expanded) */}
          <div
            role="button"
            tabIndex={0}
            aria-pressed={mainSelected}
            aria-label="Main agent log"
            className={`${ROW_GRID} focus-visible:ring-theme-accent hover:bg-theme-subtle cursor-pointer rounded-[var(--radius-sm)] py-1 transition-colors focus-visible:ring-2 focus-visible:outline-none ${
              mainSelected ? 'bg-theme-subtle' : ''
            }`}
            onClick={() => {
              onSelectSubagent(null);
            }}
            onKeyDown={activateOnKey(() => {
              onSelectSubagent(null);
            })}
          >
            <span aria-hidden="true" className="text-theme-muted text-center text-xs">
              ◈
            </span>
            <span
              className={`min-w-0 truncate font-mono text-[11px] ${
                mainSelected ? 'text-theme-accent-text font-semibold' : 'text-theme-text-secondary'
              }`}
            >
              Main
            </span>
            <div
              className={`${ROW_TRACK} bg-theme-subtle relative overflow-hidden rounded-full`}
              style={{ height: SUB_BAR_HEIGHT }}
            >
              <div
                data-testid="timeline-main-bar"
                className={`absolute top-0 h-full rounded-full ${fill}`}
                style={{
                  left: `${String(barLeft * 100)}%`,
                  width: `${String(barWidth * 100)}%`,
                }}
              />
            </div>
            <span className={ROW_META} />
          </div>

          {/* Subagent bars */}
          {subagents.map((sa, si) => (
            <SubagentBar
              key={`${sa.name}-${String(si)}`}
              segment={sa}
              colorIdx={si}
              runBarLeft={barLeft}
              runBarWidth={barWidth}
              selected={selectedSubagentIdx === si}
              onSelect={() => {
                onSelectSubagent(selectedSubagentIdx === si ? null : si);
              }}
            />
          ))}
        </div>
      )}
    </>
  );
});
