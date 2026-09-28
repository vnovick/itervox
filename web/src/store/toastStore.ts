import { create } from 'zustand';
import { TOAST_DISMISS_MS, TOAST_REGION_CAP } from '../utils/timings';

export interface ToastItem {
  id: string;
  message: string;
  variant: 'error' | 'success' | 'info';
  /** How many times this message+variant was raised while visible (dedupe counter). */
  count: number;
}

/**
 * Which live region a toast belongs to. Errors go to the assertive
 * `role="alert"` region and stay until dismissed; success and info go to the
 * polite `role="status"` region and auto-dismiss.
 */
export type ToastRegion = 'alert' | 'status';

export function toastRegion(variant: ToastItem['variant']): ToastRegion {
  return variant === 'error' ? 'alert' : 'status';
}

interface ToastState {
  toasts: ToastItem[];
  // Internal map of auto-dismiss timers keyed by toast id.
  // Kept in the store so removeToast can cancel the pending timer, preventing
  // a stale setTimeout callback from running after the toast was manually dismissed.
  // Sticky (error) toasts never have an entry here.
  _timers: Map<string, ReturnType<typeof setTimeout>>;
  addToast: (message: string, variant?: ToastItem['variant']) => void;
  removeToast: (id: string) => void;
}

export const useToastStore = create<ToastState>((set, get) => {
  const schedule = (id: string) =>
    setTimeout(() => {
      get().removeToast(id);
    }, TOAST_DISMISS_MS);

  return {
    toasts: [],
    _timers: new Map(),

    addToast: (message, variant = 'error') => {
      const sticky = variant === 'error';
      set((state) => {
        const timers = new Map(state._timers);

        // Dedupe: same message + variant bumps the counter on the existing
        // entry. A repeated success/info restarts its dismiss timer; a
        // repeated error stays sticky.
        const existing = state.toasts.find((t) => t.message === message && t.variant === variant);
        if (existing) {
          if (!sticky) {
            const prev = timers.get(existing.id);
            if (prev !== undefined) clearTimeout(prev);
            timers.set(existing.id, schedule(existing.id));
          }
          return {
            toasts: state.toasts.map((t) =>
              t.id === existing.id ? { ...t, count: t.count + 1 } : t,
            ),
            _timers: timers,
          };
        }

        const id = Math.random().toString(36).slice(2);
        let toasts = [...state.toasts, { id, message, variant, count: 1 }];
        if (!sticky) timers.set(id, schedule(id));

        // Cap per region: the oldest toast in the SAME region is evicted, so
        // sticky errors never block a success from showing and vice versa.
        const region = toastRegion(variant);
        const inRegion = toasts.filter((t) => toastRegion(t.variant) === region);
        if (inRegion.length > TOAST_REGION_CAP) {
          const evict = new Set(
            inRegion.slice(0, inRegion.length - TOAST_REGION_CAP).map((t) => t.id),
          );
          for (const evictId of evict) {
            const timer = timers.get(evictId);
            if (timer !== undefined) clearTimeout(timer);
            timers.delete(evictId);
          }
          toasts = toasts.filter((t) => !evict.has(t.id));
        }

        return { toasts, _timers: timers };
      });
    },

    removeToast: (id) => {
      set((state) => {
        const timer = state._timers.get(id);
        if (timer !== undefined) clearTimeout(timer);
        const timers = new Map(state._timers);
        timers.delete(id);
        return { toasts: state.toasts.filter((t) => t.id !== id), _timers: timers };
      });
    },
  };
});
