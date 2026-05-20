import { memo } from 'react';
import { fmtMs } from '../../../utils/format';
import type { NormalisedSession, SubagentSegment } from './types';
import { clamp01 } from './types';
import { SubagentBar } from './SubagentBar';
import { formatAxisTime } from './timeFormatting';
import {
  BAR_HEIGHT,
  SUB_BAR_HEIGHT,
  ROW_MIN_HEIGHT,
  LABEL_WIDTH,
  ELAPSED_WIDTH,
  TOKENS_WIDTH,
  SUBAGENT_LABEL_WIDTH,
  barGradient,
  statusBadgeStyle,
  statusBadgeLabel,
  TICK_MARK_STYLE,
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
  const bg = barGradient(session.status);
  const badge = statusBadgeStyle(session.status);

  const timeLabel = formatAxisTime(new Date(session.startedAt).getTime());

  return (
    <>
      {/* Run row */}
      <div
        className="hover:bg-theme-bg-soft flex cursor-pointer items-center gap-2 rounded transition-colors"
        style={{ minHeight: ROW_MIN_HEIGHT, padding: '8px 0' }}
        onClick={onToggleExpand}
      >
        {/* Expand chevron */}
        <span
          className="w-4 shrink-0 text-center text-xs"
          style={{ color: subagents.length > 0 ? 'var(--text-secondary)' : 'transparent' }}
        >
          {expanded ? '▼' : '▶'}
        </span>

        {/* Run number + time */}
        <span
          className="shrink-0 font-mono text-[11px] leading-tight"
          style={{ width: LABEL_WIDTH }}
          title={session.startedAt}
        >
          <span className="text-theme-accent-strong font-semibold">#{runNumber}</span>
          <span className="text-theme-muted"> · {timeLabel}</span>
        </span>

        {/* Bar track */}
        <div
          className="bg-theme-bg-soft relative flex-1 overflow-hidden rounded"
          style={{ height: BAR_HEIGHT }}
        >
          {/* Progress bar */}
          <div
            className="absolute top-0 flex h-full items-center rounded"
            style={{
              left: `${String(barLeft * 100)}%`,
              width: `${String(barWidth * 100)}%`,
              background: bg,
            }}
            title={`${session.identifier} — ${fmtMs(session.elapsedMs)}`}
          />
          {/* Subagent tick marks */}
          {subagents.map((sa, si) => (
            <div
              key={si}
              className="absolute top-0 z-10 h-full w-0.5"
              style={{
                left: `${String((barLeft + sa.startFrac * barWidth) * 100)}%`,
                background: TICK_MARK_STYLE,
              }}
            />
          ))}
        </div>

        {/* Elapsed time */}
        <span
          className="text-theme-text-secondary shrink-0 text-right font-mono text-[11px]"
          style={{ width: ELAPSED_WIDTH }}
        >
          {fmtMs(session.elapsedMs)}
        </span>

        {/* Tokens consumed */}
        <span
          className="text-theme-muted shrink-0 text-right font-mono text-[11px]"
          style={{ width: TOKENS_WIDTH }}
          title={`${String(session.tokens)} tokens`}
        >
          {fmtTokens(session.tokens)}
        </span>

        {/* Status badge (non-live only) */}
        {!isLive && (
          <span
            className="shrink-0 rounded-full px-2.5 py-0.5 text-[10px] font-semibold tracking-[0.03em] uppercase"
            style={badge}
          >
            {statusBadgeLabel(session.status)}
          </span>
        )}
      </div>

      {/* Expanded subagent rows */}
      {expanded && (
        <div className="space-y-0.5 pb-1">
          {/* Main run bar (when expanded) */}
          <div
            className="flex cursor-pointer items-center gap-2 rounded px-1 py-1 transition-colors"
            style={{
              paddingLeft: 24,
              background: selectedSubagentIdx === null ? 'var(--accent-soft)' : 'transparent',
            }}
            onClick={() => {
              onSelectSubagent(null);
            }}
          >
            <span className="text-theme-muted shrink-0 text-xs">◈</span>
            <span
              className="shrink-0 truncate font-mono text-[11px]"
              style={{
                width: SUBAGENT_LABEL_WIDTH,
                color:
                  selectedSubagentIdx === null ? 'var(--accent-strong)' : 'var(--text-secondary)',
              }}
            >
              Main
            </span>
            <div
              className="bg-theme-bg-soft relative flex-1 overflow-hidden rounded"
              style={{ height: SUB_BAR_HEIGHT }}
            >
              <div
                className="absolute top-0 h-full rounded"
                style={{
                  left: `${String(barLeft * 100)}%`,
                  width: `${String(barWidth * 100)}%`,
                  background: bg,
                }}
              />
            </div>
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
