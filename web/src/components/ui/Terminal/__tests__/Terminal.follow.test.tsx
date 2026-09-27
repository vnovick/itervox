import { act, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Terminal } from '../Terminal';
import type { LogEntry } from '../Terminal';

// CORE-069 — jsdom has no layout, so scrollHeight/clientHeight are pinned per
// test and scrollTop is a plain writable number (jsdom stores what it is given).
function metrics(el: HTMLElement, m: { scrollHeight: number; clientHeight: number }) {
  Object.defineProperty(el, 'scrollHeight', { configurable: true, get: () => m.scrollHeight });
  Object.defineProperty(el, 'clientHeight', { configurable: true, get: () => m.clientHeight });
}

function scrollTo(el: HTMLElement, top: number) {
  el.scrollTop = top;
  fireEvent.scroll(el);
}

const line = (seq: number, message = `line ${String(seq)}`): LogEntry => ({
  ts: seq,
  seq,
  level: 'info',
  message,
});

const range = (from: number, to: number) =>
  Array.from({ length: to - from + 1 }, (_, i) => line(from + i));

describe('Terminal', () => {
  it('follows new lines while the view is at the bottom', () => {
    const m = { scrollHeight: 1000, clientHeight: 100 };
    const { rerender } = render(<Terminal entries={range(1, 3)} />);
    const el = screen.getByTestId('terminal-scroll');
    metrics(el, m);
    scrollTo(el, 900);
    m.scrollHeight = 1100;
    rerender(<Terminal entries={range(1, 4)} />);
    expect(el.scrollTop).toBe(1100);
  });

  it('does not autoscroll when user scrolled up', () => {
    const m = { scrollHeight: 1000, clientHeight: 100 };
    const { rerender } = render(<Terminal entries={range(1, 3)} />);
    const el = screen.getByTestId('terminal-scroll');
    metrics(el, m);
    scrollTo(el, 200);
    m.scrollHeight = 1100;
    rerender(<Terminal entries={range(1, 4)} />);
    expect(el.scrollTop).toBe(200);
  });

  it('keeps following when a capped window advances at equal length', () => {
    const m = { scrollHeight: 1000, clientHeight: 100 };
    const { rerender } = render(<Terminal entries={range(1, 3)} />);
    const el = screen.getByTestId('terminal-scroll');
    metrics(el, m);
    scrollTo(el, 900);
    // Same length, window moved by one line; the new line wraps so the
    // content grew. A length-keyed effect never fires here.
    m.scrollHeight = 1040;
    rerender(<Terminal entries={range(2, 4)} />);
    expect(el.scrollTop).toBe(1040);
  });

  it('shows and clears the N new pill', () => {
    const m = { scrollHeight: 1000, clientHeight: 100 };
    const { rerender } = render(<Terminal entries={range(1, 3)} />);
    const el = screen.getByTestId('terminal-scroll');
    metrics(el, m);
    expect(screen.queryByTestId('terminal-new-pill')).not.toBeInTheDocument();
    scrollTo(el, 100);

    m.scrollHeight = 1100;
    rerender(<Terminal entries={range(1, 4)} />);
    expect(screen.getByRole('button', { name: /1 new log line/i })).toHaveTextContent('1 new');

    // Capped window: equal length, two more appended — counted by identity,
    // not by length delta.
    rerender(<Terminal entries={range(3, 6)} />);
    const pill = screen.getByRole('button', { name: /3 new log lines/i });
    expect(pill).toHaveTextContent('3 new');

    // Clicking jumps to the latest line and clears the count.
    m.scrollHeight = 1200;
    act(() => {
      pill.click();
    });
    expect(el.scrollTop).toBe(1200);
    expect(screen.queryByTestId('terminal-new-pill')).not.toBeInTheDocument();

    // Scrolling back to the bottom by hand also clears it.
    scrollTo(el, 50);
    rerender(<Terminal entries={range(3, 7)} />);
    expect(screen.getByTestId('terminal-new-pill')).toHaveTextContent('1 new');
    scrollTo(el, m.scrollHeight - m.clientHeight);
    expect(screen.queryByTestId('terminal-new-pill')).not.toBeInTheDocument();
  });

  it('resets unseen count when resetKey changes', () => {
    const m = { scrollHeight: 1000, clientHeight: 100 };
    const { rerender } = render(<Terminal entries={range(1, 3)} resetKey="ENG-1" />);
    const el = screen.getByTestId('terminal-scroll');
    metrics(el, m);
    scrollTo(el, 0);
    rerender(<Terminal entries={range(1, 5)} resetKey="ENG-1" />);
    expect(screen.getByTestId('terminal-new-pill')).toHaveTextContent('2 new');

    m.scrollHeight = 500;
    rerender(<Terminal entries={[line(1, 'other issue')]} resetKey="ENG-2" />);
    expect(screen.queryByTestId('terminal-new-pill')).not.toBeInTheDocument();
    // A new issue starts following again from the bottom.
    expect(el.scrollTop).toBe(500);
  });

  it('does not follow when follow is false', () => {
    const m = { scrollHeight: 1000, clientHeight: 100 };
    const { rerender } = render(<Terminal entries={range(1, 3)} follow={false} />);
    const el = screen.getByTestId('terminal-scroll');
    metrics(el, m);
    scrollTo(el, 900);
    m.scrollHeight = 1100;
    rerender(<Terminal entries={range(1, 4)} follow={false} />);
    expect(el.scrollTop).toBe(900);
    expect(screen.queryByTestId('terminal-new-pill')).not.toBeInTheDocument();
  });

  describe('jump pill motion', () => {
    const original = window.matchMedia;
    afterEach(() => {
      window.matchMedia = original;
    });

    function mockReducedMotion(reduce: boolean) {
      window.matchMedia = ((query: string) => ({
        matches: reduce && query.includes('prefers-reduced-motion'),
        media: query,
        addEventListener: () => undefined,
        removeEventListener: () => undefined,
      })) as unknown as typeof window.matchMedia;
    }

    function pillScroll() {
      const m = { scrollHeight: 1000, clientHeight: 100 };
      const { rerender } = render(<Terminal entries={range(1, 3)} />);
      const el = screen.getByTestId('terminal-scroll');
      metrics(el, m);
      const spy = vi.fn();
      el.scrollTo = spy as unknown as typeof el.scrollTo;
      scrollTo(el, 0);
      rerender(<Terminal entries={range(1, 4)} />);
      act(() => {
        screen.getByTestId('terminal-new-pill').click();
      });
      return spy;
    }

    it('smooth-scrolls to the latest line by default', () => {
      mockReducedMotion(false);
      expect(pillScroll()).toHaveBeenCalledWith({ top: 1000, behavior: 'smooth' });
    });

    it('jumps without animation under prefers-reduced-motion', () => {
      mockReducedMotion(true);
      expect(pillScroll()).toHaveBeenCalledWith({ top: 1000, behavior: 'auto' });
    });
  });

  // M5-close BH-M5-7 — the smooth jump emits scroll events at intermediate
  // positions; each one used to read as "scrolled up" and turned following
  // off mid-animation, so lines arriving during the jump were not followed.
  it('keeps following while the smooth jump to the latest line is animating', () => {
    const m = { scrollHeight: 1000, clientHeight: 100 };
    const { rerender } = render(<Terminal entries={range(1, 3)} />);
    const el = screen.getByTestId('terminal-scroll');
    metrics(el, m);
    // A smooth scrollTo that has not moved yet (the animation is pending).
    const smooth = vi.fn();
    Object.defineProperty(el, 'scrollTo', { configurable: true, value: smooth });
    scrollTo(el, 100);
    m.scrollHeight = 1100;
    rerender(<Terminal entries={range(1, 4)} />);
    act(() => {
      screen.getByTestId('terminal-new-pill').click();
    });
    expect(smooth).toHaveBeenCalledWith({ top: 1100, behavior: 'smooth' });
    // Mid-animation scroll event, still far from the bottom.
    scrollTo(el, 500);
    m.scrollHeight = 1200;
    rerender(<Terminal entries={range(1, 5)} />);
    // Still following: no pill, and the new line is scrolled to.
    expect(screen.queryByTestId('terminal-new-pill')).not.toBeInTheDocument();
    expect(el.scrollTop).toBe(1200);
    // Arriving at the bottom ends the jump; a later scroll-up stops following.
    scrollTo(el, m.scrollHeight - m.clientHeight);
    scrollTo(el, 200);
    m.scrollHeight = 1300;
    rerender(<Terminal entries={range(1, 6)} />);
    expect(el.scrollTop).toBe(200);
    expect(screen.getByTestId('terminal-new-pill')).toHaveTextContent('1 new');
  });
});
