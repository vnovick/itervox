import { useId, useRef, type ReactNode } from 'react';
import { useDialogLayer } from '../dialog/useDialogLayer';

type Direction = 'left' | 'right' | 'bottom';

interface SlidePanelProps {
  isOpen: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  direction?: Direction;
}

// A floating sheet (.glass-sheet), inset 12px from the window edges.
const PANEL_CLASS: Record<Direction, string> = {
  right: 'inset-y-3 right-3 left-3 md:left-auto md:w-[75vw] slide-panel-right',
  left: 'inset-y-3 left-3 right-3 md:right-auto md:w-[75vw]',
  bottom: 'inset-x-3 bottom-3 h-auto max-h-[90vh]',
};

export function SlidePanel({
  isOpen,
  onClose,
  title,
  children,
  direction = 'right',
}: SlidePanelProps) {
  const titleId = useId();

  const panelRef = useRef<HTMLDivElement>(null);

  // CORE-067: overlay stack (Escape closes only the top dialog), focus trap
  // with restore-to-opener, and the ref-counted body scroll lock.
  useDialogLayer({ isOpen, onClose, containerRef: panelRef });

  if (!isOpen) return null;

  return (
    <div className="fixed inset-0 z-50 flex">
      {/* Overlay */}
      <div
        data-testid="slide-panel-overlay"
        className="glass-scrim absolute inset-0"
        onClick={onClose}
        aria-hidden="true"
      />

      {/* Panel */}
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className={`glass-sheet text-theme-text absolute flex flex-col overflow-hidden ${PANEL_CLASS[direction]}`}
      >
        <div className="flex items-center justify-between gap-3 px-6 pt-5 pb-3">
          <h2 id={titleId} className="text-theme-text text-lg font-semibold tracking-[-0.01em]">
            {title}
          </h2>
          <button
            type="button"
            onClick={onClose}
            aria-label="Close panel"
            className="glass-chip text-theme-text-secondary hover:text-theme-text flex h-9 w-9 shrink-0 items-center justify-center text-sm transition-colors"
          >
            ✕
          </button>
        </div>
        <div className="flex min-h-0 flex-1 flex-col">{children}</div>
      </div>
    </div>
  );
}
