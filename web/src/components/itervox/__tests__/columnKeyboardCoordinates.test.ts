import { describe, expect, it } from 'vitest';
import type { ClientRect, SensorContext } from '@dnd-kit/core';
import { columnKeyboardCoordinates } from '../BoardColumn/columnKeyboardCoordinates';

function rect(left: number, top: number, width: number, height: number): ClientRect {
  return { left, top, width, height, right: left + width, bottom: top + height };
}

function ctx(collision: ClientRect | null): SensorContext {
  return {
    collisionRect: collision,
    droppableRects: new Map([
      ['Backlog', rect(0, 0, 250, 800)],
      ['In Progress', rect(262, 0, 250, 800)],
      ['Done', rect(524, 0, 250, 800)],
    ]),
  } as unknown as SensorContext;
}

const key = (code: string) => new KeyboardEvent('keydown', { code });

describe('columnKeyboardCoordinates (CORE-068)', () => {
  const card = rect(10, 100, 230, 72);
  const current = { x: 10, y: 100 };

  it('ArrowRight jumps to the next column, keeping the vertical position', () => {
    const next = columnKeyboardCoordinates(key('ArrowRight'), {
      active: 'ENG-1',
      currentCoordinates: current,
      context: ctx(card),
    });
    expect(next).toEqual({ x: 272, y: 100 });
  });

  it('ArrowLeft from the first column and ArrowRight from the last do nothing', () => {
    expect(
      columnKeyboardCoordinates(key('ArrowLeft'), {
        active: 'ENG-1',
        currentCoordinates: current,
        context: ctx(card),
      }),
    ).toBeUndefined();
    expect(
      columnKeyboardCoordinates(key('ArrowRight'), {
        active: 'ENG-1',
        currentCoordinates: { x: 534, y: 100 },
        context: ctx(rect(534, 100, 230, 72)),
      }),
    ).toBeUndefined();
  });

  it('ArrowLeft jumps back one column', () => {
    expect(
      columnKeyboardCoordinates(key('ArrowLeft'), {
        active: 'ENG-1',
        currentCoordinates: { x: 534, y: 100 },
        context: ctx(rect(534, 100, 230, 72)),
      }),
    ).toEqual({ x: 272, y: 100 });
  });

  it('ignores other keys and a missing collision rect', () => {
    expect(
      columnKeyboardCoordinates(key('ArrowDown'), {
        active: 'ENG-1',
        currentCoordinates: current,
        context: ctx(card),
      }),
    ).toBeUndefined();
    expect(
      columnKeyboardCoordinates(key('ArrowRight'), {
        active: 'ENG-1',
        currentCoordinates: current,
        context: ctx(null),
      }),
    ).toBeUndefined();
  });
});
