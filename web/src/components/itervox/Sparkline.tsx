import { useMemo } from 'react';

interface SparklineProps {
  values: number[];
  height: number;
  color: string;
  ariaLabel: string;
  /**
   * SVG width — defaults to 100 so the sparkline expands to fill its
   * container via `width="100%"`. Override only when the caller needs
   * pixel-exact sizing.
   */
  width?: number;
}

/**
 * Tiny SVG sparkline. Zero library deps so the bundle stays lean and so it
 * renders identically in the daemon TUI's screenshot tests and the browser.
 *
 * Empty / all-zero series render as a flat baseline so the user can still see
 * where the chart is anchored. The `<title>` element gives screen readers a
 * spoken label and double as a hover tooltip.
 */
export function Sparkline({ values, height, color, ariaLabel, width = 100 }: SparklineProps) {
  const path = useMemo(() => buildPath(values, width, height), [values, width, height]);
  const max = useMemo(() => values.reduce((acc, v) => (v > acc ? v : acc), 0), [values]);

  return (
    <svg
      role="img"
      aria-label={ariaLabel}
      viewBox={`0 0 ${String(width)} ${String(height)}`}
      preserveAspectRatio="none"
      className="w-full"
      style={{ height }}
      data-testid="sparkline"
      data-max={max}
    >
      <title>{ariaLabel}</title>
      <path d={path} fill="none" stroke={color} strokeWidth={1.5} strokeLinejoin="round" />
    </svg>
  );
}

function buildPath(values: number[], width: number, height: number): string {
  if (values.length === 0) {
    // Flat baseline at the bottom of the chart.
    return `M0 ${String(height)} L${String(width)} ${String(height)}`;
  }
  const max = values.reduce((acc, v) => (v > acc ? v : acc), 0);
  if (max === 0) {
    return `M0 ${String(height)} L${String(width)} ${String(height)}`;
  }
  const step = values.length === 1 ? width : width / (values.length - 1);
  const segments = values.map((v, i) => {
    const x = i * step;
    const y = height - (v / max) * height;
    return `${i === 0 ? 'M' : 'L'}${x.toFixed(2)} ${y.toFixed(2)}`;
  });
  return segments.join(' ');
}
