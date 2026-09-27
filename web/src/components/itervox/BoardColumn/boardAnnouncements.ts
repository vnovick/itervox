import type { Announcements, ScreenReaderInstructions } from '@dnd-kit/core';

/**
 * CORE-068 — dnd-kit live-region text for the board and agent-queue boards.
 * Draggable ids are issue identifiers and droppable ids are column names
 * (tracker states or profile names), so the defaults' "draggable item ENG-1"
 * / "droppable area Todo" become plain sentences.
 */
const announcements: Announcements = {
  onDragStart: ({ active }) => `Picked up ${String(active.id)}.`,
  onDragOver: ({ active, over }) =>
    over
      ? `${String(active.id)} is over column ${String(over.id)}.`
      : `${String(active.id)} is not over a column.`,
  onDragEnd: ({ active, over }) =>
    over
      ? `${String(active.id)} was dropped in column ${String(over.id)}.`
      : `${String(active.id)} was dropped outside any column.`,
  onDragCancel: ({ active }) => `Moving ${String(active.id)} was cancelled.`,
};

const screenReaderInstructions: ScreenReaderInstructions = {
  draggable:
    'To move this issue to another column, press Space to pick it up. Use the arrow keys to move it between columns, Space again to drop it, or Escape to cancel.',
};

export const BOARD_DND_ACCESSIBILITY = { announcements, screenReaderInstructions };
