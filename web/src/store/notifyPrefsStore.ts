import { create } from 'zustand';

// CORE-096 — the operator's browser-notification opt-in. Per browser (local
// storage), default off; storage failures (private mode, blocked site data)
// fall back to in-memory state.
const KEY = 'itervox.notify.optIn';

function readOptIn(): boolean {
  try {
    return localStorage.getItem(KEY) === '1';
  } catch {
    return false;
  }
}

interface NotifyPrefsState {
  optIn: boolean;
  setOptIn: (optIn: boolean) => void;
}

export const useNotifyPrefsStore = create<NotifyPrefsState>((set) => ({
  optIn: readOptIn(),
  setOptIn: (optIn) => {
    try {
      if (optIn) localStorage.setItem(KEY, '1');
      else localStorage.removeItem(KEY);
    } catch {
      // in-memory only
    }
    set({ optIn });
  },
}));
