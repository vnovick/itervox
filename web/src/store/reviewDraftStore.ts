import { create } from 'zustand';

// #238 — line comments an operator queued for the issue's next PR review.
// Ephemeral UI state (this tab only): it survives closing the issue panel
// and navigating, not a reload. Submitted as ONE GitHub review.

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
}

const EMPTY_PENDING: readonly PendingComment[] = [];

interface ReviewDraftState {
  pending: Record<string, readonly PendingComment[]>;
  add: (identifier: string, comment: Omit<PendingComment, 'id'>) => void;
  remove: (identifier: string, id: string) => void;
  clear: (identifier: string) => void;
}

let seq = 0;

export const useReviewDraftStore = create<ReviewDraftState>((set) => ({
  pending: {},
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
  clear: (identifier) => {
    set((s) => ({
      pending: Object.fromEntries(Object.entries(s.pending).filter(([k]) => k !== identifier)),
    }));
  },
}));

/** The issue's pending comments (a stable empty list when none). */
export function usePendingComments(identifier: string): readonly PendingComment[] {
  return useReviewDraftStore((s) => s.pending[identifier] ?? EMPTY_PENDING);
}
