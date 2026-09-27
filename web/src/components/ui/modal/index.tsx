import { useRef } from 'react';
import { useDialogLayer } from '../dialog/useDialogLayer';

/**
 * Every Modal needs an accessible name (CORE-067): either a visible heading
 * referenced by id, or a plain label when there is no heading.
 */
type ModalLabel =
  | { ariaLabelledBy: string; ariaLabel?: never }
  | { ariaLabel: string; ariaLabelledBy?: never };

interface ModalBaseProps {
  isOpen: boolean;
  onClose: () => void;
  className?: string;
  children: React.ReactNode;
  showCloseButton?: boolean;
  isFullscreen?: boolean;
  /** When true, adds standard p-6 padding to the content area. */
  padded?: boolean;
}

type ModalProps = ModalBaseProps & ModalLabel;

export { ConfirmModal } from './ConfirmModal';
export { ModalFooter } from './ModalFooter';

export const Modal: React.FC<ModalProps> = ({
  isOpen,
  onClose,
  children,
  className,
  showCloseButton = true, // Default to true for backwards compatibility
  isFullscreen = false,
  padded = false,
  ariaLabel,
  ariaLabelledBy,
}) => {
  const modalRef = useRef<HTMLDivElement>(null);

  // CORE-067: overlay stack (Escape closes only the top dialog, only the top
  // dialog traps focus) and the ref-counted scroll lock, which is never
  // touched while the Modal is closed.
  useDialogLayer({ isOpen, onClose, containerRef: modalRef });

  if (!isOpen) return null;

  const contentClasses = isFullscreen
    ? 'w-full h-full'
    : 'relative w-full max-h-[85vh] overflow-y-auto rounded-[var(--radius-lg)]';

  return (
    <div className="modal fixed inset-0 z-99999 flex items-center justify-center overflow-y-auto">
      {!isFullscreen && (
        // Backdrop: a mouse shortcut to close; Escape closes via useDialogLayer.
        <div
          role="presentation"
          className="fixed inset-0 h-full w-full bg-black/50 backdrop-blur-sm"
          onClick={onClose}
        ></div>
      )}
      {/* eslint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-noninteractive-element-interactions -- event boundary: stops clicks inside the dialog from reaching a clickable ancestor in the React tree; Escape closes via useDialogLayer */}
      <div
        ref={modalRef}
        role="dialog"
        aria-modal="true"
        aria-label={ariaLabel}
        aria-labelledby={ariaLabelledBy}
        className={`${contentClasses} ${className ?? ''} border-theme-line bg-theme-bg-elevated border`}
        style={{ boxShadow: 'var(--shadow-lg)' }}
        onClick={(e) => {
          e.stopPropagation();
        }}
      >
        {showCloseButton && (
          <button
            onClick={onClose}
            aria-label="Close"
            className="bg-theme-bg-soft text-theme-muted absolute top-3 right-3 z-999 flex h-8 w-8 items-center justify-center rounded-full transition-colors sm:top-4 sm:right-4"
          >
            <svg
              width="24"
              height="24"
              viewBox="0 0 24 24"
              fill="none"
              xmlns="http://www.w3.org/2000/svg"
            >
              <path
                fillRule="evenodd"
                clipRule="evenodd"
                d="M6.04289 16.5413C5.65237 16.9318 5.65237 17.565 6.04289 17.9555C6.43342 18.346 7.06658 18.346 7.45711 17.9555L11.9987 13.4139L16.5408 17.956C16.9313 18.3466 17.5645 18.3466 17.955 17.956C18.3455 17.5655 18.3455 16.9323 17.955 16.5418L13.4129 11.9997L17.955 7.4576C18.3455 7.06707 18.3455 6.43391 17.955 6.04338C17.5645 5.65286 16.9313 5.65286 16.5408 6.04338L11.9987 10.5855L7.45711 6.0439C7.06658 5.65338 6.43342 5.65338 6.04289 6.0439C5.65237 6.43442 5.65237 7.06759 6.04289 7.45811L10.5845 11.9997L6.04289 16.5413Z"
                fill="currentColor"
              />
            </svg>
          </button>
        )}
        <div className={padded ? 'p-6' : ''}>{children}</div>
      </div>
    </div>
  );
};
