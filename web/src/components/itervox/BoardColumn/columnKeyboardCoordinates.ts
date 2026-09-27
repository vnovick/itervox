import type { KeyboardCoordinateGetter } from '@dnd-kit/core';

/**
 * CORE-068 — keyboard drag moves a card one whole column per ArrowLeft /
 * ArrowRight instead of dnd-kit's default 25px step, which on a 250px column
 * needs ~10 presses before the card is "over" the neighbour. Columns are the
 * registered droppables ordered by their left edge; the card keeps its
 * horizontal offset inside the column and its vertical position.
 * Other keys (ArrowUp/Down) are ignored: cards are not ordered within a column.
 */
export const columnKeyboardCoordinates: KeyboardCoordinateGetter = (
  event,
  { currentCoordinates, context },
) => {
  const direction = event.code === 'ArrowRight' ? 1 : event.code === 'ArrowLeft' ? -1 : 0;
  const card = context.collisionRect;
  if (direction === 0 || !card) return undefined;

  const columns = [...context.droppableRects.values()].sort((a, b) => a.left - b.left);
  const centerX = card.left + card.width / 2;
  let index = columns.findIndex((c) => centerX >= c.left && centerX <= c.right);
  if (index === -1) {
    // Between columns: treat the nearest column to the left as current.
    index = columns.reduce((acc, c, i) => (c.left <= centerX ? i : acc), -1);
  }
  const current = columns[index] as (typeof columns)[number] | undefined;
  const target = columns[index + direction] as (typeof columns)[number] | undefined;
  if (!current || !target) return undefined;

  event.preventDefault();
  return { x: currentCoordinates.x + (target.left - current.left), y: currentCoordinates.y };
};
