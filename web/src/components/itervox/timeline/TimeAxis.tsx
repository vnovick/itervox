import { Fragment, useEffect, useRef, useState } from 'react';
import { AXIS_INSET } from './styles';
import { buildTimeAxisTicks, formatTimeAxisLabel, tickTargetForWidth } from './timeAxisModel';

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

/** Tracks an element's rendered width (0 until measured, or without ResizeObserver). */
function useElementWidth<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  const [width, setWidth] = useState(0);
  useEffect(() => {
    const el = ref.current;
    if (!el || typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver((entries) => {
      setWidth(entries[0]?.contentRect.width ?? 0);
    });
    observer.observe(el);
    return () => {
      observer.disconnect();
    };
  }, []);
  return [ref, width] as const;
}

export function TimeAxis({ viewStart, viewEnd }: { viewStart: number; viewEnd: number }) {
  // Fewer ticks on a narrow axis, so HH:mm labels never collide.
  const [ref, width] = useElementWidth<HTMLDivElement>();
  const span = viewEnd - viewStart;
  const ticks = span > 0 ? buildTimeAxisTicks(viewStart, viewEnd, tickTargetForWidth(width)) : [];
  const labels = ticks.map((t) => formatTimeAxisLabel(t, span));

  return (
    <div
      ref={ref}
      data-testid="timeline-axis"
      className={`border-theme-line relative h-6 border-b ${AXIS_INSET}`}
      aria-label={labels.join(' ')}
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
              className="text-theme-muted absolute px-1 font-mono text-[10px] leading-4 whitespace-nowrap"
              style={{ left: `${String(pct)}%`, transform: edgeTransform }}
            >
              {label}
            </span>
          </Fragment>
        );
      })}
      {span > 0 && <NowMarker viewStart={viewStart} viewEnd={viewEnd} />}
    </div>
  );
}
