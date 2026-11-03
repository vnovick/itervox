import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import type { TrackerIssue } from '../../../types/schemas';
import { cardWindow } from './cardWindow';

/** Columns with more cards than this render only the cards near the viewport (#211). */
export const BOARD_VIRTUALIZE_THRESHOLD = 100;
/** Starting estimate of one card plus the 8px gap; replaced by the measured average. */
const ESTIMATED_ROW_HEIGHT = 120;
/** Viewport height used until the scroll container can be measured. */
const FALLBACK_VIEWPORT = 800;
const OVERSCAN_ROWS = 8;

const LIST_CLS = 'flex-1 space-y-2 overflow-y-auto px-3 pb-3';

/**
 * The scrollable card list of a BoardColumn. Up to the threshold it renders
 * every card, exactly as before. Above it, it windows the list: only the
 * cards within the viewport (plus overscan) mount, and two padding blocks
 * stand in for the rest, sized from the measured average card height.
 *
 * dnd-kit stays correct because nothing it relies on is windowed: the column
 * itself is the droppable (cards are only draggables), the keyboard sensor
 * moves between column rects, and the board drags a DragOverlay copy. The
 * card being dragged is always kept mounted, so its placeholder keeps focus
 * even when auto-scroll moves the window away from it.
 */
export function ColumnCardList({
  issues,
  draggingId,
  renderCard,
}: {
  issues: TrackerIssue[];
  draggingId?: string;
  renderCard: (issue: TrackerIssue) => ReactNode;
}) {
  const virtual = issues.length > BOARD_VIRTUALIZE_THRESHOLD;
  const scrollRef = useRef<HTMLDivElement>(null);
  const windowRef = useRef<HTMLDivElement>(null);
  const [scrollTop, setScrollTop] = useState(0);
  const [viewport, setViewport] = useState(FALLBACK_VIEWPORT);
  const [rowHeight, setRowHeight] = useState(ESTIMATED_ROW_HEIGHT);

  useEffect(() => {
    const node = scrollRef.current;
    if (!virtual || !node) return;
    const measure = () => {
      if (node.clientHeight > 0) setViewport(node.clientHeight);
    };
    measure();
    if (typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(measure);
    observer.observe(node);
    return () => {
      observer.disconnect();
    };
  }, [virtual]);

  const dragIndex = draggingId ? issues.findIndex((i) => i.identifier === draggingId) : -1;
  const { start, end } = virtual
    ? cardWindow({
        count: issues.length,
        scrollTop,
        viewport,
        rowHeight,
        overscan: OVERSCAN_ROWS,
        keepIndex: dragIndex,
      })
    : { start: 0, end: issues.length };

  // Refine the row estimate from the cards actually rendered (jsdom and
  // detached nodes report 0 and are ignored).
  useLayoutEffect(() => {
    const node = windowRef.current;
    if (!virtual || !node || end <= start) return;
    if (node.offsetHeight <= 0) return;
    const avg = Math.max(24, (node.offsetHeight + 8) / (end - start));
    if (Math.abs(avg - rowHeight) / rowHeight > 0.1) setRowHeight(Math.round(avg));
  }, [virtual, start, end, rowHeight]);

  if (!virtual) {
    return (
      <div ref={scrollRef} className={LIST_CLS}>
        {issues.map(renderCard)}
      </div>
    );
  }

  return (
    <div
      ref={scrollRef}
      className="flex-1 overflow-y-auto px-3 pb-3"
      data-virtualized="true"
      onScroll={(event) => {
        setScrollTop(event.currentTarget.scrollTop);
      }}
    >
      <div
        style={{
          paddingTop: start * rowHeight,
          paddingBottom: (issues.length - end) * rowHeight,
        }}
      >
        <div ref={windowRef} className="space-y-2">
          {issues.slice(start, end).map(renderCard)}
        </div>
      </div>
    </div>
  );
}
