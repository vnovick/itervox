import { beforeEach, describe, expect, it, vi } from 'vitest';

// #298 — createTerminal is the one seam over the renderer: these tests pin
// what the page relies on (open + fit, read-only, data/resize
// subscriptions, size, dispose) against a stand-in for @xterm/xterm.

const xterm = vi.hoisted(() => ({
  instances: [] as {
    options: Record<string, unknown>;
    opened: HTMLElement | null;
    written: unknown[];
    disposed: boolean;
    focused: boolean;
    dataCb: ((d: string) => void) | null;
    resizeCb: ((s: { cols: number; rows: number }) => void) | null;
    addons: unknown[];
  }[],
  fits: 0,
  fitThrows: false,
}));

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    options: Record<string, unknown>;
    opened: HTMLElement | null = null;
    written: unknown[] = [];
    disposed = false;
    focused = false;
    dataCb: ((d: string) => void) | null = null;
    resizeCb: ((s: { cols: number; rows: number }) => void) | null = null;
    addons: unknown[] = [];
    cols = 120;
    rows = 32;
    constructor(options: Record<string, unknown>) {
      this.options = { ...options };
      xterm.instances.push(this);
    }
    loadAddon(a: unknown) {
      this.addons.push(a);
    }
    open(el: HTMLElement) {
      this.opened = el;
    }
    write(d: unknown) {
      this.written.push(d);
    }
    onData(cb: (d: string) => void) {
      this.dataCb = cb;
      return {
        dispose: () => {
          this.dataCb = null;
        },
      };
    }
    onResize(cb: (s: { cols: number; rows: number }) => void) {
      this.resizeCb = cb;
      return {
        dispose: () => {
          this.resizeCb = null;
        },
      };
    }
    focus() {
      this.focused = true;
    }
    dispose() {
      this.disposed = true;
    }
  },
}));

vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    fit() {
      xterm.fits++;
      if (xterm.fitThrows) throw new Error('not laid out');
    }
  },
}));

vi.mock('@xterm/xterm/css/xterm.css', () => ({}));

import { createTerminal } from '../createTerminal';

beforeEach(() => {
  xterm.instances = [];
  xterm.fits = 0;
  xterm.fitThrows = false;
});

describe('createTerminal', () => {
  it('opens into the element, fits it, and follows the element colour', () => {
    const el = document.createElement('div');
    el.style.color = 'rgb(1, 2, 3)';
    const t = createTerminal({ readOnly: false });
    t.open(el);
    const inst = xterm.instances[0];
    expect(inst.opened).toBe(el);
    expect(inst.addons).toHaveLength(1);
    expect(xterm.fits).toBe(1);
    expect(inst.options.disableStdin).toBe(false);
    expect(inst.options.theme).toMatchObject({ foreground: 'rgb(1, 2, 3)' });
    expect(t.size()).toEqual({ cols: 120, rows: 32 });
    t.write(new Uint8Array([65]));
    expect(inst.written).toHaveLength(1);
    t.focus();
    expect(inst.focused).toBe(true);
    t.dispose();
    expect(inst.disposed).toBe(true);
  });

  it('is read-only (no stdin) for a read-only attach', () => {
    createTerminal({ readOnly: true });
    expect(xterm.instances[0].options.disableStdin).toBe(true);
    expect(xterm.instances[0].options.cursorBlink).toBe(false);
  });

  it('forwards data and resizes until unsubscribed', () => {
    const t = createTerminal({ readOnly: false });
    const onData = vi.fn();
    const onResize = vi.fn();
    const offData = t.onData(onData);
    const offResize = t.onResize(onResize);
    const inst = xterm.instances[0];
    inst.dataCb?.('ls\r');
    inst.resizeCb?.({ cols: 90, rows: 20 });
    expect(onData).toHaveBeenCalledWith('ls\r');
    expect(onResize).toHaveBeenCalledWith({ cols: 90, rows: 20 });
    offData();
    offResize();
    expect(inst.dataCb).toBeNull();
    expect(inst.resizeCb).toBeNull();
  });

  it('ignores a fit before layout', () => {
    const t = createTerminal({ readOnly: true });
    xterm.fitThrows = true;
    expect(() => {
      t.fit();
    }).not.toThrow();
  });
});
