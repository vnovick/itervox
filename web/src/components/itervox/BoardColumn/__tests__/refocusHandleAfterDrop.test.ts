import { afterEach, describe, expect, it, vi } from 'vitest';
import { refocusHandleAfterDrop } from '../refocusHandleAfterDrop';

function handle(id: string): HTMLButtonElement {
  const b = document.createElement('button');
  b.setAttribute('data-drag-handle-for', id);
  document.body.appendChild(b);
  return b;
}

describe('refocusHandleAfterDrop', () => {
  afterEach(() => {
    document.body.innerHTML = '';
    vi.useRealTimers();
  });

  it('puts focus on the card handle once the re-mounted card appears', () => {
    vi.useFakeTimers({ toFake: ['requestAnimationFrame'] });
    const old = handle('ENG-1');
    old.focus();
    refocusHandleAfterDrop('ENG-1');
    vi.advanceTimersToNextFrame();
    expect(document.activeElement).toBe(old);
    // The old card unmounts (focus falls to <body>) and the new one mounts.
    old.remove();
    vi.advanceTimersToNextFrame();
    const fresh = handle('ENG-1');
    vi.advanceTimersToNextFrame();
    expect(document.activeElement).toBe(fresh);
  });

  it('leaves focus alone once the user has moved it elsewhere', () => {
    vi.useFakeTimers({ toFake: ['requestAnimationFrame'] });
    const other = document.createElement('input');
    document.body.appendChild(other);
    other.focus();
    refocusHandleAfterDrop('ENG-1');
    handle('ENG-1');
    vi.advanceTimersToNextFrame();
    expect(document.activeElement).toBe(other);
  });
});
