import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { useIsNarrow } from '../useIsNarrow';

function stubMatchMedia(matches: boolean) {
  let listener: ((e: MediaQueryListEvent) => void) | null = null;
  const mql = {
    matches,
    addEventListener: vi.fn((_: string, l: (e: MediaQueryListEvent) => void) => {
      listener = l;
    }),
    removeEventListener: vi.fn(),
  };
  vi.stubGlobal(
    'matchMedia',
    vi.fn(() => mql),
  );
  return {
    mql,
    fire: (next: boolean) => {
      listener?.({ matches: next } as MediaQueryListEvent);
    },
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('useIsNarrow', () => {
  it('is false without matchMedia', () => {
    vi.stubGlobal('matchMedia', undefined);
    const { result } = renderHook(() => useIsNarrow());
    expect(result.current).toBe(false);
  });

  it('follows the phone-width media query and stops listening on unmount', () => {
    const media = stubMatchMedia(true);
    const { result, unmount } = renderHook(() => useIsNarrow());
    expect(result.current).toBe(true);
    act(() => {
      media.fire(false);
    });
    expect(result.current).toBe(false);
    unmount();
    expect(media.mql.removeEventListener).toHaveBeenCalled();
  });
});
