/**
 * #211 — the slice [start, end) of a windowed card list to mount, from the
 * scroll offset, the viewport height and an average row height (card + gap).
 * `keepIndex` (the card being dragged, or -1) is always inside the slice.
 */
export function cardWindow({
  count,
  scrollTop,
  viewport,
  rowHeight,
  overscan,
  keepIndex = -1,
}: {
  count: number;
  scrollTop: number;
  viewport: number;
  rowHeight: number;
  overscan: number;
  keepIndex?: number;
}): { start: number; end: number } {
  if (count <= 0) return { start: 0, end: 0 };
  const row = Math.max(1, rowHeight);
  const first = Math.floor(Math.max(0, scrollTop) / row);
  const visible = Math.ceil(Math.max(0, viewport) / row) + 1;
  let start = Math.max(0, Math.min(count - 1, first - overscan));
  let end = Math.min(count, first + visible + overscan);
  if (end <= start) end = Math.min(count, start + 1);
  if (keepIndex >= 0 && keepIndex < count) {
    start = Math.min(start, keepIndex);
    end = Math.max(end, keepIndex + 1);
  }
  return { start, end };
}
