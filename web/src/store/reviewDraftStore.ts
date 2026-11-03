import { create } from 'zustand';

// #238 / review and continue — the operator's draft review of an issue's
// diff: line and range comments plus an overall note, and the files marked
// viewed. Ephemeral UI state (this tab only): it survives switching files,
// tabs, closing the issue panel and navigating, not a reload. The whole
// draft goes to the agent in one action (or to the PR as ONE review) and is
// cleared once sent.

/** A diff side: RIGHT is the new file, LEFT the base file. */
export type DiffSide = 'RIGHT' | 'LEFT';

/** One line or range comment target (GitHub's review-comment shape). */
export interface LineTarget {
  path: string;
  line: number;
  side: DiffSide;
  /** First line of a range; absent for one line. */
  startLine?: number;
  startSide?: DiffSide;
}

export interface PendingComment extends LineTarget {
  id: string;
  body: string;
  /** The selected diff lines, quoted to the agent. */
  quote?: string;
}

const EMPTY_PENDING: readonly PendingComment[] = [];
const EMPTY_VIEWED: Readonly<Record<string, true>> = {};

interface ReviewDraftState {
  pending: Record<string, readonly PendingComment[]>;
  notes: Record<string, string>;
  viewed: Record<string, Readonly<Record<string, true>>>;
  add: (identifier: string, comment: Omit<PendingComment, 'id'>) => void;
  remove: (identifier: string, id: string) => void;
  setNote: (identifier: string, note: string) => void;
  /** Drops the issue's comments and note (the viewed marks stay). */
  clear: (identifier: string) => void;
  toggleViewed: (identifier: string, path: string) => void;
}

let seq = 0;

function without<T>(m: Record<string, T>, key: string): Record<string, T> {
  return Object.fromEntries(Object.entries(m).filter(([k]) => k !== key));
}

export const useReviewDraftStore = create<ReviewDraftState>((set) => ({
  pending: {},
  notes: {},
  viewed: {},
  add: (identifier, comment) => {
    seq += 1;
    const entry: PendingComment = { ...comment, id: `c${String(seq)}` };
    set((s) => ({
      pending: { ...s.pending, [identifier]: [...(s.pending[identifier] ?? EMPTY_PENDING), entry] },
    }));
  },
  remove: (identifier, id) => {
    set((s) => ({
      pending: {
        ...s.pending,
        [identifier]: (s.pending[identifier] ?? EMPTY_PENDING).filter((c) => c.id !== id),
      },
    }));
  },
  setNote: (identifier, note) => {
    set((s) => ({ notes: { ...s.notes, [identifier]: note } }));
  },
  clear: (identifier) => {
    set((s) => ({ pending: without(s.pending, identifier), notes: without(s.notes, identifier) }));
  },
  toggleViewed: (identifier, path) => {
    set((s) => {
      const cur = s.viewed[identifier] ?? EMPTY_VIEWED;
      const next: Record<string, true> = Object.fromEntries(
        Object.entries(cur).filter(([p]) => p !== path),
      );
      if (!(path in cur)) next[path] = true;
      return { viewed: { ...s.viewed, [identifier]: next } };
    });
  },
}));

/** The issue's draft comments (a stable empty list when none). */
export function usePendingComments(identifier: string): readonly PendingComment[] {
  return useReviewDraftStore((s) => s.pending[identifier] ?? EMPTY_PENDING);
}

/** The issue's draft overall note ("" when none). */
export function useDraftNote(identifier: string): string {
  return useReviewDraftStore((s) => s.notes[identifier] ?? '');
}

/** The files of the issue marked viewed. */
export function useViewedFiles(identifier: string): Readonly<Record<string, true>> {
  return useReviewDraftStore((s) => s.viewed[identifier] ?? EMPTY_VIEWED);
}

/** Orders comments by their file's position in `files`, then by line. */
export function commentOrder(
  files: readonly string[],
  a: Pick<PendingComment, 'path' | 'line'>,
  b: Pick<PendingComment, 'path' | 'line'>,
): number {
  const fa = files.indexOf(a.path);
  const fb = files.indexOf(b.path);
  if (fa !== fb) return fa - fb;
  return a.line - b.line;
}
