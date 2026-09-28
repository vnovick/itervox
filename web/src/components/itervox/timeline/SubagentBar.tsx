import { memo } from 'react';
import { activateOnKey } from '../../../utils/activateOnKey';
import type { SubagentSegment } from './types';
import { SUB_BAR_HEIGHT, SUBAGENT_LABEL_WIDTH, subagentColor, TICK_MARK_STYLE } from './styles';

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
      className="focus-visible:ring-theme-accent flex cursor-pointer items-center gap-2 rounded px-1 py-1 transition-colors focus-visible:ring-2 focus-visible:outline-none"
      style={{
        paddingLeft: 24,
        background: selected ? 'var(--purple-soft)' : 'transparent',
      }}
      onClick={onSelect}
      onKeyDown={activateOnKey(onSelect)}
    >
      <span className="shrink-0 text-xs" style={{ color: colors.text }}>
        ↗
      </span>

      <span
        className="shrink-0 truncate font-mono text-[11px]"
        style={{ width: SUBAGENT_LABEL_WIDTH, color: colors.text }}
        title={segment.name}
      >
        {segment.name.slice(0, 8)}
      </span>

      <div
        className="bg-theme-bg-soft relative flex-1 overflow-hidden rounded"
        style={{ height: SUB_BAR_HEIGHT }}
      >
        {/* Subagent progress bar */}
        <div
          className="absolute top-0 h-full rounded"
          style={{
            left: `${String(barLeft * 100)}%`,
            width: `${String(barWidth * 100)}%`,
            background: colors.bar,
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
                className="absolute top-0 z-10 h-full w-px"
                style={{ left: `${String(frac * 100)}%`, background: TICK_MARK_STYLE }}
              />
            );
          })}
      </div>

      <span className="text-theme-text-secondary w-14 shrink-0 text-right text-[10px]">
        {tokApprox}
      </span>
    </div>
  );
});
