// #211 — dnd-kit drag and drop still works across a virtualised column
// (more than BOARD_VIRTUALIZE_THRESHOLD cards): the real KeyboardSensor and
// the CORE-068 column coordinate getter move a card out of, and into, a
// windowed column. jsdom has no layout, so element rects are stubbed per
// column (300px apart) from the `data-board-column` attribute.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, screen, within } from '@testing-library/react';
import { BoardView } from '../BoardView';
import { render } from '../../../../test/render';
import { makeSnapshot } from '../../../../test/fixtures/snapshots';
import { makeIssue, makeIssues } from '../../../../test/fixtures/issues';
import { BOARD_VIRTUALIZE_THRESHOLD } from '../../../../components/itervox/BoardColumn/ColumnCardList';

const COLUMNS = ['Todo', 'In Progress', 'Done'];
const snapshot = makeSnapshot({
  backlogStates: ['Todo'],
  activeStates: ['In Progress'],
  completionState: '',
  terminalStates: ['Done'],
});

function rect(left: number, top: number, width: number, height: number): DOMRect {
  return {
    left,
    top,
    width,
    height,
    right: left + width,
    bottom: top + height,
    x: left,
    y: top,
    toJSON: () => ({}),
  } as DOMRect;
}

function rectFor(el: Element): DOMRect {
  // The DragOverlay content sits in a position:fixed wrapper placed at the
  // lifted card's rect; report that rect for the wrapper and its content.
  for (let n: Element | null = el; n; n = n.parentElement) {
    if (n instanceof HTMLElement && n.style.position === 'fixed') {
      const px = (v: string) => Number.parseFloat(v) || 0;
      return rect(px(n.style.left), px(n.style.top), px(n.style.width), px(n.style.height));
    }
  }
  const column = el.closest('[data-board-column]');
  if (!column) return rect(0, 0, 0, 0);
  const left = COLUMNS.indexOf(column.getAttribute('data-board-column') ?? '') * 300;
  if (el === column) return rect(left, 0, 280, 800);
  return rect(left + 10, 60, 260, 100);
}

beforeEach(() => {
  vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
    return rectFor(this);
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

function column(state: string): HTMLElement {
  const node = document.querySelector(`[data-board-column="${state}"]`);
  if (!(node instanceof HTMLElement)) throw new Error(`no column ${state}`);
  return node;
}

async function press(target: Element, key: string, code: string) {
  await act(async () => {
    fireEvent.keyDown(target, { key, code });
    // KeyboardSensor attaches its document listeners on a 0ms timeout.
    await new Promise((resolve) => setTimeout(resolve, 50));
  });
}

/** Space to lift, one arrow per column, Space to drop (dnd-kit KeyboardSensor). */
async function keyboardDrag(identifier: string, code: 'ArrowRight' | 'ArrowLeft') {
  const handle = screen.getByLabelText(`Move ${identifier}`);
  handle.focus();
  await press(handle, ' ', 'Space');
  await press(document.activeElement ?? handle, code, code);
  await press(document.activeElement ?? handle, ' ', 'Space');
}

describe('BoardView drag and drop over a virtualised column', () => {
  const big = makeIssues(BOARD_VIRTUALIZE_THRESHOLD + 50, () => ({ state: 'Todo' }));
  const issues = [
    ...big,
    makeIssue({ identifier: 'MOVE-1', title: 'Mover', state: 'In Progress' }),
  ];

  it('windows the oversized column and leaves the small one whole', () => {
    render(
      <BoardView
        issues={issues}
        onSelect={vi.fn()}
        onStateChange={vi.fn()}
        availableProfiles={[]}
        onProfileChange={vi.fn()}
      />,
      { snapshot },
    );
    expect(column('Todo').querySelector('[data-virtualized="true"]')).not.toBeNull();
    expect(within(column('Todo')).queryByText('DEMO-150')).toBeNull();
    expect(column('In Progress').querySelector('[data-virtualized="true"]')).toBeNull();
  });

  it('drags a card out of a virtualised column into the next column', async () => {
    const onStateChange = vi.fn();
    render(
      <BoardView
        issues={issues}
        onSelect={vi.fn()}
        onStateChange={onStateChange}
        availableProfiles={[]}
        onProfileChange={vi.fn()}
      />,
      { snapshot },
    );
    await keyboardDrag('DEMO-1', 'ArrowRight');
    expect(onStateChange).toHaveBeenCalledWith('DEMO-1', 'In Progress');
  });

  it('drags a card into a virtualised column', async () => {
    const onStateChange = vi.fn();
    render(
      <BoardView
        issues={issues}
        onSelect={vi.fn()}
        onStateChange={onStateChange}
        availableProfiles={[]}
        onProfileChange={vi.fn()}
      />,
      { snapshot },
    );
    await keyboardDrag('MOVE-1', 'ArrowLeft');
    expect(onStateChange).toHaveBeenCalledWith('MOVE-1', 'Todo');
  });

  it('keeps a lifted card mounted and focused while its column scrolls away, then drops it', async () => {
    const onStateChange = vi.fn();
    render(
      <BoardView
        issues={issues}
        onSelect={vi.fn()}
        onStateChange={onStateChange}
        availableProfiles={[]}
        onProfileChange={vi.fn()}
      />,
      { snapshot },
    );
    const handle = screen.getByLabelText('Move DEMO-1');
    handle.focus();
    await press(handle, ' ', 'Space');
    const scroller = column('Todo').querySelector('[data-virtualized="true"]') as HTMLElement;
    scroller.scrollTop = 140 * 120;
    await act(async () => {
      fireEvent.scroll(scroller);
      await new Promise((resolve) => setTimeout(resolve, 50));
    });
    // The window moved to the bottom of the column …
    expect(within(column('Todo')).getByText('DEMO-150')).toBeInTheDocument();
    // … but the lifted card's placeholder (and its focused stand-in handle) stayed.
    expect(document.activeElement?.getAttribute('aria-label')).toBe('Move DEMO-1');
    await press(document.activeElement ?? handle, 'ArrowRight', 'ArrowRight');
    await press(document.activeElement ?? handle, ' ', 'Space');
    expect(onStateChange).toHaveBeenCalledWith('DEMO-1', 'In Progress');
  });
});
