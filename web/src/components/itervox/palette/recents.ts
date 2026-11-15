// The palette's recently run commands, per viewer (this browser), newest
// first. A convenience only: storage can be missing, full or blocked (a
// private window), so every access is guarded and the palette works the same
// without it.

export const RECENTS_KEY = 'itervox.palette.recents';
export const RECENTS_CAP = 8;

export function loadRecents(): string[] {
  try {
    const raw = window.localStorage.getItem(RECENTS_KEY);
    if (!raw) return [];
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((v): v is string => typeof v === 'string').slice(0, RECENTS_CAP);
  } catch {
    return [];
  }
}

/** Puts `id` first (once), keeps at most RECENTS_CAP, and returns the list. */
export function recordRecent(id: string): string[] {
  const next = [id, ...loadRecents().filter((r) => r !== id)].slice(0, RECENTS_CAP);
  try {
    window.localStorage.setItem(RECENTS_KEY, JSON.stringify(next));
  } catch {
    // Storage unavailable: the list just is not remembered.
  }
  return next;
}
