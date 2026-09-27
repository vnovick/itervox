// CORE-081 — DraggableCard must not read offsetHeight after every render. The
// card height is tracked by a ResizeObserver; the drag placeholder reads the
// last observed height on its first isDragging render.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import DraggableCard from '../BoardColumn/DraggableCard';
import type { TrackerIssue } from '../../../types/schemas';

const dnd = vi.hoisted(() => ({ isDragging: false }));

vi.mock('@dnd-kit/core', () => ({
  useDraggable: () => ({
    attributes: {},
    listeners: {},
    setNodeRef: vi.fn(),
    setActivatorNodeRef: vi.fn(),
    transform: null,
    isDragging: dnd.isDragging,
  }),
}));

vi.mock('@dnd-kit/utilities', () => ({
  CSS: { Translate: { toString: vi.fn(() => '') } },
}));

vi.mock('../IssueCard', () => ({
  default: ({ issue }: { issue: TrackerIssue }) => (
    <div data-testid="issue-card">{issue.identifier}</div>
  ),
}));

const issue: TrackerIssue = {
  identifier: 'ABC-1',
  title: 'Title',
  state: 'Todo',
  description: '',
  url: '',
  orchestratorState: 'idle',
  turnCount: 0,
  tokens: 0,
  elapsedMs: 0,
  lastMessage: '',
  error: '',
};

type ROCallback = (entries: { target: Element; contentRect: { height: number } }[]) => void;

class MockResizeObserver {
  static instances: MockResizeObserver[] = [];
  observed: Element[] = [];
  disconnected = false;
  constructor(public cb: ROCallback) {
    MockResizeObserver.instances.push(this);
  }
  observe(el: Element) {
    this.observed.push(el);
  }
  unobserve() {}
  disconnect() {
    this.disconnected = true;
  }
  fire(height: number) {
    this.cb(this.observed.map((target) => ({ target, contentRect: { height } })));
  }
}

function ro(): MockResizeObserver {
  const { instances } = MockResizeObserver;
  const observer = instances[instances.length - 1] as MockResizeObserver | undefined;
  if (!observer) throw new Error('no ResizeObserver was created');
  return observer;
}

function card() {
  return (
    <DraggableCard issue={issue} isBeingDragged={false} shouldCollapse={false} onSelect={vi.fn()} />
  );
}

function placeholder(): HTMLElement {
  const handle = screen.getByTestId('issue-card-drag-handle-placeholder');
  const box = handle.parentElement;
  if (!box) throw new Error('placeholder has no container');
  return box;
}

describe('DraggableCard', () => {
  beforeEach(() => {
    dnd.isDragging = false;
    MockResizeObserver.instances = [];
    vi.stubGlobal('ResizeObserver', MockResizeObserver);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('does not read offsetHeight on re-render without drag', () => {
    const getter = vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(90);
    const { rerender } = render(card());
    getter.mockClear();
    rerender(card());
    rerender(card());
    expect(getter).not.toHaveBeenCalled();
  });

  it('placeholder keeps the pre-drag measured height while dragging', () => {
    const { rerender } = render(card());
    act(() => {
      ro().fire(96);
    });
    dnd.isDragging = true;
    rerender(card());
    expect(placeholder().style.height).toBe('96px');
    // A later re-render while still dragging keeps the same height.
    rerender(card());
    expect(placeholder().style.height).toBe('96px');
  });

  it('updates measuredHeight when the observer reports a new content size', () => {
    const { rerender } = render(card());
    act(() => {
      ro().fire(80);
    });
    act(() => {
      ro().fire(140);
    });
    dnd.isDragging = true;
    rerender(card());
    expect(placeholder().style.height).toBe('140px');
  });

  it('disconnects the observer on unmount', () => {
    const { unmount } = render(card());
    const observer = ro();
    expect(observer.observed).toHaveLength(1);
    unmount();
    expect(observer.disconnected).toBe(true);
  });
});
