import { useEffect, useRef, useState } from 'react';

interface ConfirmButtonProps {
  label: string;
  confirmLabel: string;
  pendingLabel: string;
  isPending: boolean;
  onConfirm: () => void;
  /** Accessible name for the trigger when `label` alone is ambiguous (e.g. one per row). */
  ariaLabel?: string;
}

/**
 * Two-step danger button: first click shows "Are you sure?" with Yes/Cancel,
 * second click triggers the action.
 *
 * CORE-021 — two accessibility/robustness fixes live here so every caller
 * (Settings, RunningSessionsTable, IssueDetailSlide, Logs) gets them for
 * free: (1) the outer trigger is disabled while isPending, not just the
 * inner confirm button — otherwise a caller has no way to prevent a second
 * activation while the mutation is in flight; (2) focus moves to the Yes
 * button when the confirm row appears, and back to the trigger on Cancel —
 * the trigger unmounts when the confirm row replaces it, so keyboard focus
 * would otherwise silently drop to <body>.
 */
export function ConfirmButton({
  label,
  confirmLabel,
  pendingLabel,
  isPending,
  onConfirm,
  ariaLabel,
}: ConfirmButtonProps) {
  const [confirming, setConfirming] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const confirmRef = useRef<HTMLButtonElement>(null);
  const wasConfirmingRef = useRef(false);

  // Keep the confirm row visible (Yes button disabled) for the whole time
  // the mutation is pending, so a disabled-but-still-mounted Yes button is
  // what blocks a second activation — not a UI that already collapsed back
  // to the trigger after the first click. This uses React's documented
  // "adjust state during render" pattern (a render-time comparison against
  // the previous prop, guarded so it only ever fires once per transition)
  // rather than a setState-in-effect, which would trigger an extra
  // cascading render for the same result.
  const [prevIsPending, setPrevIsPending] = useState(isPending);
  if (isPending !== prevIsPending) {
    setPrevIsPending(isPending);
    if (prevIsPending && !isPending) {
      setConfirming(false);
    }
  }

  // Focus the Yes button when the confirm row appears; restore focus to the
  // trigger when it collapses back (Cancel, or the mutation settling). DOM
  // focus is a genuine side effect, so this stays in an effect.
  useEffect(() => {
    if (confirming) {
      confirmRef.current?.focus();
      wasConfirmingRef.current = true;
    } else if (wasConfirmingRef.current) {
      triggerRef.current?.focus();
      wasConfirmingRef.current = false;
    }
  }, [confirming]);

  if (confirming) {
    return (
      <div className="flex items-center gap-2">
        <span className="text-theme-muted text-xs">Are you sure?</span>
        <button
          ref={confirmRef}
          onClick={() => {
            if (isPending) return;
            onConfirm();
          }}
          disabled={isPending}
          style={{
            padding: '4px 10px',
            borderRadius: 4,
            fontSize: 12,
            fontWeight: 600,
            cursor: isPending ? 'wait' : 'pointer',
            background: 'var(--danger)',
            color: '#fff',
            border: 'none',
          }}
        >
          {isPending ? pendingLabel : confirmLabel}
        </button>
        <button
          onClick={() => {
            setConfirming(false);
          }}
          style={{
            padding: '4px 10px',
            borderRadius: 4,
            fontSize: 12,
            cursor: 'pointer',
            background: 'transparent',
            color: 'var(--text-secondary)',
            border: '1px solid var(--line)',
          }}
        >
          Cancel
        </button>
      </div>
    );
  }

  return (
    <button
      ref={triggerRef}
      aria-label={ariaLabel}
      onClick={() => {
        setConfirming(true);
      }}
      disabled={isPending}
      style={{
        padding: '6px 14px',
        borderRadius: 4,
        fontSize: 12,
        fontWeight: 500,
        cursor: isPending ? 'wait' : 'pointer',
        background: 'transparent',
        color: 'var(--danger-text)',
        border: '1px solid var(--danger)',
        opacity: isPending ? 0.5 : 1,
      }}
    >
      {label}
    </button>
  );
}
