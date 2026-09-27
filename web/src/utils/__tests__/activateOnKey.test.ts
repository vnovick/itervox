// CORE-088 — Enter/Space activation for role="button" rows (Timeline).
import { describe, expect, it, vi } from 'vitest';
import type { KeyboardEvent } from 'react';
import { activateOnKey } from '../activateOnKey';

const ev = (key: string) => {
  const preventDefault = vi.fn();
  return {
    event: { key, preventDefault } as unknown as KeyboardEvent<HTMLElement>,
    preventDefault,
  };
};

describe('activateOnKey', () => {
  it('runs on Enter and Space (preventing the page scroll) and ignores other keys', () => {
    const run = vi.fn();
    const handler = activateOnKey(run);
    for (const key of ['Enter', ' ']) {
      const { event, preventDefault } = ev(key);
      handler(event);
      expect(preventDefault).toHaveBeenCalled();
    }
    const { event, preventDefault } = ev('a');
    handler(event);
    expect(preventDefault).not.toHaveBeenCalled();
    expect(run).toHaveBeenCalledTimes(2);
  });
});
