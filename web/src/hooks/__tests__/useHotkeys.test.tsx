// CORE-095 — global keyboard shortcuts.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, renderHook } from '@testing-library/react';
import { useHotkeys } from '../useHotkeys';
import { pushLayer, removeLayer } from '../../components/ui/dialog/overlayStack';

function press(key: string, init: KeyboardEventInit = {}, target: EventTarget = document.body) {
  const ev = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...init });
  target.dispatchEvent(ev);
  return ev;
}

describe('useHotkeys', () => {
  const palette = vi.fn();
  const logs = vi.fn();
  beforeEach(() => {
    palette.mockReset();
    logs.mockReset();
    vi.useFakeTimers();
    renderHook(() => {
      useHotkeys([
        { keys: 'mod+k', run: palette },
        { keys: 'g l', run: logs },
      ]);
    });
  });
  afterEach(() => {
    vi.useRealTimers();
    document.body.innerHTML = '';
  });

  it('runs Mod+K and g-sequences, and prevents default only when an action runs', () => {
    const k = press('k', { ctrlKey: true });
    expect(palette).toHaveBeenCalledTimes(1);
    expect(k.defaultPrevented).toBe(true);
    const g = press('g');
    expect(g.defaultPrevented).toBe(false);
    const l = press('l');
    expect(logs).toHaveBeenCalledTimes(1);
    expect(l.defaultPrevented).toBe(true);
    const x = press('x');
    expect(x.defaultPrevented).toBe(false);
    // Meta works too (macOS).
    press('k', { metaKey: true });
    expect(palette).toHaveBeenCalledTimes(2);
  });

  it('drops a stale g prefix after ~1 s', () => {
    press('g');
    vi.advanceTimersByTime(1_200);
    const l = press('l');
    expect(logs).not.toHaveBeenCalled();
    expect(l.defaultPrevented).toBe(false);
  });

  it('ignores keys while focus is in input, textarea or contenteditable', () => {
    const { container } = render(
      <div>
        <input aria-label="i" />
        <textarea aria-label="t" />
        <div contentEditable suppressContentEditableWarning aria-label="c">
          x
        </div>
      </div>,
    );
    for (const el of container.querySelectorAll('input, textarea, [contenteditable]')) {
      press('k', { ctrlKey: true }, el);
      press('g', {}, el);
      press('l', {}, el);
    }
    expect(palette).not.toHaveBeenCalled();
    expect(logs).not.toHaveBeenCalled();
  });

  it('ignores keys while a dialog is open or event.isComposing is true', () => {
    pushLayer('some-dialog', () => undefined);
    press('k', { ctrlKey: true });
    press('g');
    press('l');
    removeLayer('some-dialog');
    expect(palette).not.toHaveBeenCalled();
    expect(logs).not.toHaveBeenCalled();
    press('k', { ctrlKey: true, isComposing: true });
    expect(palette).not.toHaveBeenCalled();
  });

  it('ignores keys during a dnd-kit keyboard drag', () => {
    const handle = document.createElement('button');
    handle.setAttribute('aria-roledescription', 'draggable');
    handle.setAttribute('aria-pressed', 'true');
    document.body.appendChild(handle);
    press('g', {}, handle);
    press('l', {}, handle);
    expect(logs).not.toHaveBeenCalled();
  });
});
