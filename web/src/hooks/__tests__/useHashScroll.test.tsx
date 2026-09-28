// CORE-086 — header pills and ops chips link to dashboard sections by hash;
// React Router does not scroll to hash targets, so the dashboard does.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, render } from '@testing-library/react';
import { useEffect } from 'react';
import { MemoryRouter, useNavigate } from 'react-router';
import { useHashScroll } from '../useHashScroll';

let navigateRef: ReturnType<typeof useNavigate> | null = null;

function Harness({ ready, show }: { ready: number; show: boolean }) {
  useHashScroll(ready);
  const navigate = useNavigate();
  useEffect(() => {
    navigateRef = navigate;
  });
  return show ? <div id="retry-queue">rows</div> : null;
}

// jsdom does not implement scrollIntoView.
if (!('scrollIntoView' in HTMLElement.prototype)) {
  Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', {
    configurable: true,
    writable: true,
    value: () => undefined,
  });
}

describe('useHashScroll', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('scrolls the section named by the hash once it renders, once per navigation', () => {
    const scroll = vi.fn();
    vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(scroll);
    const { rerender } = render(
      <MemoryRouter initialEntries={['/#retry-queue']}>
        <Harness ready={0} show={false} />
      </MemoryRouter>,
    );
    expect(scroll).not.toHaveBeenCalled();
    // The section appears with the first snapshot.
    rerender(
      <MemoryRouter initialEntries={['/#retry-queue']}>
        <Harness ready={1} show />
      </MemoryRouter>,
    );
    expect(scroll).toHaveBeenCalledTimes(1);
    // Later pushes do not yank the page back while the operator scrolls.
    rerender(
      <MemoryRouter initialEntries={['/#retry-queue']}>
        <Harness ready={2} show />
      </MemoryRouter>,
    );
    expect(scroll).toHaveBeenCalledTimes(1);
    // A new navigation to the same hash scrolls again.
    act(() => {
      void navigateRef?.('/#retry-queue');
    });
    expect(scroll).toHaveBeenCalledTimes(2);
  });

  it('ignores a hash that names no element', () => {
    const scroll = vi.fn();
    vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(scroll);
    render(
      <MemoryRouter initialEntries={['/#nowhere']}>
        <Harness ready={1} show />
      </MemoryRouter>,
    );
    expect(scroll).not.toHaveBeenCalled();
  });
});
