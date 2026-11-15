import { memo } from 'react';
import { activateOnKey } from '../../../utils/activateOnKey';
import type { SubagentSegment } from './types';
import { ROW_GRID, ROW_META, ROW_TRACK, SUB_BAR_HEIGHT, subagentColor } from './styles';

interface SubagentBarProps {
  segment: SubagentSegment;
  colorIdx: number;
  runBarLeft: number;
  runBarWidth: number;
  selected: boolean;
  onSelect: () => void;
}

export const SubagentBar = memo(function SubagentBar({
  segment,
  colorIdx,
  runBarLeft,
  runBarWidth,
  selected,
  onSelect,
}: SubagentBarProps) {
  const barLeft = runBarLeft + segment.startFrac * runBarWidth;
  const barWidth = Math.max((segment.endFrac - segment.startFrac) * runBarWidth, 0.005);
  const colors = subagentColor(colorIdx);

  const tokApprox =
    segment.logSlice.length > 0
      ? `${String(Math.round((segment.logSlice.length * 80) / 1000))}k tok`
      : `${String(segment.logSlice.length)} ev`;

  return (
    <div
      role="button"
      tabIndex={0}
      aria-pressed={selected}
      aria-label={`Subagent ${segment.name} log`}
      className={`${ROW_GRID} focus-visible:ring-theme-accent hover:bg-theme-subtle cursor-pointer rounded-[var(--radius-sm)] py-1 transition-colors focus-visible:ring-2 focus-visible:outline-none ${
        selected ? 'bg-theme-subtle' : ''
      }`}
      onClick={onSelect}
      onKeyDown={activateOnKey(onSelect)}
    >
      <span aria-hidden="true" className={`text-center text-xs ${colors.text}`}>
        ↗
      </span>

      <span
        className={`min-w-0 truncate font-mono text-[11px] ${colors.text} ${selected ? 'font-semibold' : ''}`}
        title={segment.name}
      >
        {segment.name.slice(0, 8)}
      </span>

      <div
        className={`${ROW_TRACK} bg-theme-subtle relative overflow-hidden rounded-full`}
        style={{ height: SUB_BAR_HEIGHT }}
      >
        {/* Subagent progress bar */}
        <div
          className={`absolute top-0 h-full rounded-full ${colors.bar}`}
          style={{
            left: `${String(barLeft * 100)}%`,
            width: `${String(barWidth * 100)}%`,
          }}
        />
        {/* Action tick marks */}
        {segment.logSlice
          .map((e, i) => ({ e, i }))
          .filter(({ e }) => e.event === 'action')
          .map(({ i }) => {
            const frac = barLeft + (i / Math.max(segment.logSlice.length, 1)) * barWidth;
            return (
              <div
                key={i}
                className="bg-theme-hairline absolute top-0 z-10 h-full w-px"
                style={{ left: `${String(frac * 100)}%` }}
              />
            );
          })}
      </div>

      <span className={`${ROW_META} text-theme-text-secondary text-[10px]`}>{tokApprox}</span>
    </div>
  );
});
