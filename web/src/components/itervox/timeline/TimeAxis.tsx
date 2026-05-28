import { Fragment, useEffect, useState } from 'react';
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
  const labels = ticks.map((t) => formatTimeAxisLabel(t, span));

  return (
    <div
      className="border-theme-line relative h-6 border-b"
      aria-label={labels.join(' ')}
      style={{ marginLeft: AXIS_MARGIN_LEFT, marginRight: AXIS_MARGIN_RIGHT }}
    >
      {ticks.map((t, idx) => {
        const pct = ((t - viewStart) / span) * 100;
        if (pct < 0 || pct > 100) return null;
        const label = labels[idx];
        const edgeTransform =
          pct <= 3 ? 'translateX(0)' : pct >= 97 ? 'translateX(-100%)' : 'translateX(-50%)';
        return (
          <Fragment key={t}>
            {idx > 0 && (
              <span aria-hidden="true" className="sr-only">
                {' '}
              </span>
            )}
            <span
              className="text-theme-muted bg-theme-bg absolute rounded-sm px-1 font-mono text-[10px] leading-4 whitespace-nowrap"
              style={{ left: `${String(pct)}%`, transform: edgeTransform }}
            >
              {label}
            </span>
          </Fragment>
        );
      })}
      <NowMarker viewStart={viewStart} viewEnd={viewEnd} />
    </div>
  );
}
