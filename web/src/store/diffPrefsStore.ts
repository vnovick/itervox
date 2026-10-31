import { create } from 'zustand';

// Review and continue — the diff viewer's layout, unified or side by side.
// Per browser (local storage), default unified; storage failures (private
// mode, blocked site data) fall back to in-memory state.
const KEY = 'itervox.diffLayout';

export type DiffLayout = 'unified' | 'split';

function readLayout(): DiffLayout {
  try {
    return localStorage.getItem(KEY) === 'split' ? 'split' : 'unified';
  } catch {
    return 'unified';
  }
}

interface DiffPrefsState {
  layout: DiffLayout;
  setLayout: (layout: DiffLayout) => void;
}

export const useDiffPrefsStore = create<DiffPrefsState>((set) => ({
  layout: readLayout(),
  setLayout: (layout) => {
    try {
      localStorage.setItem(KEY, layout);
    } catch {
      // in-memory only
    }
    set({ layout });
  },
}));
