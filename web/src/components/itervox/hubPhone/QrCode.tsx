import { useMemo } from 'react';
import { encode } from 'uqr';

/**
 * #231 — a QR code as SVG (uqr encodes; each dark module is a rect). Black on
 * white whatever the theme, with the quiet zone uqr adds, so every phone
 * camera reads it.
 */
export function QrCode({
  value,
  label,
  size = 224,
}: {
  value: string;
  label: string;
  size?: number;
}) {
  const { data, size: modules } = useMemo(() => encode(value, { ecc: 'M', border: 2 }), [value]);
  const path = useMemo(() => {
    let d = '';
    data.forEach((row, y) => {
      row.forEach((dark, x) => {
        if (dark) d += `M${String(x)} ${String(y)}h1v1h-1z`;
      });
    });
    return d;
  }, [data]);
  return (
    <svg
      role="img"
      aria-label={label}
      width={size}
      height={size}
      viewBox={`0 0 ${String(modules)} ${String(modules)}`}
      shapeRendering="crispEdges"
      className="rounded bg-white"
    >
      <rect width={modules} height={modules} fill="#fff" />
      <path d={path} fill="#000" />
    </svg>
  );
}
