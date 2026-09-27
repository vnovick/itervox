import {
  useCallback,
  useRef,
  type CSSProperties,
  type FocusEvent,
  type KeyboardEvent,
} from 'react';
import { toastRegion, useToastStore, type ToastItem } from '../../store/toastStore';

/**
 * Stacking order for the toast container. Must stay strictly above Modal's
 * `z-99999` (components/ui/modal/index.tsx) so a toast raised while a dialog
 * is open is not hidden behind it.
 */
export const TOAST_Z_INDEX = 100_000;

function toastStyle(variant: ToastItem['variant']): CSSProperties {
  const [soft, text] =
    variant === 'error'
      ? ['var(--danger-soft)', 'var(--danger-text)']
      : variant === 'success'
        ? ['var(--success-soft)', 'var(--success-text)']
        : ['var(--accent-soft)', 'var(--accent-strong)'];
  // The translucent -soft tint is layered over a solid --panel so the text
  // contrast is measured against a known surface, not whatever the toast
  // happens to overlap.
  return {
    borderColor: soft,
    background: `linear-gradient(${soft}, ${soft}), var(--panel)`,
    color: text,
  };
}

function ToastEntry({ toast, onDismiss }: { toast: ToastItem; onDismiss: (id: string) => void }) {
  // Escape while focus is inside the toast dismisses it. Stop propagation so
  // the document-level dialog Escape handler does not also close a dialog.
  const handleKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key !== 'Escape') return;
    e.stopPropagation();
    onDismiss(toast.id);
  };
  return (
    // Escape from the focused dismiss button bubbles here; the container is
    // not itself a control.
    <div
      role="presentation"
      data-testid="toast"
      onKeyDown={handleKeyDown}
      className="pointer-events-auto flex max-w-sm min-w-[240px] items-start gap-3 rounded-[var(--radius-md)] border px-4 py-3 text-sm shadow-lg"
      style={toastStyle(toast.variant)}
    >
      <span className="flex-1">{toast.message}</span>
      {toast.count > 1 && (
        <span className="shrink-0 font-mono text-xs">
          <span aria-hidden="true">×{toast.count}</span>
          <span className="sr-only">{`(repeated ${String(toast.count)} times)`}</span>
        </span>
      )}
      <button
        type="button"
        onClick={() => {
          onDismiss(toast.id);
        }}
        aria-label="Dismiss notification"
        data-toast-dismiss={toast.id}
        className="shrink-0 opacity-80 transition-opacity hover:opacity-100"
      >
        ×
      </button>
    </div>
  );
}

/**
 * Toast notifications anchored to the bottom-right corner.
 *
 * Two live regions stay mounted for the life of the app (CORE-066) so a
 * screen reader never misses an announcement because its region was being
 * created in the same tick as the message:
 *  - `role="status"` (polite) for success / info, which auto-dismiss;
 *  - `role="alert"` (assertive) for errors, which stay until dismissed.
 */
export default function Toast() {
  const toasts = useToastStore((s) => s.toasts);
  const removeToast = useToastStore((s) => s.removeToast);
  const containerRef = useRef<HTMLDivElement>(null);
  // Where keyboard focus was before it entered the toast stack, so a
  // dismissal can hand it back instead of dropping it to <body> (M5-close).
  const returnFocusRef = useRef<HTMLElement | null>(null);

  const handleFocus = (e: FocusEvent<HTMLDivElement>) => {
    const from = e.relatedTarget;
    if (from instanceof HTMLElement && !e.currentTarget.contains(from)) {
      returnFocusRef.current = from;
    }
  };

  const dismiss = useCallback(
    (id: string) => {
      const container = containerRef.current;
      const hadFocus = !!container && container.contains(document.activeElement);
      // The next dismiss control (another toast), chosen before removal.
      const next = hadFocus
        ? Array.from(container.querySelectorAll<HTMLElement>('[data-toast-dismiss]')).find(
            (el) => el.dataset.toastDismiss !== id,
          )
        : undefined;
      removeToast(id);
      if (!hadFocus) return;
      const back = returnFocusRef.current;
      if (next) {
        next.focus();
      } else if (back?.isConnected) {
        back.focus();
      } else {
        const main = document.querySelector<HTMLElement>('main');
        if (main) {
          if (!main.hasAttribute('tabindex')) main.setAttribute('tabindex', '-1');
          main.focus();
        }
      }
    },
    [removeToast],
  );

  const errors = toasts.filter((t) => toastRegion(t.variant) === 'alert');
  const statuses = toasts.filter((t) => toastRegion(t.variant) === 'status');

  return (
    <div
      ref={containerRef}
      onFocus={handleFocus}
      data-testid="toast-container"
      className="pointer-events-none fixed right-4 bottom-4 flex flex-col gap-2"
      style={{ zIndex: TOAST_Z_INDEX }}
    >
      <div role="alert" aria-live="assertive" className="flex flex-col gap-2">
        {errors.map((t) => (
          <ToastEntry key={t.id} toast={t} onDismiss={dismiss} />
        ))}
      </div>
      <div role="status" aria-live="polite" className="flex flex-col gap-2">
        {statuses.map((t) => (
          <ToastEntry key={t.id} toast={t} onDismiss={dismiss} />
        ))}
      </div>
    </div>
  );
}
