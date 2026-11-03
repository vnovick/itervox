import type { EditorChoice } from '../store/editorPrefsStore';

// #238 — "Open in editor" deep links for a worktree file. Only meaningful
// when the daemon's worktree is on the machine the browser runs on; for a
// remote daemon the dashboard shows a copyable path instead.

export const EDITOR_LABEL: Record<EditorChoice, string> = { vscode: 'VS Code', cursor: 'Cursor' };

/** True when the dashboard is served from this machine (a loopback host). */
export function isLoopbackHost(hostname: string): boolean {
  const h = hostname.toLowerCase();
  return (
    h === 'localhost' ||
    h.endsWith('.localhost') ||
    h === '::1' ||
    h === '[::1]' ||
    /^127(\.\d{1,3}){3}$/.test(h)
  );
}

/** The absolute path of `file` inside the worktree `root`. */
export function worktreeFilePath(root: string, file: string): string {
  return `${root.replace(/[\\/]+$/, '')}/${file.replace(/^[\\/]+/, '')}`;
}

/**
 * The editor URL that opens `absPath` (at `line`): vscode://file/<path>:<line>
 * or cursor://file/<path>:<line>. Windows paths get the leading slash the
 * URL form needs.
 */
export function editorHref(editor: EditorChoice, absPath: string, line?: number): string {
  let p = absPath.replace(/\\/g, '/');
  if (!p.startsWith('/')) p = `/${p}`;
  const suffix = line && line > 0 ? `:${String(line)}` : '';
  return `${editor}://file${encodeURI(p)}${suffix}`;
}
