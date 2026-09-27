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

const PANEL_CLASS: Record<Direction, string> = {
  right: 'inset-y-0 right-0 w-full md:w-[75vw] slide-panel-right',
  left: 'inset-y-0 left-0 w-full md:w-[75vw]',
  bottom: 'inset-x-0 bottom-0 h-auto max-h-[90vh]',
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
        className="absolute inset-0 bg-black/50"
        onClick={onClose}
        aria-hidden="true"
      />

      {/* Panel */}
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className={`absolute flex flex-col overflow-hidden ${PANEL_CLASS[direction]} bg-theme-panel border-theme-line border-l`}
      >
        <div className="border-theme-line flex items-center justify-between border-b px-4 py-3">
          <h2 id={titleId} className="text-theme-text font-semibold">
            {title}
          </h2>
          <button
            onClick={onClose}
            aria-label="Close panel"
            className="text-theme-text-secondary flex h-8 w-8 items-center justify-center rounded-lg transition-colors"
          >
            ✕
          </button>
        </div>
        <div className="flex min-h-0 flex-1 flex-col">{children}</div>
      </div>
    </div>
  );
}
