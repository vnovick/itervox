import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';

/**
 * #298 — the one seam between the Sessions page and the terminal renderer.
 * Everything else (TmuxTerminal, its tests) talks to `TerminalHandle`, so
 * the renderer can be swapped by changing this file alone: it is written
 * against the xterm.js API, which ghostty-web also implements (see the
 * issue's renderer note), and only this module imports `@xterm/*`. The page
 * loads it lazily (TmuxTerminal is a React.lazy chunk), so the renderer
 * costs nothing until a live terminal opens.
 */

export interface TerminalHandle {
  /** Renders into `el` and fits the grid to it. */
  open: (el: HTMLElement) => void;
  /** Draws terminal output (raw bytes from the daemon). */
  write: (data: Uint8Array | string) => void;
  /** Keys typed (and pastes); returns an unsubscribe. */
  onData: (cb: (data: string) => void) => () => void;
  /** The grid changed size; returns an unsubscribe. */
  onResize: (cb: (size: { cols: number; rows: number }) => void) => () => void;
  /** Re-fits the grid to its element (after the element resized). */
  fit: () => void;
  size: () => { cols: number; rows: number };
  focus: () => void;
  dispose: () => void;
}

export interface TerminalOptions {
  /** No cursor blink and no stdin: a read-only attach. */
  readOnly: boolean;
}

/**
 * Colours from the host element (its theme tokens), so the terminal follows
 * the theme; the background stays the console surface's own.
 */
function themeFrom(el: HTMLElement) {
  const fg = getComputedStyle(el).color || undefined;
  return { background: 'transparent', foreground: fg, cursor: fg };
}

export function createTerminal({ readOnly }: TerminalOptions): TerminalHandle {
  const term = new Terminal({
    allowTransparency: true,
    convertEol: false,
    cursorBlink: !readOnly,
    disableStdin: readOnly,
    fontFamily: '"IBM Plex Mono", ui-monospace, SFMono-Regular, Menlo, monospace',
    fontSize: 13,
    scrollback: 5000,
  });
  const fitter = new FitAddon();
  term.loadAddon(fitter);
  const fit = () => {
    try {
      fitter.fit();
    } catch {
      // Not laid out yet (a hidden tab): the next resize fits it.
    }
  };
  return {
    open: (el) => {
      term.options.theme = themeFrom(el);
      term.open(el);
      fit();
    },
    write: (data) => {
      term.write(data);
    },
    onData: (cb) => {
      const d = term.onData(cb);
      return () => {
        d.dispose();
      };
    },
    onResize: (cb) => {
      const d = term.onResize(cb);
      return () => {
        d.dispose();
      };
    },
    fit,
    size: () => ({ cols: term.cols, rows: term.rows }),
    focus: () => {
      term.focus();
    },
    dispose: () => {
      term.dispose();
    },
  };
}
