import { describe, expect, it } from 'vitest';
import type { Active, Over } from '@dnd-kit/core';
import { BOARD_DND_ACCESSIBILITY } from '../BoardColumn/boardAnnouncements';

const active = { id: 'ENG-1' } as unknown as Active;
const over = { id: 'In Progress' } as unknown as Over;
const a = BOARD_DND_ACCESSIBILITY.announcements;

describe('board dnd announcements (CORE-068)', () => {
  it('names the issue identifier and the column', () => {
    expect(a.onDragStart({ active })).toBe('Picked up ENG-1.');
    expect(a.onDragOver({ active, over })).toBe('ENG-1 is over column In Progress.');
    expect(a.onDragOver({ active, over: null })).toBe('ENG-1 is not over a column.');
    expect(a.onDragEnd({ active, over })).toBe('ENG-1 was dropped in column In Progress.');
    expect(a.onDragEnd({ active, over: null })).toBe('ENG-1 was dropped outside any column.');
    expect(a.onDragCancel({ active, over: null })).toBe('Moving ENG-1 was cancelled.');
  });

  it('tells keyboard users how to operate the drag handle', () => {
    expect(BOARD_DND_ACCESSIBILITY.screenReaderInstructions.draggable).toMatch(/space/i);
  });
});
