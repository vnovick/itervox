import { useEffect, useState } from 'react';
import { AXIS_MARGIN_LEFT, AXIS_MARGIN_RIGHT } from './styles';
import { buildTimeAxisTicks, formatTimeAxisLabel } from './timeAxisModel';

function NowMarker({ viewStart, viewEnd }: { viewStart: number; viewEnd: number }) {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    const id = setInterval(() => {
      setNow(Date.now());
    }, 1000);
    return () => {
      clearInterval(id);
    };
  }, []);
  const pct = ((now - viewStart) / (viewEnd - viewStart)) * 100;
  if (pct < 0 || pct > 100) return null;
  return (
    <div
      className="bg-theme-danger pointer-events-none absolute top-0 bottom-0 w-px"
      style={{ left: `${String(pct)}%` }}
    />
  );
}

export function TimeAxis({ viewStart, viewEnd }: { viewStart: number; viewEnd: number }) {
  const span = viewEnd - viewStart;
  if (span <= 0) return null;
  const ticks = buildTimeAxisTicks(viewStart, viewEnd);

  return (
    <div
      className="border-theme-line relative h-6 border-b"
      style={{ marginLeft: AXIS_MARGIN_LEFT, marginRight: AXIS_MARGIN_RIGHT }}
    >
      {ticks.map((t) => {
        const pct = ((t - viewStart) / span) * 100;
        if (pct < 0 || pct > 100) return null;
        const label = formatTimeAxisLabel(t, span);
        return (
          <span
            key={t}
            className="text-theme-muted absolute -translate-x-1/2 font-mono text-[10px] whitespace-nowrap"
            style={{ left: `${String(pct)}%` }}
          >
            {label}
          </span>
        );
      })}
      <NowMarker viewStart={viewStart} viewEnd={viewEnd} />
    </div>
  );
}
